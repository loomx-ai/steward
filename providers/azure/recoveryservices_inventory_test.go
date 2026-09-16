package azure

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/provider/contracts"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

func recoveryServicesExample(t *testing.T, name string) map[string]any {
	t.Helper()
	wire, err := os.ReadFile("fixtures/recoveryservices/" + name + ".json")
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err = json.Unmarshal(wire, &doc); err != nil {
		t.Fatal(err)
	}
	return object(object(object(doc["responses"])["200"])["body"])
}

func TestRecoveryServicesSourceEvidence(t *testing.T) {
	wire, err := os.ReadFile("fixtures/recoveryservices/sources.json")
	if err != nil {
		t.Fatal(err)
	}
	var sources []struct{ File, SHA256 string }
	if err = json.Unmarshal(wire, &sources); err != nil {
		t.Fatal(err)
	}
	if len(sources) != 6 {
		t.Fatal("missing native evidence")
	}
	for _, source := range sources {
		wire, err = os.ReadFile("fixtures/recoveryservices/" + source.File)
		sum := sha256.Sum256(wire)
		if err != nil || hex.EncodeToString(sum[:]) != source.SHA256 {
			t.Fatal("changed native fixture", source.File, err)
		}
	}
}

func TestRecoveryServicesUnchangedMetadata(t *testing.T) {
	for _, tc := range []struct{ name, kind string }{{"Vaults_Get", recoveryServicesVault}, {"DeletedVaults_Get", recoveryServicesDeletedVault}, {"ProtectedItems_Get", recoveryServicesItem}} {
		t.Run(tc.name, func(t *testing.T) {
			raw := recoveryServicesExample(t, tc.name)
			c := &client{subscription: strings.Split(text(raw["id"]), "/")[2]}
			id, err := c.recoveryServicesIdentity(text(raw["id"]), tc.kind)
			if err != nil {
				t.Fatal(err)
			}
			if err = c.recoveryServicesMetadata(raw, id, tc.kind); err != nil {
				t.Fatal(err)
			}
		})
	}
	raw := recoveryServicesExample(t, "BackupProtectedItems_List")
	for _, value := range array(raw["value"]) {
		item := object(value)
		c := &client{subscription: strings.Split(text(item["id"]), "/")[2]}
		if _, err := c.recoveryServicesIdentity(text(item["id"]), recoveryServicesItem); err == nil {
			t.Fatal("fabric-omitting schema example was silently repaired")
		}
	}
}

func TestRecoveryServicesHANARecordedIdentities(t *testing.T) {
	wire, err := os.ReadFile("fixtures/recoveryservices/hana-read-recording.json")
	if err != nil {
		t.Fatal(err)
	}
	var rows []struct {
		Request  struct{ Method, URL string }
		Response struct {
			Body   struct{ String string }
			Status struct{ Code int }
		}
	}
	if err = json.Unmarshal(wire, &rows); err != nil || len(rows) != 3 {
		t.Fatal("missing recorded context", err)
	}
	for i, row := range rows {
		if row.Request.Method != "GET" || row.Response.Status.Code != 200 {
			t.Fatal("recorded operation changed", i)
		}
		var body map[string]any
		if err = json.Unmarshal([]byte(row.Response.Body.String), &body); err != nil {
			t.Fatal(err)
		}
		values := array(body["value"])
		if values == nil {
			values = []any{body}
		}
		if len(values) == 0 {
			t.Fatal("empty recorded collection")
		}
		for _, v := range values {
			raw := object(v)
			kind := text(raw["type"])
			c := &client{subscription: strings.Split(text(raw["id"]), "/")[2]}
			id, err := c.recoveryServicesIdentity(text(raw["id"]), kind)
			if err != nil {
				t.Fatal(i, err)
			}
			if err = c.recoveryServicesMetadata(raw, id, kind); err != nil {
				t.Fatal(i, err)
			}
			if !strings.Contains(id, ";") {
				t.Fatal("compound workload names lost")
			}
		}
	}
}

func recoveryServicesTestItem() (string, map[string]any) {
	vault := strings.ToLower(resourceID(recoveryServicesVault, "vault"))
	id := vault + "/backupfabrics/azure/protectioncontainers/vmappcontainer;compute;test;vm/protecteditems/saphanadatabase;hdb;systemdb"
	return id, map[string]any{"id": id, "name": last(id), "type": recoveryServicesItem, "properties": map[string]any{"protectedItemType": "AzureVmWorkloadSAPHanaDatabase", "vaultId": vault}}
}

func TestRecoveryServicesIdentityBoundaries(t *testing.T) {
	c := &client{subscription: testSubscription}
	id, raw := recoveryServicesTestItem()
	for _, bad := range []string{" " + id, id + "\t", strings.Replace(id, "/backupfabrics/azure", "", 1), strings.Replace(id, testSubscription, "00000000-0000-0000-0000-000000000000", 1), id + "/extra/name", strings.Replace(id, "systemdb", "%2Fsystemdb", 1), strings.TrimSuffix(id, last(id)) + ".."} {
		if _, err := c.recoveryServicesIdentity(bad, recoveryServicesItem); err == nil {
			t.Fatal("accepted invalid native ID", bad)
		}
	}
	for _, mode := range []string{"type", "name", "properties", "vault", "item-type"} {
		changed := batchClone(raw)
		switch mode {
		case "type":
			changed["type"] = recoveryServicesContainer
		case "name":
			changed["name"] = "different"
		case "properties":
			changed["properties"] = []any{}
		case "vault":
			object(changed["properties"])["vaultId"] = recoveryServicesVaultID(id) + "other"
		case "item-type":
			delete(object(changed["properties"]), "protectedItemType")
		}
		if err := c.recoveryServicesMetadata(changed, id, recoveryServicesItem); err == nil {
			t.Fatal("accepted invalid metadata", mode)
		}
	}
}

func TestRecoveryServicesCollectionBoundaries(t *testing.T) {
	for _, mode := range []string{"valid", "duplicate", "foreign-parent", "filter", "version", "foreign-host", "cycle", "async"} {
		t.Run(mode, func(t *testing.T) {
			id, raw := recoveryServicesTestItem()
			path := recoveryServicesVaultID(id) + "/backupprotecteditems"
			calls := 0
			r := protocolRuntime(t, func(q *http.Request) (*http.Response, error) {
				calls++
				if q.Method != "GET" || !strings.EqualFold(q.URL.Path, path) || q.URL.Query().Get("api-version") != recoveryServicesBackupVersion {
					t.Fatal("unexpected native collection request", q.Method, q.URL.Path)
				}
				value := batchClone(raw)
				rows := []any{value}
				next := ""
				headers := http.Header{}
				switch mode {
				case "duplicate":
					rows = append(rows, value)
				case "foreign-parent":
					other := strings.Replace(id, "/vaults/vault/", "/vaults/other/", 1)
					value["id"] = other
					object(value["properties"])["vaultId"] = recoveryServicesVaultID(other)
				case "filter":
					next = apiURL(path, recoveryServicesBackupVersion) + "&$filter=workloadType%20eq%20%27SAPHanaDatabase%27"
				case "version":
					next = apiURL(path, "2023-04-01")
				case "foreign-host":
					next = strings.Replace(apiURL(path, recoveryServicesBackupVersion), "management.azure.com", "foreign.invalid", 1)
				case "cycle":
					next = apiURL(path, recoveryServicesBackupVersion)
				case "async":
					headers.Set("Azure-AsyncOperation", apiURL(path, recoveryServicesBackupVersion))
				}
				body := map[string]any{"value": rows}
				if next != "" {
					body["nextLink"] = next
				}
				return jsonResponse(200, body, headers), nil
			})
			c, err := r.resolve(t.Context(), "connection")
			if err != nil {
				t.Fatal(err)
			}
			got, err := c.recoveryServicesCollection(t.Context(), path, recoveryServicesItem)
			if mode == "valid" {
				if err != nil || len(got) != 1 {
					t.Fatal(got, err)
				}
			} else if err == nil {
				t.Fatal("unsafe collection accepted")
			}
			if calls != 1 {
				t.Fatal("invalid page reached transport", calls)
			}
		})
	}
}

type recoveryServicesFixture struct {
	runtime                         *Runtime
	objects                         map[string]map[string]any
	omitted                         map[string]bool
	override                        func(*http.Request) (*http.Response, bool)
	vault, container, item, deleted string
}

func newRecoveryServicesFixture(t *testing.T) *recoveryServicesFixture {
	t.Helper()
	id, item := recoveryServicesTestItem()
	vault := recoveryServicesVaultID(id)
	container := redisParentID(id)
	deleted := "/subscriptions/" + testSubscription + "/providers/microsoft.recoveryservices/locations/eastus/deletedvaults/deleted-one"
	f := &recoveryServicesFixture{objects: map[string]map[string]any{}, omitted: map[string]bool{}, vault: vault, container: container, item: id, deleted: deleted}
	f.objects[id] = item
	object(item["properties"])["protectionState"] = "Protected"
	object(item["properties"])["privateFutureSetting"] = "must-not-leak"
	f.objects[vault] = map[string]any{"id": vault, "name": last(vault), "type": recoveryServicesVault, "location": "eastus", "properties": map[string]any{"provisioningState": "Succeeded"}}
	f.objects[container] = map[string]any{"id": container, "name": last(container), "type": recoveryServicesContainer, "properties": map[string]any{"containerType": "VMAppContainer", "registrationStatus": "Registered"}}
	f.objects[deleted] = map[string]any{"id": deleted, "name": last(deleted), "type": recoveryServicesDeletedVault, "properties": map[string]any{"vaultId": vault, "vaultDeletionTime": "2026-09-01T00:00:00Z", "purgeAt": "2026-10-01T00:00:00Z"}}
	f.runtime = protocolRuntime(t, func(q *http.Request) (*http.Response, error) {
		if q.Method != "GET" {
			t.Fatal("inventory issued mutation")
		}
		if f.override != nil {
			if res, ok := f.override(q); ok {
				return res, nil
			}
		}
		path := strings.ToLower(q.URL.Path)
		if path == "/subscriptions/"+testSubscription+"/locations" {
			return jsonResponse(200, map[string]any{"value": []any{map[string]any{"name": "eastus"}, map[string]any{"name": "westus"}}}, nil), nil
		}
		if raw := f.objects[path]; raw != nil {
			kind := recoveryServicesKind(text(raw["type"]))
			if q.URL.Query().Get("api-version") != recoveryServicesVersion(kind) {
				t.Fatal("wrong detail API version")
			}
			return jsonResponse(200, raw, nil), nil
		}
		collection := last(path)
		kind := ""
		switch collection {
		case "vaults":
			kind = recoveryServicesVault
		case "backupprotectioncontainers":
			kind = recoveryServicesContainer
		case "backupprotecteditems":
			kind = recoveryServicesItem
		case "deletedvaults":
			kind = recoveryServicesDeletedVault
		}
		if kind != "" {
			if q.URL.Query().Get("api-version") != recoveryServicesVersion(kind) {
				t.Fatal("wrong collection API version")
			}
			values := []any{}
			for id, raw := range f.objects {
				if text(raw["type"]) != kind || f.omitted[id] {
					continue
				}
				if kind == recoveryServicesDeletedVault && !strings.HasPrefix(id, path+"/") {
					continue
				}
				if kind != recoveryServicesVault && kind != recoveryServicesDeletedVault && !strings.HasPrefix(path, recoveryServicesVaultID(id)+"/") {
					continue
				}
				values = append(values, raw)
			}
			return jsonResponse(200, map[string]any{"value": values}, nil), nil
		}
		return jsonResponse(404, map[string]any{"error": map[string]any{"code": "ResourceNotFound"}}, nil), nil
	})
	return f
}

func recoveryServicesRequest(f *recoveryServicesFixture, kind string) contracts.InventoryRequest {
	k := f.runtime.resourceKind(kind)
	return contracts.InventoryRequest{ConnectionID: "connection", Source: recoveryServicesSource, ResourceKind: &k, Scope: asset.Scope{Kind: asset.ScopeRegion, NativeID: "eastus"}}
}

func TestRecoveryServicesRegisteredInventory(t *testing.T) {
	f := newRecoveryServicesFixture(t)
	var logs []execution.JobLogEntry
	ctx := execution.WithJobLogSink(t.Context(), execution.JobLogSinkFunc(func(_ context.Context, e execution.JobLogEntry) { logs = append(logs, e) }))
	for _, kind := range []string{recoveryServicesVault, recoveryServicesContainer, recoveryServicesItem, recoveryServicesDeletedVault} {
		req := recoveryServicesRequest(f, kind)
		batch, err := f.runtime.List(ctx, req)
		if err != nil || !batch.Complete || len(batch.Items) != 1 {
			t.Fatal(kind, err, len(batch.Items))
		}
		item := batch.Items[0]
		if item.NativeType != kind || item.Location != "eastus" || item.Actionable == nil || *item.Actionable || item.Normalized["cleanup_protected"] != true {
			t.Fatal("invalid inventory authority", kind, item)
		}
		if kind == recoveryServicesDeletedVault && (item.State != "soft_deleted" || item.Normalized["originalVaultId"] != f.vault) {
			t.Fatal("retention identity lost")
		}
		if kind == recoveryServicesItem && item.Normalized["containerId"] != f.container {
			t.Fatal("container relationship lost")
		}
		req.Scope.NativeID = "westus"
		batch, err = f.runtime.List(ctx, req)
		if err != nil || len(batch.Items) != 0 || !batch.Complete {
			t.Fatal("foreign region leaked", kind, err)
		}
		req.Source = inventorySource
		batch, err = f.runtime.List(ctx, req)
		if err != nil || len(batch.Items) != 0 || !batch.Complete {
			t.Fatal("generic index took native authority", err)
		}
		req.Source = productInventorySource
		if _, err = f.runtime.List(ctx, req); err == nil {
			t.Fatal("wrong source accepted")
		}
	}
	wire, _ := json.Marshal(logs)
	if len(logs) == 0 || strings.Contains(string(wire), "must-not-leak") {
		t.Fatal("native diagnostics leaked private workload configuration")
	}
}

func TestRecoveryServicesKnownIdentityAndContinuation(t *testing.T) {
	for _, mode := range []string{"omitted-live", "missing-own", "parent-404", "listed-404", "changed-cursor"} {
		t.Run(mode, func(t *testing.T) {
			f := newRecoveryServicesFixture(t)
			req := recoveryServicesRequest(f, recoveryServicesItem)
			req.KnownNativeIDs = []string{f.item}
			switch mode {
			case "omitted-live":
				f.omitted[f.item] = true
			case "missing-own":
				delete(f.objects, f.item)
			case "parent-404":
				f.override = func(q *http.Request) (*http.Response, bool) {
					if strings.EqualFold(q.URL.Path, f.item) {
						return jsonResponse(404, map[string]any{"error": map[string]any{"code": "ParentResourceNotFound"}}, nil), true
					}
					return nil, false
				}
				f.omitted[f.item] = true
			case "listed-404":
				f.override = func(q *http.Request) (*http.Response, bool) {
					if strings.EqualFold(q.URL.Path, f.item) {
						return jsonResponse(404, map[string]any{"error": map[string]any{"code": "ResourceNotFound"}}, nil), true
					}
					return nil, false
				}
			case "changed-cursor":
				other := f.item + "2"
				raw := batchClone(f.objects[f.item])
				raw["id"], raw["name"] = other, last(other)
				f.objects[other] = raw
				req.Limit = 1
			}
			batch, err := f.runtime.List(t.Context(), req)
			if mode == "parent-404" || mode == "listed-404" {
				if err == nil || len(batch.AbsentNativeIDs) != 0 {
					t.Fatal("failed read retired known asset")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if mode == "missing-own" {
				if len(batch.AbsentNativeIDs) != 1 || batch.AbsentNativeIDs[0] != f.item {
					t.Fatal("own absence not reconciled")
				}
				return
			}
			if len(batch.Items) != 1 || len(batch.AbsentNativeIDs) != 0 {
				t.Fatal("live item omitted")
			}
			if mode == "changed-cursor" {
				if batch.NextCursor == "" || batch.Complete {
					t.Fatal("missing continuation")
				}
				req.Cursor = batch.NextCursor
				object(f.objects[f.item]["properties"])["privateFutureSetting"] = "changed-private-value"
				if _, err = f.runtime.List(t.Context(), req); err == nil {
					t.Fatal("changed private configuration reused cursor")
				}
			}
		})
	}
}

func TestRecoveryServicesVaultReferences(t *testing.T) {
	id, raw := recoveryServicesTestItem()
	c := &client{subscription: testSubscription}
	vault := recoveryServicesVaultID(id)
	for _, reference := range []string{vault, "https://management.azure.com" + vault} {
		changed := batchClone(raw)
		object(changed["properties"])["vaultId"] = reference
		if err := c.recoveryServicesMetadata(changed, id, recoveryServicesItem); err != nil {
			t.Fatal("native reference refused", err)
		}
	}
	for _, reference := range []string{"https://foreign.invalid" + vault, "http://management.azure.com" + vault, "https://management.azure.com:443" + vault, "https://user@management.azure.com" + vault, "https://management.azure.com" + vault + "?x=1", "https://management.azure.com" + vault + "#x", "https://management.azure.com" + vault + "other"} {
		changed := batchClone(raw)
		object(changed["properties"])["vaultId"] = reference
		if err := c.recoveryServicesMetadata(changed, id, recoveryServicesItem); err == nil {
			t.Fatal("foreign/ambiguous vault URL accepted")
		}
	}
	object(raw["properties"])["isScheduledForDeferredDelete"] = "true"
	if err := c.recoveryServicesMetadata(raw, id, recoveryServicesItem); err == nil {
		t.Fatal("malformed retention flag accepted")
	}
}

func TestRecoveryServicesContainerAndRetentionBoundaries(t *testing.T) {
	for _, mode := range []string{"missing-container", "changing-container", "retained", "missing-deleted-regional-hint"} {
		t.Run(mode, func(t *testing.T) {
			f := newRecoveryServicesFixture(t)
			req := recoveryServicesRequest(f, recoveryServicesItem)
			switch mode {
			case "missing-container":
				delete(f.objects, f.container)
			case "changing-container":
				n := 0
				f.override = func(q *http.Request) (*http.Response, bool) {
					if strings.EqualFold(q.URL.Path, f.container) {
						n++
						raw := batchClone(f.objects[f.container])
						object(raw["properties"])["privateRevision"] = n
						return jsonResponse(200, raw, nil), true
					}
					return nil, false
				}
			case "retained":
				object(f.objects[f.item]["properties"])["isScheduledForDeferredDelete"] = true
			case "missing-deleted-regional-hint":
				req = recoveryServicesRequest(f, recoveryServicesDeletedVault)
				req.KnownNativeIDs = []string{strings.Replace(f.deleted, "/eastus/", "/westus/", 1)}
			}
			batch, err := f.runtime.List(t.Context(), req)
			if mode == "missing-container" || mode == "changing-container" {
				if err == nil {
					t.Fatal("unstable parent accepted")
				}
				return
			}
			if err != nil || len(batch.Items) != 1 || len(batch.AbsentNativeIDs) != 0 {
				t.Fatal("invalid retained inventory", err)
			}
			if mode == "retained" && (batch.Items[0].State != "soft_deleted" || batch.Items[0].Normalized["retained"] != true) {
				t.Fatal("retained data shown as live protection")
			}
		})
	}
}

func TestRecoveryServicesSQLiteScanReconciliation(t *testing.T) {
	f := newRecoveryServicesFixture(t)
	repository, registry, _ := azureNativeWorkerRepository(t, f.runtime)
	kinds := []string{recoveryServicesVault, recoveryServicesContainer, recoveryServicesItem, recoveryServicesDeletedVault}
	values := azureNativeWorkerScan(t, f.runtime, recoveryServicesSource, repository, registry, kinds, false, false)
	if len(values) != 4 {
		t.Fatal("registered scan lost native resources", len(values))
	}
	for _, value := range values {
		if value.Capabilities.Has(asset.CapabilityActionable) || value.Location != "eastus" || value.Normalized["_inventory_source"] != recoveryServicesSource {
			t.Fatal("scan assigned unsupported cleanup capability")
		}
	}
	f.omitted[f.item] = true
	values = azureNativeWorkerScan(t, f.runtime, recoveryServicesSource, repository, registry, []string{recoveryServicesItem}, false, false)
	if len(values) != 4 {
		t.Fatal("list omission retired live item")
	}
	f.override = func(q *http.Request) (*http.Response, bool) {
		if strings.EqualFold(q.URL.Path, f.item) {
			return jsonResponse(403, map[string]any{"error": map[string]any{"code": "Forbidden"}}, nil), true
		}
		return nil, false
	}
	values = azureNativeWorkerScan(t, f.runtime, recoveryServicesSource, repository, registry, []string{recoveryServicesItem}, true, false)
	if len(values) != 4 {
		t.Fatal("permission failure retired known item")
	}
	f.override = nil
	delete(f.objects, f.item)
	values = azureNativeWorkerScan(t, f.runtime, recoveryServicesSource, repository, registry, []string{recoveryServicesItem}, false, false)
	if len(values) != 3 {
		t.Fatal("own absence not persisted", len(values))
	}
	for _, value := range values {
		if value.Identity.NativeID == f.item {
			t.Fatal("absent protected item remained active")
		}
	}
}

func TestRecoveryServicesRecordedRetentionTimestamps(t *testing.T) {
	wire, err := os.ReadFile("fixtures/recoveryservices/deleted-vault-read-recording.json")
	if err != nil {
		t.Fatal(err)
	}
	var rows []struct {
		Response struct {
			Body   struct{ String string }
			Status struct{ Code int }
		}
	}
	if err = json.Unmarshal(wire, &rows); err != nil || len(rows) != 2 {
		t.Fatal("missing deleted-vault recording", err)
	}
	earlier := 0
	count := 0
	for _, row := range rows {
		if row.Response.Status.Code != 200 {
			t.Fatal("unexpected native response")
		}
		var body map[string]any
		if err = json.Unmarshal([]byte(row.Response.Body.String), &body); err != nil {
			t.Fatal(err)
		}
		values := array(body["value"])
		if values == nil {
			values = []any{body}
		}
		for _, value := range values {
			raw := object(value)
			c := &client{subscription: strings.Split(text(raw["id"]), "/")[2]}
			id, err := c.recoveryServicesIdentity(text(raw["id"]), recoveryServicesDeletedVault)
			if err != nil {
				t.Fatal(err)
			}
			if err = c.recoveryServicesMetadata(raw, id, recoveryServicesDeletedVault); err != nil {
				t.Fatal(err)
			}
			props := object(raw["properties"])
			deleted, _ := time.Parse(time.RFC3339Nano, text(props["vaultDeletionTime"]))
			purge, _ := time.Parse(time.RFC3339Nano, text(props["purgeAt"]))
			if purge.Before(deleted) {
				earlier++
			}
			count++
			malformed := batchClone(raw)
			object(malformed["properties"])["purgeAt"] = "not-a-time"
			if err = c.recoveryServicesMetadata(malformed, id, recoveryServicesDeletedVault); err == nil {
				t.Fatal("invalid time accepted")
			}
		}
	}
	if count < 2 || earlier == 0 {
		t.Fatal("recorded earlier purge timestamps no longer exercised")
	}
}
