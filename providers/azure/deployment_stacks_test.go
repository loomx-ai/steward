package azure

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/provider/catalog"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

func TestDeploymentStacksNativeContracts(t *testing.T) {
	wire, err := os.ReadFile("fixtures/deployment-stacks/sources.json")
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Ref     string
		Version string `json:"api_version"`
		URI     string `json:"source_uri"`
		SHA     string `json:"source_sha256"`
		Files   map[string]struct{ URL, SHA256, Operation string }
	}
	if json.Unmarshal(wire, &manifest) != nil || manifest.Ref != "07a27fbba41f8597cdfe0f866fcbf9f7c37390f4" || manifest.Version != deploymentStackVersion || manifest.SHA != "0817df7a4e678c850d504ba18119976e0b398793a8ff0399c244e0c1d1783d2c" || len(manifest.Files) != 9 {
		t.Fatal("source manifest")
	}
	wire, err = os.ReadFile("catalog/source/swagger.json")
	if err != nil {
		t.Fatal(err)
	}
	var source catalog.RESTDocumentSet
	if json.Unmarshal(wire, &source) != nil {
		t.Fatal("source snapshot")
	}
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft4)
	compiler.UseLoader(offlineSchemaLoader{})
	found := false
	var native map[string]any
	for _, doc := range source.Documents {
		if doc.SourceURI == manifest.URI {
			found = true
			if json.Unmarshal(doc.Document, &native) != nil {
				t.Fatal("native source")
			}
			if doc.SourceSHA256 != manifest.SHA {
				t.Fatal("source fingerprint")
			}
		}
		value, err := jsonschema.UnmarshalJSON(bytes.NewReader(doc.Document))
		if err != nil {
			t.Fatal(err)
		}
		if err := compiler.AddResource(doc.SourceURI, value); err != nil {
			t.Fatal(err)
		}
	}
	if !found {
		t.Fatal("missing native source")
	}
	metadata, err := providerData()
	if err != nil {
		t.Fatal(err)
	}
	bodies := 0
	readMismatch := 0
	schemaMismatch := 0
	for file, row := range manifest.Files {
		t.Run(file, func(t *testing.T) {
			wire, err := os.ReadFile("fixtures/deployment-stacks/" + file)
			if err != nil || fmt.Sprintf("%x", sha256.Sum256(wire)) != row.SHA256 || !strings.HasPrefix(row.URL, strings.TrimSuffix(manifest.URI, "deploymentStacks.json")) {
				t.Fatal("unchanged example provenance", err)
			}
			var example map[string]any
			if json.Unmarshal(wire, &example) != nil {
				t.Fatal("example JSON")
			}
			op, ok := metadata.catalog.Operation("Azure.Microsoft.Resources." + row.Operation)
			if !ok || op.SourceURI != manifest.URI {
				t.Fatal("native operation missing", row.Operation)
			}
			nativeOperation := object(object(object(native["paths"])[op.Call.Path])[strings.ToLower(op.Call.Method)])
			if strings.Contains(row.Operation, "_Delete") {
				responses := object(nativeOperation["responses"])
				if nativeOperation["x-ms-long-running-operation"] != true || object(nativeOperation["x-ms-long-running-operation-options"])["final-state-via"] != "location" || len(responses) != 4 || responses["200"] == nil || responses["202"] == nil || responses["204"] == nil || object(object(responses["202"])["headers"])["Location"] == nil {
					t.Fatal("native deletion completion contract")
				}
			} else if strings.Contains(row.Operation, "_List") && (op.Pagination == nil || op.Pagination.ItemsPath != "value" || op.Pagination.OutputTokenPath != "nextLink") {
				t.Fatal("native list pagination")
			}
			params := object(example["parameters"])
			bound, err := catalog.BindREST(op, params)
			if err != nil {
				t.Fatal(err)
			}
			endpoint, err := url.Parse(bound.URL)
			if err != nil || endpoint.Query().Get("api-version") != deploymentStackVersion {
				t.Fatal("native URL", bound)
			}
			if strings.Contains(row.Operation, "_Delete") {
				for _, mode := range []string{"delete", "detach"} {
					params["unmanageAction.Resources"], params["unmanageAction.ResourceGroups"], params["unmanageAction.ManagementGroups"] = mode, mode, mode
					params["unmanageAction.ResourcesWithoutDeleteSupport"], params["bypassStackOutOfSyncError"] = "fail", false
					b, err := catalog.BindREST(op, params)
					if err != nil {
						t.Fatal(err)
					}
					u, _ := url.Parse(b.URL)
					if b.Method != "DELETE" || len(u.Query()) != 6 || u.Query().Get("unmanageAction.Resources") != mode || u.Query().Get("unmanageAction.ResourceGroups") != mode || u.Query().Get("unmanageAction.ManagementGroups") != mode || u.Query().Get("unmanageAction.ResourcesWithoutDeleteSupport") != "fail" || u.Query().Get("bypassStackOutOfSyncError") != "false" {
						t.Fatal("unmanage flags not preserved", b)
					}
				}
			}
			for status, value := range object(example["responses"]) {
				body, exists := object(value)["body"]
				if !exists {
					continue
				}
				definition := "DeploymentStack"
				if strings.Contains(row.Operation, "_List") {
					definition = "DeploymentStackListResult"
				}
				schema, err := compiler.Compile(manifest.URI + "#/definitions/" + definition)
				if err != nil {
					t.Fatal(err)
				}
				validation := schema.Validate(body)
				if strings.Contains(row.Operation, "_List") {
					// Upstream examples use a state absent from their own enum.
					if validation == nil || !strings.Contains(validation.Error(), "provisioningState") {
						t.Fatal("expected upstream state mismatch", validation)
					}
					control := batchClone(object(body))
					properties := object(object(array(control["value"])[1])["properties"])
					if properties["provisioningState"] != "succeededWithFailures" {
						t.Fatal("upstream mismatch changed")
					}
					properties["provisioningState"] = "succeeded"
					if err := schema.Validate(control); err != nil {
						t.Fatal("remaining native response schema", status, err)
					}
					schemaMismatch++
				} else if validation != nil {
					t.Fatal("native response schema", status, validation)
				}
				bodies++
				if strings.Contains(row.Operation, "_Get") {
					response := response{status: 200, data: object(body), header: http.Header{}}
					err := validateDeploymentStackRead(response, endpoint.Path)
					if file == "DeploymentStackSubscriptionGet.json" {
						if err == nil {
							t.Fatal("wrong-scope upstream example accepted")
						}
						readMismatch++
					} else if err != nil {
						t.Fatal("native identity", err)
					}
				}
			}
		})
	}
	if bodies != 6 || readMismatch != 1 || schemaMismatch != 3 {
		t.Fatal("example coverage", bodies, readMismatch)
	}
}

func TestDeploymentStacksIdentityAndReadBoundaries(t *testing.T) {
	ids := []string{
		"/subscriptions/" + testSubscription + "/providers/Microsoft.Resources/deploymentStacks/Stack(1)",
		resourceID(deploymentStackType, "Stack(1)"),
		"/providers/Microsoft.Management/managementGroups/Group/providers/Microsoft.Resources/deploymentStacks/Stack(1)",
	}
	for i, id := range ids {
		scope, params, err := deploymentStackParameters(id)
		if err != nil || scope != []string{"Subscription", "ResourceGroup", "ManagementGroup"}[i] || params["deploymentStackName"] != "Stack(1)" {
			t.Fatal("scope", scope, params, err)
		}
		good := response{status: 200, header: http.Header{}, data: map[string]any{"id": strings.ToUpper(id), "type": deploymentStackType, "properties": map[string]any{}}}
		if err := validateDeploymentStackRead(good, id); err != nil {
			t.Fatal(err)
		}
		for _, fault := range []string{"id", "type", "properties", "async", "error", "202", "204"} {
			t.Run(scope+"/"+fault, func(t *testing.T) {
				bad := good
				bad.data = batchClone(good.data)
				bad.header = http.Header{}
				switch fault {
				case "id":
					bad.data["id"] = ids[(i+1)%3]
				case "type":
					bad.data["type"] = vmType
				case "properties":
					bad.data["properties"] = []any{}
				case "async":
					bad.header.Set("Location", "https://management.azure.com/operation")
				case "error":
					bad.data["error"] = map[string]any{"code": "Failed"}
				case "202":
					bad.status = 202
				case "204":
					bad.status = 204
				}
				if validateDeploymentStackRead(bad, id) == nil {
					t.Fatal("invalid own response accepted")
				}
			})
		}
	}
	for _, id := range []string{ids[0] + "/", ids[0] + "?api-version=x", ids[0] + "#fragment", ids[0] + "%2fchild", " " + ids[0], strings.Replace(ids[0], testSubscription, "not-a-uuid", 1), strings.Replace(ids[0], "deploymentStacks", "deployments", 1), strings.Replace(ids[0], "Stack(1)", "..", 1), ids[0] + "/children/item", strings.Replace(ids[2], "managementGroups/Group", "managementGroups/", 1)} {
		if _, _, err := deploymentStackParameters(id); err == nil {
			t.Fatal("invalid stack ID", id)
		}
	}
}

func TestDeploymentStacksPrivateAPIPayload(t *testing.T) {
	secret := "deployment-stack-private-canary"
	raw := map[string]any{"id": resourceID(deploymentStackType, "stack"), "type": deploymentStackType, "properties": map[string]any{
		"provisioningState": "succeeded", "parameters": map[string]any{"innocent": map[string]any{"value": secret}}, "outputs": map[string]any{"innocent": secret}, "template": map[string]any{"plain": secret}, "templateLink": map[string]any{"uri": "https://example.test/?sig=" + secret}, "parametersLink": map[string]any{"uri": secret}, "externalInputs": map[string]any{"value": secret}, "extensionConfigs": map[string]any{"value": secret}, "description": secret, "futureField": secret,
		"actionOnUnmanage": map[string]any{"resources": "delete", "resourceGroups": "detach", "managementGroups": "detach", "resourcesWithoutDeleteSupport": "fail", "future": secret},
		"denySettings":     map[string]any{"mode": "denyDelete", "applyToChildScopes": true, "excludedPrincipals": []any{secret}},
		"resources":        []any{map[string]any{"id": resourceID(vmType, "vm"), "status": "managed", "denyStatus": "denyDelete", "extension": map[string]any{"value": secret}, "identifiers": map[string]any{"key": secret}}},
		"failedResources":  []any{map[string]any{"id": resourceID(vmType, "failed"), "error": map[string]any{"code": "Conflict", "message": secret, "details": []any{map[string]any{"code": "Failed", "message": secret}}}}},
	}}
	for _, prefix := range []string{"/subscriptions/" + testSubscription, "/subscriptions/" + testSubscription + "/resourceGroups/test", "/providers/Microsoft.Management/managementGroups/group"} {
		endpoint := apiURL(prefix+"/providers/Microsoft.Resources/deploymentStacks/stack", deploymentStackVersion)
		for _, payload := range []map[string]any{raw, {"value": []any{raw}, "nextLink": "https://example.test/?sig=" + secret}} {
			safe := safeAPIPayload(payload, endpoint)
			wire, _ := json.Marshal(safe)
			if strings.Contains(string(wire), secret) || !strings.Contains(string(wire), "managed") || !strings.Contains(string(wire), "actionOnUnmanage") {
				t.Fatal("privacy lost useful evidence or leaked configuration", string(wire))
			}
		}
	}
}
