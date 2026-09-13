package azure

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/app/governance"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func newHybridLicenseFixture(t *testing.T) *hybridCleanupFixture {
	t.Helper()
	f := newHybridCleanupFixture(t)
	licenseID := strings.ToLower(resourceID(hybridLicenseType, "license"))
	license := f.values[licenseID]
	props := object(license["properties"])
	props["tenantId"] = testTenant
	details := object(props["licenseDetails"])
	details["immutableId"], details["assignedLicenses"] = "99999999-8888-7777-6666-555555555555", float64(1)
	f.deleteStatus = 202
	previous := f.override
	profileID := strings.ToLower(resourceID(hybridMachineType, "machine")) + "/licenseprofiles/default"
	f.override = func(req *http.Request) (*http.Response, bool) {
		if req.Method == "DELETE" && strings.EqualFold(req.URL.Path, licenseID) {
			count, _ := batchInteger(details["assignedLicenses"], 32)
			if count != 0 || f.values[profileID] != nil || req.URL.Query().Get("api-version") != hybridComputeVersion || req.Header.Get("If-Match") != "" || req.Header.Get("X-Ms-Client-Request-Id") == "" {
				t.Fatal("unsafe shared license DELETE")
			}
			f.deleted[licenseID]++
			if f.deleted[licenseID] != 1 {
				t.Fatal("duplicate license deletion")
			}
			if !f.hold {
				delete(f.values, licenseID)
			}
			status := f.deleteStatus
			if status == 202 {
				status = 200
			}
			return &http.Response{StatusCode: status, Header: http.Header{}, Body: http.NoBody}, true
		}
		prior := f.values[profileID]
		res, handled := previous(req)
		if prior != nil && f.values[profileID] == nil {
			count, _ := batchInteger(details["assignedLicenses"], 32)
			details["assignedLicenses"] = count - 1
			license["etag"] = "assignment-removed"
		}
		return res, handled
	}
	return f
}

func licenseRequestFixture(t *testing.T) (*hybridCleanupFixture, contracts.ActionRequest) {
	f := newHybridLicenseFixture(t)
	request := f.requestAsset(t, hybridLicenseType)
	request.PrerequisiteDeletions = []contracts.ActionImpact{{Asset: f.requestAsset(t, hybridProfileType).Asset, ControllerID: request.Asset.ID, Delete: true}}
	return f, request
}

func TestHybridComputeSharedLicensePlans(t *testing.T) {
	f, request := licenseRequestFixture(t)
	profile := request.PrerequisiteDeletions[0].Asset
	values := []asset.Asset{request.Asset, profile}
	cascades := &serviceCascades{client: f.client, connectionID: "connection"}
	contribution := governance.Contribution{}
	if err := cascades.contributeHybridComputeLicenses(t.Context(), values, &contribution); err != nil || len(contribution.Bindings) != 0 || len(contribution.Relationships) != 1 {
		t.Fatal("shared license became owner", err)
	}
	for _, selectProfile := range []bool{false, true} {
		selected := []asset.AssetID{request.Asset.ID}
		if selectProfile {
			selected = append(selected, profile.ID)
		}
		solved, err := plan.Solve(plan.Input{Assets: values, ResolvedAssetIDs: selected, Relationships: contribution.Relationships})
		if err != nil || (len(solved.Blockers) == 0) != selectProfile {
			t.Fatal("consumer selection boundary", err, solved.Blockers)
		}
		if selectProfile {
			frozen := servicePlanRequest(solved, values, request.Asset)
			if len(frozen.PrerequisiteDeletions) != 1 || len(frozen.LifecycleImpacts) != 0 || len(solved.Steps) != 2 {
				t.Fatal("shared consumer order lost")
			}
			if _, err := f.driver(t, frozen).Preflight(t.Context(), frozen); err == nil {
				t.Fatal("frozen plan ignored live assignment")
			}
		}
	}
	contribution = governance.Contribution{}
	if err := cascades.contributeHybridComputeLicenses(t.Context(), values[:1], &contribution); err != nil || len(contribution.Unresolved) != 1 || contribution.Unresolved[0].Relationship != graph.RelationshipDependsOn {
		t.Fatal("unscanned assignment lost", err)
	}
	// Current native indexes cannot hide an individually known profile.
	f.omitted[profile.Identity.NativeID] = true
	contribution = governance.Contribution{}
	if err := cascades.contributeHybridComputeLicenses(t.Context(), values, &contribution); err != nil || len(contribution.Relationships) != 1 {
		t.Fatal("omitted profile lost", err)
	}
}

func TestHybridComputeSharedLicenseDeleteAndResidual(t *testing.T) {
	for _, status := range []int{200, 204, 404} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			f, request := licenseRequestFixture(t)
			driver := f.driver(t, request)
			if _, err := driver.Execute(t.Context(), request); err == nil || len(f.deleted) != 0 {
				t.Fatal("assigned license deleted")
			}
			profileID := request.PrerequisiteDeletions[0].Asset.Identity.NativeID
			profile := f.values[profileID]
			delete(f.values, profileID)
			details := object(object(f.values[request.Asset.Identity.NativeID]["properties"])["licenseDetails"])
			details["assignedLicenses"] = float64(2)
			if _, err := driver.Execute(t.Context(), request); err == nil || len(f.deleted) != 0 {
				t.Fatal("foreign subscription assignments ignored")
			}
			details["assignedLicenses"] = float64(0)
			f.deleteStatus, f.hold = status, true
			result, err := driver.Execute(t.Context(), request)
			if err != nil {
				t.Fatal("license delete", err)
			}
			for range 2 {
				encoded, _ := json.Marshal(result)
				if json.Unmarshal(encoded, &result) != nil {
					t.Fatal("restore receipt")
				}
				fresh, err := NewRuntime(f.runtime.credentials)
				if err != nil {
					t.Fatal(err)
				}
				fresh.transport = f.runtime.transport
				f.runtime = fresh
				driver = f.driver(t, request)
				wait, err := driver.Wait(t.Context(), request, result)
				if err != nil || wait.Done {
					t.Fatal("successful operation proved resource absence", err)
				}
				result.Data = wait.Data
			}
			delete(f.values, request.Asset.Identity.NativeID)
			f.values[profileID] = profile
			request.ExecutionResult = &result
			if read, err := driver.Readback(t.Context(), request); err != nil || !read.Exists {
				t.Fatal("license 404 hid known assignment", err)
			}
			if _, err := driver.Execute(t.Context(), request); err != nil || f.deleted[request.Asset.Identity.NativeID] != 1 {
				t.Fatal("resume repeated license DELETE", err)
			}
			delete(f.values, profileID)
			if wait, err := driver.Wait(t.Context(), request, result); err != nil || !wait.Done {
				t.Fatal("license own absence", err)
			}
			request.PrerequisiteDeletions = nil
			if _, err := driver.Wait(t.Context(), request, result); err == nil {
				t.Fatal("tampered prerequisite accepted")
			}
		})
	}
}

func TestHybridComputeSharedLicenseBoundaries(t *testing.T) {
	for _, mode := range []string{"immutable id", "tenant", "configuration", "counter missing", "counter malformed", "counter negative", "group protection", "lock", "own permission", "profile permission", "machine index permission", "new assignment", "unrelated prerequisite", "foreign prerequisite", "protected license", "deleting"} {
		t.Run(mode, func(t *testing.T) {
			f, request := licenseRequestFixture(t)
			driver := f.driver(t, request)
			root := f.values[request.Asset.Identity.NativeID]
			profileID := request.PrerequisiteDeletions[0].Asset.Identity.NativeID
			profile := f.values[profileID]
			delete(f.values, profileID)
			props := object(root["properties"])
			details := object(props["licenseDetails"])
			details["assignedLicenses"] = float64(0)
			fail := ""
			switch mode {
			case "immutable id":
				details["immutableId"] = testSubscription
			case "tenant":
				props["tenantId"] = testSubscription
			case "configuration":
				details["processors"] = float64(20)
			case "counter missing":
				delete(details, "assignedLicenses")
			case "counter malformed":
				details["assignedLicenses"] = "0"
			case "counter negative":
				details["assignedLicenses"] = float64(-1)
			case "group protection":
				f.group["tags"] = map[string]any{"steward/protected": "true"}
			case "lock":
				f.locks = []any{map[string]any{"id": text(f.group["id"]) + "/providers/Microsoft.Authorization/locks/keep", "properties": map[string]any{"level": "CanNotDelete"}}}
			case "own permission":
				fail = request.Asset.Identity.NativeID
			case "profile permission":
				fail = profileID
				delete(f.values, request.Asset.Identity.NativeID)
			case "machine index permission":
				fail = "/subscriptions/" + testSubscription + "/providers/microsoft.hybridcompute/machines"
			case "new assignment":
				profile["id"], profile["name"] = profileID+"other", "defaultother"
				f.values[profileID+"other"] = profile
			case "unrelated prerequisite":
				request.PrerequisiteDeletions[0].ControllerID = "other"
			case "foreign prerequisite":
				request.PrerequisiteDeletions[0].Asset.Identity.ConnectionID = "foreign"
			case "protected license":
				root["tags"] = map[string]any{"steward/protected": "true"}
			case "deleting":
				props["provisioningState"] = "Deleting"
			}
			previous := f.override
			f.override = func(req *http.Request) (*http.Response, bool) {
				if strings.EqualFold(req.URL.Path, fail) {
					return jsonResponse(403, map[string]any{"error": map[string]any{"code": "AuthorizationFailed"}}, nil), true
				}
				return previous(req)
			}
			_, err := driver.Execute(t.Context(), request)
			if (err == nil) != (mode == "deleting") || len(f.deleted) != 0 {
				t.Fatal("unsafe license mutation", mode, err)
			}
		})
	}
}

func TestHybridComputeRecordedLicenseDelete(t *testing.T) {
	payload, err := os.ReadFile("fixtures/hybridcompute/license-delete-recording.json")
	if err != nil {
		t.Fatal(err)
	}
	var meta map[string]any
	if json.Unmarshal(payload, &meta) != nil || meta["source_sha256"] != "ddecf934e4335742f1354eb65c9be44a8085698af2edfa3487689055525aef11" || meta["interaction_index"] != float64(5) {
		t.Fatal("license recording provenance changed")
	}
	if fmt.Sprintf("%x", sha256.Sum256(payload)) != "af9f2649432e228790c0ee3b02ebba970a0d8ad0c26206de0ef8f127369d99df" {
		t.Fatal("recorded license transport changed")
	}
	payload = []byte(strings.ReplaceAll(string(payload), "00000000-0000-0000-0000-000000000000", testSubscription))
	var record redisRecordedResponse
	if json.Unmarshal(payload, &record) != nil {
		t.Fatal("license recording")
	}
	r := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
		if req.Method != record.Method || req.URL.String() != record.URI {
			t.Fatal("original license request changed")
		}
		return streamAnalyticsRecordedHTTP(record), nil
	})
	c, err := r.resolve(t.Context(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	res, err := c.request(t.Context(), record.Method, record.URI)
	if err != nil {
		t.Fatal(err)
	}
	id := strings.ToLower(strings.Split(strings.TrimPrefix(record.URI, armOrigin), "?")[0])
	receipt, err := c.hybridComputeDeleteReceipt(id, res)
	if err != nil || len(receipt) != 1 {
		t.Fatal("native synchronous license receipt", err)
	}
	if result, err := c.hybridComputePoll(t.Context(), id, receipt); err != nil || !result.Done {
		t.Fatal("synchronous receipt", err)
	}
	status, _ := hybridComputeTestURLs()
	for _, response := range []response{{status: 202}, {status: 200, data: map[string]any{"unexpected": true}}, {status: 200, header: http.Header{"Azure-Asyncoperation": {status}}}} {
		if _, err := c.hybridComputeDeleteReceipt(id, response); err == nil {
			t.Fatal("unproven license delete protocol accepted")
		}
	}
}

func TestHybridComputeLicenseNativeIdentityAndInventory(t *testing.T) {
	for _, mode := range []string{"opaque identity", "missing identity", "foreign tenant", "missing count", "omitted profile", "forged history", "empty roster etag"} {
		t.Run(mode, func(t *testing.T) {
			f := newHybridLicenseFixture(t)
			id := strings.ToLower(resourceID(hybridLicenseType, "license"))
			props := object(f.values[id]["properties"])
			details := object(props["licenseDetails"])
			profile := strings.ToLower(resourceID(hybridMachineType, "machine")) + "/licenseprofiles/default"
			switch mode {
			case "opaque identity":
				details["immutableId"] = "provider-generated-opaque-identity"
			case "missing identity":
				delete(details, "immutableId")
			case "foreign tenant":
				props["tenantId"] = testSubscription
			case "missing count":
				delete(details, "assignedLicenses")
			case "empty roster etag":
				delete(f.values, profile)
				details["assignedLicenses"] = float64(0)
			}
			scan := f.request(hybridLicenseType)
			batch, err := f.runtime.List(t.Context(), scan)
			if err != nil || len(batch.Items) != 1 {
				t.Fatal("license inventory", err)
			}
			eligible := mode != "missing identity" && mode != "foreign tenant" && mode != "missing count"
			if *batch.Items[0].Actionable != eligible {
				t.Fatal("license metadata eligibility", mode)
			}
			if mode == "empty roster etag" {
				request := f.requestAsset(t, hybridLicenseType)
				driver := f.driver(t, request)
				f.values[id]["etag"] = "changed"
				if _, err := driver.Execute(t.Context(), request); err == nil || len(f.deleted) != 0 {
					t.Fatal("unexplained license etag change accepted")
				}
			}
			if mode != "omitted profile" && mode != "forged history" {
				return
			}
			scan.KnownNativeIDs = []string{id}
			scan.KnownNativeMetadata = map[string]map[string]any{id: batch.Items[0].Normalized}
			f.omitted[profile] = true
			if mode == "forged history" {
				scan.KnownNativeMetadata[id][hybridComputeCleanupProof] = "forged"
			}
			batch, err = f.runtime.List(t.Context(), scan)
			if mode == "forged history" {
				if err == nil {
					t.Fatal("forged saved assignments accepted")
				}
				return
			}
			if err != nil || len(batch.Items) != 1 || len(object(object(batch.Items[0].Normalized[hybridComputeCleanup])["assignments"])) != 1 {
				t.Fatal("known omitted assignment lost", err)
			}
		})
	}
}
