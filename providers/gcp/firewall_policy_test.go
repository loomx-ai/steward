package gcp

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const firewallTestHierarchy = "locations/global/firewallPolicies/1001"
const firewallTestGlobal = "projects/sample-project/global/firewallPolicies/global-policy"
const firewallTestRegional = "projects/sample-project/regions/us-central1/firewallPolicies/regional-policy"

type firewallScenario struct {
	root                                       string
	policies, containers, networks, operations map[string]map[string]any
	calls, writes                              []string
	emptyPage                                  bool
	hook                                       func(*http.Request) (*http.Response, bool)
}

func newFirewallScenario() *firewallScenario {
	s := &firewallScenario{root: "organizations/123", policies: map[string]map[string]any{}, containers: map[string]map[string]any{}, networks: map[string]map[string]any{}, operations: map[string]map[string]any{}}
	for name, parent := range map[string]string{"organizations/123": "", "folders/456": "organizations/123", "folders/789": "organizations/123"} {
		row := map[string]any{"name": name, "state": "ACTIVE", "createTime": "2026-01-01T00:00:00Z", "displayName": name}
		if parent != "" {
			row["parent"] = parent
		}
		s.containers[name] = row
	}
	for i, name := range []string{firewallTestHierarchy, firewallTestGlobal, firewallTestRegional} {
		id := fmt.Sprint(1001 + i)
		row := map[string]any{"id": id, "name": last(name), "selfLink": "https://www.googleapis.com/compute/v1/" + name, "creationTimestamp": "2026-01-02T00:00:00.000-08:00", "fingerprint": "YWJjZA==", "kind": "compute#firewallPolicy", "description": "reviewed firewall rules", "rules": []any{map[string]any{"priority": float64(1000), "action": "deny", "direction": "INGRESS", "match": map[string]any{"srcIpRanges": []any{"10.0.0.0/8"}, "layer4Configs": []any{map[string]any{"ipProtocol": "tcp", "ports": []any{"22"}}}}, "enableLogging": true}}}
		targets, names := []string{"folders/456", "folders/789"}, []string{"folder 456", "folder 789"}
		if i == 0 {
			row["parent"], row["shortName"] = "folders/456", "hierarchical-policy"
		} else {
			targets, names = []string{"https://www.googleapis.com/compute/v1/projects/sample-project/global/networks/network-a", "https://www.googleapis.com/compute/v1/projects/sample-project/global/networks/network-b"}, []string{"network-a", "network-b"}
			if i == 2 {
				row["region"] = "https://www.googleapis.com/compute/v1/projects/sample-project/regions/us-central1"
			}
		}
		rows := []any{}
		for j, target := range targets {
			rows = append(rows, map[string]any{"name": names[j], "attachmentTarget": target, "firewallPolicyId": id, "shortName": last(name)})
		}
		row["associations"] = rows
		s.policies[name] = row
	}
	for i, name := range []string{"network-a", "network-b"} {
		path := "projects/sample-project/global/networks/" + name
		s.networks[path] = map[string]any{"id": fmt.Sprint(2001 + i), "name": name, "selfLink": "https://www.googleapis.com/compute/v1/" + path, "creationTimestamp": "2026-01-01T00:00:00Z"}
	}
	return s
}

// Literal routes below come from Google's REST API. They do not use generated
// bindings or the adapter's identity parsers, including query-only associations.
func (s *firewallScenario) transport(t *testing.T) roundTripFunc {
	return func(r *http.Request) (*http.Response, error) {
		s.calls = append(s.calls, r.Method+" "+r.URL.String())
		if s.hook != nil {
			if response, handled := s.hook(r); handled {
				return response, nil
			}
		}
		respond := func(status int, data any) (*http.Response, error) { return dataformResponse(r, status, data), nil }
		q := r.URL.Query()
		if r.URL.Host == "cloudasset.googleapis.com" && r.Method == "GET" {
			return respond(200, map[string]any{})
		}
		if r.URL.Host == "cloudresourcemanager.googleapis.com" && r.Method == "GET" {
			name := strings.TrimPrefix(r.URL.Path, "/v3/")
			if name == "folders" {
				rows := []any{}
				if s.emptyPage && q.Get("pageToken") == "" {
					return respond(200, map[string]any{"nextPageToken": "folders-next"})
				}
				if q.Get("pageToken") != "" && q.Get("pageToken") != "folders-next" {
					t.Fatal("wrong folder page token")
				}
				for _, row := range s.containers {
					if row["parent"] == q.Get("parent") {
						rows = append(rows, row)
					}
				}
				return respond(200, map[string]any{"folders": rows})
			}
			if row := s.containers[name]; row != nil {
				return respond(200, row)
			}
			return respond(404, map[string]any{})
		}
		if r.URL.Host != "compute.googleapis.com" || !strings.HasPrefix(r.URL.Path, "/compute/v1/") {
			t.Fatalf("unexpected firewall API %s %s", r.Method, r.URL)
		}
		name := strings.TrimPrefix(r.URL.Path, "/compute/v1/")
		name = strings.Replace(name, "projects/123456/", "projects/sample-project/", 1)
		if strings.Contains(name, "/operations/") && r.Method == "GET" {
			if row := s.operations[name]; row != nil {
				return respond(200, row)
			}
			return respond(404, map[string]any{})
		}
		if row := s.networks[name]; row != nil && r.Method == "GET" {
			return respond(200, row)
		}
		if name == "locations/global/firewallPolicies/listAssociations" && r.Method == "GET" {
			if q.Get("includeInheritedPolicies") != "false" || !strings.Contains(q.Get("targetResource"), "/") {
				t.Fatal("missing reverse association scope")
			}
			rows := []any{}
			for path, policy := range s.policies {
				if strings.HasPrefix(path, "locations/") {
					for _, v := range array(policy["associations"]) {
						if object(v)["attachmentTarget"] == q.Get("targetResource") {
							rows = append(rows, v)
						}
					}
				}
			}
			return respond(200, map[string]any{"associations": rows})
		}
		if (name == "locations/global/firewallPolicies" || name == "projects/sample-project/aggregated/firewallPolicies") && r.Method == "GET" {
			if q.Get("filter") != "" || q.Get("maxResults") != "500" {
				t.Fatal("incomplete or filtered policy listing")
			}
			if s.emptyPage && q.Get("pageToken") == "" {
				return respond(200, map[string]any{"nextPageToken": "policies-next"})
			}
			if q.Get("pageToken") != "" && q.Get("pageToken") != "policies-next" {
				t.Fatal("wrong policy page token")
			}
			if name == "locations/global/firewallPolicies" {
				rows := []any{}
				for _, policy := range s.policies {
					if policy["parent"] == q.Get("parentId") {
						rows = append(rows, policy)
					}
				}
				return respond(200, map[string]any{"items": rows})
			}
			if q.Get("includeAllScopes") != "true" {
				t.Fatal("missing all-scope network policy discovery")
			}
			groups := map[string]any{}
			for path, policy := range s.policies {
				if strings.HasPrefix(path, "projects/") {
					parts := strings.Split(path, "/")
					scope := strings.Join(parts[2:len(parts)-2], "/")
					groups[scope] = map[string]any{"firewallPolicies": []any{policy}}
				}
			}
			return respond(200, map[string]any{"items": groups})
		}
		parent, method := name, ""
		if strings.HasSuffix(name, "/getAssociation") || strings.HasSuffix(name, "/removeAssociation") {
			parent = name[:strings.LastIndex(name, "/")]
			method = last(name)
		}
		policy := s.policies[parent]
		if policy == nil {
			return respond(404, map[string]any{})
		}
		if r.Method == "GET" {
			if method == "" {
				return respond(200, policy)
			}
			for _, row := range array(policy["associations"]) {
				if object(row)["name"] == q.Get("name") {
					return respond(200, row)
				}
			}
			return respond(404, map[string]any{})
		}
		if r.Method != "DELETE" && (r.Method != "POST" || method != "removeAssociation") {
			t.Fatalf("unexpected mutation %s %s", r.Method, r.URL)
		}
		body, _ := io.ReadAll(r.Body)
		if len(body) != 0 || len(q.Get("requestId")) != 36 || q.Get("fingerprint") != "" || len(q) != 1 && (len(q) != 2 || q.Get("name") == "") {
			t.Fatalf("wrong mutation body or query: %s %s", body, r.URL)
		}
		if method == "removeAssociation" {
			found := false
			for _, v := range array(policy["associations"]) {
				found = found || object(v)["name"] == q.Get("name")
			}
			if !found {
				return respond(404, map[string]any{})
			}
		} else if len(array(policy["associations"])) != 0 {
			t.Fatal("policy DELETE preceded association removal")
		}
		s.writes = append(s.writes, r.Method+" "+r.URL.String())
		opName := fmt.Sprint("operation-", len(s.writes))
		parts := strings.Split(parent, "/")
		opPath := strings.Join(parts[:len(parts)-2], "/") + "/operations/" + opName
		operationType := "deleteFirewallPolicy"
		if method == "removeAssociation" {
			operationType = "opaque-native-association-operation"
		}
		row := map[string]any{"name": opName, "status": "RUNNING", "targetLink": policy["selfLink"], "targetId": policy["id"], "clientOperationId": q.Get("requestId"), "operationType": operationType, "selfLink": "https://www.googleapis.com/compute/v1/" + opPath}
		s.operations[opPath] = row
		return respond(200, row)
	}
}

func (s *firewallScenario) runtime(t *testing.T) *Runtime {
	r := protocolRuntime(t, s.transport(t))
	credentials := r.credentials
	r.credentials = credentialFunc(func(ctx context.Context, id asset.ConnectionID) (contracts.Credential, error) {
		value, err := credentials.Resolve(ctx, id)
		value.Values["firewall_policy_parent"] = s.root
		return value, err
	})
	return r
}

func (s *firewallScenario) inventory(t *testing.T, r *Runtime) []asset.Asset {
	t.Helper()
	var values []asset.Asset
	for _, kind := range []string{firewallPolicyType, firewallAssociationType, networkFirewallPolicyType, networkFirewallAssociationType} {
		req := productRequest(r, kind, "project")
		if firewallParentType(kind) == firewallPolicyType {
			req.Source = firewallInventorySource
		}
		req.Limit = 1
		for i := 0; ; i++ {
			batch, err := r.List(context.Background(), req)
			if err != nil || i > 20 {
				t.Fatalf("firewall inventory %s: %+v %v", kind, batch, err)
			}
			for _, item := range batch.Items {
				if item.Actionable == nil || !*item.Actionable {
					t.Fatalf("firewall asset not actionable: %+v", item)
				}
				values = append(values, asset.Asset{ID: asset.AssetID(item.NativeID), Identity: asset.Identity{Provider: asset.ProviderGCP, ConnectionID: "connection", Partition: "gcp", NativeType: kind, NativeID: item.NativeID}, Normalized: item.Normalized, Capabilities: asset.CapabilitySet{asset.CapabilityActionable}, Location: item.Location})
			}
			if batch.Complete {
				break
			}
			req.Cursor = batch.NextCursor
		}
	}
	return values
}

func firewallReviewed(t *testing.T, s *firewallScenario, root string) (*Runtime, []asset.Asset, plan.Result, contracts.ActionRequest) {
	t.Helper()
	r := s.runtime(t)
	values := s.inventory(t, r)
	contributor, err := r.ServiceLifecycle(context.Background(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	contribution, err := contributor.Contribute(context.Background(), "scope", values)
	if err != nil || len(contribution.Unresolved) != 0 || len(contribution.Bindings) != 6 {
		t.Fatalf("firewall contribution: %+v %v", contribution, err)
	}
	for _, b := range contribution.Bindings {
		if b.CleanupPolicy != graph.CleanupDirect || !b.DirectCleanupAllowed || b.Ownership != graph.OwnershipExclusive {
			t.Fatalf("association must have its own removal step: %+v", b)
		}
	}
	value := batchAsset(values, root)
	result, err := plan.Solve(plan.Input{Assets: values, ResolvedAssetIDs: []asset.AssetID{value.ID}, Relationships: contribution.Relationships, LifecycleBindings: contribution.Bindings})
	if err != nil || len(result.Blockers) != 0 || len(result.Steps) != 3 {
		t.Fatalf("firewall plan: %+v %v", result, err)
	}
	request := dataformRequest(t, result, values, value)
	return r, values, result, request
}

func (s *firewallScenario) finish(result contracts.ActionResult, request contracts.ActionRequest) {
	for _, row := range s.operations {
		if row["name"] == last(result.ProviderOperationID) {
			row["status"] = "DONE"
		}
	}
	name := strings.TrimPrefix(request.Asset.Identity.NativeID, "//compute.googleapis.com/")
	if isFirewallPolicy(request.Asset.Identity.NativeType) {
		delete(s.policies, name)
		return
	}
	parent := text(request.Asset.Normalized[firewallContainingPolicy])
	policy := s.policies[strings.TrimPrefix(parent, "//compute.googleapis.com/")]
	rows := []any{}
	for _, row := range array(policy["associations"]) {
		if object(row)["name"] != request.Asset.Normalized["name"] {
			rows = append(rows, row)
		}
	}
	policy["associations"], policy["fingerprint"] = rows, fmt.Sprint("changed-", len(rows))
}

func TestFirewallPoliciesNativePlanAndRestart(t *testing.T) {
	for _, root := range []string{firewallTestHierarchy, firewallTestGlobal, firewallTestRegional} {
		t.Run(root, func(t *testing.T) {
			s := newFirewallScenario()
			s.emptyPage = true
			r, values, solved, request := firewallReviewed(t, s, root)
			if len(values) != 9 || len(request.PrerequisiteDeletions) != 2 || len(request.LifecycleImpacts) != 0 || len(s.writes) != 0 {
				t.Fatalf("invalid reviewed set: %d %+v", len(values), request)
			}
			driver, err := r.ResolveAction(context.Background(), "connection", request.Asset)
			if err != nil {
				t.Fatal(err)
			}
			if check, err := driver.Preflight(context.Background(), request); err == nil && check.Allowed {
				t.Fatal("policy allowed before child deletion")
			}
			for _, prerequisite := range request.PrerequisiteDeletions {
				child := dataformRequest(t, solved, values, prerequisite.Asset)
				childDriver, err := r.ResolveAction(context.Background(), "connection", child.Asset)
				if err != nil {
					t.Fatal(err)
				}
				result, err := childDriver.Execute(context.Background(), child)
				if err != nil {
					t.Fatalf("detach: %v", err)
				}
				if wait, err := childDriver.Wait(context.Background(), child, result); err != nil || wait.Done {
					t.Fatalf("pending association: %+v %v", wait, err)
				}
				s.finish(result, child)
				result, child = roundTripDataformJSON(t, result), roundTripDataformJSON(t, child)
				restart := s.runtime(t)
				childDriver, err = restart.ResolveAction(context.Background(), "connection", child.Asset)
				if err != nil {
					t.Fatal(err)
				}
				if wait, err := childDriver.Wait(context.Background(), child, result); err != nil || !wait.Done {
					t.Fatalf("restarted association: %+v %v", wait, err)
				}
				if read, err := childDriver.Readback(context.Background(), child); err != nil || read.Exists {
					t.Fatalf("association readback: %+v %v", read, err)
				}
			}
			result, err := driver.Execute(context.Background(), request)
			if err != nil {
				t.Fatalf("delete: %v", err)
			}
			s.finish(result, request)
			result, request = roundTripDataformJSON(t, result), roundTripDataformJSON(t, request)
			restart := s.runtime(t)
			driver, err = restart.ResolveAction(context.Background(), "connection", request.Asset)
			if err != nil {
				t.Fatal(err)
			}
			if wait, err := driver.Wait(context.Background(), request, result); err != nil || !wait.Done {
				t.Fatalf("restarted deletion: %+v %v", wait, err)
			}
			before := len(s.writes)
			if _, err := driver.Execute(context.Background(), request); err != nil || len(s.writes) != before {
				t.Fatal("repeated completed delete mutated API", err)
			}
			if len(s.writes) != 3 || len(s.policies) != 2 || len(s.networks) != 2 || len(s.containers) != 3 {
				t.Fatalf("cleanup affected target or another policy: %v", s.writes)
			}
			if strings.HasPrefix(root, "locations/") && !slices.ContainsFunc(s.calls, func(call string) bool { return strings.Contains(call, "/locations/global/operations/") }) {
				t.Fatal("hierarchical policy did not use organization operation endpoint")
			}
		})
	}
}
