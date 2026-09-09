package azure

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func recoveryScenario(t *testing.T, namespace string, bare bool) (*dnsScenario, *Runtime, []asset.Asset) {
	t.Helper()
	s, r, raw := recoveryInventoryScenario(t, namespace, bare)
	for i := 0; i < 2; i++ {
		id := text(raw[i]["id"])
		collections := []string{"queues", "topics", "authorizationrules", "disasterrecoveryconfigs", "migrationconfigurations", "privateendpointconnections", "networkrulesets"}
		if namespace == eventHubNamespaceType {
			collections = []string{"eventhubs", "authorizationrules", "disasterrecoveryconfigs", "schemagroups", "applicationgroups", "privateendpointconnections", "networkrulesets", "networksecurityperimeterconfigurations"}
		}
		for _, suffix := range collections {
			s.lists[id+"/"+suffix] = []any{}
		}
		s.lists[id+"/disasterrecoveryconfigs"] = []any{raw[i+2]}
		auth := map[string]any{"id": text(raw[i+2]["id"]) + "/authorizationrules/auth", "type": namespace + "/disasterRecoveryConfigs/authorizationRules", "properties": map[string]any{"rights": []any{"Listen"}}}
		s.add(auth, "2024-01-01")
		s.lists[text(raw[i+2]["id"])+"/authorizationrules"] = []any{auth}
		raw = append(raw, auth)
	}
	for i := 0; i < 2; i++ {
		kind, suffix := serviceBusQueueType, "queues"
		if namespace == eventHubNamespaceType {
			kind, suffix = eventHubType, "eventhubs"
		}
		id := text(raw[i]["id"]) + "/" + suffix + "/entity"
		entity := map[string]any{"id": id, "type": kind, "properties": map[string]any{"createdAt": "entity-creation"}}
		s.add(entity, "2024-01-01")
		s.lists[text(raw[i]["id"])+"/"+suffix] = []any{entity}
		s.lists[id+"/authorizationrules"] = []any{}
		if namespace == eventHubNamespaceType {
			s.lists[id+"/consumergroups"] = []any{}
		}
		raw = append(raw, entity)
	}
	assets := []asset.Asset{}
	for _, value := range raw {
		assets = append(assets, dnsAsset(t, r, value))
	}
	return s, r, assets
}

func TestRecoveryPairChangesCannotAuthorizeNativeMutation(t *testing.T) {
	for _, namespace := range []string{serviceBusNamespaceType, eventHubNamespaceType} {
		for _, afterBreak := range []bool{false, true} {
			for _, mode := range []string{"own-recreated", "peer-recreated", "peer-403", "peer-404", "peer-206", "peer-protected", "peer-group-managed", "peer-lock", "alias-lock", "peer-alias-protected", "peer-alias-missing", "peer-promoted", "peer-config-changed", "backlink-changed", "own-pair-changed", "own-config-changed", "malformed-partner", "malformed-pending", "new-auth", "auth-list-403", "auth-list-206", "missing-impact", "foreign-impact", "missing-config-proof", "missing-peer-proof", "own-missing"} {
				t.Run(namespace+map[bool]string{false: "/before/", true: "/after/"}[afterBreak]+mode, func(t *testing.T) {
					s, r, assets := recoveryScenario(t, namespace, false)
					primary, peer := assets[2], assets[3]
					request, _ := dnsRequest(t, r, assets, primary)
					driver, _ := r.ResolveAction(context.Background(), "connection", primary)
					posts := 0
					s.handle = func(req *http.Request) (*http.Response, bool) {
						if req.Method == "POST" {
							posts++
							object(s.records[primary.Identity.NativeID]["properties"])["provisioningState"] = "Accepted"
							return jsonResponse(200, nil, nil), true
						}
						return nil, false
					}
					var op contracts.ActionResult
					if afterBreak {
						var err error
						op, err = driver.Execute(context.Background(), request)
						if err != nil || posts != 1 {
							t.Fatalf("setup break failed %+v %v", op, err)
						}
					}
					switch mode {
					case "own-recreated", "peer-recreated":
						i := map[string]int{"own-recreated": 0, "peer-recreated": 1}[mode]
						object(s.records[assets[i].Identity.NativeID]["properties"])["createdAt"] = "replacement"
					case "peer-403", "peer-404", "peer-206":
						s.status[assets[1].Identity.NativeID] = map[string]int{"peer-403": 403, "peer-404": 404, "peer-206": 206}[mode]
					case "peer-protected":
						s.records[assets[1].Identity.NativeID]["tags"] = map[string]any{"steward/protected": "true"}
					case "peer-group-managed":
						id := strings.Join(strings.Split(peer.Identity.NativeID, "/")[:5], "/")
						s.add(map[string]any{"id": id, "type": groupType, "managedBy": "external-controller"}, resourcesVersion)
					case "peer-lock", "alias-lock":
						id := assets[1].Identity.NativeID
						if mode == "alias-lock" {
							id = peer.Identity.NativeID
						}
						lockID := id + "/providers/microsoft.authorization/locks/retained"
						s.lists["/subscriptions/"+testSubscription+"/providers/microsoft.authorization/locks"] = []any{map[string]any{"id": lockID, "properties": map[string]any{"level": "CanNotDelete"}}}
					case "peer-alias-protected":
						s.records[peer.Identity.NativeID]["tags"] = map[string]any{"steward:protected": "yes"}
					case "peer-alias-missing":
						s.gone[peer.Identity.NativeID] = true
					case "peer-promoted":
						object(s.records[peer.Identity.NativeID]["properties"])["role"] = "Primary"
					case "peer-config-changed":
						object(s.records[peer.Identity.NativeID]["properties"])["alternateName"] = "replacement"
					case "backlink-changed":
						object(s.records[peer.Identity.NativeID]["properties"])["partnerNamespace"] = assets[1].Identity.NativeID
					case "own-pair-changed":
						object(s.records[primary.Identity.NativeID]["properties"])["partnerNamespace"] = assets[0].Identity.NativeID
					case "own-config-changed":
						object(s.records[primary.Identity.NativeID]["properties"])["alternateName"] = "replacement"
					case "malformed-partner":
						object(s.records[primary.Identity.NativeID]["properties"])["partnerNamespace"] = map[string]any{"invalid": "peer"}
					case "malformed-pending":
						object(s.records[primary.Identity.NativeID]["properties"])["pendingReplicationOperationsCount"] = -1
					case "new-auth":
						id := peer.Identity.NativeID + "/authorizationrules/new"
						raw := map[string]any{"id": id, "type": namespace + "/disasterRecoveryConfigs/authorizationRules", "properties": map[string]any{"rights": []any{"Listen"}}}
						s.add(raw, "2024-01-01")
						s.lists[peer.Identity.NativeID+"/authorizationrules"] = append(s.lists[peer.Identity.NativeID+"/authorizationrules"], raw)
					case "auth-list-403", "auth-list-206":
						s.status[peer.Identity.NativeID+"/authorizationrules"] = map[string]int{"auth-list-403": 403, "auth-list-206": 206}[mode]
					case "missing-impact":
						request.LifecycleImpacts = nil
					case "foreign-impact":
						request.LifecycleImpacts[0].Asset.Identity.ConnectionID = "other"
					case "missing-config-proof":
						delete(request.Asset.Normalized, "_recovery_configuration")
					case "missing-peer-proof":
						delete(request.Asset.Normalized, "_recovery_peer_namespace_creation")
					case "own-missing":
						s.gone[primary.Identity.NativeID] = true
					}
					var err error
					if afterBreak {
						_, err = driver.Wait(context.Background(), request, op)
					} else {
						_, err = driver.Execute(context.Background(), request)
					}
					if mode == "own-missing" && !afterBreak {
						// The completed root still needs native peer/auth absence;
						// Execute performs no further mutation while readback waits.
						read, readErr := driver.Readback(context.Background(), request)
						if readErr != nil || !read.Exists {
							t.Fatalf("missing root hid live views %+v %v", read, readErr)
						}
					} else if err == nil {
						t.Fatal("changed pairing allowed execution")
					}
					var call *contracts.ProviderCallError
					if errors.As(err, &call) && call.Provider.Category == execution.ErrorNotFound {
						t.Fatal("missing dependency was reported as missing cleanup target")
					}
					if len(s.deletes) != 0 || posts != map[bool]int{false: 0, true: 1}[afterBreak] {
						t.Fatalf("unsafe mutation posts=%d deletes=%v", posts, s.deletes)
					}
				})
			}
		}
	}
}

func TestRecoveryPairRequiresCompleteInventoryAndRetainsBothAliasViews(t *testing.T) {
	for _, namespace := range []string{serviceBusNamespaceType, eventHubNamespaceType} {
		for _, mode := range []string{"missing-primary", "missing-secondary", "duplicate-secondary", "wrong-connection", "stale-proof", "retain-peer", "retain-peer-auth"} {
			t.Run(namespace+"/"+mode, func(t *testing.T) {
				_, r, assets := recoveryScenario(t, namespace, false)
				if strings.HasPrefix(mode, "retain-") {
					_, input := dnsRequest(t, r, assets, assets[2])
					retained := assets[3]
					if mode == "retain-peer-auth" {
						retained = assets[5]
					}
					input.RequestOptions = map[asset.AssetID]map[string]any{assets[2].ID: {"retain_resources": []string{retained.Identity.NativeID}}}
					if result, err := plan.Solve(input); err != nil || len(result.Blockers) == 0 {
						t.Fatal("retained alias view deleted implicitly")
					}
					return
				}
				parentID, missingID := assets[1].ID, assets[2].Identity.NativeID
				switch mode {
				case "missing-primary":
					assets = slices.Delete(assets, 2, 3)
				case "missing-secondary":
					parentID, missingID = assets[2].ID, assets[3].Identity.NativeID
					assets = slices.Delete(assets, 3, 4)
				case "duplicate-secondary":
					duplicate := assets[3]
					duplicate.ID = "duplicate"
					assets = append(assets, duplicate)
				case "wrong-connection":
					assets[2].Identity.ConnectionID = "other"
				case "stale-proof":
					assets[3].Normalized["_recovery_peer_configuration"] = "unreviewed"
				}
				contributor, _ := r.ServiceLifecycle(context.Background(), "connection")
				contribution, err := contributor.Contribute(context.Background(), "scope", assets)
				if mode == "duplicate-secondary" || mode == "stale-proof" {
					if err == nil {
						t.Fatal("unproven pair accepted")
					}
				} else {
					if err != nil {
						t.Fatal(err)
					}
					if !slices.ContainsFunc(contribution.Unresolved, func(ref graph.UnresolvedReference) bool {
						return ref.ControllerID == parentID && ref.NativeID == missingID
					}) {
						t.Fatalf("missing pair view not reported %+v", contribution.Unresolved)
					}
				}
			})
		}
	}
}

func TestRecoveryBreakNativeFailuresAndChangingReadSets(t *testing.T) {
	for _, namespace := range []string{serviceBusNamespaceType, eventHubNamespaceType} {
		for _, mode := range []string{"post-403", "post-409", "post-429", "post-500", "post-202", "post-error", "pair-changed-during-peer-read", "protected-during-peer-read", "peer-gone-after-unpair", "missing-impact-after-root-absence"} {
			t.Run(namespace+"/"+mode, func(t *testing.T) {
				s, r, assets := recoveryScenario(t, namespace, false)
				primary, peer := assets[2], assets[3]
				request, _ := dnsRequest(t, r, assets, primary)
				properties := object(s.records[primary.Identity.NativeID]["properties"])
				posts := 0
				s.handle = func(req *http.Request) (*http.Response, bool) {
					if req.Method == "POST" {
						posts++
						status := map[string]int{"post-403": 403, "post-409": 409, "post-429": 429, "post-500": 500, "post-202": 202, "post-error": 200}[mode]
						if status != 0 {
							return jsonResponse(status, map[string]any{"error": map[string]any{"code": "CannotBreakPairing", "message": "native failure"}}, http.Header{"X-Ms-Request-Id": {"native-break-failure"}}), true
						}
						properties["role"], properties["partnerNamespace"], properties["provisioningState"] = "PrimaryNotReplicating", "", "Succeeded"
						return jsonResponse(200, nil, nil), true
					}
					if strings.EqualFold(req.URL.Path, peer.Identity.NativeID) && (mode == "pair-changed-during-peer-read" || mode == "protected-during-peer-read") {
						// Replace the response object so previously fetched bodies
						// preserve the earlier native snapshot.
						payload, _ := json.Marshal(s.records[primary.Identity.NativeID])
						var changed map[string]any
						_ = json.Unmarshal(payload, &changed)
						if mode == "pair-changed-during-peer-read" {
							object(changed["properties"])["partnerNamespace"] = assets[0].Identity.NativeID
						} else {
							changed["tags"] = map[string]any{"steward/protected": "true"}
						}
						s.records[primary.Identity.NativeID] = changed
					}
					return nil, false
				}
				driver, _ := r.ResolveAction(context.Background(), "connection", primary)
				if mode == "missing-impact-after-root-absence" {
					s.gone[primary.Identity.NativeID] = true
					request.LifecycleImpacts = nil
				}
				op, err := driver.Execute(context.Background(), request)
				if mode != "peer-gone-after-unpair" {
					if err == nil || len(s.deletes) != 0 {
						t.Fatalf("native failure/drift accepted %+v %v", op, err)
					}
					if strings.HasPrefix(mode, "post-") && posts != 1 {
						t.Fatalf("unexpected mutation count %d", posts)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				s.gone[peer.Identity.NativeID], s.gone[assets[5].Identity.NativeID] = true, true
				wait, err := driver.Wait(context.Background(), request, op)
				if err != nil || text(wait.Data["phase"]) != "delete" || !slices.Equal(s.deletes, []string{primary.Identity.NativeID}) {
					t.Fatalf("early peer absence could not resume %+v %v", wait, err)
				}
				op.Data = wait.Data
				// Own authorization views are independently read after the
				// alias vanishes; denied or partial reads cannot close the task.
				for _, status := range []int{403, 206} {
					s.status[assets[4].Identity.NativeID] = status
					if wait, err := driver.Wait(context.Background(), request, op); err == nil || wait.Done {
						t.Fatalf("unreadable auth view treated as absent %+v %v", wait, err)
					}
				}
				delete(s.status, assets[4].Identity.NativeID)
				s.gone[assets[4].Identity.NativeID] = true
				if wait, err := driver.Wait(context.Background(), request, op); err != nil || !wait.Done {
					t.Fatalf("all alias views did not finish %+v %v", wait, err)
				}
			})
		}
	}
}

func TestRecoveryNamespacePrerequisiteRequiresFrozenPeerAndNoNewPair(t *testing.T) {
	for _, namespace := range []string{serviceBusNamespaceType, eventHubNamespaceType} {
		for _, mode := range []string{"new-pair", "_recovery_peer_alias", "_recovery_partner_namespace", "_recovery_peer_namespace_creation", "_recovery_peer_role", "_recovery_peer_configuration", "_recovery_configuration", "_recovery_namespace_creation", "namespace-settings-changed"} {
			t.Run(namespace+"/"+mode, func(t *testing.T) {
				s, r, assets := recoveryScenario(t, namespace, false)
				request, _ := dnsRequest(t, r, assets, assets[1])
				for _, value := range assets[2:6] {
					s.gone[value.Identity.NativeID] = true
				}
				if mode == "new-pair" {
					id := assets[1].Identity.NativeID + "/disasterrecoveryconfigs/new-alias"
					raw := map[string]any{"id": id, "type": namespace + "/disasterRecoveryConfigs", "properties": map[string]any{"partnerNamespace": assets[0].Identity.NativeID, "role": "Secondary", "provisioningState": "Succeeded"}}
					s.add(raw, "2024-01-01")
					s.lists[assets[1].Identity.NativeID+"/disasterrecoveryconfigs"] = []any{raw}
				} else if mode == "namespace-settings-changed" {
					s.records[assets[1].Identity.NativeID]["etag"] = "unpair-and-unreviewed-change"
					object(s.records[assets[1].Identity.NativeID]["properties"])["publicNetworkAccess"] = "Disabled"
				} else {
					delete(request.PrerequisiteDeletions[0].Asset.Normalized, mode)
				}
				driver, _ := r.ResolveAction(context.Background(), "connection", assets[1])
				if _, err := driver.Execute(context.Background(), request); err == nil || len(s.deletes) != 0 {
					t.Fatal("changed prerequisite/namespace authorized deletion")
				}
			})
		}
	}
}

func TestRecoveryPairCleanupPlansOnePrimaryAndResumesNativeBreak(t *testing.T) {
	for _, namespace := range []string{serviceBusNamespaceType, eventHubNamespaceType} {
		for _, selection := range []string{"primary", "secondary", "both", "primary-alias", "secondary-alias"} {
			t.Run(namespace+"/"+selection, func(t *testing.T) {
				s, r, assets := recoveryScenario(t, namespace, true)
				primary, secondary := assets[2], assets[3]
				_, input := dnsRequest(t, r, assets, assets[1])
				selected := map[string][]asset.AssetID{"primary": {assets[0].ID}, "secondary": {assets[1].ID}, "both": {assets[0].ID, assets[1].ID}, "primary-alias": {primary.ID}, "secondary-alias": {secondary.ID}}[selection]
				input.ResolvedAssetIDs = selected
				result, err := plan.Solve(input)
				if selection == "secondary-alias" {
					if err != nil || len(result.Blockers) != 1 || result.Blockers[0].Code != plan.BlockManagedByController || result.Blockers[0].ControllerID != primary.ID {
						t.Fatalf("secondary did not identify its native primary controller %+v %v", result, err)
					}
					secondaryDriver, _ := r.ResolveAction(context.Background(), "connection", secondary)
					if _, err := secondaryDriver.Execute(context.Background(), contracts.ActionRequest{Asset: secondary, Action: "delete"}); err == nil || len(s.deletes) != 0 {
						t.Fatal("secondary alias allowed independent deletion")
					}
					input.ResolvedAssetIDs = append(input.ResolvedAssetIDs, primary.ID)
					result, err = plan.Solve(input)
				}
				if err != nil || len(result.Blockers) > 0 {
					t.Fatalf("pair plan %+v %v", result, err)
				}
				deletes := map[asset.AssetID]int{}
				for _, step := range result.Steps {
					if step.Action == "delete" {
						deletes[step.AssetID]++
						if step.AssetID != primary.ID && !slices.Contains(selected, step.AssetID) {
							t.Fatalf("unselected namespace/entity added: %s", step.AssetID)
						}
					}
				}
				if deletes[primary.ID] != 1 || deletes[secondary.ID] != 0 {
					t.Fatalf("pair has multiple deletion owners %+v", deletes)
				}
				request := servicePlanRequest(result, assets, primary)
				if len(request.LifecycleImpacts) != 3 {
					t.Fatalf("alias/auth views not reviewed %+v", request.LifecycleImpacts)
				}
				properties := object(s.records[primary.Identity.NativeID]["properties"])
				properties["pendingReplicationOperationsCount"] = 2
				posts := 0
				s.handle = func(req *http.Request) (*http.Response, bool) {
					if req.Method == "POST" {
						body, _ := io.ReadAll(req.Body)
						if strings.ToLower(req.URL.Path) != primary.Identity.NativeID+"/breakpairing" || req.URL.Query().Get("api-version") != "2024-01-01" || len(body) != 0 || req.Header.Get("x-ms-client-request-id") == "" {
							t.Fatalf("unexpected recovery mutation %s body=%s", req.URL, body)
						}
						posts++
						properties["provisioningState"] = "Accepted"
						return jsonResponse(200, nil, http.Header{"X-Ms-Request-Id": {"break-request"}}), true
					}
					return nil, false
				}
				request.IdempotencyKey = "reviewed-pair"
				driver, _ := r.ResolveAction(context.Background(), "connection", primary)
				op, err := driver.Execute(context.Background(), request)
				if err != nil || text(op.Data["phase"]) != "await_recovery" || posts != 0 {
					t.Fatalf("pending copies not awaited %+v %v", op, err)
				}
				properties["pendingReplicationOperationsCount"] = 0
				wait, err := driver.Wait(context.Background(), request, op)
				if err != nil || text(wait.Data["phase"]) != "break_recovery" || posts != 1 || len(s.deletes) != 0 {
					t.Fatalf("native break not persisted %+v %v", wait, err)
				}
				op.Data = wait.Data
				// Reload both the immutable request and action data into a new runtime.
				payload, _ := json.Marshal(struct {
					Request contracts.ActionRequest
					Result  contracts.ActionResult
				}{request, op})
				var restored struct {
					Request contracts.ActionRequest
					Result  contracts.ActionResult
				}
				if err := json.Unmarshal(payload, &restored); err != nil {
					t.Fatal(err)
				}
				request, op = restored.Request, restored.Result
				driver, _ = s.runtime(t).ResolveAction(context.Background(), "connection", primary)
				for i := 0; i < 3; i++ {
					if wait, err := driver.Wait(context.Background(), request, op); err != nil || wait.Done || posts != 1 || len(s.deletes) != 0 {
						t.Fatalf("Accepted treated as unpaired %+v %v", wait, err)
					}
				}
				properties["role"], properties["partnerNamespace"], properties["provisioningState"] = "PrimaryNotReplicating", "", "Succeeded"
				s.records[primary.Identity.NativeID]["etag"] = "unpaired"
				wait, err = driver.Wait(context.Background(), request, op)
				if err != nil || wait.Done || text(wait.Data["phase"]) != "delete" || !slices.Equal(s.deletes, []string{primary.Identity.NativeID}) {
					t.Fatalf("unpaired primary not deleted %+v %v", wait, err)
				}
				op.Data = wait.Data
				if wait, err := driver.Wait(context.Background(), request, op); err != nil || wait.Done {
					t.Fatalf("live peer alias not awaited %+v %v", wait, err)
				}
				for _, impact := range request.LifecycleImpacts {
					s.gone[impact.Asset.Identity.NativeID] = true
				}
				if wait, err := driver.Wait(context.Background(), request, op); err != nil || !wait.Done || posts != 1 {
					t.Fatalf("alias readback incomplete %+v %v", wait, err)
				}
				for _, parent := range assets[:2] {
					if !slices.Contains(selected, parent.ID) {
						continue
					}
					parentRequest := servicePlanRequest(result, assets, parent)
					if len(parentRequest.PrerequisiteDeletions) != 1 || parentRequest.PrerequisiteDeletions[0].Asset.ID != primary.ID {
						t.Fatalf("namespace lost shared prerequisite %+v", parentRequest)
					}
					parentDriver, _ := r.ResolveAction(context.Background(), "connection", parent)
					if _, err := parentDriver.Execute(context.Background(), parentRequest); err != nil {
						t.Fatal(err)
					}
					for _, impact := range parentRequest.LifecycleImpacts {
						s.gone[impact.Asset.Identity.NativeID] = true
					}
				}
				for _, value := range append(assets[:2:2], assets[6:]...) {
					own := strings.Join(strings.Split(value.Identity.NativeID, "/")[:9], "/")
					if !slices.Contains(selected, asset.AssetID(own)) && s.gone[value.Identity.NativeID] {
						t.Fatalf("unselected namespace/entity deleted %s", value.Identity.NativeID)
					}
				}
			})
		}
	}
}
