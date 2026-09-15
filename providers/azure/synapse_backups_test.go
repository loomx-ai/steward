package azure

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/app/cleanup"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

type synapseBackupFixture struct {
	*synapseInventoryFixture
	backups         map[string]map[string]any
	hidden, missing map[string]bool
	listFault       int
	paginate        bool
	intercepted     func(*http.Request) (*http.Response, bool)
}

func newSynapseBackupFixture(t *testing.T) *synapseBackupFixture {
	f := &synapseBackupFixture{synapseInventoryFixture: newSynapseInventoryFixture(t), backups: map[string]map[string]any{}, hidden: map[string]bool{}, missing: map[string]bool{}}
	for _, workspace := range []string{"first", "second"} {
		for _, kind := range []string{synapseDroppedType, synapseRestorePointType} {
			example := "RestorableDroppedSqlPoolGet"
			parent := strings.ToLower(resourceID(synapseType, workspace))
			if kind == synapseRestorePointType {
				example = "SqlPoolRestorePointsGet"
				parent += "/sqlpools/pool"
			}
			wire, err := os.ReadFile("fixtures/synapse/" + example + ".json")
			var native map[string]any
			if err != nil || json.Unmarshal(wire, &native) != nil {
				t.Fatal(err)
			}
			raw := object(object(object(native["responses"])["200"])["body"])
			id := strings.ToLower(parent + "/" + last(kind) + "/" + text(raw["name"]))
			raw["id"] = id
			raw["location"] = "eastus"
			if workspace == "second" {
				raw["location"] = "westus"
			}
			object(raw["properties"])["futurePrivateField"] = "backup-private-canary"
			f.backups[id] = raw
		}
	}
	f.override = func(q *http.Request) (*http.Response, bool) {
		if f.intercepted != nil {
			if res, ok := f.intercepted(q); ok {
				return res, true
			}
		}
		path := strings.ToLower(q.URL.Path)
		if f.missing[path] {
			return jsonResponse(404, nil, nil), true
		}
		if raw := f.backups[path]; raw != nil {
			return jsonResponse(200, raw, http.Header{"X-Ms-Request-Id": {"backup-own-request"}}), true
		}
		if strings.HasSuffix(path, "/restorabledroppedsqlpools") || strings.HasSuffix(path, "/restorepoints") {
			if f.listFault != 0 {
				return jsonResponse(f.listFault, nil, nil), true
			}
			if f.paginate && q.URL.Query().Get("$skiptoken") == "" {
				return jsonResponse(200, map[string]any{"value": []any{}, "nextLink": apiURL(path, synapseVersion) + "&$skiptoken=second"}, nil), true
			}
			rows := []any{}
			for id, raw := range f.backups {
				if redisParentID(id) == path[:strings.LastIndex(path, "/")] && strings.EqualFold(text(raw["type"]), synapseBackupKind(text(raw["type"]))) && strings.EqualFold(path, redisParentID(id)+"/"+last(text(raw["type"]))) && !f.hidden[id] && !f.missing[id] {
					rows = append(rows, raw)
				}
			}
			return jsonResponse(200, map[string]any{"value": rows}, nil), true
		}
		return fleetGraphEmptyIndexes(t, q)
	}
	return f
}
func backupRequest(r *Runtime, kind string) contracts.InventoryRequest {
	req := productRequest(r, kind)
	req.Source = synapseBackupSource
	return req
}
func TestSynapseBackupInventoryAndKnownAbsence(t *testing.T) {
	for _, kind := range []string{synapseDroppedType, synapseRestorePointType} {
		t.Run(kind, func(t *testing.T) {
			f := newSynapseBackupFixture(t)
			f.paginate = true
			req := backupRequest(f.runtime, kind)
			req.Limit = 1
			first, err := f.runtime.List(t.Context(), req)
			if err != nil || len(first.Items) != 1 || first.Complete {
				t.Fatal(first, err)
			}
			item := first.Items[0]
			if item.Actionable == nil || *item.Actionable != (kind == synapseRestorePointType) || item.State != "Retained" || first.RequestID != "backup-own-request" {
				t.Fatal(item)
			}
			raw, _ := json.Marshal(item)
			if strings.Contains(string(raw), "backup-private-canary") {
				t.Fatal("private native field persisted")
			}
			req.Cursor = first.NextCursor
			second, err := f.runtime.List(t.Context(), req)
			if err != nil || len(second.Items) != 1 || !second.Complete || second.Items[0].NativeID == item.NativeID {
				t.Fatal(second, err)
			}
			req.Cursor = ""
			req.Scope = asset.Scope{Kind: asset.ScopeRegion, NativeID: item.Location}
			req.KnownNativeIDs = []string{item.NativeID}
			req.KnownNativeMetadata = map[string]map[string]any{item.NativeID: item.Normalized}
			f.hidden[item.NativeID] = true
			recovered, err := f.runtime.List(t.Context(), req)
			if err != nil || len(recovered.Items) != 1 || len(recovered.AbsentNativeIDs) != 0 {
				t.Fatal("known omission", recovered, err)
			}
			f.missing[item.NativeID] = true
			gone, err := f.runtime.List(t.Context(), req)
			if err != nil || len(gone.Items) != 0 || len(gone.AbsentNativeIDs) != 1 {
				t.Fatal("own absence", gone, err)
			}
		})
	}
}
func TestSynapseBackupMissingParentNeverErases(t *testing.T) {
	for _, kind := range []string{synapseDroppedType, synapseRestorePointType} {
		for _, fault := range []string{"workspace", "parent", "list404", "list403"} {
			t.Run(kind+"/"+fault, func(t *testing.T) {
				f := newSynapseBackupFixture(t)
				req := backupRequest(f.runtime, kind)
				req.Scope = asset.Scope{Kind: asset.ScopeRegion, NativeID: "eastus"}
				first, err := f.runtime.List(t.Context(), req)
				if err != nil || len(first.Items) != 1 {
					t.Fatal(err)
				}
				item := first.Items[0]
				req.KnownNativeIDs = []string{item.NativeID}
				req.KnownNativeMetadata = map[string]map[string]any{item.NativeID: item.Normalized}
				f.missing[item.NativeID] = true
				switch fault {
				case "workspace":
					f.missing[strings.Join(strings.Split(item.NativeID, "/")[:9], "/")] = true
				case "parent":
					f.missing[redisParentID(item.NativeID)] = true
				case "list404":
					f.listFault = 404
				case "list403":
					f.listFault = 403
				}
				batch, err := f.runtime.List(t.Context(), req)
				if err == nil || batch.Complete || len(batch.AbsentNativeIDs) != 0 || isNotFound(err) {
					t.Fatal("inaccessible retained backup erased", batch, err)
				}
			})
		}
	}
}
func TestSynapseBackupCursorAndScope(t *testing.T) {
	for _, fault := range []string{"private configuration", "parent change", "foreign known", "wrong source", "cross-host page", "partial page", "continuous"} {
		t.Run(fault, func(t *testing.T) {
			f := newSynapseBackupFixture(t)
			req := backupRequest(f.runtime, synapseRestorePointType)
			req.Limit = 1
			first, err := f.runtime.List(t.Context(), req)
			if err != nil {
				t.Fatal(err)
			}
			item := first.Items[0]
			switch fault {
			case "private configuration":
				object(f.backups[item.NativeID]["properties"])["futurePrivateField"] = "changed"
				req.Cursor = first.NextCursor
			case "parent change":
				object(f.objects[redisParentID(item.NativeID)]["properties"])["creationDate"] = "2026-09-15T00:00:00Z"
				req.Cursor = first.NextCursor
			case "foreign known":
				req.KnownNativeIDs = []string{strings.Replace(item.NativeID, testSubscription, "foreign", 1)}
			case "wrong source":
				req.Source = productInventorySource
			case "cross-host page", "partial page":
				f.intercepted = func(q *http.Request) (*http.Response, bool) {
					if strings.HasSuffix(strings.ToLower(q.URL.Path), "/restorepoints") {
						if fault == "partial page" {
							return jsonResponse(206, map[string]any{"value": []any{}}, nil), true
						}
						return jsonResponse(200, map[string]any{"value": []any{}, "nextLink": "https://example.com/leak?api-version=" + synapseVersion}, nil), true
					}
					return nil, false
				}
			case "continuous":
				props := object(f.backups[item.NativeID]["properties"])
				props["restorePointType"] = "CONTINUOUS"
				delete(props, "restorePointCreationDate")
				props["earliestRestoreDate"] = "2026-09-01T00:00:00Z"
			}
			batch, err := f.runtime.List(t.Context(), req)
			if fault == "continuous" {
				if err != nil || *batch.Items[0].Actionable {
					t.Fatal("continuous backup", batch, err)
				}
			} else if err == nil {
				t.Fatal("invalid scope accepted", batch)
			}
		})
	}
}
func TestSynapseBackupRegisteredScanPreservesRecoveryRecords(t *testing.T) {
	f := newSynapseBackupFixture(t)
	repo, registry, _ := azureNativeWorkerRepository(t, f.runtime)
	azureNativeWorkerScan(t, f.runtime, synapseSource, repo, registry, []string{synapseSQLType}, false, false)
	values := azureNativeWorkerScan(t, f.runtime, synapseBackupSource, repo, registry, []string{synapseDroppedType, synapseRestorePointType}, false, true)
	backups := 0
	var sql asset.Asset
	for _, v := range values {
		if synapseBackupKind(v.Identity.NativeType) != "" {
			backups++
			wire, _ := json.Marshal(v)
			if strings.Contains(string(wire), "backup-private-canary") {
				t.Fatal("private field in SQLite")
			}
		}
		if v.Identity.NativeType == synapseSQLType && v.Location == "eastus" {
			sql = v
		}
	}
	if backups != 4 {
		t.Fatal("registered backup kinds missing", backups)
	}
	task, err := cleanup.NewService(repo, registry).CreateTask(t.Context(), cleanup.CreateTaskRequest{ConnectionID: "connection", Selectors: []plan.CleanupSelector{{Kind: plan.SelectorAsset, AssetID: sql.ID}}, CreatedBy: "operator"})
	if err != nil || task.Task.Status != plan.StatusReady || len(task.Steps) != 1 || len(task.ImpactItems) != 0 {
		t.Fatal("backup lookup scope became deletion ownership", task, err)
	}
	for id := range f.backups {
		f.hidden[id] = true
	}
	values = azureNativeWorkerScan(t, f.runtime, synapseBackupSource, repo, registry, []string{synapseDroppedType, synapseRestorePointType}, false, false)
	backups = 0
	for _, v := range values {
		if synapseBackupKind(v.Identity.NativeType) != "" {
			backups++
		}
	}
	if backups != 4 {
		t.Fatal("list omission erased recovery records", backups)
	}

	var lost string
	for id, raw := range f.backups {
		if text(raw["type"]) == synapseDroppedType && resourceRegion(raw) == "eastus" {
			lost = id
		}
	}
	parent := redisParentID(lost)
	f.missing[lost], f.missing[parent] = true, true
	values = azureNativeWorkerScan(t, f.runtime, synapseBackupSource, repo, registry, []string{synapseDroppedType, synapseRestorePointType}, true, false)
	backups = 0
	for _, v := range values {
		if synapseBackupKind(v.Identity.NativeType) != "" {
			backups++
		}
	}
	if backups != 4 {
		t.Fatal("failed parent read erased stored backups", backups)
	}
	delete(f.missing, parent)
	values = azureNativeWorkerScan(t, f.runtime, synapseBackupSource, repo, registry, []string{synapseDroppedType, synapseRestorePointType}, false, false)
	backups = 0
	for _, v := range values {
		if synapseBackupKind(v.Identity.NativeType) != "" {
			backups++
		}
	}
	if backups != 3 {
		t.Fatal("own missing backup not reconciled", backups)
	}
}

func TestSynapseBackupKnownParentsOmittedFromIndexes(t *testing.T) {
	for _, kind := range []string{synapseDroppedType, synapseRestorePointType} {
		t.Run(kind, func(t *testing.T) {
			f := newSynapseBackupFixture(t)
			req := backupRequest(f.runtime, kind)
			req.Scope = asset.Scope{Kind: asset.ScopeRegion, NativeID: "eastus"}
			first, err := f.runtime.List(t.Context(), req)
			if err != nil || len(first.Items) != 1 {
				t.Fatal(err)
			}
			item := first.Items[0]
			req.KnownNativeIDs = []string{item.NativeID}
			req.KnownNativeMetadata = map[string]map[string]any{item.NativeID: item.Normalized}
			f.collections["/subscriptions/"+testSubscription+"/providers/microsoft.synapse/workspaces"] = nil
			workspace := strings.Join(strings.Split(item.NativeID, "/")[:9], "/")
			f.collections[workspace+"/sqlpools"] = nil
			batch, err := f.runtime.List(t.Context(), req)
			if err != nil || len(batch.Items) != 1 || len(batch.AbsentNativeIDs) != 0 {
				t.Fatal("known backup hidden by omitted parent indexes", batch, err)
			}
		})
	}
}

func TestSynapseBackupOptionalDates(t *testing.T) {
	for _, kind := range []string{synapseDroppedType, synapseRestorePointType} {
		t.Run(kind, func(t *testing.T) {
			f := newSynapseBackupFixture(t)
			for _, raw := range f.backups {
				if text(raw["type"]) == kind {
					for _, key := range []string{"creationDate", "deletionDate", "earliestRestoreDate", "restorePointCreationDate", "restorePointType", "databaseName"} {
						delete(object(raw["properties"]), key)
					}
				}
			}
			req := backupRequest(f.runtime, kind)
			batch, err := f.runtime.List(t.Context(), req)
			if err != nil || len(batch.Items) != 2 {
				t.Fatal("optional native dates treated as mandatory", batch, err)
			}
			for _, item := range batch.Items {
				if item.Actionable == nil || *item.Actionable {
					t.Fatal("missing dates granted cleanup")
				}
			}
			for _, raw := range f.backups {
				if text(raw["type"]) == kind {
					field := "creationDate"
					if kind == synapseRestorePointType {
						field = "restorePointCreationDate"
					}
					object(raw["properties"])[field] = "invalid-date"
				}
			}
			if _, err = f.runtime.List(t.Context(), req); err == nil {
				t.Fatal("malformed native date accepted")
			}
		})
	}
}

func TestSynapseBackupParentErrorCodesNeverErase(t *testing.T) {
	for _, kind := range []string{synapseRestorePointType, synapseDroppedType} {
		for _, code := range []string{"SubscriptionDoesNotHaveServer", "DatabaseDoesNotExist", "SourceDatabaseNotFound", "ParentResourceNotFound", "ResourceGroupNotFound"} {
			t.Run(kind+"/"+code, func(t *testing.T) {
				f := newSynapseBackupFixture(t)
				req := backupRequest(f.runtime, kind)
				req.Scope = asset.Scope{Kind: asset.ScopeRegion, NativeID: "eastus"}
				batch, err := f.runtime.List(t.Context(), req)
				if err != nil || len(batch.Items) != 1 {
					t.Fatal(batch, err)
				}
				id := batch.Items[0].NativeID
				req.KnownNativeIDs = []string{id}
				f.hidden[id] = true
				f.intercepted = func(q *http.Request) (*http.Response, bool) {
					if strings.EqualFold(q.URL.Path, id) {
						return jsonResponse(404, map[string]any{"error": map[string]any{"code": code}}, nil), true
					}
					return nil, false
				}
				failed, err := f.runtime.List(t.Context(), req)
				if err == nil || isNotFound(err) || failed.Complete || len(failed.AbsentNativeIDs) != 0 {
					t.Fatal("parent error erased backup", failed, err)
				}
			})
		}
	}
}
