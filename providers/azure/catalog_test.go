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
		if !ok || read.Call.Method != "GET" || read.SourceURI == "" {
			t.Fatalf("missing official read operation for %s", kind.NativeType)
		}
		if len(kind.DeleteOperations) > 0 {
			deletion, ok := compiled.Definition.Actions["delete"]
			if !ok || deletion.Read == nil {
				t.Fatalf("action lacks live readback: %s", kind.NativeType)
			}
		}
		for _, relation := range compiled.Definition.Relationships {
			// Native firewall inheritance and nested Traffic Manager endpoints
			// reference another resource of the same kind.
			if relation.TargetType == kind.NativeType && kind.NativeType != "Microsoft.Network/firewallPolicies" && kind.NativeType != "Microsoft.Network/trafficManagerProfiles" && kind.NativeType != serviceBusQueueType {
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
				if match[1] == "nspConfigName" {
					value = "00000001-2222-3333-4444-111144444444.assoc1"
				}
				if strings.EqualFold(match[1], "recordType") {
					value = kind.Collection
				}
				if strings.EqualFold(match[1], "subscriptionId") {
					value = testSubscription
				}
				if match[1] == "resourceUri" {
					value = strings.TrimPrefix(resourceID(vmType, "monitored"), "/")
				}
				nativeID = strings.ReplaceAll(nativeID, match[0], value)
			}
			endpoint, err := c.resourceURL(kind, nativeID)
			if err != nil {
				t.Fatalf("binding %s: %v", nativeID, err)
			}
			u, _ := url.Parse(endpoint)
			if !strings.EqualFold(u.Path, nativeID) || u.Query().Get("api-version") != operation.Call.Version {
				t.Fatalf("wrong request %s", endpoint)
			}
			if !kind.ReadOnly {
				deletion, parameters, err := c.resourceOperation(kind, nativeID, "DELETE")
				if err != nil {
					t.Fatal(err)
				}
				request, err := catalog.BindREST(deletion, parameters)
				if err != nil || request.Method != "DELETE" {
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
