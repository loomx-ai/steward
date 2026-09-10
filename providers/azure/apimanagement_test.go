package azure

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

// Sequential fault cases reuse the same reviewed plan only when every fixture
// asset is identical. Each case restores its own copy, as a restarted worker
// would, and still performs all live preflight, mutation and readback calls.
type apimReviewCache map[[32]byte][]byte

func (cache apimReviewCache) request(t *testing.T, r *Runtime, assets []asset.Asset, target asset.Asset) (contracts.ActionRequest, plan.Input) {
	t.Helper()
	identity, err := json.Marshal(struct {
		Target asset.Asset
		Assets []asset.Asset
	}{target, assets})
	if err != nil {
		t.Fatal(err)
	}
	key := sha256.Sum256(identity)
	var review struct {
		Request contracts.ActionRequest
		Input   plan.Input
	}
	if _, exists := cache[key]; !exists {
		review.Request, review.Input = dnsRequest(t, r, assets, target)
		cache[key], err = json.Marshal(review)
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := json.Unmarshal(cache[key], &review); err != nil {
		t.Fatal(err)
	}
	return review.Request, review.Input
}

func apimExample(t *testing.T, file string) map[string]any {
	t.Helper()
	payload, err := os.ReadFile("fixtures/apimanagement/" + file)
	var example map[string]any
	if err != nil || json.Unmarshal(payload, &example) != nil {
		t.Fatal(file, err)
	}
	return object(object(object(example["responses"])["200"])["body"])
}

// Compose one of every native kind. Copy actual GET examples, supply fixture
// identities and ETags, and keep every collection explicit, including empties.
func apimScenario(t *testing.T) (*dnsScenario, *Runtime, []asset.Asset) {
	t.Helper()
	metadata, err := providerData()
	if err != nil {
		t.Fatal(err)
	}
	payload, err := os.ReadFile("fixtures/apimanagement/sources.json")
	var manifest []map[string]string
	if err != nil || json.Unmarshal(payload, &manifest) != nil {
		t.Fatal(err)
	}
	examples := map[string]string{}
	for _, entry := range manifest {
		if examples[entry["operation"]] == "" {
			examples[entry["operation"]] = entry["file"]
		}
	}
	s := newDNSScenario()
	root := "/subscriptions/" + testSubscription
	s.lists[root+"/providers/microsoft.apimanagement/gateways"] = []any{}
	s.version[root+"/providers/microsoft.apimanagement/gateways"] = apimVersion
	for _, kind := range []string{apimVaultType, apimIdentityType} {
		mapping, _ := findType(kind)
		path := root + "/providers/" + strings.ToLower(kind)
		s.lists[path], s.version[path] = []any{}, mapping.Version
	}
	s.lists[root+"/resourcegroups"] = []any{map[string]any{"id": root + "/resourcegroups/stewardtest", "type": groupType, "name": "stewardtest", "location": "centralus"}}
	privateEndpointID := root + "/resourcegroups/network-rg/providers/microsoft.network/privateendpoints/apim-endpoint"
	s.add(map[string]any{"id": privateEndpointID, "name": "apim-endpoint", "type": privateEndpointType, "location": "eastus2", "properties": map[string]any{"provisioningState": "Succeeded"}}, "2024-05-01")
	paramsPattern := regexp.MustCompile(`\{([^}]+)\}`)
	var records []map[string]any
	for _, kind := range metadata.kinds {
		if !isAPIMType(kind.NativeType) || isAPIMAssociation(kind.NativeType) || isAPIMGateway(kind.NativeType) {
			continue
		}
		op, _ := metadata.catalog.Operation(kind.ReadOperations[0])
		id := op.Call.Path
		for _, match := range paramsPattern.FindAllStringSubmatch(id, -1) {
			value := "stewardtest"
			if match[1] == "subscriptionId" {
				value = testSubscription
			}
			if values := array(object(object(op.InputSchema["properties"])[match[1]])["enum"]); len(values) > 0 {
				value = text(values[0])
			}
			id = strings.ReplaceAll(id, match[0], value)
		}
		id = strings.ToLower(id)
		raw := apimExample(t, examples[strings.TrimPrefix(kind.ReadOperations[0], "Azure.Microsoft.ApiManagement.")])
		if raw == nil {
			t.Fatal("no native GET example", kind.NativeType)
		}
		originalRoot := apimRootID(text(raw["id"]))
		encoded, _ := json.Marshal(raw)
		if originalRoot != "" {
			pattern := regexp.MustCompile("(?i)" + regexp.QuoteMeta(originalRoot))
			encoded = pattern.ReplaceAll(encoded, []byte(apimRootID(id)))
		}
		json.Unmarshal(encoded, &raw)
		raw["id"], raw["name"], raw["type"] = id, last(id), kind.NativeType
		raw["_apim_header_etag"] = fmt.Sprintf(`"etag-%x"`, sha256.Sum256([]byte(id)))
		props := object(raw["properties"])
		if isAPIMNotification(kind.NativeType) {
			props["recipients"] = map[string]any{"emails": []any{}, "users": []any{}}
		}
		if kind.NativeType == apimServiceType+"/privateEndpointConnections" {
			props["privateEndpoint"] = map[string]any{"id": privateEndpointID}
		}
		if kind.NativeType == apimServiceType {
			raw["location"] = "West US"
			props["provisioningState"] = "Succeeded"
			delete(props, "targetProvisioningState")
			delete(props, "privateEndpointConnections")
		}
		if strings.EqualFold(last(kind.NativeType), "apis") {
			props["apiRevision"], props["isCurrent"] = "1", true
		}
		if strings.EqualFold(last(kind.NativeType), "groups") {
			props["builtIn"] = false
		}
		if kind.NativeType == apimIssueType {
			props["apiId"] = redisParentID(id)
		}
		if strings.EqualFold(last(kind.NativeType), "apis") {
			s.lists[id+"/revisions"] = []any{map[string]any{"apiId": id + ";rev=1", "apiRevision": "1", "isCurrent": true, "isOnline": true, "createdDateTime": "2026-01-27T15:35:05.873Z"}}
			s.version[id+"/revisions"] = apimVersion
		}
		s.add(raw, apimVersion)
		records = append(records, raw)
		collection := redisParentID(id) + "/" + strings.ToLower(last(kind.NativeType))
		if kind.NativeType == apimServiceType {
			collection = root + "/providers/microsoft.apimanagement/service"
			s.lists[id+"/workspacelinks"], s.version[id+"/workspacelinks"] = []any{}, apimVersion
			s.lists[id+"/issues"], s.version[id+"/issues"] = []any{}, apimVersion
		}
		s.lists[collection] = append(s.lists[collection], raw)
		s.version[collection] = apimVersion
	}
	for _, raw := range records {
		for _, kind := range apimOwnedKinds(text(raw["type"])) {
			path := text(raw["id"]) + "/" + strings.ToLower(last(kind))
			if _, ok := s.lists[path]; !ok {
				s.lists[path] = []any{}
			}
			s.version[path] = apimVersion
		}
	}
	s.handle = func(req *http.Request) (*http.Response, bool) {
		id := strings.ToLower(req.URL.Path)
		if req.Method == "GET" && strings.HasSuffix(id, "/revisions") && s.lists[id] != nil && s.status[id] == 0 {
			var rows []any
			for _, value := range s.lists[id] {
				row := object(value)
				revision, err := apimRevisionID(strings.TrimSuffix(id, "/revisions"), row["apiId"])
				if err != nil {
					t.Fatal(err)
				}
				if row["isCurrent"] == true {
					revision = apimBaseAPI(revision)
				}
				if !s.gone[revision] {
					rows = append(rows, row)
				}
			}
			if rows == nil {
				rows = []any{}
			}
			return jsonResponse(200, map[string]any{"value": rows, "count": len(rows), "nextLink": ""}, nil), true
		}
		raw := s.records[id]
		if raw == nil || s.status[id] != 0 || s.gone[id] {
			return nil, false
		}
		if req.Method == "GET" {
			body := maps.Clone(raw)
			delete(body, "_apim_header_etag")
			return jsonResponse(200, body, http.Header{"Etag": {text(raw["_apim_header_etag"])}}), true
		}
		if req.Method == "DELETE" {
			kind, _ := findType(text(raw["type"]))
			op, _ := metadata.catalog.Operation(kind.DeleteOperations[0])
			if object(object(op.InputSchema["properties"])["If-Match"])["required"] == true && req.Header.Get("If-Match") != text(raw["_apim_header_etag"]) {
				t.Fatal("APIM lost native conditional deletion", id)
			}
			for _, key := range []string{"deleteRevisions", "deleteSubscriptions", "force"} {
				if req.URL.Query().Get(key) == "true" {
					t.Fatal("unreviewed APIM force/cascade option", key)
				}
			}
		}
		return nil, false
	}
	for _, raw := range records {
		if raw["type"] == apimIssueType {
			apimIssueProjectionScenario(s, raw)
		}
	}
	r := s.runtime(t)
	var assets []asset.Asset
	for _, raw := range records {
		value := dnsAsset(t, r, raw)
		if kind, _ := findType(value.Identity.NativeType); kind.ReadOnly {
			value.Capabilities = nil
		}
		assets = append(assets, value)
	}
	return s, r, assets
}

func TestAPIMEveryNativeInventoryKind(t *testing.T) {
	_, r, assets := apimScenario(t)
	if len(assets) != 86 {
		t.Fatal("incomplete APIM fixture", len(assets))
	}
	for _, value := range assets {
		t.Run(value.Identity.NativeType, func(t *testing.T) {
			batch, err := r.List(context.Background(), productRequest(r, value.Identity.NativeType))
			if err != nil || !batch.Complete || len(batch.Items) != 1 {
				t.Fatal("native inventory", len(batch.Items), err)
			}
			item := batch.Items[0]
			if item.NativeID != value.Identity.NativeID || item.Location != "westus" || item.Normalized["_inventory_source"] != productInventorySource || text(item.Normalized["arm_etag"]) == "" {
				t.Fatal("native identity, inherited region, or header ETag lost", item.NativeID, item.Location)
			}
			if text(item.Normalized["_apim_private_configuration"]) == "" {
				t.Fatal("missing private drift guard")
			}
		})
	}
}
func TestAPIMReviewedNativeCleanup(t *testing.T) {
	for _, kind := range []string{apimServiceType, apimWorkspaceType, apimAPIType, apimAPIType + "/operations", apimServiceType + "/products", apimServiceType + "/namedValues", apimServiceType + "/privateEndpointConnections"} {
		t.Run(kind, func(t *testing.T) {
			s, r, assets := apimScenario(t)
			target := cdnAsset(t, assets, kind)
			request, _ := dnsRequest(t, r, assets, target)
			driver, err := r.ResolveAction(context.Background(), "connection", target)
			if err != nil {
				t.Fatal(err)
			}
			result, err := driver.Execute(context.Background(), request)
			if err != nil || len(s.deletes) != 1 || s.deletes[0] != target.Identity.NativeID {
				t.Fatal("native APIM delete", err, s.deletes)
			}
			if len(apimOwnedKinds(kind)) > 0 {
				waited, err := driver.Wait(context.Background(), request, result)
				if err != nil || waited.Done {
					t.Fatal("parent absence hid surviving children", waited, err)
				}
			}
			streamAnalyticsAfterDelete(s)
			encoded, _ := json.Marshal(request)
			json.Unmarshal(encoded, &request)
			encoded, _ = json.Marshal(result)
			json.Unmarshal(encoded, &result)
			driver, err = r.ResolveAction(context.Background(), "connection", request.Asset)
			if err != nil {
				t.Fatal(err)
			}
			waited, err := driver.Wait(context.Background(), request, result)
			if err != nil || !waited.Done {
				t.Fatal("resumed APIM absence", waited, err)
			}
			before := len(s.deletes)
			if _, err := driver.Execute(context.Background(), request); err != nil || len(s.deletes) != before {
				t.Fatal("APIM deletion not idempotent", err)
			}
		})
	}
}
func TestAPIMChangedDefinitionAndAncestorsPreventWrites(t *testing.T) {
	for _, mode := range []string{"policy", "ancestor", "identity", "locked", "etag-missing", "etag-conflict"} {
		t.Run(mode, func(t *testing.T) {
			s, r, assets := apimScenario(t)
			target := cdnAsset(t, assets, apimAPIType+"/operations/policies")
			request := contracts.ActionRequest{Asset: target, Action: "delete"}
			driver, err := r.ResolveAction(context.Background(), "connection", target)
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "policy":
				object(s.records[target.Identity.NativeID]["properties"])["value"] = "<policies><inbound><set-header name='Authorization'><value>new-secret</value></set-header></inbound></policies>"
			case "ancestor":
				object(s.records[apimRootID(target.Identity.NativeID)]["properties"])["publisherName"] = "changed"
			case "identity":
				request.Asset.Identity.NativeID += "-other"
			case "locked":
				s.lists["/subscriptions/"+testSubscription+"/providers/microsoft.authorization/locks"] = []any{map[string]any{"id": apimRootID(target.Identity.NativeID) + "/providers/microsoft.authorization/locks/lock", "properties": map[string]any{"level": "CanNotDelete"}}}
			case "etag-missing":
				s.records[target.Identity.NativeID]["_apim_header_etag"] = ""
			case "etag-conflict":
				s.deleteStatus = 412
			}
			_, err = driver.Execute(context.Background(), request)
			if err == nil || mode != "etag-conflict" && len(s.deletes) != 0 || mode == "etag-conflict" && len(s.deletes) != 1 {
				t.Fatal("changed APIM request authorized", err, s.deletes)
			}
		})
	}
}
func TestAPIMBuiltinsRequireTheirOwningController(t *testing.T) {
	s, r, assets := apimScenario(t)
	for _, kind := range []string{apimServiceType + "/templates", apimServiceType + "/groups"} {
		target := cdnAsset(t, assets, kind)
		if last(kind) == "groups" {
			object(s.records[target.Identity.NativeID]["properties"])["builtIn"] = true
			target = dnsAsset(t, r, s.records[target.Identity.NativeID])
		}
		if !controllerOnlyReason(text(target.Normalized["cleanup_protection_reason"])) {
			t.Fatal("builtin is not controller-owned", kind)
		}
		driver, err := r.ResolveAction(context.Background(), "connection", target)
		if last(kind) == "templates" {
			if err == nil {
				t.Fatal("template reset exposed as deletion")
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		check, err := driver.Preflight(context.Background(), contracts.ActionRequest{Asset: target, Action: "delete"})
		if err != nil || check.Allowed {
			t.Fatal("builtin group can delete independently", check, err)
		}
		if !slices.Contains(apimOwnedKinds(apimServiceType), kind) {
			t.Fatal("missing intrinsic APIM child")
		}
	}
}
