package gcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

// Native paths and response shapes are explicit, independent of the catalog.
// Each scope exercises product listing, identity, deletion, operation polling,
// and a separate target read after the operation reports completion.
func TestComputeExtendedResourceWireLifecycles(t *testing.T) {
	for _, test := range []struct {
		kind, collection, scope string
		aggregate               bool
	}{
		{"ExternalVpnGateway", "externalVpnGateways", "global", false},
		{"FutureReservation", "futureReservations", "zones/us-central1-a", true},
		{"Interconnect", "interconnects", "global", false},
		{"InterconnectAttachment", "interconnectAttachments", "regions/us-central1", true},
		{"NetworkAttachment", "networkAttachments", "regions/us-central1", true},
		{"NetworkFirewallPolicy", "firewallPolicies", "global", true},
		{"NetworkFirewallPolicy", "firewallPolicies", "regions/us-central1", true},
		{"NodeGroup", "nodeGroups", "zones/us-central1-a", true},
		{"NodeTemplate", "nodeTemplates", "regions/us-central1", true},
		{"Reservation", "reservations", "zones/us-central1-a", true},
		{"ResourcePolicy", "resourcePolicies", "regions/us-central1", true},
		{"SecurityPolicy", "securityPolicies", "global", true},
		{"SecurityPolicy", "securityPolicies", "regions/us-central1", true},
		{"ServiceAttachment", "serviceAttachments", "regions/us-central1", true},
		{"TargetVpnGateway", "targetVpnGateways", "regions/us-central1", true},
		{"VpnGateway", "vpnGateways", "regions/us-central1", true},
		{"VpnTunnel", "vpnTunnels", "regions/us-central1", true},
		{"TargetTcpProxy", "targetTcpProxies", "global", true},
		{"TargetTcpProxy", "targetTcpProxies", "regions/us-central1", true},
		{"TargetSslProxy", "targetSslProxies", "global", false},
		{"BackendBucket", "backendBuckets", "global", true},
		{"BackendBucket", "backendBuckets", "regions/us-central1", true},
		{"SslPolicy", "sslPolicies", "global", true},
		{"SslPolicy", "sslPolicies", "regions/us-central1", true},
		{"TargetPool", "targetPools", "regions/us-central1", true},
		{"HttpHealthCheck", "httpHealthChecks", "global", false},
		{"HttpsHealthCheck", "httpsHealthChecks", "global", false},
		{"NetworkEndpointGroup", "networkEndpointGroups", "zones/us-central1-a", true},
		{"NetworkEndpointGroup", "networkEndpointGroups", "regions/us-central1", true},
		{"NetworkEndpointGroup", "networkEndpointGroups", "global", true},
	} {
		t.Run(test.kind+"/"+test.scope, func(t *testing.T) {
			nativeType := "compute.googleapis.com/" + test.kind
			name := "projects/sample-project/" + test.scope + "/" + test.collection + "/fixture"
			path := "/compute/v1/" + name
			listPath := "/compute/v1/projects/sample-project/" + test.scope + "/" + test.collection
			if test.aggregate {
				listPath = "/compute/v1/projects/sample-project/aggregated/" + test.collection
			}
			data := map[string]any{"name": "fixture", "id": "1001", "selfLink": "https://compute.googleapis.com" + path, "description": "wire fixture"}
			if strings.HasPrefix(test.scope, "zones/") {
				data["zone"] = "https://compute.googleapis.com/compute/v1/projects/sample-project/" + test.scope
			}
			if strings.HasPrefix(test.scope, "regions/") {
				data["region"] = "https://compute.googleapis.com/compute/v1/projects/sample-project/" + test.scope
			}
			deleted, polls, reads := false, 0, 0
			transport := func(r *http.Request) (*http.Response, error) {
				if r.URL.Host != "compute.googleapis.com" {
					t.Fatalf("foreign endpoint %s", r.URL)
				}
				var body any = data
				switch {
				case r.URL.Path == listPath:
					if r.Method != "GET" || r.URL.Query().Get("maxResults") != "100" {
						t.Fatalf("invalid product list %s %s", r.Method, r.URL)
					}
					body = map[string]any{"items": []any{data}}
					if test.aggregate {
						body = map[string]any{"items": map[string]any{test.scope: map[string]any{test.collection: []any{data}}}}
					}
				case r.URL.Path == path && r.Method == "DELETE":
					if deleted {
						t.Fatal("duplicate mutation")
					}
					deleted = true
					if r.URL.Query().Get("requestId") != googleRequestID("extended-compute") {
						t.Fatalf("lost request id %s", r.URL)
					}
					body = map[string]any{"name": "delete-fixture", "status": "PENDING"}
				case r.URL.Path == "/compute/v1/projects/sample-project/"+test.scope+"/operations/delete-fixture":
					polls++
					status := "RUNNING"
					if polls > 1 {
						status = "DONE"
					}
					body = map[string]any{"name": "delete-fixture", "status": status}
				case r.URL.Path == path && r.Method == "GET":
					reads++
					if deleted && polls >= 3 {
						return apiResponse(r, 404, `{}`), nil
					}
				default:
					t.Fatalf("unexpected native API %s %s", r.Method, r.URL)
				}
				encoded, _ := json.Marshal(body)
				return apiResponse(r, 200, string(encoded)), nil
			}
			runtime := protocolRuntime(t, transport)
			region := "us-central1"
			if test.scope == "global" {
				region = "global"
			}
			page, err := runtime.List(context.Background(), productRequest(runtime, nativeType, region))
			if err != nil || !page.Complete || len(page.Items) != 1 {
				t.Fatalf("product list=%+v err=%v", page, err)
			}
			item := page.Items[0]
			if item.NativeID != "//compute.googleapis.com/"+name || item.Actionable == nil || !*item.Actionable {
				t.Fatalf("wrong inventory identity/action: %+v", item)
			}
			request := contracts.ActionRequest{Action: "delete", IdempotencyKey: "extended-compute", Asset: asset.Asset{Identity: asset.Identity{NativeType: nativeType, NativeID: item.NativeID}, Normalized: item.Normalized}}
			driver := protocolAction(t, nativeType, name, transport)
			result, err := driver.Execute(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			for poll := 0; poll < 3; poll++ {
				wait, err := driver.Wait(context.Background(), request, result)
				if err != nil {
					t.Fatal(err)
				}
				if wait.Done != (poll == 2) {
					t.Fatalf("completion omitted native operation/absence check on poll %d: %+v", poll, wait)
				}
			}
			if reads < 3 {
				t.Fatalf("missing live reads: %d", reads)
			}
		})
	}
}

func TestComputeExtendedRelationshipsAndSecrets(t *testing.T) {
	c := &client{project: "sample-project", number: "123456"}
	link := func(scope, collection, name string) string {
		return "https://compute.googleapis.com/compute/v1/projects/sample-project/" + scope + "/" + collection + "/" + name
	}
	data := map[string]any{
		"selfLink": link("zones/us-central1-a", "instances", "vm"), "zone": "https://compute.googleapis.com/compute/v1/projects/sample-project/zones/us-central1-a",
		"vpnGateway": link("regions/us-central1", "vpnGateways", "gateway"), "peerExternalGateway": link("global", "externalVpnGateways", "peer"), "router": link("regions/us-central1", "routers", "router"),
		"natSubnets": []any{link("regions/us-central1", "subnetworks", "nat")}, "resourcePolicies": []any{link("regions/us-central1", "resourcePolicies", "schedule")}, "nodeTemplate": link("regions/us-central1", "nodeTemplates", "template"),
		"targetService": link("regions/us-central1", "forwardingRules", "producer"), "sslPolicy": link("global", "sslPolicies", "tls"), "securityPolicy": link("global", "securityPolicies", "armor"), "bucketName": "cdn-bucket",
		"scheduling":          map[string]any{"nodeAffinities": []any{map[string]any{"key": "compute.googleapis.com/node-group-name", "operator": "IN", "values": []any{"hosts"}}}},
		"reservationAffinity": map[string]any{"key": "compute.googleapis.com/reservation-name", "consumeReservationType": "SPECIFIC_RESERVATION", "values": []any{"capacity"}},
		"sharedSecret":        "PRIVATE_VPN_SECRET", "sharedSecretHash": "PRIVATE_VPN_HASH",
	}
	refs := references(c, data)
	for _, kind := range []string{"VpnGateway", "ExternalVpnGateway", "Router", "Subnetwork", "ResourcePolicy", "NodeTemplate", "ForwardingRule", "SslPolicy", "SecurityPolicy", "NodeGroup", "Reservation"} {
		if len(refs["compute.googleapis.com/"+kind]) != 1 {
			t.Fatalf("missing %s: %v", kind, refs)
		}
	}
	if len(refs["storage.googleapis.com/Bucket"]) != 1 {
		t.Fatal("backend bucket storage dependency missing")
	}
	safe, _ := json.Marshal(safePayload(data))
	if strings.Contains(string(safe), "PRIVATE_VPN") {
		t.Fatalf("VPN credentials escaped redaction: %s", safe)
	}
	for _, foreign := range []string{"projects/foreign-project/zones/us-central1-a/reservations/capacity", "../../foreign"} {
		object(data["reservationAffinity"])["values"] = []any{foreign}
		if len(references(c, data)["compute.googleapis.com/Reservation"]) != 0 {
			t.Fatal(fmt.Sprint("foreign affinity accepted ", foreign))
		}
	}
}
