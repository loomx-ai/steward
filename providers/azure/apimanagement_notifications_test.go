package azure

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func apimRecipientScenario(t *testing.T, namespace, recipient string) (*dnsScenario, *Runtime, []asset.Asset, asset.Asset, asset.Asset) {
	t.Helper()
	s, r, assets := apimScenario(t)
	notification := cdnAsset(t, assets, namespace+"/notifications")
	user := cdnAsset(t, assets, apimServiceType+"/users")
	example := "ApiManagementListNotificationRecipientUsers.json"
	if recipient == "recipientEmails" {
		example = "ApiManagementListNotificationRecipientEmails.json"
	}
	raw := maps.Clone(object(array(apimExample(t, example)["value"])[0]))
	name := last(user.Identity.NativeID)
	if recipient == "recipientEmails" {
		name = text(raw["name"])
	}
	kind := namespace + "/notifications/" + recipient
	id := notification.Identity.NativeID + "/" + strings.ToLower(recipient) + "/" + name
	raw["id"], raw["name"], raw["type"] = id, name, kind
	if recipient == "recipientUsers" {
		object(raw["properties"])["userId"] = user.Identity.NativeID
	}
	s.add(raw, apimVersion)
	collection := strings.TrimSuffix(id, "/"+name)
	s.lists[collection] = []any{raw}
	recipients := object(object(s.records[notification.Identity.NativeID]["properties"])["recipients"])
	if recipient == "recipientEmails" {
		recipients["emails"] = []any{id}
	} else {
		recipients["users"] = []any{user.Identity.NativeID}
	}
	base := s.handle
	s.handle = func(req *http.Request) (*http.Response, bool) {
		if strings.EqualFold(req.URL.Path, id) {
			if req.Method == "GET" {
				t.Fatal("recipient has no native GET", req.URL)
			}
			if req.Method == "HEAD" {
				status := 204
				if s.gone[id] {
					status = 404
				}
				if s.status[id] != 0 {
					status = s.status[id]
				}
				return jsonResponse(status, nil, nil), true
			}
			if req.Method == "DELETE" {
				if req.Header.Get("If-Match") != "" {
					t.Fatal("unsupported recipient If-Match")
				}
			}
		}
		if req.Method == "GET" && strings.EqualFold(req.URL.Path, notification.Identity.NativeID) && s.gone[id] && !s.gone[notification.Identity.NativeID] {
			body := maps.Clone(s.records[notification.Identity.NativeID])
			body["properties"] = maps.Clone(object(body["properties"]))
			object(body["properties"])["recipients"] = map[string]any{"users": []any{}, "emails": []any{}}
			delete(body, "_apim_header_etag")
			return jsonResponse(200, body, http.Header{"ETag": {text(s.records[notification.Identity.NativeID]["_apim_header_etag"])}}), true
		}
		return base(req)
	}
	c, _ := r.resolve(context.Background(), "connection")
	live, err := c.apimResource(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	value := dnsAsset(t, r, live)
	assets = append(assets, value)
	return s, r, assets, value, user
}

func TestAPIMNotificationRecipientsUseNativeHEADAndReviewedOwnership(t *testing.T) {
	for _, namespace := range []string{apimServiceType, apimWorkspaceType} {
		for _, recipient := range []string{"recipientUsers", "recipientEmails"} {
			for _, mode := range []string{"inventory", "unlink", "service-cascade", "user-prerequisite"} {
				t.Run(namespace+"/"+recipient+"/"+mode, func(t *testing.T) {
					if mode == "user-prerequisite" && recipient == "recipientEmails" {
						return
					}
					s, r, assets, value, user := apimRecipientScenario(t, namespace, recipient)
					if mode == "inventory" {
						batch, err := r.List(context.Background(), productRequest(r, value.Identity.NativeType))
						if err != nil || len(batch.Items) != 1 || !batch.Complete {
							t.Fatal("native recipient inventory", len(batch.Items), err)
						}
						if batch.Items[0].NativeID != value.Identity.NativeID || batch.Items[0].Location != "westus" || batch.Items[0].Actionable == nil || !*batch.Items[0].Actionable {
							t.Fatal("native recipient inventory identity")
						}
						notification := cdnAsset(t, assets, namespace+"/notifications")
						parents, err := r.List(context.Background(), productRequest(r, notification.Identity.NativeType))
						if err != nil || len(parents.Items) != 1 || parents.Items[0].Actionable == nil || *parents.Items[0].Actionable {
							t.Fatal("notification incorrectly actionable", err)
						}
						if _, err := r.ResolveAction(context.Background(), "connection", notification); err == nil {
							t.Fatal("invented notification DELETE")
						}
						return
					}
					target := value
					if mode == "service-cascade" {
						target = cdnAsset(t, assets, namespace)
					}
					if mode == "user-prerequisite" {
						target = user
					}
					request, input := dnsRequest(t, r, assets, target)
					solved, err := plan.Solve(input)
					if err != nil {
						t.Fatal(err)
					}
					if mode == "user-prerequisite" {
						if !slices.ContainsFunc(request.PrerequisiteDeletions, func(p contracts.ActionImpact) bool { return p.Asset.ID == value.ID }) {
							t.Fatal("user recipient is not a deletion prerequisite")
						}
						driver, _ := r.ResolveAction(context.Background(), "connection", user)
						if _, err := driver.Execute(context.Background(), request); err == nil || len(s.deletes) != 0 {
							t.Fatal("subscribed user deleted before recipient removal")
						}
					}
					if mode == "service-cascade" && !slices.ContainsFunc(request.LifecycleImpacts, func(p contracts.ActionImpact) bool { return p.Asset.ID == value.ID }) {
						t.Fatal("notification recipient missing from service review")
					}
					for _, step := range solved.Steps {
						var selected asset.Asset
						for _, a := range assets {
							if a.ID == step.AssetID {
								selected = a
							}
						}
						req := servicePlanRequest(solved, assets, selected)
						encoded, _ := json.Marshal(req)
						json.Unmarshal(encoded, &req)
						driver, err := r.ResolveAction(context.Background(), "connection", selected)
						if err != nil {
							t.Fatal(err)
						}
						result, err := driver.Execute(context.Background(), req)
						if err != nil {
							t.Fatal("notification cleanup", selected.Identity.NativeType, err)
						}
						streamAnalyticsAfterDelete(s)
						waited, err := driver.Wait(context.Background(), req, result)
						if err != nil || !waited.Done {
							t.Fatal("native recipient absence", waited, err)
						}
					}
					if mode == "unlink" && (len(s.deletes) != 1 || s.deletes[0] != value.Identity.NativeID || s.gone[user.Identity.NativeID]) {
						t.Fatal("unlink deleted recipient user", s.deletes)
					}
				})
			}
		}
	}
}

func TestAPIMNotificationIndexDisagreementAndRecipientDriftBlockWrites(t *testing.T) {
	reviews := apimReviewCache{}
	for _, namespace := range []string{apimServiceType, apimWorkspaceType} {
		for _, recipient := range []string{"recipientUsers", "recipientEmails"} {
			for _, mode := range []string{"missing-index", "invalid-index", "foreign-index", "duplicate-index", "wrong-selector", "wrong-name", "foreign-association", "head-denied", "list-denied", "parent-protected", "new-recipient", "retained-recipient"} {
				t.Run(namespace+"/"+recipient+"/"+mode, func(t *testing.T) {
					s, r, assets, value, _ := apimRecipientScenario(t, namespace, recipient)
					target := cdnAsset(t, assets, namespace)
					request, input := reviews.request(t, r, assets, target)
					parent := redisParentID(value.Identity.NativeID)
					raw := s.records[value.Identity.NativeID]
					index := object(object(s.records[parent]["properties"])["recipients"])
					field := "users"
					if recipient == "recipientEmails" {
						field = "emails"
					}
					switch mode {
					case "missing-index":
						index[field] = []any{}
					case "invalid-index":
						index[field] = "not-an-array"
					case "foreign-index":
						index[field] = []any{strings.Replace(text(array(index[field])[0]), "/stewardtest/", "/foreign/", 1)}
					case "duplicate-index":
						index[field] = append(array(index[field]), array(index[field])[0])
					case "wrong-selector":
						if recipient == "recipientUsers" {
							object(raw["properties"])["userId"] = apimRootID(parent) + "/users/other"
						} else {
							object(raw["properties"])["email"] = "different@example.com"
						}
					case "wrong-name":
						raw["name"] = "other"
					case "foreign-association":
						raw["id"] = strings.Replace(value.Identity.NativeID, "/stewardtest/", "/foreign/", 1)
					case "head-denied":
						s.status[value.Identity.NativeID] = 403
					case "list-denied":
						s.status[strings.TrimSuffix(value.Identity.NativeID, "/"+last(value.Identity.NativeID))] = 403
					case "parent-protected":
						s.records[parent]["tags"] = map[string]any{"steward:protect": "true"}
					case "new-recipient":
						collection := strings.TrimSuffix(value.Identity.NativeID, "/"+last(value.Identity.NativeID))
						new := maps.Clone(raw)
						new["id"], new["name"] = collection+"/new", "new"
						s.lists[collection] = append(s.lists[collection], new)
					case "retained-recipient":
						input.RequestOptions = map[asset.AssetID]map[string]any{target.ID: {"retain_resources": []string{value.Identity.NativeID}}}
						solved, err := plan.Solve(input)
						if err != nil || len(solved.Blockers) == 0 {
							t.Fatal("retained recipient did not block service", err)
						}
						return
					}
					driver, err := r.ResolveAction(context.Background(), "connection", target)
					if err != nil {
						t.Fatal(err)
					}
					if _, err := driver.Execute(context.Background(), request); err == nil || len(s.deletes) != 0 {
						t.Fatal("incomplete or unreviewed notification configuration deleted", mode, err, s.deletes)
					}
				})
			}
		}
	}
}
