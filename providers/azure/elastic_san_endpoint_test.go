package azure

import (
	"encoding/json"
	"maps"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func newElasticSanEndpointCleanupFixture(t *testing.T) *elasticSanCleanupFixture {
	f := newElasticSanCleanupFixture(t)
	f.kind = elasticSanEndpointType
	return f
}

func TestElasticSanEndpointNativeDeleteAndDisconnection(t *testing.T) {
	for _, status := range []string{"Pending", "Approved", "Rejected", "Disconnected"} {
		t.Run(status, func(t *testing.T) {
			f := newElasticSanEndpointCleanupFixture(t)
			raw := f.values[f.ids[f.kind]]
			connection := object(object(raw["properties"])["privateLinkServiceConnectionState"])
			connection["status"] = status
			a, req := f.action(t)
			result, err := a.Execute(t.Context(), req)
			if err != nil || f.deletes != 1 {
				t.Fatal("native connection delete", err)
			}
			connection["status"], connection["actionsRequired"] = "Disconnected", "None"
			wait, err := a.Wait(t.Context(), req, result)
			if err != nil || wait.Done {
				t.Fatal("disconnection closed live connection", wait, err)
			}
			result.Data = wait.Data
			req.ExecutionResult = &result
			if _, err := a.Execute(t.Context(), req); err != nil || f.deletes != 1 {
				t.Fatal("replayed DELETE", err)
			}
			delete(f.values, f.ids[f.kind])
			wait, err = a.Wait(t.Context(), req, result)
			if err != nil || !wait.Done || f.polls != 1 {
				t.Fatal("independent connection absence", wait, err)
			}
			for _, kind := range []string{elasticSanType, elasticSanGroupType, elasticSanVolumeType, elasticSanSnapshotType} {
				if f.values[f.ids[kind]] == nil {
					t.Fatal("deleted a related storage resource", kind)
				}
			}
		})
	}
}

func TestElasticSanEndpointPreflightBoundaries(t *testing.T) {
	for _, mode := range []string{"status", "created", "target", "groups", "description", "etag", "group-tag", "san-tag", "group-lock", "group-denied", "missing-group", "missing-san", "group-deleting", "san-region", "proof", "parameters"} {
		t.Run(mode, func(t *testing.T) {
			f := newElasticSanEndpointCleanupFixture(t)
			a, req := f.action(t)
			raw, group := f.values[f.ids[f.kind]], f.values[f.ids[elasticSanGroupType]]
			props := object(raw["properties"])
			wait := false
			switch mode {
			case "status":
				object(props["privateLinkServiceConnectionState"])["status"] = "Approved"
			case "created":
				object(raw["systemData"])["createdAt"] = "2026-03-11T09:51:01Z"
			case "target":
				object(props["privateEndpoint"])["id"] = strings.ToLower(resourceID(privateEndpointType, "other"))
			case "groups":
				props["groupIds"] = []any{f.ids[elasticSanGroupType] + "-retained"}
			case "description":
				object(props["privateLinkServiceConnectionState"])["description"] = "changed"
			case "etag":
				raw["etag"] = "changed"
			case "group-tag":
				group["tags"] = map[string]any{"steward:protected": "true"}
			case "san-tag":
				f.values[f.ids[elasticSanType]]["tags"] = map[string]any{"steward:protected": "true"}
			case "group-lock":
				f.locks = []any{map[string]any{"id": f.ids[elasticSanGroupType] + "/providers/Microsoft.Authorization/locks/hold", "properties": map[string]any{"level": "CanNotDelete"}}}
			case "group-denied":
				f.hook = func(r *http.Request) (*http.Response, bool) {
					if strings.EqualFold(r.URL.Path, f.ids[elasticSanGroupType]) {
						return jsonResponse(403, map[string]any{"error": map[string]any{"code": "AuthorizationFailed"}}, nil), true
					}
					return nil, false
				}
			case "missing-group":
				delete(f.values, f.ids[elasticSanGroupType])
				wait = true
			case "missing-san":
				delete(f.values, f.ids[elasticSanType])
				wait = true
			case "group-deleting":
				object(group["properties"])["provisioningState"] = "Deleting"
				wait = true
			case "san-region":
				f.values[f.ids[elasticSanType]]["location"] = "westus"
			case "proof":
				req.Asset.Normalized = maps.Clone(req.Asset.Normalized)
				req.Asset.Normalized[elasticSanSnapshotCleanupProof] = "forged"
			case "parameters":
				req.Parameters = map[string]any{"force": true}
			}
			result, err := a.Execute(t.Context(), req)
			if f.deletes != 0 || !wait && err == nil || wait && err != nil {
				t.Fatal("unsafe mutation or lost wait", f.deletes, err)
			}
			if wait {
				out, err := a.Wait(t.Context(), req, result)
				if err != nil || out.Done {
					t.Fatal("missing/deleting ancestry closed live connection", out, err)
				}
			}
		})
	}
}

func TestElasticSanEndpointIncompleteInventoryCannotAuthorizeDelete(t *testing.T) {
	for _, mode := range []string{"created", "empty-groups", "opaque-groups", "duplicate-groups", "missing-target", "status"} {
		t.Run(mode, func(t *testing.T) {
			f := newElasticSanEndpointCleanupFixture(t)
			raw := f.values[f.ids[f.kind]]
			props := object(raw["properties"])
			switch mode {
			case "created":
				delete(raw, "systemData")
			case "empty-groups":
				props["groupIds"] = []any{}
			case "opaque-groups":
				props["groupIds"] = []any{"volumegroup"}
			case "duplicate-groups":
				props["groupIds"] = []any{f.ids[elasticSanGroupType], f.ids[elasticSanGroupType]}
			case "missing-target":
				delete(props, "privateEndpoint")
			case "status":
				object(props["privateLinkServiceConnectionState"])["status"] = "Unrecognized"
			}
			batch, err := f.runtime.List(t.Context(), f.request(f.kind))
			if err != nil || len(batch.Items) != 1 || *batch.Items[0].Actionable {
				t.Fatal("incomplete identity authorized deletion", err)
			}
			if _, err := f.runtime.ResolveAction(t.Context(), "connection", elasticSanTestAsset(batch.Items[0])); err == nil {
				t.Fatal("protected connection has driver")
			}
		})
	}
}

func TestElasticSanEndpointExpiredCallbackAndRecreation(t *testing.T) {
	f := newElasticSanEndpointCleanupFixture(t)
	a, req := f.action(t)
	result, err := a.Execute(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	f.pollStatus = 404
	if out, err := a.Wait(t.Context(), req, result); err == nil || out.Done {
		t.Fatal("expired callback closed live endpoint")
	}
	delete(f.values, f.ids[elasticSanType])
	delete(f.values, f.ids[elasticSanGroupType])
	if out, err := a.Wait(t.Context(), req, result); err == nil || out.Done {
		t.Fatal("missing parents closed live endpoint")
	}
	object(f.values[f.ids[f.kind]]["systemData"])["createdAt"] = "2026-04-01T01:00:00Z"
	if out, err := a.Wait(t.Context(), req, result); err == nil || out.Done {
		t.Fatal("same-name recreation closed")
	}
	delete(f.values, f.ids[f.kind])
	if out, err := a.Wait(t.Context(), req, result); err != nil || !out.Done {
		t.Fatal("own absence failed", out, err)
	}
}

func TestElasticSanEndpointOfficialGroupReferences(t *testing.T) {
	// Use the unchanged upstream example; substitute only its documented ARM
	// placeholders in memory. This is protocol evidence, not a cloud recording.
	raw, err := os.ReadFile("fixtures/elastic-san/PrivateEndpointConnections_Get_MaximumSet_Gen.json")
	if err != nil {
		t.Fatal(err)
	}
	var example map[string]any
	if err = json.Unmarshal(raw, &example); err != nil {
		t.Fatal(err)
	}
	body := object(object(object(example["responses"])["200"])["body"])
	encoded, _ := json.Marshal(body)
	replacer := strings.NewReplacer("{subscriptionId}", testSubscription, "{resourceGroupName}", "test", "{elasticSanName}", "san", "{privateEndpointConnectionName}", "connection", "{volumeGroupName}", "group", "{privateEndpointName}", "endpoint")
	if err = json.Unmarshal([]byte(replacer.Replace(string(encoded))), &body); err != nil {
		t.Fatal(err)
	}
	f := newElasticSanEndpointCleanupFixture(t)
	groups, err := f.client.elasticSanEndpointGroups(body)
	if err != nil || len(groups) != 1 {
		t.Fatal("native group mapping", groups, err)
	}
	refs, err := elasticSanReferences(strings.ToLower(text(body["id"])), elasticSanEndpointType, body)
	if err != nil || !slices.Equal(refs[elasticSanGroupType], groups) || len(refs[privateEndpointType]) != 1 {
		t.Fatal("native group graph", refs, err)
	}
	if reason := f.client.elasticSanChildProtection(elasticSanEndpointType, body); reason != "" {
		t.Fatal(reason)
	}
	batch, err := f.runtime.List(t.Context(), f.request(f.kind))
	if err != nil || len(batch.Items) != 1 {
		t.Fatal(err)
	}
	if !slices.Contains(batch.Items[0].NetworkReferences, f.ids[subnetType]) {
		t.Fatal("volume group network closure missing")
	}
	// The consumer endpoint is a reference, never a deletion or ownership member.
	a, req := f.action(t)
	req.LifecycleImpacts = []contracts.ActionImpact{{}}
	if _, err := a.Execute(t.Context(), req); err == nil || f.deletes != 0 {
		t.Fatal("accepted unreviewed cascading effect")
	}
}
