package azure

import (
	"bytes"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/catalog"
)

func TestCatalogReproducibleAndSpecsExecutable(t *testing.T) {
	metadata, err := loadProviderData()
	if err != nil {
		t.Fatal(err)
	}
	source, err := os.ReadFile("catalog/source/swagger.json")
	if err != nil {
		t.Fatal(err)
	}
	imported, err := catalog.ImportOfficial("azure-openapi", asset.ProviderAzure, "catalog/source/swagger.json", source)
	if err != nil {
		t.Fatal(err)
	}
	generated, err := catalog.MarshalGenerated(imported)
	if err != nil {
		t.Fatal(err)
	}
	embedded, err := providerFiles.ReadFile("catalog/generated/catalog.json")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(generated, embedded) {
		t.Fatal("generated catalog is stale; run go generate ./providers/azure")
	}
	for _, compiled := range metadata.bundle.Specs {
		kind, ok := findType(compiled.ResourceKind.NativeType)
		if !ok {
			t.Fatalf("spec has no resource mapping: %s", compiled.ResourceKind.NativeType)
		}
		read, ok := metadata.catalog.Operation(compiled.Definition.Discovery.Detail.Operation)
		if !ok || read.Call.Method != resourceReadMethod(kind.NativeType) || read.SourceURI == "" {
			t.Fatalf("missing official read operation for %s", kind.NativeType)
		}
		if len(kind.DeleteOperations) > 0 {
			deletion, ok := compiled.Definition.Actions["delete"]
			if !ok || deletion.Read == nil {
				t.Fatalf("action lacks live readback: %s", kind.NativeType)
			}
		}
		for _, relation := range compiled.Definition.Relationships {
			// Native inheritance, replication, Batch tasks, and APIM revisions,
			// backend pools/fragments use explicit same-kind resource references.
			apimReference := isAPIMType(kind.NativeType) && (last(kind.NativeType) == "apis" || last(kind.NativeType) == "backends" || last(kind.NativeType) == "policyFragments")
			if relation.TargetType == kind.NativeType && !apimReference && kind.NativeType != "Microsoft.Network/firewallPolicies" && kind.NativeType != "Microsoft.Network/trafficManagerProfiles" && kind.NativeType != serviceBusQueueType && kind.NativeType != cognitiveDeploymentType && kind.NativeType != cosmosMongoRoleType && kind.NativeType != mongoClusterType && kind.NativeType != kustoType && kind.NativeType != batchTaskType && kind.NativeType != synapsePipelineType {
				t.Fatalf("unexpected blanket/self dependency for %s", kind.NativeType)
			}
		}
	}
	// The old all-pairs rules invented dependencies from storage to compute.
	for _, compiled := range metadata.bundle.Specs {
		if compiled.ResourceKind.NativeType == diskType && len(compiled.Definition.Relationships) != 0 {
			t.Fatal("bucket contains invented dependencies")
		}
	}
}

func TestEveryResourceBindsItsOfficialReadAndDelete(t *testing.T) {
	metadata, err := providerData()
	if err != nil {
		t.Fatal(err)
	}
	c := &client{subscription: testSubscription}
	for _, kind := range metadata.kinds {
		t.Run(kind.NativeType, func(t *testing.T) {
			operation, _ := metadata.catalog.Operation(kind.ReadOperations[0])
			nativeID := operation.Call.Path
			for _, match := range regexp.MustCompile(`\{([^}]+)\}`).FindAllStringSubmatch(nativeID, -1) {
				value := "stewardtest"
				if synapseDataKind(kind.NativeType).spark {
					if match[1] == "livyApiVersion" {
						value = synapseDataVersion
					}
					if match[1] == "batchId" || match[1] == "sessionId" {
						value = "0"
					}
				}
				if match[1] == "nspConfigName" {
					value = "00000001-2222-3333-4444-111144444444.assoc1"
				}
				if kind.NativeType == fleetGateType && match[1] == "gateName" {
					value = rbacTestRoleName
				}
				if isCommunicationDataType(kind.NativeType) {
					switch match[1] {
					case "phoneNumber":
						value = "+12065551234"
					case "reservationId":
						value = "65c18c7f-8074-4efb-a572-e0df127a9964"
					case "roomId":
						value = "RoomOpaqueID"
					}
				}
				if strings.EqualFold(match[1], "recordType") {
					value = kind.Collection
				}
				if strings.EqualFold(match[1], "subscriptionId") {
					value = testSubscription
				}
				if kind.NativeType == insightsLinkedStorageType && match[1] == "storageType" {
					value = "ServiceProfiler"
				}
				if match[1] == "resourceUri" {
					value = strings.TrimPrefix(resourceID(vmType, "monitored"), "/")
					if azureLocalKind(kind.NativeType) != "" {
						value = strings.TrimPrefix(resourceID(hybridMachineType, "local-vm"), "/")
					}
				}
				if kind.NativeType == defenderPricingType && match[1] == "scopeId" {
					value = "subscriptions/" + testSubscription
				}
				if budget, _ := monitorBudgetKind(kind.NativeType); budget != "" && match[1] == "scope" {
					value = "subscriptions/" + testSubscription
				}
				if rbacResourceKind(kind.NativeType) != "" {
					if match[1] == "scope" {
						value = "subscriptions/" + testSubscription
					} else {
						value = rbacTestRoleName
					}
				}
				nativeID = strings.ReplaceAll(nativeID, match[0], value)
			}
			wantPath := nativeID
			if d := synapseDataKind(kind.NativeType); d.kind != "" {
				if d.spark {
					nativeID = "https://stewardtest.dev.azuresynapse.net" + nativeID
				} else {
					nativeID = resourceID(synapseType, "stewardtest") + "/" + d.collection + "/stewardtest"
				}
			}
			if isBatchDataType(kind.NativeType) {
				nativeID = "https://account.eastus2.batch.azure.com" + nativeID
			}
			if isCommunicationDataType(kind.NativeType) {
				nativeID = "https://account.communication.azure.com" + nativeID
			}
			if row := insightsLegacyKind(kind.NativeType); row.kind != "" {
				nativeID, err = insightsLegacyURL(resourceID(applicationInsightsType, "stewardtest"), row.kind, "OpaqueID")
				if err != nil {
					t.Fatal(err)
				}
				u, _ := url.Parse(nativeID)
				wantPath = u.Path
			}
			endpoint, err := c.resourceURL(kind, nativeID)
			if err != nil {
				t.Fatalf("binding %s: %v", nativeID, err)
			}
			u, _ := url.Parse(endpoint)
			if !strings.EqualFold(u.Path, wantPath) || !synapseDataKind(kind.NativeType).spark && u.Query().Get("api-version") != operation.Call.Version || synapseDataKind(kind.NativeType).spark && (u.Query().Has("api-version") || !strings.Contains(u.Path, "/versions/"+synapseDataVersion+"/")) {
				t.Fatalf("wrong request %s", endpoint)
			}
			if isCommunicationDataType(kind.NativeType) && (u.Path != wantPath || u.Host != "account.communication.azure.com") {
				t.Fatal("Communication endpoint or opaque native identity changed")
			}
			if insightsLegacyKind(kind.NativeType).kind != "" {
				if err := c.insightsLegacyEndpoint(endpoint, nativeID); err != nil {
					t.Fatal("opaque native selector changed", err)
				}
			}
			if !kind.ReadOnly {
				deletion, parameters, err := c.resourceOperation(kind, nativeID, "DELETE")
				if err != nil {
					t.Fatal(err)
				}
				if object(object(deletion.InputSchema["properties"])["If-Match"])["required"] == true {
					if _, err := catalog.BindREST(deletion, parameters); err == nil {
						t.Fatal("conditional delete accepted without its native ETag")
					}
					parameters["If-Match"] = `"reviewed-etag"`
				}
				request, err := catalog.BindREST(deletion, parameters)
				wantMethod := "DELETE"
				if kind.NativeType == batchNodeType {
					wantMethod = "POST"
				}
				if err != nil || request.Method != wantMethod {
					t.Fatalf("delete = %+v, %v", request, err)
				}
			}
		})
	}
}

func TestResourceBindingRejectsCrossSubscriptionAndWrongCollections(t *testing.T) {
	c := &client{subscription: testSubscription}
	kind, _ := findType(vmType)
	base := c.root() + "/resourceGroups/test/providers/Microsoft.Compute/virtualMachines/vm"
	for _, id := range []string{strings.Replace(base, testSubscription, testTenant, 1), strings.Replace(base, "virtualMachines", "disks", 1), base + "/extensions/ext", base + "/..", base + "%2f..", base + "?api-version=other", "https://evil.invalid" + base} {
		if _, err := c.resourceURL(kind, id); err == nil {
			t.Errorf("accepted %q", id)
		}
	}
}
