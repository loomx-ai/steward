package gcp

import (
	"bytes"
	"os"
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
	source, err := os.ReadFile("catalog/source/discovery.json")
	if err != nil {
		t.Fatal(err)
	}
	imported, err := catalog.ImportOfficial("google-discovery", asset.ProviderGCP, "catalog/source/discovery.json", source)
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
		t.Fatal("generated catalog is stale; run go generate ./providers/gcp")
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
			if relation.TargetType == kind.NativeType {
				t.Fatalf("unexpected blanket/self dependency for %s", kind.NativeType)
			}
		}
	}
	// The old all-pairs rules invented dependencies from storage to compute.
	for _, compiled := range metadata.bundle.Specs {
		if compiled.ResourceKind.NativeType == "storage.googleapis.com/Bucket" && len(compiled.Definition.Relationships) != 0 {
			t.Fatal("bucket contains invented dependencies")
		}
	}
}

func TestResourceURLsFollowOfficialMethods(t *testing.T) {
	c := &client{project: "sample-project", number: "123456"}
	tests := []struct{ kind, name, endpoint, operation string }{
		{"compute.googleapis.com/Instance", "projects/sample-project/zones/us-central1-a/instances/vm-a", "https://compute.googleapis.com/compute/v1/projects/sample-project/zones/us-central1-a/instances/vm-a", "compute.instances.get"},
		{"compute.googleapis.com/RegionDisk", "projects/sample-project/regions/us-central1/disks/data", "https://compute.googleapis.com/compute/v1/projects/sample-project/regions/us-central1/disks/data", "compute.regionDisks.get"},
		{"compute.googleapis.com/InstanceTemplate", "projects/sample-project/regions/us-central1/instanceTemplates/template", "https://compute.googleapis.com/compute/v1/projects/sample-project/regions/us-central1/instanceTemplates/template", "compute.regionInstanceTemplates.get"},
		{"compute.googleapis.com/RegionBackendService", "projects/sample-project/regions/us-central1/backendServices/backend", "https://compute.googleapis.com/compute/v1/projects/sample-project/regions/us-central1/backendServices/backend", "compute.regionBackendServices.get"},
		{"compute.googleapis.com/GlobalAddress", "projects/sample-project/global/addresses/ip", "https://compute.googleapis.com/compute/v1/projects/sample-project/global/addresses/ip", "compute.globalAddresses.get"},
		{"secretmanager.googleapis.com/Secret", "projects/sample-project/locations/us-central1/secrets/key", "https://secretmanager.googleapis.com/v1/projects/sample-project/locations/us-central1/secrets/key", "secretmanager.projects.locations.secrets.get"},
		{"run.googleapis.com/Service", "projects/123456/locations/us-central1/services/web", "https://run.googleapis.com/v2/projects/123456/locations/us-central1/services/web", "run.projects.locations.services.get"},
		{"pubsub.googleapis.com/Topic", "projects/sample-project/topics/events", "https://pubsub.googleapis.com/v1/projects/sample-project/topics/events", "pubsub.projects.topics.get"},
		{"sqladmin.googleapis.com/Instance", "projects/sample-project/instances/db", "https://sqladmin.googleapis.com/sql/v1beta4/projects/sample-project/instances/db", "sql.instances.get"},
		{"storage.googleapis.com/Bucket", "sample-bucket", "https://storage.googleapis.com/storage/v1/b/sample-bucket", "storage.buckets.get"},
	}
	for _, test := range tests {
		t.Run(test.operation, func(t *testing.T) {
			kind, ok := findType(test.kind)
			if !ok {
				t.Fatalf("unknown type %s", test.kind)
			}
			nativeID := "//" + strings.Split(test.kind, "/")[0] + "/" + test.name
			got, err := c.resourceURL(kind, nativeID)
			if err != nil || got != test.endpoint {
				t.Fatalf("URL = %q, %v; want %q", got, err, test.endpoint)
			}
			op, _, err := c.resourceOperation(kind, nativeID, "GET")
			if err != nil || op.ID != test.operation {
				t.Fatalf("operation = %q, %v", op.ID, err)
			}
		})
	}
}

func TestResourceURLRejectsWrongOwnershipAndPaths(t *testing.T) {
	c := &client{project: "sample-project", number: "123456"}
	for _, value := range []struct{ kind, name string }{
		{"compute.googleapis.com/Instance", "//compute.googleapis.com/projects/other-project/zones/us-central1-a/instances/vm"},
		{"compute.googleapis.com/Instance", "//compute.googleapis.com/projects/sample-project/zones/us-central1-a/disks/vm"},
		{"compute.googleapis.com/Instance", "//compute.googleapis.com/projects/sample-project/zones/us-central1-a/instances/.."},
		{"compute.googleapis.com/Instance", "//evil.googleapis.com/projects/sample-project/zones/us-central1-a/instances/vm"},
		{"compute.googleapis.com/Instance", "//compute.googleapis.com/projects/sample-project/global/instances/vm"},
		{"compute.googleapis.com/GlobalAddress", "//compute.googleapis.com/projects/sample-project/regions/us-central1/addresses/ip"},
		{"pubsub.googleapis.com/Topic", "//pubsub.googleapis.com/projects/sample-project/subscriptions/events"},
		{"run.googleapis.com/Service", "//run.googleapis.com/projects/sample-project/locations/us-central1/jobs/web"},
		{"storage.googleapis.com/Bucket", "//storage.googleapis.com/sample-bucket/objects/file"},
		{"storage.googleapis.com/Bucket", "//storage.googleapis.com/sample-bucket%2Fobjects"},
	} {
		kind, _ := findType(value.kind)
		if got, err := c.resourceURL(kind, value.name); err == nil {
			t.Errorf("accepted %q as %q", value.name, got)
		}
	}
}
