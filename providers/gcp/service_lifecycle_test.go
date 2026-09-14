package gcp

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func serviceTreeAssets() []asset.Asset {
	root := "projects/sample-project/locations/us-central1/namespaces/apps"
	var result []asset.Asset
	for _, entry := range []struct{ id, kind, name string }{{"namespace", "Namespace", root}, {"service", "Service", root + "/services/api"}, {"endpoint", "Endpoint", root + "/services/api/endpoints/backend"}} {
		result = append(result, asset.Asset{ID: asset.AssetID(entry.id), Identity: asset.Identity{Provider: asset.ProviderGCP, ConnectionID: "gcp-connection", Partition: "google-cloud", NativeType: "servicedirectory.googleapis.com/" + entry.kind, NativeID: "//servicedirectory.googleapis.com/" + entry.name}, Capabilities: asset.CapabilitySet{asset.CapabilityActionable}, Normalized: map[string]any{"name": entry.name, "uid": entry.id + "-uid", "project_id": "sample-project", "project_number": "123456"}})
	}
	return result
}

func serviceTreeRequest() contracts.ActionRequest {
	assets := serviceTreeAssets()
	return contracts.ActionRequest{Action: "delete", Asset: assets[0], IdempotencyKey: "delete-tree", LifecycleImpacts: []contracts.ActionImpact{{ControllerID: assets[0].ID, Asset: assets[1], Delete: true}, {ControllerID: assets[1].ID, Asset: assets[2], Delete: true}}}
}

func serviceTreeRead(t *testing.T, records []asset.Asset) roundTripFunc {
	t.Helper()
	return func(r *http.Request) (*http.Response, error) {
		if r.Method != "GET" || r.URL.Host != "servicedirectory.googleapis.com" {
			t.Fatalf("unexpected service discovery: %s %s", r.Method, r.URL)
		}
		var body any
		for _, value := range records {
			if r.URL.Path == "/v1/"+text(value.Normalized["name"]) {
				body = value.Normalized
				break
			}
		}
		if body == nil {
			switch {
			case strings.HasSuffix(r.URL.Path, "/services"):
				body = map[string]any{"services": []any{records[1].Normalized}}
			case strings.HasSuffix(r.URL.Path, "/endpoints"):
				body = map[string]any{"endpoints": []any{records[2].Normalized}}
			default:
				t.Fatalf("unexpected service read %s", r.URL)
			}
		}
		encoded, _ := json.Marshal(body)
		return apiResponse(r, 200, string(encoded)), nil
	}
}

func TestServiceCascadePlansNestedNativeOwnership(t *testing.T) {
	assets := serviceTreeAssets()
	c := &client{project: "sample-project", number: "123456", http: &http.Client{Transport: serviceTreeRead(t, assets)}}
	contribution, err := (&serviceCascades{client: c}).Contribute(context.Background(), "scope", assets)
	if err != nil || len(contribution.Bindings) != 2 || len(contribution.Unresolved) != 0 {
		t.Fatalf("contribution=%+v error=%v", contribution, err)
	}
	for _, binding := range contribution.Bindings {
		if binding.Evidence[graph.LifecycleEvidenceControllerDeleteGuaranteed] != true || binding.Evidence[graph.LifecycleEvidenceControllerVerifiesManagedAbsence] != true || binding.Ownership != graph.OwnershipExclusive {
			t.Fatalf("unverified cascade: %+v", binding)
		}
	}
	input := plan.Input{CleanupTaskID: "cleanup", Assets: assets, ResolvedAssetIDs: []asset.AssetID{"namespace", "service", "endpoint"}, Relationships: contribution.Relationships, LifecycleBindings: contribution.Bindings}
	result, err := plan.Solve(input)
	if err != nil || len(result.Blockers) != 0 || len(result.Steps) != 1 || result.Steps[0].AssetID != "namespace" || len(result.ImpactItems) != 2 {
		t.Fatalf("nested plan=%+v error=%v", result, err)
	}
	for _, impact := range result.ImpactItems {
		if impact.Expected != plan.ExpectedDelegatedDelete {
			t.Fatalf("lost native cascade: %+v", impact)
		}
	}
	input.RequestOptions = map[asset.AssetID]map[string]any{"namespace": {"retain_resources": []string{"endpoint"}}}
	result, err = plan.Solve(input)
	if err != nil || len(result.Blockers) == 0 {
		t.Fatalf("impossible nested retention was accepted: %+v %v", result, err)
	}
}

func TestServiceCascadeDiscoveryPreservesMissingAndChangedChildren(t *testing.T) {
	for _, mode := range []string{"missing", "foreign-connection", "duplicate", "changed"} {
		t.Run(mode, func(t *testing.T) {
			live := serviceTreeAssets()
			assets := serviceTreeAssets()
			switch mode {
			case "missing":
				assets = assets[:2]
			case "foreign-connection":
				assets[2].Identity.ConnectionID = "other"
			case "duplicate":
				assets = append(assets, assets[2])
			case "changed":
				assets[2].Normalized["uid"] = "previous-incarnation"
			}
			c := &client{project: "sample-project", number: "123456", http: &http.Client{Transport: serviceTreeRead(t, live)}}
			contribution, err := (&serviceCascades{client: c}).Contribute(context.Background(), "scope", assets)
			if mode == "changed" || mode == "duplicate" {
				if err == nil {
					t.Fatal("invalid child accepted")
				}
				return
			}
			if err != nil || len(contribution.Unresolved) != 1 || contribution.Unresolved[0].NativeID != live[2].Identity.NativeID {
				t.Fatalf("missing impact hidden: %+v %v", contribution, err)
			}
		})
	}
}

func TestServiceCascadeRejectsUnreviewedRetainedOrChangedImpact(t *testing.T) {
	for _, mode := range []string{"unreviewed", "retained", "changed", "protected", "foreign", "orphan", "duplicate", "permission", "partial", "bad-token"} {
		t.Run(mode, func(t *testing.T) {
			request := serviceTreeRequest()
			live := serviceTreeAssets()
			switch mode {
			case "unreviewed":
				request.LifecycleImpacts = request.LifecycleImpacts[:1]
			case "retained":
				request.LifecycleImpacts[1].Delete = false
			case "changed":
				live[2].Normalized["uid"] = "new-endpoint"
			case "protected":
				live[2].Normalized["deletionProtection"] = true
			case "foreign":
				request.LifecycleImpacts[1].Asset.Identity.ConnectionID = "another-connection"
			case "orphan":
				request.LifecycleImpacts[1].ControllerID = "another-parent"
			case "duplicate":
				request.LifecycleImpacts = append(request.LifecycleImpacts, request.LifecycleImpacts[1])
			}
			read := serviceTreeRead(t, live)
			driver := protocolAction(t, request.Asset.Identity.NativeType, text(request.Asset.Normalized["name"]), func(r *http.Request) (*http.Response, error) {
				if r.Method != "GET" {
					t.Fatalf("unsafe cascade mutation %s", r.URL)
				}
				if mode == "permission" && strings.HasSuffix(r.URL.Path, "/endpoints") {
					return apiResponse(r, 403, `{}`), nil
				}
				if mode == "partial" && strings.HasSuffix(r.URL.Path, "/endpoints") {
					return apiResponse(r, 200, `{"unreachable":["us-central1"]}`), nil
				}
				if mode == "bad-token" && strings.HasSuffix(r.URL.Path, "/endpoints") {
					return apiResponse(r, 200, `{"nextPageToken":123}`), nil
				}
				return read(r)
			})
			if _, err := driver.Execute(context.Background(), request); err == nil {
				t.Fatal("unsafe cascade accepted")
			}
		})
	}
}

func TestServiceCascadeSurvivesParentAbsenceAndRestart(t *testing.T) {
	request := serviceTreeRequest()
	assets := serviceTreeAssets()
	read := serviceTreeRead(t, assets)
	deleted, serviceReads, endpointReads, deletes := false, 0, 0, 0
	transport := func(r *http.Request) (*http.Response, error) {
		if r.Method == "DELETE" {
			if r.URL.Path != "/v1/"+text(assets[0].Normalized["name"]) {
				t.Fatal("delegated child separately deleted")
			}
			deletes++
			deleted = true
			return apiResponse(r, 200, `{}`), nil
		}
		if deleted {
			if strings.HasSuffix(r.URL.Path, "/apps") {
				return apiResponse(r, 404, `{}`), nil
			}
			if strings.HasSuffix(r.URL.Path, "/services/api") {
				serviceReads++
				if serviceReads > 1 {
					return apiResponse(r, 404, `{}`), nil
				}
			}
			if strings.HasSuffix(r.URL.Path, "/endpoints/backend") {
				endpointReads++
				if endpointReads > 1 {
					return apiResponse(r, 404, `{}`), nil
				}
			}
		}
		return read(r)
	}
	newDriver := func() *action {
		return protocolAction(t, request.Asset.Identity.NativeType, text(request.Asset.Normalized["name"]), transport)
	}
	result, err := newDriver().Execute(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 3; attempt++ {
		encoded, _ := json.Marshal(result)
		if err := json.Unmarshal(encoded, &result); err != nil {
			t.Fatal(err)
		}
		wait, err := newDriver().Wait(context.Background(), request, result)
		if err != nil || wait.Done != (attempt == 2) {
			t.Fatalf("attempt=%d wait=%+v error=%v", attempt, wait, err)
		}
	}
	if deletes != 1 || serviceReads != 3 || endpointReads != 2 {
		t.Fatalf("missing independent absence checks: deletes=%d service=%d endpoint=%d", deletes, serviceReads, endpointReads)
	}
	if _, err := newDriver().Execute(context.Background(), request); err != nil || deletes != 1 {
		t.Fatalf("completed cascade resubmitted: %d %v", deletes, err)
	}
}

func TestDatabaseAndBrokerCascadesRequireReviewedNativeChildren(t *testing.T) {
	const p = "projects/sample-project/"
	for _, test := range []struct{ parent, child, name, collection, childName string }{
		{"spanner.googleapis.com/Instance", "spanner.googleapis.com/Database", p + "instances/sql", "databases", p + "instances/sql/databases/app"},
		{"bigtableadmin.googleapis.com/Instance", "bigtableadmin.googleapis.com/Table", p + "instances/wide", "tables", p + "instances/wide/tables/app"},
		{"bigtableadmin.googleapis.com/Instance", "bigtableadmin.googleapis.com/Cluster", p + "instances/wide", "clusters", p + "instances/wide/clusters/zone-a"},
		{"alloydb.googleapis.com/Cluster", "alloydb.googleapis.com/Instance", p + "locations/us-central1/clusters/sql", "instances", p + "locations/us-central1/clusters/sql/instances/primary"},
		{"managedkafka.googleapis.com/Cluster", "managedkafka.googleapis.com/Topic", p + "locations/us-central1/clusters/broker", "topics", p + "locations/us-central1/clusters/broker/topics/__remote_log_metadata"},
	} {
		t.Run(test.parent+"/"+test.child, func(t *testing.T) {
			host := strings.Split(test.parent, "/")[0]
			version := "v1"
			if host == "bigtableadmin.googleapis.com" {
				version = "v2"
			}
			parent := asset.Asset{ID: "parent", Identity: asset.Identity{Provider: asset.ProviderGCP, ConnectionID: "connection", NativeType: test.parent, NativeID: "//" + host + "/" + test.name}, Normalized: map[string]any{"name": test.name}, Capabilities: asset.CapabilitySet{asset.CapabilityActionable}}
			child := asset.Asset{ID: "child", Identity: asset.Identity{Provider: asset.ProviderGCP, ConnectionID: "connection", NativeType: test.child, NativeID: "//" + host + "/" + test.childName}, Normalized: map[string]any{"name": test.childName}, Capabilities: asset.CapabilitySet{asset.CapabilityActionable}}
			writes := 0
			tableFailure := ""
			parentGone, childGone := false, false
			transport := func(r *http.Request) (*http.Response, error) {
				if r.URL.Host != host {
					t.Fatal("cross-service request")
				}
				var body any
				switch {
				case r.Method == "DELETE":
					if r.URL.Path != "/"+version+"/"+test.name {
						t.Fatal("delegated child directly deleted")
					}
					if host == "alloydb.googleapis.com" && r.URL.Query().Get("force") != "true" {
						t.Fatal("native cascade parameter missing")
					}
					writes++
					body = map[string]any{}
					if host == "alloydb.googleapis.com" || host == "managedkafka.googleapis.com" {
						body = map[string]any{"name": p + "locations/us-central1/operations/delete"}
					}
				case r.URL.Path == "/"+version+"/"+test.name:
					if parentGone {
						return apiResponse(r, 404, `{}`), nil
					}
					body = parent.Normalized
				case r.URL.Path == "/"+version+"/"+test.childName:
					body = child.Normalized
					if test.child == bigtableTestKind {
						if r.URL.Query().Get("view") != "FULL" {
							t.Fatal("cascade used incomplete table view", r.URL)
						}
						if childGone {
							return apiResponse(r, 404, `{}`), nil
						}
						switch tableFailure {
						case "protected":
							body = map[string]any{"name": test.childName, "deletionProtection": true}
						case "denied":
							return apiResponse(r, 403, `{}`), nil
						case "changed":
							body = map[string]any{"name": test.childName + "-other"}
						}
					}
				case r.URL.Path == "/"+version+"/"+test.name+"/"+test.collection:
					body = map[string]any{test.collection: []any{child.Normalized}}
				case host == "bigtableadmin.googleapis.com" && (strings.HasSuffix(r.URL.Path, "/tables") || strings.HasSuffix(r.URL.Path, "/clusters")):
					body = map[string]any{}
				default:
					t.Fatalf("unexpected native child request: %s", r.URL)
				}
				encoded, _ := json.Marshal(body)
				return apiResponse(r, 200, string(encoded)), nil
			}
			c := &client{project: "sample-project", number: "123456", http: &http.Client{Transport: roundTripFunc(transport)}}
			contribution, err := (&serviceCascades{client: c}).Contribute(context.Background(), "scope", []asset.Asset{parent, child})
			if err != nil || len(contribution.Bindings) != 1 {
				t.Fatalf("cascade not discovered: %+v %v", contribution, err)
			}
			request := contracts.ActionRequest{Asset: parent, Action: "delete"}
			driver := protocolAction(t, test.parent, test.name, transport)
			if _, err := driver.Execute(context.Background(), request); err == nil || writes != 0 {
				t.Fatal("unreviewed database child deleted")
			}
			request.LifecycleImpacts = []contracts.ActionImpact{{Asset: child, ControllerID: parent.ID, Delete: true}}
			if test.child == bigtableTestKind {
				for _, failure := range []string{"protected", "denied", "changed"} {
					tableFailure = failure
					if _, err := driver.Execute(t.Context(), request); err == nil || writes != 0 {
						t.Fatal("unsafe instance cascade", failure, writes, err)
					}
				}
				tableFailure = ""
			}
			if _, err := driver.Execute(context.Background(), request); err != nil || writes != 1 {
				t.Fatalf("reviewed cascade failed: writes=%d %v", writes, err)
			}
			if test.child == bigtableTestKind {
				parentGone = true
				read, err := driver.Readback(t.Context(), request)
				if err != nil || !read.Exists {
					t.Fatal("parent absence hid surviving table", read, err)
				}
				childGone = true
				read, err = driver.Readback(t.Context(), request)
				if err != nil || read.Exists {
					t.Fatal("native table absence not confirmed", read, err)
				}
			}
		})
	}
}

func TestNCCHubOwnsNativeReadOnlyTablesGroupsAndRoutes(t *testing.T) {
	const root = "projects/sample-project/locations/global/hubs/transit"
	const host = "networkconnectivity.googleapis.com"
	records := []struct{ id, kind, name string }{
		{"hub", "Hub", root}, {"group", "Group", root + "/groups/default"},
		{"table", "RouteTable", root + "/routeTables/default"}, {"route", "Route", root + "/routeTables/default/routes/subnet"},
	}
	var values []asset.Asset
	for _, r := range records {
		caps := asset.CapabilitySet{}
		if r.id == "hub" {
			caps = asset.CapabilitySet{asset.CapabilityActionable}
		}
		values = append(values, asset.Asset{ID: asset.AssetID(r.id), Identity: asset.Identity{Provider: asset.ProviderGCP, ConnectionID: "connection", NativeType: host + "/" + r.kind, NativeID: "//" + host + "/" + r.name}, Capabilities: caps, Normalized: map[string]any{"name": r.name, "uid": r.id + "-uid"}})
	}
	deleted, childrenGone := false, false
	writes := 0
	transport := func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != host {
			t.Fatalf("foreign NCC request %s", r.URL)
		}
		if r.Method == "DELETE" {
			if r.URL.Path != "/v1/"+root {
				t.Fatalf("read-only NCC child deletion invented: %s", r.URL)
			}
			deleted = true
			writes++
			return apiResponse(r, 200, `{"name":"projects/sample-project/locations/global/operations/delete"}`), nil
		}
		if r.Method != "GET" {
			t.Fatalf("unexpected NCC method %s", r.Method)
		}
		if r.URL.Path == "/v1/projects/sample-project/locations/global/operations/delete" {
			return apiResponse(r, 200, `{"done":true}`), nil
		}
		for i, value := range values {
			if r.URL.Path == "/v1/"+text(value.Normalized["name"]) {
				if (i == 0 && deleted) || (i > 0 && childrenGone) {
					return apiResponse(r, 404, `{}`), nil
				}
				raw, _ := json.Marshal(value.Normalized)
				return apiResponse(r, 200, string(raw)), nil
			}
		}
		var body any
		switch r.URL.Path {
		case "/v1/projects/sample-project/locations/global/hubs":
			body = map[string]any{"hubs": []any{values[0].Normalized}}
		case "/v1/" + root + "/groups":
			body = map[string]any{"groups": []any{values[1].Normalized}}
		case "/v1/" + root + "/routeTables":
			body = map[string]any{"routeTables": []any{values[2].Normalized}}
		case "/v1/" + root + "/routeTables/default/routes":
			body = map[string]any{"routes": []any{values[3].Normalized}}
		default:
			t.Fatalf("unexpected NCC path %s", r.URL)
		}
		raw, _ := json.Marshal(body)
		return apiResponse(r, 200, string(raw)), nil
	}
	runtime := protocolRuntime(t, transport)
	for _, kind := range []string{"Group", "RouteTable", "Route"} {
		page, err := runtime.List(context.Background(), productRequest(runtime, host+"/"+kind, "global"))
		if err != nil || len(page.Items) != 1 || page.Items[0].Actionable == nil || *page.Items[0].Actionable {
			t.Fatalf("NCC intrinsic inventory %s: %+v %v", kind, page, err)
		}
	}
	c := &client{project: "sample-project", number: "123456", http: &http.Client{Transport: roundTripFunc(transport)}}
	contribution, err := (&serviceCascades{client: c}).Contribute(context.Background(), "scope", values)
	if err != nil || len(contribution.Bindings) != 3 {
		t.Fatalf("NCC ownership %+v %v", contribution, err)
	}
	input := plan.Input{CleanupTaskID: "cleanup", Assets: values, ResolvedAssetIDs: []asset.AssetID{"hub"}, Relationships: contribution.Relationships, LifecycleBindings: contribution.Bindings}
	result, err := plan.Solve(input)
	if err != nil || len(result.Blockers) != 0 || len(result.Steps) != 1 || len(result.ImpactItems) != 3 {
		t.Fatalf("NCC root-only plan %+v %v", result, err)
	}
	request := contracts.ActionRequest{Action: "delete", Asset: values[0]}
	driver := protocolAction(t, host+"/Hub", root, transport)
	if _, err = driver.Execute(context.Background(), request); err == nil || writes != 0 {
		t.Fatal("unreviewed NCC child set deleted")
	}
	for i := 1; i < len(values); i++ {
		controller := values[0].ID
		if i == 3 {
			controller = values[2].ID
		}
		request.LifecycleImpacts = append(request.LifecycleImpacts, contracts.ActionImpact{ControllerID: controller, Asset: values[i], Delete: true})
	}
	actionResult, err := driver.Execute(context.Background(), request)
	if err != nil || writes != 1 {
		t.Fatalf("reviewed NCC delete %v writes=%d", err, writes)
	}
	wait, err := driver.Wait(context.Background(), request, actionResult)
	if err != nil || wait.Done {
		t.Fatalf("NCC parent absence hid existing children %+v %v", wait, err)
	}
	childrenGone = true
	encoded, _ := json.Marshal(request)
	var restored contracts.ActionRequest
	_ = json.Unmarshal(encoded, &restored)
	wait, err = protocolAction(t, host+"/Hub", root, transport).Wait(context.Background(), restored, actionResult)
	if err != nil || !wait.Done || writes != 1 {
		t.Fatalf("NCC recovery %+v %v", wait, err)
	}
	input.RequestOptions = map[asset.AssetID]map[string]any{"hub": {"retain_resources": []string{"route"}}}
	result, err = plan.Solve(input)
	if err != nil || len(result.Blockers) == 0 {
		t.Fatalf("native NCC route retention accepted %+v %v", result, err)
	}
}
