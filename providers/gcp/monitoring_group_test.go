package gcp

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/loomx-ai/steward/internal/app/inventory"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const testMonitoringGroupName = "projects/sample-project/groups/9876"
const testMonitoringGroupID = "//monitoring.googleapis.com/" + testMonitoringGroupName

func monitoringGroupFixture() map[string]any {
	return map[string]any{"name": testMonitoringGroupName, "displayName": "Group", "filter": `resource.type = "gce_instance" AND metadata.user_labels.private = "PRIVATE_GROUP"`, "isCluster": false, "futureField": "bound"}
}
func monitoringGroupMemberFixture() map[string]any {
	return map[string]any{"type": "gce_instance", "labels": map[string]any{"project_id": "sample-project", "zone": "us-central1-a", "instance_id": "87654321"}}
}

type monitoringGroupScenario struct {
	r                  *Runtime
	mode               string
	group              map[string]any
	members            []any
	reads, memberReads int
	interval           string
}

func newMonitoringGroupScenario(t *testing.T) *monitoringGroupScenario {
	t.Helper()
	s := &monitoringGroupScenario{group: monitoringGroupFixture(), members: []any{monitoringGroupMemberFixture()}}
	s.r = protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
		var body []byte
		if req.Body != nil {
			body, _ = io.ReadAll(req.Body)
		}
		if req.Method != "GET" || len(body) != 0 || req.URL.Host != "monitoring.googleapis.com" {
			t.Fatal("unexpected group mutation or host", req.Method, req.URL)
		}
		data := map[string]any{}
		switch req.URL.Path {
		case "/v3/projects/sample-project/groups":
			if req.URL.Query().Get("pageSize") != "100" {
				t.Fatal(req.URL)
			}
			for k := range req.URL.Query() {
				if k != "pageSize" && k != "pageToken" {
					t.Fatal("group list filtered", req.URL)
				}
			}
			if s.mode == "list-denied" {
				return apiResponse(req, 403, `{}`), nil
			}
			data["group"] = []any{s.group}
			if s.mode == "empty" {
				data["group"] = []any{}
			}
			if s.mode == "list-paged" && req.URL.Query().Get("pageToken") == "" {
				data["group"] = []any{}
				data["nextPageToken"] = "next"
			}
			if s.mode == "list-null" {
				data["group"] = nil
			}
			if s.mode == "list-duplicate" {
				data["group"] = []any{s.group, s.group}
			}
			if s.mode == "list-partial" {
				data["unreachable"] = []any{"unknown"}
			}
		case "/v3/" + testMonitoringGroupName:
			if req.URL.RawQuery != "" {
				t.Fatal(req.URL)
			}
			s.reads++
			if s.mode == "get-denied" || s.mode == "late-denied" && s.reads > 1 {
				return apiResponse(req, 403, `{}`), nil
			}
			if s.mode == "get-missing" {
				return apiResponse(req, 404, `{}`), nil
			}
			for k, v := range s.group {
				data[k] = v
			}
			if s.mode == "get-drift" || s.mode == "late-drift" && s.reads > 1 {
				data["filter"] = `resource.type = "different"`
			}
		case "/v3/" + testMonitoringGroupName + "/members":
			s.memberReads++
			query := req.URL.Query()
			if query.Get("pageSize") != "100" || query.Get("filter") != "" {
				t.Fatal("member list filtered", req.URL)
			}
			for k := range query {
				if k != "pageSize" && k != "pageToken" && k != "interval.startTime" && k != "interval.endTime" {
					t.Fatal(req.URL)
				}
			}
			start, e1 := time.Parse(time.RFC3339, query.Get("interval.startTime"))
			end, e2 := time.Parse(time.RFC3339, query.Get("interval.endTime"))
			if e1 != nil || e2 != nil || end.Sub(start) != time.Minute || end.After(time.Now().UTC()) {
				t.Fatal("invalid member interval", req.URL)
			}
			interval := query.Get("interval.startTime") + "/" + query.Get("interval.endTime")
			if s.interval != "" && s.interval != interval {
				t.Fatal("interval changed within snapshot")
			}
			s.interval = interval
			if s.mode == "members-denied" || s.mode == "members-page-denied" && query.Get("pageToken") != "" {
				return apiResponse(req, 403, `{}`), nil
			}
			if s.mode == "members-missing" {
				return apiResponse(req, 404, `{}`), nil
			}
			data["members"] = s.members
			data["totalSize"] = len(s.members)
			if s.mode == "members-paged" || s.mode == "members-page-denied" {
				if query.Get("pageToken") == "" {
					data["members"] = []any{}
					data["nextPageToken"] = "next"
				}
			}
			if s.mode == "members-loop" {
				data["nextPageToken"] = "loop"
			}
			if s.mode == "members-null" {
				data["members"] = nil
			}
			if s.mode == "members-duplicate" {
				data["members"] = append(append([]any{}, s.members...), s.members...)
				data["totalSize"] = 2 * len(s.members)
			}
			if s.mode == "members-total" {
				data["totalSize"] = len(s.members) + 1
			}
			if s.mode == "members-total-null" {
				data["totalSize"] = nil
			}
			if s.mode == "members-total-fraction" {
				data["totalSize"] = 1.5
			}
			if s.mode == "members-partial" {
				data["unreachable"] = []any{"unknown"}
			}
			if s.mode == "members-drift" && s.memberReads > 1 {
				data["members"] = []any{}
				data["totalSize"] = 0
			}
		default:
			t.Fatal("unexpected group endpoint", req.URL)
		}
		b, _ := json.Marshal(data)
		return apiResponse(req, 200, string(b)), nil
	})
	return s
}

func TestMonitoringGroupInventorySnapshot(t *testing.T) {
	for _, mode := range []string{"normal", "empty", "alias", "parent", "members-empty", "members-paged", "list-paged", "list-denied", "list-null", "list-duplicate", "list-partial", "get-denied", "get-missing", "get-drift", "late-denied", "late-drift", "members-denied", "members-missing", "members-page-denied", "members-loop", "members-null", "members-duplicate", "members-total", "members-total-null", "members-total-fraction", "members-partial", "members-drift", "foreign", "foreign-parent", "self-parent", "filter-missing", "filter-null", "member-label-null", "member-id-invalid"} {
		t.Run(mode, func(t *testing.T) {
			s := newMonitoringGroupScenario(t)
			s.mode = mode
			good := mode == "normal" || mode == "empty" || mode == "alias" || mode == "parent" || mode == "members-empty" || mode == "members-paged" || mode == "list-paged"
			switch mode {
			case "alias":
				s.group["name"] = strings.Replace(testMonitoringGroupName, "sample-project", "123456", 1)
			case "parent":
				s.group["parentName"] = "projects/123456/groups/1234"
			case "members-empty":
				s.members = []any{}
			case "foreign":
				s.group["name"] = "projects/foreign-project/groups/9876"
			case "foreign-parent":
				s.group["parentName"] = "projects/foreign-project/groups/1234"
			case "self-parent":
				s.group["parentName"] = testMonitoringGroupName
			case "filter-missing":
				delete(s.group, "filter")
			case "filter-null":
				s.group["filter"] = nil
			case "member-label-null":
				object(s.members[0])["labels"] = nil
			case "member-id-invalid":
				object(object(s.members[0])["labels"])["instance_id"] = "named-vm"
			}
			req := productRequest(s.r, monitoringGroupType, "global")
			batch, err := s.r.List(t.Context(), req)
			if mode == "list-paged" {
				if err != nil || batch.Complete || batch.NextCursor == "" {
					t.Fatal(batch, err)
				}
				req.Cursor = batch.NextCursor
				batch, err = s.r.List(t.Context(), req)
			}
			if (err == nil) != good {
				t.Fatal(batch, err)
			}
			if !good {
				if batch.Complete || len(batch.Items) != 0 {
					t.Fatal("invalid snapshot emitted inventory")
				}
				return
			}
			if mode == "empty" {
				if !batch.Complete || len(batch.Items) != 0 {
					t.Fatal(batch)
				}
				return
			}
			if !batch.Complete || len(batch.Items) != 1 {
				t.Fatal(batch)
			}
			item := batch.Items[0]
			encoded, _ := json.Marshal(item)
			if strings.Contains(string(encoded), "PRIVATE_GROUP") || item.Actionable == nil || *item.Actionable || item.Normalized[monitoringGroupReview] == nil || item.Normalized["_monitoring_group_member_interval"] == nil {
				t.Fatal(item)
			}
			if item.NativeID != testMonitoringGroupID || item.ResourceKind.NativeType != monitoringGroupType {
				t.Fatal(item)
			}
			if mode != "members-empty" && len(object(item.Normalized[monitoringGroupMembers])[instanceType].([]any)) != 1 {
				t.Fatal(item)
			}
			if mode == "parent" && len(item.NetworkReferences) != 2 {
				t.Fatal(item.NetworkReferences)
			}
		})
	}
}

func TestMonitoringGroupNativeSchemasAndRedaction(t *testing.T) {
	compiler := infraFixtureSchemas(t, "fixtures/monitoring-group/schemas.json", "20260903", "9273bd1f36bbc4c94fb948450a2cf63f876e04ca8a1b331fd80158b152907019")
	group := monitoringGroupFixture()
	delete(group, "futureField")
	for name, value := range map[string]any{"Group": group, "ListGroupsResponse": map[string]any{"group": []any{group}}, "ListGroupMembersResponse": map[string]any{"members": []any{monitoringGroupMemberFixture()}, "totalSize": 1}} {
		schema, err := compiler.Compile("https://fixture.test/infra-manager.json#/definitions/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if err := schema.Validate(value); err != nil {
			t.Fatal(err)
		}
	}
	s := newMonitoringGroupScenario(t)
	for _, operation := range []string{"monitoring.projects.groups.get", monitoringGroupMembersList} {
		params := map[string]any{"name": testMonitoringGroupName}
		if operation == monitoringGroupMembersList {
			end := time.Now().UTC().Truncate(time.Second)
			params["interval.endTime"] = end.Format(time.RFC3339)
			params["interval.startTime"] = end.Add(-time.Minute).Format(time.RFC3339)
			params["pageSize"] = 100
			object(object(s.members[0])["labels"])["arbitrary"] = "PRIVATE_MEMBER"
		}
		result, err := s.r.Invoke(t.Context(), contracts.Invocation{ConnectionID: "connection", Operation: operation, Parameters: params})
		if err != nil {
			t.Fatal(err)
		}
		encoded, _ := json.Marshal(result)
		if strings.Contains(string(encoded), "PRIVATE_") {
			t.Fatal("private group data escaped Invoke")
		}
	}
}

func TestMonitoringGroupNetworkAndTargetGraph(t *testing.T) {
	s := newMonitoringGroupScenario(t)
	s.group["parentName"] = "projects/sample-project/groups/1234"
	s.members = append(s.members, map[string]any{"type": "aws_ec2_instance", "labels": map[string]any{"instance_id": "PRIVATE_AWS"}})
	batch, err := s.r.List(t.Context(), productRequest(s.r, monitoringGroupType, "global"))
	if err != nil {
		t.Fatal(err)
	}
	groupItem := batch.Items[0]
	c := &client{project: "sample-project", number: "123456"}
	vm, err := s.r.inventoryItem(c, map[string]any{"name": uptimeVM, "assetType": instanceType, "resource": map[string]any{"location": "us-central1-a", "data": map[string]any{"id": "87654321", "name": "vm", "networkInterfaces": []any{map[string]any{"network": uptimeNetwork}}}}})
	if err != nil {
		t.Fatal(err)
	}
	checkData := uptimeFixture()
	delete(checkData, "monitoredResource")
	checkData["resourceGroup"] = map[string]any{"groupId": "9876", "resourceType": "INSTANCE"}
	check, err := s.r.inventoryItem(c, map[string]any{"name": uptimeID, "assetType": uptimeType, "resource": map[string]any{"location": "global", "data": checkData}})
	if err != nil {
		t.Fatal(err)
	}
	members := inventory.FilterNetworkClosure(asset.ScanTarget{Kind: asset.ScanTargetVPC, NativeID: uptimeNetwork}, []contracts.InventoryItem{check, groupItem, vm})
	if len(members) != 3 {
		t.Fatal("group chain not included in network scope", members)
	}
	convert := func(id string, item contracts.InventoryItem) asset.Asset {
		return asset.Asset{ID: asset.AssetID(id), Identity: asset.Identity{Provider: asset.ProviderGCP, Partition: "gcp", ConnectionID: "connection", NativeType: item.NativeType, NativeID: item.NativeID}, Normalized: item.Normalized}
	}
	source, target, uptime := convert("group", groupItem), convert("vm", vm), convert("check", check)
	result, err := NewUptimeTargets().Contribute(t.Context(), "global", []asset.Asset{source, target, uptime})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Relationships) != 2 || len(result.Unresolved) != 2 || len(result.Bindings) != 0 {
		t.Fatal(result)
	}
	for _, edge := range result.Relationships {
		if edge.Type != graph.RelationshipDependsOn || edge.Evidence[graph.RelationshipEvidenceRequiredDeletion] == true {
			t.Fatal("monitoring membership authorized cleanup", edge)
		}
	}
	for _, ref := range result.Unresolved {
		if ref.BlocksCleanup {
			t.Fatal("observation became ownership proof", ref)
		}
	}
	encoded, _ := json.Marshal(result)
	if strings.Contains(string(encoded), "PRIVATE_") {
		t.Fatal("private member data escaped graph")
	}
	target.Normalized = map[string]any{"id": "87654322"}
	result, err = NewUptimeTargets().Contribute(t.Context(), "global", []asset.Asset{source, target, uptime})
	if err != nil || len(result.Relationships) != 1 {
		t.Fatal("recreated VM matched old numeric identity", result, err)
	}
}
