package azure

import (
	"encoding/json"
	"maps"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/app/inventory"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

type dataMigrationFixture struct {
	runtime          *Runtime
	client           *client
	ids, kinds       map[string]string
	resources, nodes map[string]map[string]any
	group            map[string]any
	locks            []any
	omitted          map[string]bool
	calls            map[string]int
	override         func(*http.Request) (*http.Response, bool)
}

func newDataMigrationFixture(t *testing.T) *dataMigrationFixture {
	t.Helper()
	f := &dataMigrationFixture{ids: map[string]string{}, kinds: map[string]string{}, resources: map[string]map[string]any{}, nodes: map[string]map[string]any{}, locks: []any{}, omitted: map[string]bool{}, calls: map[string]int{}}
	groupID := "/subscriptions/" + testSubscription + "/resourcegroups/test"
	f.group = map[string]any{"id": groupID, "name": "test", "type": groupType, "location": "eastus2", "properties": map[string]any{"provisioningState": "Succeeded"}}
	add := func(id, kind, example, region string) {
		raw := dataMigrationBody(t, example)
		raw["id"], raw["name"], raw["type"] = id, last(id), kind
		if region != "" {
			raw["location"] = region
		}
		raw["futurePrivateSetting"] = "private-native-dms-configuration"
		f.ids[kind], f.kinds[id], f.resources[id] = id, kind, raw
	}
	service := groupID + "/providers/microsoft.datamigration/services/classic"
	project := service + "/projects/project"
	add(service, dataMigrationServiceType, "Services_Get", "westus")
	object(f.resources[service]["properties"])["virtualSubnetId"] = groupID + "/providers/microsoft.network/virtualnetworks/network/subnets/default"
	add(project, dataMigrationProjectType, "Projects_Get", "westus")
	add(project+"/tasks/task", dataMigrationTaskType, "Tasks_Get", "")
	add(project+"/files/file", dataMigrationFileType, "Files_Get", "")
	add(service+"/servicetasks/task", dataMigrationServiceTaskType, "ServiceTasks_Get", "")
	sqlService := groupID + "/providers/microsoft.datamigration/sqlmigrationservices/sql"
	mongoService := groupID + "/providers/microsoft.datamigration/migrationservices/mongo"
	add(sqlService, dataMigrationSQLServiceType, "SqlMigrationServices_Get", "eastus")
	add(mongoService, dataMigrationMongoServiceType, "MigrationServices_Get", "westus")
	f.nodes[sqlService] = dataMigrationBody(t, "SqlMigrationServices_listMonitoringData")
	for _, row := range []struct{ scenario, kind, collection, example, targetExample string }{
		{"SqlDb", "Microsoft.Sql/servers", "servers", "DatabaseMigrationsSqlDb_Get", ""},
		{"SqlMi", "Microsoft.Sql/managedInstances", "managedInstances", "DatabaseMigrationsSqlMi_Get", "ManagedInstances_Get"},
		{"SqlVm", "Microsoft.SqlVirtualMachine/sqlVirtualMachines", "sqlVirtualMachines", "DatabaseMigrationsSqlVm_Get", "SqlVirtualMachines_Get"},
		{"MongoRU", "Microsoft.DocumentDB/databaseAccounts", "databaseAccounts", "DatabaseMigrationsMongoToCosmosDbRUMongo_Get", ""},
		{"MongoVCore", "Microsoft.DocumentDB/mongoClusters", "mongoClusters", "DatabaseMigrationsMongoToCosmosDbvCoreMongo_Get", ""},
	} {
		target := groupID + "/providers/" + strings.ToLower(strings.Split(row.kind, "/")[0]) + "/" + strings.ToLower(row.collection) + "/target"
		raw := map[string]any{"properties": map[string]any{"provisioningState": "Succeeded"}}
		if row.targetExample != "" {
			raw = dataMigrationBody(t, row.targetExample)
		}
		raw["id"], raw["name"], raw["type"], raw["location"] = target, "target", row.kind, "westus2"
		f.kinds[target], f.resources[target] = row.kind, raw
		id := target + "/providers/microsoft.datamigration/databasemigrations/database"
		add(id, dataMigrationType, row.example, "")
		f.ids[row.scenario] = id
		props := object(f.resources[id]["properties"])
		props["scope"], props["migrationService"], props["provisioningState"], props["migrationStatus"] = target, sqlService, "Succeeded", "InProgress"
		props["migrationOperationId"], props["startedOn"] = "858ba109-5ab7-4fa1-8aea-bea487cacdcd", "2026-09-01T00:00:00Z"
		if strings.HasPrefix(row.scenario, "Mongo") {
			props["migrationService"] = mongoService
		}
	}
	f.ids[dataMigrationType] = f.ids["SqlDb"]
	f.runtime = protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
		path := strings.ToLower(req.URL.Path)
		f.calls[req.Method+" "+path]++
		if f.override != nil {
			if res, ok := f.override(req); ok {
				return res, nil
			}
		}
		if req.URL.Host != "management.azure.com" {
			t.Fatal("unexpected native host", req.URL.Host)
		}
		if req.Method == "GET" {
			switch path {
			case groupID, "/subscriptions/" + testSubscription + "/resourcegroups":
				if req.URL.Query().Get("api-version") != resourcesVersion {
					t.Fatal("wrong group version")
				}
				if path == groupID {
					return jsonResponse(200, f.group, nil), nil
				}
				return jsonResponse(200, map[string]any{"value": []any{f.group}}, nil), nil
			case "/subscriptions/" + testSubscription + "/providers/microsoft.authorization/locks":
				return jsonResponse(200, map[string]any{"value": f.locks}, nil), nil
			}
		}
		if armPathProvider(path) != "microsoft.datamigration" {
			for _, kind := range []string{"Microsoft.DocumentDB/databaseAccounts", "Microsoft.DocumentDB/mongoClusters"} {
				mapping, _ := findType(kind)
				if path == "/subscriptions/"+testSubscription+"/providers/"+strings.ToLower(kind) {
					if req.Method != "GET" || req.URL.Query().Get("api-version") != mapping.Version {
						t.Fatal("wrong target index operation", req.Method, req.URL)
					}
					rows := []any{}
					for _, id := range slices.Sorted(maps.Keys(f.resources)) {
						if f.kinds[id] == kind && !f.omitted[id] {
							rows = append(rows, f.resources[id])
						}
					}
					return jsonResponse(200, map[string]any{"value": rows}, nil), nil
				}
			}
			if req.Method != "GET" {
				t.Fatal("target mutation", req.Method, req.URL)
			}
			if raw := f.resources[path]; raw != nil {
				return jsonResponse(200, raw, nil), nil
			}
			return jsonResponse(404, map[string]any{"error": map[string]any{"code": "ResourceNotFound"}}, nil), nil
		}
		if req.URL.Query().Get("api-version") != dataMigrationVersion || len(req.URL.Query()) != 1 {
			t.Fatal("unexpected migration version/query", req.URL)
		}
		if req.Method == "POST" && strings.HasSuffix(path, "/listmonitoringdata") {
			id := strings.TrimSuffix(path, "/listmonitoringdata")
			if f.resources[id] != nil {
				return jsonResponse(200, f.nodes[id], nil), nil
			}
			return jsonResponse(404, map[string]any{"error": map[string]any{"code": "ResourceNotFound"}}, nil), nil
		}
		if req.Method != "GET" {
			t.Fatal("unexpected migration mutation", req.Method, req.URL)
		}
		if raw := f.resources[path]; raw != nil {
			return jsonResponse(200, raw, nil), nil
		}
		collection := false
		rows := []any{}
		for _, id := range slices.Sorted(maps.Keys(f.resources)) {
			kind := f.kinds[id]
			if dataMigrationKind(kind) == "" {
				continue
			}
			raw := f.resources[id]
			parent := dataMigrationParent(id, kind)
			rootCollection := "/subscriptions/" + testSubscription + "/providers/" + strings.ToLower(kind)
			match := kind != dataMigrationType && (parent == "" && path == rootCollection || parent != "" && path == parent+"/"+strings.ToLower(last(kind)))
			if kind == dataMigrationType {
				target, _ := dataMigrationTarget(id)
				match = path == strings.ToLower(text(object(raw["properties"])["migrationService"]))+"/listmigrations" || path == target+"/providers/microsoft.datamigration/databasemigrations"
			}
			if match {
				collection = true
				if !f.omitted[id] {
					rows = append(rows, raw)
				}
			}
		}
		if collection {
			return jsonResponse(200, map[string]any{"value": rows}, nil), nil
		}
		// Empty native collections remain valid after their final child is gone.
		for _, kind := range []string{dataMigrationServiceType, dataMigrationMongoServiceType, dataMigrationSQLServiceType} {
			if path == "/subscriptions/"+testSubscription+"/providers/"+strings.ToLower(kind) {
				return jsonResponse(200, map[string]any{"value": []any{}}, nil), nil
			}
		}
		for id, raw := range f.resources {
			for _, child := range dataMigrationChildKinds(f.kinds[id]) {
				if path == id+"/"+strings.ToLower(last(child)) {
					return jsonResponse(200, map[string]any{"value": []any{}}, nil), nil
				}
			}
			if path == id+"/listmigrations" && (f.kinds[id] == dataMigrationSQLServiceType || f.kinds[id] == dataMigrationMongoServiceType) || path == id+"/providers/microsoft.datamigration/databasemigrations" && strings.HasPrefix(text(raw["type"]), "Microsoft.DocumentDB/") {
				return jsonResponse(200, map[string]any{"value": []any{}}, nil), nil
			}
		}
		return jsonResponse(404, map[string]any{"error": map[string]any{"code": "ResourceNotFound"}}, nil), nil
	})
	var err error
	f.client, err = f.runtime.resolve(t.Context(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *dataMigrationFixture) list(t *testing.T, kind string, known ...contracts.InventoryItem) (contracts.InventoryBatch, error) {
	t.Helper()
	return f.runtime.List(t.Context(), f.request(kind, known...))
}

func (f *dataMigrationFixture) request(kind string, known ...contracts.InventoryItem) contracts.InventoryRequest {
	resourceKind := f.runtime.resourceKind(kind)
	request := contracts.InventoryRequest{ConnectionID: "connection", Source: dataMigrationInventorySource, Scope: asset.Scope{Kind: asset.ScopeSubscription, NativeID: testSubscription}, ResourceKind: &resourceKind, KnownNativeMetadata: map[string]map[string]any{}}
	for _, item := range known {
		request.KnownNativeIDs = append(request.KnownNativeIDs, item.NativeID)
		request.KnownNativeMetadata[item.NativeID] = item.Normalized
	}
	return request
}

func TestDataMigrationNativeInventory(t *testing.T) {
	f := newDataMigrationFixture(t)
	for _, kind := range []string{dataMigrationServiceType, dataMigrationProjectType, dataMigrationTaskType, dataMigrationFileType, dataMigrationServiceTaskType, dataMigrationSQLServiceType, dataMigrationMongoServiceType, dataMigrationType} {
		t.Run(kind, func(t *testing.T) {
			batch, err := f.list(t, kind)
			want := 1
			if kind == dataMigrationType {
				want = 5
			}
			if err != nil || !batch.Complete || len(batch.Items) != want || len(batch.AbsentNativeIDs) != 0 {
				t.Fatal("native inventory", kind, err, len(batch.Items), batch)
			}
			for _, item := range batch.Items {
				if item.Actionable == nil || !*item.Actionable || item.Normalized[dataMigrationProof] == "" || f.client.dataMigrationRecorded(item.NativeID, kind, item.Normalized) != nil {
					t.Fatal("unsigned or unactionable native item", item.NativeID, item.Normalized["cleanup_protection_reason"])
				}
				if item.Location == "eastus2" || item.Location == "westus2" {
					t.Fatal("migration inherited unrelated group/target region")
				}
				payload, _ := json.Marshal(item)
				if strings.Contains(string(payload), "private-native-dms-configuration") || strings.Contains(string(payload), "ssma-test-server") || strings.Contains(string(payload), "abc.mongodb.com") || strings.Contains(string(payload), "SchemaInput/") {
					t.Fatal("private migration input leaked")
				}
				if f.calls["GET "+item.NativeID] < 2 {
					t.Fatal("native own GET skipped")
				}
			}
		})
	}
}

func TestDataMigrationTargetReferencesReachNetworkScope(t *testing.T) {
	f := newDataMigrationFixture(t)
	mi, _ := dataMigrationTarget(f.ids["SqlMi"])
	vm, _ := dataMigrationTarget(f.ids["SqlVm"])
	subnet := strings.ToLower(text(object(f.resources[mi]["properties"])["subnetId"]))
	compute := strings.ToLower(text(object(f.resources[vm]["properties"])["virtualMachineResourceId"]))
	batch, err := f.list(t, dataMigrationType)
	if err != nil {
		t.Fatal(err)
	}
	items := append(slices.Clone(batch.Items), contracts.InventoryItem{NativeID: compute, NativeAliases: []string{compute}, NetworkReferences: []string{subnet}})
	selected := inventory.FilterNetworkClosure(asset.ScanTarget{Kind: asset.ScanTargetVSwitch, NativeID: subnet}, items)
	if len(selected) != 3 || !slices.ContainsFunc(selected, func(item contracts.InventoryItem) bool { return item.NativeID == f.ids["SqlMi"] }) || !slices.ContainsFunc(selected, func(item contracts.InventoryItem) bool { return item.NativeID == f.ids["SqlVm"] }) {
		t.Fatal("target-backed migrations lost network scope", selected)
	}
	values := f.assets(t)
	lifecycle, _ := f.runtime.ServiceLifecycle(t.Context(), "connection")
	contribution, err := lifecycle.Contribute(t.Context(), "scope", values)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ migration, id, kind string }{{"SqlMi", subnet, subnetType}, {"SqlVm", compute, vmType}} {
		if !slices.ContainsFunc(contribution.Unresolved, func(ref graph.UnresolvedReference) bool {
			return ref.ControllerID == asset.AssetID(f.ids[test.migration]) && ref.NativeID == test.id && ref.NativeType == test.kind && ref.Relationship == graph.RelationshipUses && ref.Evidence[graph.RelationshipEvidenceRequiredDeletion] != true
		}) {
			t.Fatal("target context did not produce an independent native dependency", test)
		}
	}
	object(f.resources[mi]["properties"])["subnetId"] = compute
	if _, err := f.list(t, dataMigrationType); err == nil {
		t.Fatal("invalid typed target network reference accepted")
	}
}

func TestDataMigrationKnownAbsenceRequiresOwnReads(t *testing.T) {
	for _, mode := range []string{"classic-omitted", "classic-root-omitted", "classic-root-gone-child-live", "classic-child-forbidden", "classic-all-gone", "modern-omitted", "modern-root-omitted", "modern-root-gone-child-live", "modern-target-gone", "modern-child-forbidden", "modern-child-gone"} {
		t.Run(mode, func(t *testing.T) {
			f := newDataMigrationFixture(t)
			kind := dataMigrationTaskType
			if strings.HasPrefix(mode, "modern") {
				kind = dataMigrationType
			}
			batch, err := f.list(t, kind)
			if err != nil {
				t.Fatal(err)
			}
			known := batch.Items[0]
			if kind == dataMigrationType {
				for _, item := range batch.Items {
					if item.NativeID == f.ids["MongoRU"] {
						known = item
					}
				}
			}
			id := known.NativeID
			service := text(known.Normalized["_datamigration_service"])
			before := f.calls["GET "+id]
			switch mode {
			case "classic-omitted", "modern-omitted":
				f.omitted[id] = true
			case "classic-root-omitted", "modern-root-omitted":
				f.omitted[service] = true
			case "classic-root-gone-child-live", "modern-root-gone-child-live":
				delete(f.resources, service)
			case "classic-all-gone":
				for child := range f.resources {
					if strings.HasPrefix(child, service) {
						delete(f.resources, child)
					}
				}
			case "modern-child-gone":
				delete(f.resources, id)
			case "modern-target-gone":
				target, _ := dataMigrationTarget(id)
				delete(f.resources, target)
			case "classic-child-forbidden", "modern-child-forbidden":
				f.override = func(req *http.Request) (*http.Response, bool) {
					if req.Method == "GET" && strings.EqualFold(req.URL.Path, id) {
						return jsonResponse(403, map[string]any{"error": map[string]any{"code": "AuthorizationFailed"}}, nil), true
					}
					return nil, false
				}
			}
			result, err := f.list(t, kind, known)
			failed := strings.Contains(mode, "child-live") || strings.Contains(mode, "forbidden") || mode == "modern-target-gone"
			if failed {
				if err == nil || isNotFound(err) || len(result.AbsentNativeIDs) != 0 {
					t.Fatal("incomplete family proved absence", err, result.AbsentNativeIDs)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			missing := mode == "classic-all-gone" || mode == "modern-child-gone"
			if slices.Contains(result.AbsentNativeIDs, id) != missing || f.calls["GET "+id] <= before {
				t.Fatal("native known read missing", missing, result.AbsentNativeIDs)
			}
			present := false
			for _, item := range result.Items {
				present = present || item.NativeID == id
			}
			if present == missing {
				t.Fatal("incorrect known inventory membership")
			}
		})
	}
}

func TestDataMigrationInventoryRejectsIncompleteAndChangedFamilies(t *testing.T) {
	for _, mode := range []string{"duplicate", "foreign", "wrong-type", "wrong-name", "accepted-read", "list-403", "next-filter", "configuration-changed", "file-replaced", "missing-properties", "modern-service-conflict", "modern-target-conflict", "modern-operation-kind", "modern-duplicate", "node-work-missing", "duplicate-node"} {
		t.Run(mode, func(t *testing.T) {
			f := newDataMigrationFixture(t)
			kind := dataMigrationServiceType
			if strings.HasPrefix(mode, "modern") || strings.Contains(mode, "node") {
				kind = dataMigrationSQLServiceType
			}
			root := f.ids[kind]
			collection := "/subscriptions/" + testSubscription + "/providers/" + strings.ToLower(kind)
			switch mode {
			case "wrong-type":
				f.resources[root]["type"] = dataMigrationProjectType
			case "wrong-name":
				f.resources[root]["name"] = "different"
			case "missing-properties":
				delete(f.resources[root], "properties")
			case "modern-service-conflict":
				object(f.resources[f.ids["SqlDb"]]["properties"])["migrationService"] = f.ids[dataMigrationMongoServiceType]
			case "modern-target-conflict":
				object(f.resources[f.ids["SqlDb"]]["properties"])["scope"] = strings.ToLower(resourceID("Microsoft.Sql/servers", "other"))
			case "modern-operation-kind":
				object(f.resources[f.ids["SqlDb"]]["properties"])["kind"] = "SqlMi"
			case "node-work-missing":
				delete(object(array(f.nodes[root]["nodes"])[0]), "concurrentJobsRunning")
			case "duplicate-node":
				f.nodes[root]["nodes"] = append(array(f.nodes[root]["nodes"]), array(f.nodes[root]["nodes"])[0])
			}
			f.override = func(req *http.Request) (*http.Response, bool) {
				path := strings.ToLower(req.URL.Path)
				if req.Method != "GET" {
					return nil, false
				}
				if path == collection {
					switch mode {
					case "duplicate":
						return jsonResponse(200, map[string]any{"value": []any{f.resources[root], f.resources[root]}}, nil), true
					case "foreign":
						raw := batchClone(f.resources[root])
						raw["id"] = strings.Replace(root, testSubscription, "99999999-9999-9999-9999-999999999999", 1)
						return jsonResponse(200, map[string]any{"value": []any{raw}}, nil), true
					case "list-403":
						return jsonResponse(403, map[string]any{"error": map[string]any{"code": "AuthorizationFailed"}}, nil), true
					case "next-filter":
						return jsonResponse(200, map[string]any{"value": []any{f.resources[root]}, "nextLink": apiURL(collection, dataMigrationVersion) + "&$filter=state"}, nil), true
					}
				}
				if path == root && mode == "accepted-read" {
					return jsonResponse(202, f.resources[root], nil), true
				}
				if path == root && mode == "configuration-changed" && f.calls["GET "+root] > 1 {
					f.resources[root]["futurePrivateSetting"] = "changed-native-input"
				}
				if path == f.ids[dataMigrationFileType] && mode == "file-replaced" && f.calls["GET "+path] > 1 {
					object(f.resources[path]["properties"])["lastModified"] = "2026-09-10T00:00:00Z"
				}
				if path == root+"/listmigrations" && mode == "modern-duplicate" {
					raw := f.resources[f.ids["SqlDb"]]
					return jsonResponse(200, map[string]any{"value": []any{raw, raw}}, nil), true
				}
				return nil, false
			}
			batch, err := f.list(t, kind)
			if err == nil || len(batch.Items)+len(batch.AbsentNativeIDs) != 0 || isNotFound(err) {
				t.Fatal("invalid native family accepted", err, len(batch.Items))
			}
		})
	}
}

func TestDataMigrationTypedReferencesAndPrivateBinding(t *testing.T) {
	f := newDataMigrationFixture(t)
	id := f.ids[dataMigrationTaskType]
	raw := f.resources[id]
	props := object(raw["properties"])
	props["taskType"] = "MigrateSchemaSqlServerSqlDb"
	props["input"] = map[string]any{"selectedDatabases": []any{map[string]any{"schemaSetting": map[string]any{"schemaOption": "UseStorageFile", "fileId": f.ids[dataMigrationFileType]}}}, "arbitraryInput": map[string]any{"resourceId": strings.ToLower(resourceID(storageType, "fake"))}}
	props["clientData"] = map[string]any{"pretendResource": strings.ToLower(resourceID(storageType, "fake"))}
	member := dataMigrationMemberFrom(id, dataMigrationTaskType, raw)
	refs, err := dataMigrationReferences(member, nil)
	if err != nil || !slices.Equal(refs[dataMigrationFileType], []string{f.ids[dataMigrationFileType]}) || len(refs[storageType]) != 0 {
		t.Fatal("typed references", refs, err)
	}
	batch, err := f.list(t, dataMigrationFileType)
	if err != nil {
		t.Fatal(err)
	}
	if object(batch.Items[0].Normalized["_datamigration_dependents"])[id] == nil {
		t.Fatal("file lost consuming migration task")
	}
	before := f.client.privateConfiguration(dataMigrationSnapshot(dataMigrationTaskType, raw))
	props["state"], props["output"], props["errors"] = "Canceled", []any{map[string]any{"private": "result"}}, []any{}
	if after := f.client.privateConfiguration(dataMigrationSnapshot(dataMigrationTaskType, raw)); after != before {
		t.Fatal("native task state invalidated authored configuration")
	}
	object(props["input"])["arbitraryInput"] = "different-private-input"
	if f.client.privateConfiguration(dataMigrationSnapshot(dataMigrationTaskType, raw)) == before {
		t.Fatal("authored input escaped binding")
	}
}
