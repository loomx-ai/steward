package gcp

import (
	"context"
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func infraFixtureSchemas(t *testing.T, path, revision, hash string) *jsonschema.Compiler {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var source map[string]any
	if err := json.Unmarshal(raw, &source); err != nil {
		t.Fatal(err)
	}
	if source["revision"] != revision || source["source_sha256"] != hash {
		t.Fatal("unreviewed native schema provenance")
	}
	schemas := object(source["schemas"])
	var convert func(any)
	convert = func(value any) {
		switch v := value.(type) {
		case map[string]any:
			if _, ok := v["id"].(string); ok {
				delete(v, "id")
			}
			if ref, ok := v["$ref"].(string); ok {
				v["$ref"] = "#/definitions/" + ref
			}
			if v["type"] == "any" {
				delete(v, "type")
			}
			for _, child := range v {
				convert(child)
			}
		case []any:
			for _, child := range v {
				convert(child)
			}
		}
	}
	convert(schemas)
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft4)
	if err := compiler.AddResource("https://fixture.test/infra-manager.json", map[string]any{"definitions": schemas}); err != nil {
		t.Fatal(err)
	}
	return compiler
}

func TestInfraManagerFixturesMatchOfficialSchemas(t *testing.T) {
	compiler := infraFixtureSchemas(t, "fixtures/infra-manager/native-schemas.json", "20260831", "15c2ddd49765663081856686923abef3861993df467c711862b67795cf7217e5")
	s := newInfraScenario(t)
	for name, data := range s.resources {
		kind := map[string]string{"deployments": "Deployment", "revisions": "Revision", "resources": "Resource", "previews": "Preview", "resourceChanges": "ResourceChange", "resourceDrifts": "ResourceDrift"}[strings.Split(name, "/")[len(strings.Split(name, "/"))-2]]
		schema, err := compiler.Compile("https://fixture.test/infra-manager.json#/definitions/" + kind)
		if err != nil {
			t.Fatal(err)
		}
		if err := schema.Validate(data); err != nil {
			t.Fatalf("%s native fixture: %v", kind, err)
		}
	}
	// The native enum, arbitrary before/after values and nested TF IDs must be
	// checked by the independent validator, not silently stripped while adapting $ref.
	resource, err := compiler.Compile("https://fixture.test/infra-manager.json#/definitions/Resource")
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"state", "id"} {
		data := roundTripDataformJSON(t, s.resources[infraTestRevision+"/resources/network"])
		if field == "state" {
			data[field] = "NOT_NATIVE"
		} else {
			object(data["terraformInfo"])[field] = true
		}
		if resource.Validate(data) == nil {
			t.Fatalf("official schema accepted invalid %s", field)
		}
	}
	metadata, err := compiler.Compile("https://fixture.test/infra-manager.json#/definitions/OperationMetadata")
	if err != nil {
		t.Fatal(err)
	}
	if err := metadata.Validate(map[string]any{"target": infraTestDeployment, "verb": "delete", "apiVersion": "v1", "deploymentMetadata": map[string]any{"step": "RUNNING_TF_DESTROY"}}); err != nil {
		t.Fatal(err)
	}
}

func TestInfraManagerIdentityScopeCursorAndInvokeBoundaries(t *testing.T) {
	c := &client{project: "sample-project", number: "123456"}
	want := "//config.googleapis.com/" + infraTestDeployment
	for _, value := range []string{infraTestDeployment, want, strings.Replace(want, "sample-project", "123456", 1), "https://config.googleapis.com/v1/" + infraTestDeployment} {
		id, err := c.infraID(infraDeployment, value)
		if err != nil || id != want {
			t.Fatalf("native alias %s: %s %v", value, id, err)
		}
	}
	for _, value := range []string{want + "/", strings.Replace(want, "sample-project", "other-project", 1), strings.Replace(want, "stack-a", "..", 1), strings.Replace(want, "stack-a", "%2e%2e", 1), strings.Replace(want, "us-central1", "-", 1), "https://config.googleapis.com/v2/" + infraTestDeployment, "https://config.googleapis.com/v1/" + infraTestDeployment + "?key=private", "https://user@config.googleapis.com/v1/" + infraTestDeployment} {
		if _, err := c.infraID(infraDeployment, value); err == nil {
			t.Fatalf("invalid Config identity accepted: %s", value)
		}
	}
	s := newInfraScenario(t)
	r := protocolRuntime(t, s.transport(t))
	for _, kind := range []string{infraDeployment, infraRevision, infraResource, infraPreview, infraChange, infraDrift} {
		before := len(s.calls)
		batch, err := r.List(context.Background(), productRequest(r, kind, "global"))
		if err != nil || !batch.Complete || len(batch.Items) != 0 || len(s.calls) != before {
			t.Fatalf("global shard called a regional Config endpoint: %+v %v", batch, err)
		}
	}
	result, err := r.Invoke(context.Background(), contracts.Invocation{ConnectionID: "connection", Operation: "config.projects.locations.deployments.get", Parameters: map[string]any{"name": infraTestDeployment}})
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(result)
	if strings.Contains(string(encoded), "INFRA_PRIVATE") {
		t.Fatal("native Invoke exposed Terraform inputs")
	}
	before := len(s.calls)
	for _, name := range []string{strings.Replace(infraTestDeployment, "sample-project", "other-project", 1), infraTestDeployment + "/..", strings.Replace(infraTestDeployment, "stack-a", "..", 1), strings.Replace(infraTestDeployment, "stack-a", "%2fprivate", 1), "https://config.googleapis.com/v1/" + infraTestDeployment} {
		if _, err := r.Invoke(context.Background(), contracts.Invocation{ConnectionID: "connection", Operation: "config.projects.locations.deployments.get", Parameters: map[string]any{"name": name}}); err == nil {
			t.Fatal("invalid Config invocation accepted")
		}
	}
	if len(s.calls) != before {
		t.Fatal("invalid Config invocation reached API")
	}
	request := productRequest(r, infraResource, "us-central1")
	request.Limit = 1
	batch, err := r.List(context.Background(), request)
	if err != nil || batch.Complete || batch.NextCursor == "" {
		t.Fatalf("missing revision fanout cursor: %+v %v", batch, err)
	}
	request.Cursor = batch.NextCursor
	s.resources[infraTestDeployment]["labels"] = map[string]any{"environment": "changed"}
	if _, err := r.List(context.Background(), request); err == nil {
		t.Fatal("stale parent cursor skipped an altered deployment")
	}
	for _, kind := range infraTerraformKinds {
		native, ok := findType(kind)
		if !ok || len(native.ReadOperations) == 0 || len(native.DeleteOperations) == 0 {
			t.Fatalf("Terraform mapping has no native driver: %s", kind)
		}
	}
	if !slices.Contains(r.resourceKind(infraDeployment).Capabilities, asset.CapabilityActionable) {
		t.Fatal("deployment cleanup not exposed")
	}
}
