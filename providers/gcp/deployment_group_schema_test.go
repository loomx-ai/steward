package gcp

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func TestDeploymentGroupFixturesMatchOfficialSchemas(t *testing.T) {
	compiler := infraFixtureSchemas(t, "fixtures/deployment-group/native-schemas.json", "20260831", "0a4d3eed2f6ff6b98863520cb9e3718949871c9bfaac7b9cb5f276db16cbfafd")
	s := newDeploymentGroupScenario(t)
	for name, data := range s.resources {
		parts := strings.Split(name, "/")
		kind := map[string]string{"deployments": "Deployment", "revisions": "Revision", "resources": "Resource", "previews": "Preview", "resourceChanges": "ResourceChange", "resourceDrifts": "ResourceDrift"}[parts[len(parts)-2]]
		if parts[4] == "deploymentGroups" {
			kind = "DeploymentGroup"
			if len(parts) == 8 {
				kind = "DeploymentGroupRevision"
			}
		}
		schema, err := compiler.Compile("https://fixture.test/infra-manager.json#/definitions/" + kind)
		if err != nil {
			t.Fatal(err)
		}
		if err := schema.Validate(data); err != nil {
			t.Fatalf("%s native fixture: %v", kind, err)
		}
	}
	group, err := compiler.Compile("https://fixture.test/infra-manager.json#/definitions/DeploymentGroup")
	if err != nil {
		t.Fatal(err)
	}
	for _, mutation := range []func(map[string]any){
		func(data map[string]any) { data["provisioningState"] = "NOT_NATIVE" },
		func(data map[string]any) { object(data["deploymentUnits"].([]any)[0])["id"] = false },
		func(data map[string]any) { object(data["deploymentUnits"].([]any)[0])["dependencies"] = "current" },
	} {
		data := roundTripDataformJSON(t, s.resources[groupTestName])
		mutation(data)
		if group.Validate(data) == nil {
			t.Fatal("official schema accepted malformed group state/unit data")
		}
	}
	deprovision, err := compiler.Compile("https://fixture.test/infra-manager.json#/definitions/DeprovisionDeploymentGroupRequest")
	if err != nil {
		t.Fatal(err)
	}
	if err := deprovision.Validate(map[string]any{"force": true, "deletePolicy": "ABANDON"}); err != nil {
		t.Fatal(err)
	}
	if deprovision.Validate(map[string]any{"force": true, "deletePolicy": "IGNORE_DEPLOYMENT_REFERENCES"}) == nil {
		t.Fatal("metadata reference policy was mistaken for physical retention")
	}
	metadata, err := compiler.Compile("https://fixture.test/infra-manager.json#/definitions/OperationMetadata")
	if err != nil {
		t.Fatal(err)
	}
	if err := metadata.Validate(map[string]any{"verb": "update", "target": groupTestName, "provisionDeploymentGroupMetadata": map[string]any{"step": "DEPROVISIONING_DEPLOYMENT_UNITS", "deploymentUnitProgresses": []any{map[string]any{"unitId": "current", "deployment": infraTestDeployment, "intent": "DELETE_DEPLOYMENT", "state": "DELETING_DEPLOYMENT"}}}}); err != nil {
		t.Fatal(err)
	}
}

func TestDeploymentGroupScopeAliasesAndCollectionContract(t *testing.T) {
	c := &client{project: "sample-project", number: "123456"}
	want := "//config.googleapis.com/" + groupTestName
	for _, name := range []string{groupTestName, want, strings.Replace(want, "sample-project", "123456", 1), "https://config.googleapis.com/v1/" + groupTestName} {
		id, err := c.infraID(infraGroup, name)
		if err != nil || id != want {
			t.Fatalf("group alias: %s %v", id, err)
		}
	}
	for _, name := range []string{want + "/revisions/wrong", strings.Replace(want, "sample-project", "foreign-project", 1), strings.Replace(want, "us-central1", "-", 1), "https://config.googleapis.com/v2/" + groupTestName, want + "?force=true"} {
		if _, err := c.infraID(infraGroup, name); err == nil {
			t.Fatalf("invalid group identity accepted: %s", name)
		}
	}
	s := newDeploymentGroupScenario(t)
	r := protocolRuntime(t, s.transport(t))
	for _, scope := range []string{"us-central1", "europe-west1", "global"} {
		before := len(s.calls)
		batch, err := r.List(context.Background(), productRequest(r, infraGroup, scope))
		want := 0
		if scope == "us-central1" {
			want = 1
		}
		if err != nil || !batch.Complete || len(batch.Items) != want || scope == "global" && len(s.calls) != before {
			t.Fatalf("group regional inventory %s: %+v %v", scope, batch, err)
		}
	}
	for _, operation := range []string{"config.projects.locations.deploymentGroups.get", "config.projects.locations.deploymentGroups.revisions.get"} {
		name := groupTestName
		if strings.HasSuffix(operation, "revisions.get") {
			name += "/revisions/g-2"
		}
		result, err := r.Invoke(context.Background(), contracts.Invocation{ConnectionID: "connection", Operation: operation, Parameters: map[string]any{"name": name}})
		encoded, _ := json.Marshal(result.Data)
		if err != nil || strings.Contains(string(encoded), "GROUP_PRIVATE") {
			t.Fatalf("group invoke redaction: %+v %v", result, err)
		}
	}
	s.hook = func(req *http.Request) (*http.Response, bool) {
		if req.URL.Path == "/v1/"+groupTestName+"/revisions" {
			// The group revision API's field is deploymentGroupRevisions, unlike
			// the deployment revision API's revisions field.
			return dataformResponse(req, 200, map[string]any{"revisions": []any{s.resources[groupTestName+"/revisions/g-2"]}}), true
		}
		return nil, false
	}
	if batch, err := r.List(context.Background(), productRequest(r, infraGroupRevision, "us-central1")); err == nil || batch.Complete {
		t.Fatalf("wrong Config collection was accepted as empty: %+v %v", batch, err)
	}
}
