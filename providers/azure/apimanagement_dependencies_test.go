package azure

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/loomx-ai/steward/internal/app/governance"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func apimRevisionScenario(t *testing.T, kind string) (*dnsScenario, *Runtime, []asset.Asset, asset.Asset, asset.Asset) {
	t.Helper()
	s, r, assets := apimScenario(t)
	current := cdnAsset(t, assets, kind)
	raw := apimExample(t, "ApiManagementGetApiRevision.json")
	id := current.Identity.NativeID + ";rev=3"
	raw["id"], raw["name"], raw["type"] = id, last(id), kind
	raw["_apim_header_etag"] = `"revision-three"`
	s.add(raw, apimVersion)
	for _, childKind := range apimOwnedKinds(kind) {
		collection := id + "/" + strings.ToLower(last(childKind))
		s.lists[collection], s.version[collection] = []any{}, apimVersion
	}
	s.lists[current.Identity.NativeID+"/revisions"] = append(s.lists[current.Identity.NativeID+"/revisions"], map[string]any{"apiId": "/apis/" + last(id), "apiRevision": "3", "isCurrent": false, "isOnline": false})
	revision := dnsAsset(t, r, raw)
	assets = append(assets, revision)
	return s, r, assets, current, revision
}

func TestAPIMRevisionsUseNativeMetadataAndDistinctAPIIdentities(t *testing.T) {
	for _, kind := range []string{apimAPIType, apimWorkspaceType + "/apis"} {
		for _, mode := range []string{"logical-list", "explicit-current", "paginated", "orphan-revision", "revision-read-denied", "index-read-denied", "ambiguous-current", "mismatched-revision", "changed-index", "foreign-next-page"} {
			t.Run(kind+"/"+mode, func(t *testing.T) {
				s, r, _, current, revision := apimRevisionScenario(t, kind)
				collection := redisParentID(current.Identity.NativeID) + "/apis"
				index := current.Identity.NativeID + "/revisions"
				base := s.handle
				reads := 0
				switch mode {
				case "explicit-current":
					listed := maps.Clone(s.records[current.Identity.NativeID])
					listed["id"], listed["name"] = current.Identity.NativeID+";rev=1", last(current.Identity.NativeID)+";rev=1"
					s.lists[collection] = []any{listed, s.records[revision.Identity.NativeID]}
				case "orphan-revision":
					s.gone[current.Identity.NativeID] = true
					s.lists[collection] = []any{s.records[revision.Identity.NativeID]}
				case "revision-read-denied":
					s.status[revision.Identity.NativeID] = 403
				case "index-read-denied":
					s.status[index] = 403
				case "ambiguous-current":
					object(s.lists[index][1])["isCurrent"] = true
				case "mismatched-revision":
					object(s.records[revision.Identity.NativeID]["properties"])["apiRevision"] = "4"
				case "paginated", "changed-index", "foreign-next-page":
					s.handle = func(req *http.Request) (*http.Response, bool) {
						if req.Method == "GET" && strings.EqualFold(req.URL.Path, index) {
							reads++
							if mode == "paginated" {
								if req.URL.Query().Get("$skip") == "1" {
									return jsonResponse(200, map[string]any{"value": s.lists[index][1:]}, nil), true
								}
								return jsonResponse(200, map[string]any{"value": s.lists[index][:1], "nextLink": "https://management.azure.com" + index + "?api-version=" + apimVersion + "&$skip=1"}, nil), true
							}
							if mode == "foreign-next-page" {
								return jsonResponse(200, map[string]any{"value": s.lists[index], "nextLink": "https://management.azure.com" + redisParentID(current.Identity.NativeID) + "/apis/unreviewed/revisions?api-version=" + apimVersion}, nil), true
							}
							if reads == 2 {
								object(s.lists[index][1])["isOnline"] = true
							}
						}
						return base(req)
					}
				}
				batch, err := r.List(context.Background(), productRequest(r, kind))
				if !slices.Contains([]string{"logical-list", "explicit-current", "paginated", "orphan-revision"}, mode) {
					if err == nil {
						t.Fatal("incomplete or changed revision inventory accepted", batch)
					}
					return
				}
				want := 2
				if mode == "orphan-revision" {
					want = 1
				}
				if err != nil || !batch.Complete || len(batch.Items) != want {
					t.Fatal("revision inventory", len(batch.Items), err)
				}
				found := false
				for _, item := range batch.Items {
					if item.NativeID == revision.Identity.NativeID {
						found = item.Normalized["apiRevision"] == "3" && slices.Contains(stringValues(item.Normalized["_apim_references"]), current.Identity.NativeID)
					}
				}
				if !found {
					t.Fatal("non-current revision was hidden or merged with alias")
				}
				if mode == "orphan-revision" {
					driver, _ := r.ResolveAction(context.Background(), "connection", revision)
					result, err := driver.Execute(context.Background(), contracts.ActionRequest{Asset: revision, Action: "delete"})
					if err != nil {
						t.Fatal("orphan revision cannot be cleaned up", err)
					}
					waited, err := driver.Wait(context.Background(), contracts.ActionRequest{Asset: revision, Action: "delete"}, result)
					if err != nil || !waited.Done || len(s.deletes) != 1 || s.deletes[0] != revision.Identity.NativeID {
						t.Fatal("orphan revision cleanup", waited, err, s.deletes)
					}
				}
			})
		}
	}
}

func TestAPIMRevisionInventoryPreservesNativeListRequestIDs(t *testing.T) {
	for _, mode := range []string{"single", "empty", "paged"} {
		t.Run(mode, func(t *testing.T) {
			s, r, assets := apimScenario(t)
			api := cdnAsset(t, assets, apimAPIType)
			path := redisParentID(api.Identity.NativeID) + "/apis"
			original := s.handle
			if mode == "empty" {
				s.lists[path] = []any{}
				s.gone[api.Identity.NativeID] = true
			}
			s.handle = func(req *http.Request) (*http.Response, bool) {
				if req.Method == "GET" && strings.EqualFold(req.URL.Path, path) {
					if mode == "paged" && req.URL.Query().Get("$skip") == "" {
						return jsonResponse(200, map[string]any{"value": []any{}, "nextLink": "https://management.azure.com" + path + "?api-version=" + apimVersion + "&$skip=1"}, http.Header{"X-Ms-Request-Id": {"apim-first-page"}}), true
					}
					return jsonResponse(200, map[string]any{"value": s.lists[path]}, http.Header{"X-Ms-Request-Id": {"apim-list-request"}}), true
				}
				return original(req)
			}
			batch, err := r.List(context.Background(), productRequest(r, apimAPIType))
			if err != nil || !batch.Complete || batch.RequestID != "apim-list-request" {
				t.Fatal("APIM aggregate inventory lost native list provenance", batch.RequestID, err)
			}
		})
	}
}

func TestAPIMRevisionAndSubscriptionPrerequisitesAreReviewed(t *testing.T) {
	for _, targetKind := range []string{apimAPIType, apimWorkspaceType + "/apis", apimServiceType + "/products", apimWorkspaceType + "/products", apimServiceType + "/users", apimServiceType + "/apiVersionSets", apimWorkspaceType + "/apiVersionSets"} {
		t.Run(targetKind, func(t *testing.T) {
			s, r, assets := apimScenario(t)
			if strings.EqualFold(last(targetKind), "apis") {
				s, r, assets, _, _ = apimRevisionScenario(t, targetKind)
			}
			target := cdnAsset(t, assets, targetKind)
			namespaceKind := apimServiceType
			if strings.Contains(targetKind, "/workspaces/") {
				namespaceKind = apimWorkspaceType
			}
			referrerKind := namespaceKind + "/subscriptions"
			field := "scope"
			if last(targetKind) == "users" {
				field = "ownerId"
			}
			if last(targetKind) == "apiVersionSets" {
				referrerKind, field = namespaceKind+"/apis", "apiVersionSetId"
			}
			referrer := cdnAsset(t, assets, referrerKind)
			object(s.records[referrer.Identity.NativeID]["properties"])[field] = target.Identity.NativeID
			for i := range assets {
				if assets[i].ID == referrer.ID {
					assets[i] = dnsAsset(t, r, s.records[referrer.Identity.NativeID])
				}
			}
			// Changing an API also changes the ancestor snapshot of its children.
			for i := range assets {
				if strings.HasPrefix(assets[i].Identity.NativeID, referrer.Identity.NativeID+"/") {
					assets[i] = dnsAsset(t, r, s.records[assets[i].Identity.NativeID])
				}
			}
			request, input := dnsRequest(t, r, assets, target)
			solved, err := plan.Solve(input)
			if err != nil || len(request.PrerequisiteDeletions) == 0 {
				t.Fatal("missing APIM prerequisites", err)
			}
			driver, _ := r.ResolveAction(context.Background(), "connection", target)
			if _, err := driver.Execute(context.Background(), request); err == nil || len(s.deletes) != 0 {
				t.Fatal("referenced APIM resource deleted")
			}
			for _, step := range solved.Steps {
				var selected asset.Asset
				for _, value := range assets {
					if value.ID == step.AssetID {
						selected = value
					}
				}
				stepRequest := servicePlanRequest(solved, assets, selected)
				encoded, _ := json.Marshal(stepRequest)
				json.Unmarshal(encoded, &stepRequest)
				native, err := r.ResolveAction(context.Background(), "connection", selected)
				if err != nil {
					t.Fatal(err)
				}
				result, err := native.Execute(context.Background(), stepRequest)
				if err != nil {
					t.Fatal("native prerequisite", selected.Identity.NativeID, err)
				}
				streamAnalyticsAfterDelete(s)
				waited, err := native.Wait(context.Background(), stepRequest, result)
				if err != nil || !waited.Done {
					t.Fatal("prerequisite absence", selected.Identity.NativeID, waited, err)
				}
			}
			if len(s.deletes) != len(solved.Steps) || s.deletes[len(s.deletes)-1] != target.Identity.NativeID {
				t.Fatal("unreviewed deletion or wrong dependency order", s.deletes)
			}
		})
	}
}

func TestAPIMRevisionRetentionAndNewReferencePreventDeletion(t *testing.T) {
	for _, mode := range []string{"retained", "unobserved", "new-revision", "index-permission", "forged-prerequisite"} {
		t.Run(mode, func(t *testing.T) {
			s, r, assets, target, revision := apimRevisionScenario(t, apimAPIType)
			request, input := dnsRequest(t, r, assets, target)
			switch mode {
			case "retained":
				input.RequestOptions = map[asset.AssetID]map[string]any{target.ID: {"retain_resources": []string{revision.Identity.NativeID}}}
				solved, err := plan.Solve(input)
				if err != nil || len(solved.Blockers) == 0 {
					t.Fatal("retained revision failed to block current API", err)
				}
				return
			case "unobserved":
				assets = slices.DeleteFunc(assets, func(value asset.Asset) bool { return value.ID == revision.ID })
				contributor, _ := r.ServiceLifecycle(context.Background(), "connection")
				contribution, err := contributor.Contribute(context.Background(), "scope", assets)
				if err != nil || len(contribution.Unresolved) == 0 {
					t.Fatal("missing revision observation was ignored", err)
				}
				return
			case "new-revision":
				s.gone[revision.Identity.NativeID] = true
				raw := apimExample(t, "ApiManagementGetApiRevision.json")
				id := target.Identity.NativeID + ";rev=4"
				raw["id"], raw["name"], raw["_apim_header_etag"] = id, last(id), `"revision-four"`
				object(raw["properties"])["apiRevision"] = "4"
				s.add(raw, apimVersion)
				s.lists[target.Identity.NativeID+"/revisions"] = append(s.lists[target.Identity.NativeID+"/revisions"], map[string]any{"apiId": id, "apiRevision": "4", "isCurrent": false})
			case "index-permission":
				s.status[target.Identity.NativeID+"/revisions"] = 403
				request.PrerequisiteDeletions = nil
			case "forged-prerequisite":
				s.gone[revision.Identity.NativeID] = true
				request.PrerequisiteDeletions[0].Asset.Normalized = map[string]any{}
			}
			driver, _ := r.ResolveAction(context.Background(), "connection", target)
			if _, err := driver.Execute(context.Background(), request); err == nil || len(s.deletes) != 0 {
				t.Fatal("unreviewed or forged revision dependency permitted a write")
			}
		})
	}
}

// apimAddAPIs adds current APIs to the scenario's service, each with empty
// native child collections, and returns their IDs in walk order.
func apimAddAPIs(t *testing.T, s *dnsScenario, service, template asset.Asset, count int) []string {
	t.Helper()
	var ids []string
	for i := range count {
		name := fmt.Sprintf("extra-%02d", i)
		id := service.Identity.NativeID + "/apis/" + name
		raw := maps.Clone(s.records[template.Identity.NativeID])
		raw["properties"] = maps.Clone(object(raw["properties"]))
		raw["id"], raw["name"], raw["_apim_header_etag"] = id, name, `"etag-`+name+`"`
		s.add(raw, apimVersion)
		s.lists[service.Identity.NativeID+"/apis"] = append(s.lists[service.Identity.NativeID+"/apis"], raw)
		s.lists[id+"/revisions"], s.version[id+"/revisions"] = []any{map[string]any{"apiId": id + ";rev=1", "apiRevision": "1", "isCurrent": true, "isOnline": true, "createdDateTime": "2026-01-27T15:35:05.873Z"}}, apimVersion
		for _, kind := range apimOwnedKinds(text(raw["type"])) {
			path := id + "/" + strings.ToLower(last(kind))
			s.lists[path], s.version[path] = []any{}, apimVersion
		}
		ids = append(ids, id)
	}
	return ids
}

// Contribute walks each service once (two stability passes) for the union of
// its targets' kinds, and every target keeps exactly the referrers its own
// walk finds.
func TestAPIMContributeWalksEachServiceOnceForAllTargetKinds(t *testing.T) {
	s, r, assets := apimScenario(t)
	service := cdnAsset(t, assets, apimServiceType)
	var apiLists atomic.Int32
	s.before = func(req *http.Request) {
		if req.Method == "GET" && strings.EqualFold(req.URL.Path, service.Identity.NativeID+"/apis") {
			apiLists.Add(1)
		}
	}
	c, err := r.resolve(t.Context(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	var targets []asset.Asset
	kindSets := map[string]bool{}
	for _, value := range assets {
		if kinds := apimIncomingKinds(value.Identity.NativeType); len(kinds) != 0 && apimRootID(value.Identity.NativeID) == service.Identity.NativeID {
			targets = append(targets, value)
			kindSets[strings.Join(slices.Compact(slices.Sorted(slices.Values(kinds))), ",")] = true
		}
	}
	var result governance.Contribution
	if err := (&serviceCascades{client: c, connectionID: "connection"}).contributeAPIMReferences(t.Context(), assets, &result); err != nil {
		t.Fatal(err)
	}
	if got := apiLists.Load(); got != 2 || len(kindSets) < 2 {
		t.Fatal("service was not walked exactly once per Contribute", got, len(kindSets))
	}
	found := 0
	for _, target := range targets {
		index, err := c.apimIncomingIndex(t.Context(), target.Identity)
		if err != nil {
			t.Fatal(err)
		}
		var want, got []string
		for _, child := range index[strings.ToLower(target.Identity.NativeID)] {
			want = append(want, child.id)
		}
		for _, value := range result.Relationships {
			if value.SourceAssetID == target.ID && value.Source == "azure:apim-reference" {
				got = append(got, text(value.Evidence["instance_id"]))
			}
		}
		for _, value := range result.Unresolved {
			if value.ControllerID == target.ID {
				got = append(got, value.NativeID)
			}
		}
		slices.Sort(want)
		slices.Sort(got)
		if !slices.Equal(want, got) {
			t.Fatal("union walk changed a target's referrers", target.Identity.NativeType, want, got)
		}
		found += len(want)
	}
	if found == 0 {
		t.Fatal("scenario exercised no APIM references")
	}
}

// One pass walks the service's API subtrees concurrently and fails with the
// first error in API order, as a serial walk would.
func TestAPIMIncomingWalkReadsAPIsConcurrentlyAndFailsInOrder(t *testing.T) {
	for _, fail := range []bool{false, true} {
		s, r, assets := apimScenario(t)
		service := cdnAsset(t, assets, apimServiceType)
		backend := cdnAsset(t, assets, apimServiceType+"/backends")
		apis := apimAddAPIs(t, s, service, cdnAsset(t, assets, apimServiceType+"/apis"), 12)
		if fail {
			s.status[apis[3]+"/operations"], s.status[apis[9]+"/operations"] = 403, 409
		}
		var inFlight, peak atomic.Int32
		s.before = func(req *http.Request) {
			path := strings.ToLower(req.URL.Path)
			if !strings.HasPrefix(path, service.Identity.NativeID+"/apis/") || !strings.HasSuffix(path, "/operations") {
				return
			}
			now := inFlight.Add(1)
			defer inFlight.Add(-1)
			for old := peak.Load(); now > old && !peak.CompareAndSwap(old, now); old = peak.Load() {
			}
			switch {
			case fail && path == apis[3]+"/operations":
				time.Sleep(30 * time.Millisecond)
			case fail && path == apis[9]+"/operations":
			default:
				time.Sleep(5 * time.Millisecond)
			}
		}
		c, err := r.resolve(t.Context(), "connection")
		if err != nil {
			t.Fatal(err)
		}
		index, err := c.apimIncomingIndex(t.Context(), backend.Identity)
		if fail {
			var call *contracts.ProviderCallError
			if !errors.As(err, &call) || call.Provider.Code != "403" || index != nil {
				t.Fatal("walk did not fail with the first error in API order", err)
			}
			continue
		}
		if err != nil || peak.Load() < 2 || peak.Load() > detailReadConcurrency {
			t.Fatal(err, peak.Load())
		}
	}
}

// Concurrent delete checks of one service and kind share a walk that starts
// after they arrive; a later check walks again.
func TestAPIMIncomingIndexSharesOnlyQueuedLiveWalks(t *testing.T) {
	s, r, assets := apimScenario(t)
	service := cdnAsset(t, assets, apimServiceType)
	backend := cdnAsset(t, assets, apimServiceType+"/backends")
	var apiLists, gated atomic.Int32
	release := make(chan struct{})
	s.before = func(req *http.Request) {
		switch {
		case req.Method != "GET":
		case strings.EqualFold(req.URL.Path, service.Identity.NativeID) && gated.Add(1) == 1:
			<-release // Hold the first walk until the other checks queue.
		case strings.EqualFold(req.URL.Path, service.Identity.NativeID+"/apis"):
			apiLists.Add(1)
		}
	}
	c, err := r.resolve(t.Context(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	const callers = 6
	indexes := make([]map[string][]serviceChild, callers)
	errs := make([]error, callers)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); indexes[0], errs[0] = c.apimIncomingIndex(t.Context(), backend.Identity) }()
	for gated.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	for i := 1; i < callers; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); indexes[i], errs[i] = c.apimIncomingIndex(t.Context(), backend.Identity) }()
	}
	key := sharedReadKey{c, "apim-incoming:" + service.Identity.NativeID + "|" + strings.Join(slices.Compact(slices.Sorted(slices.Values(apimIncomingKinds(backend.Identity.NativeType)))), ",")}
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(time.Millisecond) {
		sharedReads.Lock()
		queued := sharedReads.queued[key]
		sharedReads.Unlock()
		if queued != nil {
			break
		}
		if time.Now().After(deadline) {
			close(release)
			t.Fatal("concurrent checks did not queue a shared walk")
		}
	}
	time.Sleep(20 * time.Millisecond) // Let the other checks join the queued walk.
	close(release)
	wg.Wait()
	for i := range callers {
		if errs[i] != nil || !reflect.DeepEqual(indexes[i], indexes[0]) {
			t.Fatal("shared walk result differs", i, errs[i])
		}
	}
	if got := apiLists.Load(); got != 4 {
		t.Fatal("concurrent checks were not coalesced into two walks", got)
	}
	if _, err := c.apimIncomingIndex(t.Context(), backend.Identity); err != nil || apiLists.Load() != 6 {
		t.Fatal("a completed walk was reused", apiLists.Load(), err)
	}
}
