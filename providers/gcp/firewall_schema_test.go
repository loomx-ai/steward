package gcp

import (
	"context"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/loomx-ai/steward/internal/app/inventory"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/persistence"
	"github.com/loomx-ai/steward/internal/persistence/sqlite"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func TestFirewallFixturesMatchOfficialSchemas(t *testing.T) {
	compute := infraFixtureSchemas(t, "fixtures/firewall-policy/compute-v1-schemas.json", "20260828", "5cee2d2fedf69f756fbc23aaef9153db01c2a2caffdbd6f63681912b65ca8139")
	crm := infraFixtureSchemas(t, "fixtures/firewall-policy/crm-v3-schemas.json", "20260820", "e46acd311b9d23a7ddac98ab85e2d9edaec3df9105f66679821c055c8e1c6131")
	s := newFirewallScenario()
	for name, data := range s.policies {
		schema, err := compute.Compile("https://fixture.test/infra-manager.json#/definitions/FirewallPolicy")
		if err != nil {
			t.Fatal(err)
		}
		if err := schema.Validate(data); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		for _, v := range array(data["associations"]) {
			schema, err := compute.Compile("https://fixture.test/infra-manager.json#/definitions/FirewallPolicyAssociation")
			if err != nil {
				t.Fatal(err)
			}
			if err := schema.Validate(v); err != nil {
				t.Fatal(err)
			}
		}
		malformed := roundTripDataformJSON(t, data)
		malformed["policyType"] = "HIERARCHICAL_POLICY"
		if schema.Validate(malformed) == nil {
			t.Fatal("invented policy type accepted by official schema")
		}
	}
	for name, data := range s.containers {
		kind := "Folder"
		if strings.HasPrefix(name, "organizations/") {
			kind = "Organization"
		}
		schema, err := crm.Compile("https://fixture.test/infra-manager.json#/definitions/" + kind)
		if err != nil {
			t.Fatal(err)
		}
		if err := schema.Validate(data); err != nil {
			t.Fatal(err)
		}
	}
	for _, data := range s.networks {
		schema, err := compute.Compile("https://fixture.test/infra-manager.json#/definitions/Network")
		if err != nil {
			t.Fatal(err)
		}
		if err := schema.Validate(data); err != nil {
			t.Fatal(err)
		}
	}
	r, request := firewallChildRequest(t, s, firewallTestHierarchy)
	driver, err := r.ResolveAction(context.Background(), "connection", request.Asset)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := driver.Execute(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	schema, err := compute.Compile("https://fixture.test/infra-manager.json#/definitions/Operation")
	if err != nil {
		t.Fatal(err)
	}
	for _, op := range s.operations {
		if err := schema.Validate(op); err != nil {
			t.Fatal(err)
		}
		op["status"] = "CANCELLED"
		if schema.Validate(op) == nil {
			t.Fatal("non-native operation status accepted")
		}
	}
}

func TestFirewallInvokeExplicitHierarchyBoundary(t *testing.T) {
	s := newFirewallScenario()
	r := s.runtime(t)
	for _, test := range []struct {
		operation  string
		parameters map[string]any
	}{
		{"cloudresourcemanager.organizations.get", map[string]any{"name": "organizations/123"}},
		{"cloudresourcemanager.folders.get", map[string]any{"name": "folders/456"}},
		{"cloudresourcemanager.folders.list", map[string]any{"parent": "organizations/123"}},
		{"compute.firewallPolicies.get", map[string]any{"firewallPolicy": "1001"}},
		{"compute.firewallPolicies.list", map[string]any{"parentId": "folders/456", "maxResults": 500}},
		{"compute.firewallPolicies.getAssociation", map[string]any{"firewallPolicy": "1001", "name": "folder 456"}},
		{"compute.firewallPolicies.listAssociations", map[string]any{"targetResource": "folders/456", "includeInheritedPolicies": false}},
		{"compute.networkFirewallPolicies.get", map[string]any{"project": "123456", "firewallPolicy": "global-policy"}},
		{"compute.regionNetworkFirewallPolicies.getAssociation", map[string]any{"region": "us-central1", "firewallPolicy": "regional-policy", "name": "network-a"}},
	} {
		if _, err := r.Invoke(context.Background(), contracts.Invocation{ConnectionID: "connection", Operation: test.operation, Parameters: test.parameters}); err != nil {
			t.Fatalf("native authorized call %s: %v", test.operation, err)
		}
	}
	before := len(s.writes)
	for _, test := range []struct {
		operation  string
		parameters map[string]any
	}{
		{"cloudresourcemanager.organizations.get", map[string]any{"name": "organizations/999"}},
		{"cloudresourcemanager.folders.list", map[string]any{"parent": "projects/sample-project"}},
		{"compute.firewallPolicies.get", map[string]any{"firewallPolicy": "-1"}},
		{"compute.firewallPolicies.get", map[string]any{"firewallPolicy": "1001/../1002"}},
		{"compute.firewallPolicies.delete", map[string]any{"firewallPolicy": "1001"}},
		{"compute.firewallPolicies.removeAssociation", map[string]any{"firewallPolicy": "1001", "name": "folder\n456"}},
		{"compute.networkFirewallPolicies.delete", map[string]any{"project": "other-project", "firewallPolicy": "global-policy"}},
		{"compute.regionNetworkFirewallPolicies.removeAssociation", map[string]any{"project": "other-project", "region": "us-central1", "firewallPolicy": "regional-policy", "name": "network-a"}},
	} {
		if _, err := r.Invoke(context.Background(), contracts.Invocation{ConnectionID: "connection", Operation: test.operation, Parameters: test.parameters}); err == nil {
			t.Fatalf("invalid invocation accepted: %s %+v", test.operation, test.parameters)
		}
	}
	s.root = ""
	if _, err := r.Invoke(context.Background(), contracts.Invocation{ConnectionID: "connection", Operation: "compute.firewallPolicies.removeAssociation", Parameters: map[string]any{"firewallPolicy": "1001", "name": "folder 456"}}); err == nil {
		t.Fatal("project-only connection mutated organization policy")
	}
	if len(s.writes) != before {
		t.Fatal("out of scope invocation reached mutation")
	}
}

func TestFirewallAssociationCompositeIdentityAndScopeValidation(t *testing.T) {
	c := &client{project: "sample-project", number: "123456", firewallParent: "organizations/123"}
	parent := "//compute.googleapis.com/" + firewallTestHierarchy
	for _, name := range []string{"folder 456", "organization 123", "a/b", "policy+name", "规则"} {
		id := firewallAssociationID(parent, name)
		gotParent, gotName, err := c.firewallIdentityParts(firewallAssociationType, id)
		if err != nil || gotParent != parent || gotName != name {
			t.Fatalf("native composite association %q: %s %s %v", name, gotParent, gotName, err)
		}
	}
	for _, id := range []string{parent + "/associations/", parent + "/associations/..", parent + "/associations/folder%20456/", parent + "/associations/a%2fb", parent + "/associations/a%00b", parent + "/associations/%ff", strings.Replace(parent, "1001", "01001", 1) + "/associations/folder%20456"} {
		if _, _, err := c.firewallIdentityParts(firewallAssociationType, id); err == nil {
			t.Fatalf("invalid native association accepted: %s", id)
		}
	}
	for _, root := range []string{"organizations/0", "folders/01", "folders/-1", "projects/sample-project", "organizations/123/", "organizations/123?key=x", "organizations/18446744073709551616"} {
		credential, _ := testCredential(t)
		credential.Values["firewall_policy_parent"] = root
		if _, err := newClient(credential, nil); err == nil {
			t.Fatalf("invalid hierarchy credential %s", root)
		}
	}
}

func TestNativeCleanupAcceptsValidatedGCPPartition(t *testing.T) {
	// Exercise the connection validator itself so fixture spellings cannot hide a
	// runtime mismatch between newly created connections and native action drivers.
	credential, _ := testCredential(t)
	validator := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
		if req.URL.Host != "cloudasset.googleapis.com" {
			t.Fatalf("unexpected validation call %s", req.URL)
		}
		return dataformResponse(req, 200, map[string]any{}), nil
	})
	identity, err := validator.ValidateConnection(context.Background(), credential)
	if err != nil || identity.Partition != "gcp" {
		t.Fatalf("validate connection: %+v %v", identity, err)
	}
	for _, kind := range []string{monitoredProjectType, infraDeployment, infraPreview, infraGroup} {
		t.Run(kind, func(t *testing.T) {
			var r *Runtime
			var request contracts.ActionRequest
			switch kind {
			case monitoredProjectType:
				s := newMetricsScenario(t)
				r = protocolRuntime(t, s.transport(t))
				value := metricsAsset(s.assets(t, r), metricsTestLink)
				request = contracts.ActionRequest{Action: "delete", Asset: value, IdempotencyKey: "validated-gcp-partition"}
			case infraGroup:
				s := newDeploymentGroupScenario(t)
				r, _, _, request = deploymentGroupReviewed(t, s, nil)
			default:
				s := newInfraScenario(t)
				name := infraTestDeployment
				if kind == infraPreview {
					name = infraTestPreview
				}
				r, _, _, request = infraReviewed(t, s, name, nil)
			}
			request.Asset.Identity.Partition = identity.Partition
			for i := range request.LifecycleImpacts {
				request.LifecycleImpacts[i].Asset.Identity.Partition = identity.Partition
			}
			for i := range request.PrerequisiteDeletions {
				request.PrerequisiteDeletions[i].Asset.Identity.Partition = identity.Partition
			}
			request = projectNativeRequest(t, r, request)
			driver, err := r.ResolveAction(context.Background(), "connection", request.Asset)
			if err != nil {
				t.Fatal(err)
			}
			if check, err := driver.Preflight(context.Background(), request); err != nil || !check.Allowed {
				t.Fatalf("validated partition rejected: %+v %v", check, err)
			}
			if _, err := driver.Execute(context.Background(), request); err != nil {
				t.Fatal("validated partition could not execute", err)
			}
		})
	}
}

// Re-project a reviewed native fixture through the common persistence boundary,
// then reconstruct its frozen references using the persisted asset identities.
func projectNativeRequest(t *testing.T, r *Runtime, request contracts.ActionRequest) contracts.ActionRequest {
	t.Helper()
	ctx := context.Background()
	now := time.Now()
	repositories, err := sqlite.Open(filepath.Join(t.TempDir(), "native-action.db"), filepath.Join("..", "..", "migrations"))
	if err != nil {
		t.Fatal(err)
	}
	connection := asset.CloudConnection{ID: request.Asset.Identity.ConnectionID, Provider: asset.ProviderGCP, Partition: request.Asset.Identity.Partition, Status: asset.ConnectionActive, CreatedAt: now, UpdatedAt: now}
	scope := asset.Scope{ID: "native-projection", ConnectionID: connection.ID, Kind: asset.ScopeProject, NativeID: "sample-project", Name: "sample-project", CreatedAt: now, UpdatedAt: now}
	if err := repositories.Connections().PutConnection(ctx, connection); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Inventory().PutScope(ctx, scope); err != nil {
		t.Fatal(err)
	}
	values := []asset.Asset{request.Asset}
	for _, impact := range request.LifecycleImpacts {
		values = append(values, impact.Asset)
	}
	for _, impact := range request.PrerequisiteDeletions {
		values = append(values, impact.Asset)
	}
	batch := contracts.InventoryBatch{Complete: true}
	seen := map[asset.AssetID]bool{}
	for _, value := range values {
		if seen[value.ID] {
			continue
		}
		seen[value.ID] = true
		name := text(value.Normalized["displayName"])
		if name == "" {
			name = last(text(value.Normalized["name"]))
		}
		batch.Items = append(batch.Items, contracts.InventoryItem{NativeType: value.Identity.NativeType, NativeID: value.Identity.NativeID, ResourceKind: r.resourceKind(value.Identity.NativeType), Name: name, State: resourceState(value.Normalized), Location: value.Location, Tags: map[string]string{}, Normalized: value.Normalized})
	}
	shard := asset.ScanShard{ID: "native-shard", ScanRunID: "native-run", Provider: asset.ProviderGCP, ScopeID: scope.ID, Source: productInventorySource, Status: asset.ShardPending, CreatedAt: now}
	if err := repositories.Inventory().CreateScanRun(ctx, asset.ScanRun{ID: shard.ScanRunID, ConnectionID: connection.ID, Status: asset.ScanRunning, RequestedBy: "test", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Inventory().PutScanShard(ctx, shard); err != nil {
		t.Fatal(err)
	}
	service := inventory.NewService(repositories.Inventory())
	if err := service.ProjectBatch(ctx, &shard, connection, batch, inventory.ProjectionOptions{ObservedAt: now}); err != nil {
		t.Fatal(err)
	}
	page, err := repositories.Inventory().ListAssets(ctx, persistence.ListOptions{Limit: 100})
	if err != nil || len(page.Items) != len(seen) {
		t.Fatal("native fixture projection", err, len(page.Items), len(seen))
	}
	projected := map[asset.AssetID]asset.Asset{}
	for _, old := range values {
		for _, value := range page.Items {
			if old.Identity.NativeID == value.Identity.NativeID && old.Identity.NativeType == value.Identity.NativeType {
				projected[old.ID] = value
			}
		}
	}
	request.Asset = projected[request.Asset.ID]
	for i := range request.LifecycleImpacts {
		impact := &request.LifecycleImpacts[i]
		impact.Asset = projected[impact.Asset.ID]
		impact.ControllerID = projected[impact.ControllerID].ID
	}
	for i := range request.PrerequisiteDeletions {
		impact := &request.PrerequisiteDeletions[i]
		impact.Asset = projected[impact.Asset.ID]
		impact.ControllerID = projected[impact.ControllerID].ID
	}
	return request
}
