package aws

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const ec2Namespace = ` xmlns="http://ec2.amazonaws.com/doc/2016-11-15/"`

func clientVPNEndpointXML(id, status string) string {
	return `<item><clientVpnEndpointId>` + id + `</clientVpnEndpointId><status><code>` + status + `</code></status><vpcId>vpc-1</vpcId>` +
		`<securityGroupIdSet><item>sg-1</item></securityGroupIdSet><serverCertificateArn>arn:aws:acm:us-east-1:123456789012:certificate/server</serverCertificateArn>` +
		`<connectionLogOptions><enabled>true</enabled><cloudwatchLogGroup>vpn-logs</cloudwatchLogGroup></connectionLogOptions></item>`
}

// The endpoint listing, child listings and deletes follow the documented
// ec2Query shapes and are decoded by the official SDK client.
func TestClientVPNProtocolListsChildrenOfEveryEndpointAndDeletesThem(t *testing.T) {
	var mu sync.Mutex
	endpointStatus := "available"
	associated, revoked := true, false
	var calls []string
	runtime := protocolNativeRuntime(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		form := formAction(t, r)
		action := form.Get("Action")
		calls = append(calls, action+":"+form.Get("ClientVpnEndpointId"))
		w.Header().Set("Content-Type", "text/xml")
		w.Header().Set("X-Amzn-RequestId", "rid-"+strings.ToLower(action))
		switch action {
		case "DescribeClientVpnEndpoints":
			if form.Get("ClientVpnEndpointId.1") == "cvpn-endpoint-gone" {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(w, `<Response><Errors><Error><Code>InvalidClientVpnEndpointId.NotFound</Code><Message>missing</Message></Error></Errors><RequestID>rid-missing</RequestID></Response>`)
				return
			}
			items := clientVPNEndpointXML("cvpn-endpoint-a", endpointStatus)
			if form.Get("ClientVpnEndpointId.1") == "" {
				items += clientVPNEndpointXML("cvpn-endpoint-b", "available")
			}
			_, _ = io.WriteString(w, `<DescribeClientVpnEndpointsResponse`+ec2Namespace+`><requestId>rid</requestId><clientVpnEndpoint>`+items+`</clientVpnEndpoint></DescribeClientVpnEndpointsResponse>`)
		case "DescribeClientVpnTargetNetworks":
			items := ""
			if associated || form.Get("ClientVpnEndpointId") != "cvpn-endpoint-a" {
				endpoint := form.Get("ClientVpnEndpointId")
				suffix := strings.TrimPrefix(endpoint, "cvpn-endpoint-")
				items = `<item><associationId>cvpn-assoc-` + suffix + `</associationId><vpcId>vpc-1</vpcId><targetNetworkId>subnet-` + suffix + `</targetNetworkId><clientVpnEndpointId>` + endpoint + `</clientVpnEndpointId><status><code>associated</code></status></item>`
			}
			_, _ = io.WriteString(w, `<DescribeClientVpnTargetNetworksResponse`+ec2Namespace+`><requestId>rid</requestId><clientVpnTargetNetworks>`+items+`</clientVpnTargetNetworks></DescribeClientVpnTargetNetworksResponse>`)
		case "DisassociateClientVpnTargetNetwork":
			if form.Get("AssociationId") != "cvpn-assoc-a" {
				t.Errorf("disassociate %v", form)
			}
			associated = false
			_, _ = io.WriteString(w, `<DisassociateClientVpnTargetNetworkResponse`+ec2Namespace+`><requestId>rid</requestId><associationId>cvpn-assoc-a</associationId><status><code>disassociating</code></status></DisassociateClientVpnTargetNetworkResponse>`)
		case "DescribeClientVpnAuthorizationRules":
			items := ""
			if !revoked {
				items = `<item><clientVpnEndpointId>` + form.Get("ClientVpnEndpointId") + `</clientVpnEndpointId><accessAll>true</accessAll><destinationCidr>10.0.0.0/16</destinationCidr><status><code>active</code></status></item>`
			}
			_, _ = io.WriteString(w, `<DescribeClientVpnAuthorizationRulesResponse`+ec2Namespace+`><requestId>rid</requestId><authorizationRule>`+items+`</authorizationRule></DescribeClientVpnAuthorizationRulesResponse>`)
		case "RevokeClientVpnIngress":
			if form.Get("RevokeAllGroups") != "true" || form.Get("TargetNetworkCidr") != "10.0.0.0/16" || form.Get("AccessGroupId") != "" {
				t.Errorf("revoke %v", form)
			}
			revoked = true
			_, _ = io.WriteString(w, `<RevokeClientVpnIngressResponse`+ec2Namespace+`><requestId>rid</requestId><status><code>revoking</code></status></RevokeClientVpnIngressResponse>`)
		case "DescribeClientVpnRoutes":
			_, _ = io.WriteString(w, `<DescribeClientVpnRoutesResponse`+ec2Namespace+`><requestId>rid</requestId><routes><item><clientVpnEndpointId>`+form.Get("ClientVpnEndpointId")+`</clientVpnEndpointId><destinationCidr>10.0.0.0/16</destinationCidr><targetSubnet>subnet-a</targetSubnet><origin>associate</origin><status><code>active</code></status></item></routes></DescribeClientVpnRoutesResponse>`)
		case "DeleteClientVpnEndpoint":
			endpointStatus = "deleted"
			_, _ = io.WriteString(w, `<DeleteClientVpnEndpointResponse`+ec2Namespace+`><requestId>rid</requestId><status><code>deleting</code></status></DeleteClientVpnEndpointResponse>`)
		default:
			t.Errorf("unexpected action %s", action)
		}
	})
	ctx := context.Background()
	list := func(nativeType string) []contracts.InventoryItem {
		t.Helper()
		kind := runtime.resourceKind(nativeType, asset.ScopeRegion)
		batch, err := runtime.List(ctx, contracts.InventoryRequest{
			ConnectionID: "connection-protocol", Source: productAPISource, ResourceKind: &kind,
			Scope: asset.Scope{Kind: asset.ScopeRegion, NativeID: "us-east-1", Location: "us-east-1"},
		})
		if err != nil || !batch.Complete {
			t.Fatalf("%s list = %+v err=%v", nativeType, batch, err)
		}
		return batch.Items
	}
	endpoints := list(clientVPNEndpointType)
	if len(endpoints) != 2 || endpoints[0].NativeID != "cvpn-endpoint-a" || endpoints[0].State != "available" {
		t.Fatalf("endpoints = %+v", endpoints)
	}
	references := endpoints[0].Normalized
	if references["vpc_id"] != "vpc-1" || strings.Join(stringSliceValue(references["security_group_ids"]), ",") != "sg-1" ||
		strings.Join(stringSliceValue(references["certificate_arns"]), ",") != "arn:aws:acm:us-east-1:123456789012:certificate/server" || references["log_group_name"] != "vpn-logs" {
		t.Fatalf("endpoint references = %+v", references)
	}
	associations := list(clientVPNAssociationType)
	if len(associations) != 2 || associations[0].NativeID != "cvpn-endpoint-a/cvpn-assoc-a" || associations[1].NativeID != "cvpn-endpoint-b/cvpn-assoc-b" ||
		strings.Join(stringSliceValue(associations[0].Normalized["subnet_ids"]), ",") != "subnet-a" {
		t.Fatalf("associations = %+v", associations)
	}
	rules := list(clientVPNRuleType)
	if len(rules) != 2 || rules[0].NativeID != "cvpn-endpoint-a/10.0.0.0/16/*" {
		t.Fatalf("rules = %+v", rules)
	}
	routes := list(clientVPNRouteType)
	if len(routes) != 2 || routes[0].NativeID != "cvpn-endpoint-a/10.0.0.0/16/subnet-a" {
		t.Fatalf("routes = %+v", routes)
	}

	run := func(nativeType, id string) {
		t.Helper()
		request := nativeRequest(nativeType, id)
		driver, err := runtime.ResolveAction(ctx, "connection-protocol", request.Asset)
		if err != nil {
			t.Fatal(err)
		}
		preflight, err := driver.Preflight(ctx, request)
		if err != nil || !preflight.Allowed {
			t.Fatalf("%s preflight = %+v err=%v", id, preflight, err)
		}
		result, err := driver.Execute(ctx, request)
		if err != nil {
			t.Fatalf("%s execute err=%v", id, err)
		}
		wait, err := driver.Wait(ctx, request, result)
		if err != nil || !wait.Done {
			t.Fatalf("%s wait = %+v err=%v", id, wait, err)
		}
	}
	run(clientVPNAssociationType, "cvpn-endpoint-a/cvpn-assoc-a")
	run(clientVPNRuleType, "cvpn-endpoint-a/10.0.0.0/16/*")
	run(clientVPNEndpointType, "cvpn-endpoint-a")

	// A route added by the association is removed only by disassociating.
	request := nativeRequest(clientVPNRouteType, "cvpn-endpoint-a/10.0.0.0/16/subnet-a")
	driver, err := runtime.ResolveAction(ctx, "connection-protocol", request.Asset)
	if err != nil {
		t.Fatal(err)
	}
	if preflight, err := driver.Preflight(ctx, request); err != nil || preflight.Allowed || !strings.Contains(preflight.Reason, "disassociating") {
		t.Fatalf("association route preflight = %+v err=%v", preflight, err)
	}
	if _, err := driver.Execute(ctx, request); err == nil {
		t.Fatal("association route was deleted directly")
	}
	for _, call := range calls {
		if strings.HasPrefix(call, "DeleteClientVpnRoute") {
			t.Fatalf("calls = %v", calls)
		}
	}
	missing := nativeRequest(clientVPNEndpointType, "cvpn-endpoint-gone")
	missingDriver, err := runtime.ResolveAction(ctx, "connection-protocol", missing.Asset)
	if err != nil {
		t.Fatal(err)
	}
	if preflight, err := missingDriver.Preflight(ctx, missing); err != nil || !preflight.Absent {
		t.Fatalf("missing endpoint preflight = %+v err=%v", preflight, err)
	}
	if readback, err := missingDriver.Readback(ctx, missing); err != nil || readback.Exists {
		t.Fatalf("missing endpoint readback = %+v err=%v", readback, err)
	}
}

func TestClientVPNKeysRejectMalformedIdentifiers(t *testing.T) {
	for _, id := range []string{"", "cvpn-assoc-1", "vpc-1/cvpn-assoc-1"} {
		if _, err := splitClientVPNKey(id, 2); err == nil {
			t.Errorf("%q was accepted", id)
		}
	}
	for _, id := range []string{"cvpn-endpoint-1/subnet-1", "cvpn-endpoint-1//subnet-1"} {
		if _, err := splitClientVPNKey(id, 3); err == nil {
			t.Errorf("%q was accepted", id)
		}
	}
	parts, err := splitClientVPNKey("cvpn-endpoint-1/10.0.0.0/16/subnet-1", 3)
	if err != nil || parts[0] != "cvpn-endpoint-1" || parts[1] != "10.0.0.0/16" || parts[2] != "subnet-1" {
		t.Fatalf("parts = %v err=%v", parts, err)
	}
}

// EMR uses the awsJson1_1 protocol. Termination protection is turned off and
// read back before the cluster is terminated; a terminated cluster is absent.
func TestEMRProtocolDisablesTerminationProtectionBeforeTerminating(t *testing.T) {
	var mu sync.Mutex
	protected, state := true, "WAITING"
	var targets []string
	runtime := protocolNativeRuntime(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		target := r.Header.Get("X-Amz-Target")
		targets = append(targets, strings.TrimPrefix(target, "ElasticMapReduce."))
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/x-amz-json-1.1")
		w.Header().Set("X-Amzn-RequestId", "rid-"+target)
		switch target {
		case "ElasticMapReduce.DescribeCluster":
			if body["ClusterId"] != "j-1" {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(w, `{"__type":"InvalidRequestException","Message":"Cluster id 'j-2' is not valid."}`)
				return
			}
			payload, _ := json.Marshal(map[string]any{"Cluster": map[string]any{
				"Id": "j-1", "Name": "etl", "TerminationProtected": protected, "ServiceRole": "arn:aws:iam::123456789012:role/service-role/EMR_DefaultRole",
				"Status": map[string]any{"State": state},
				"Ec2InstanceAttributes": map[string]any{
					"Ec2SubnetId": "subnet-1", "IamInstanceProfile": "EMR_EC2_DefaultRole",
					"EmrManagedMasterSecurityGroup": "sg-master", "EmrManagedSlaveSecurityGroup": "sg-core", "AdditionalSlaveSecurityGroups": []string{"sg-extra"},
				},
			}})
			_, _ = w.Write(payload)
		case "ElasticMapReduce.ListClusters":
			states, _ := body["ClusterStates"].([]any)
			if len(states) != len(emrActiveStates) {
				t.Errorf("cluster states = %v", states)
			}
			_, _ = io.WriteString(w, `{"Clusters":[{"Id":"j-1","Name":"etl","Status":{"State":"WAITING"}},{"Id":"j-2","Name":"gone","Status":{"State":"WAITING"}}]}`)
		case "ElasticMapReduce.SetTerminationProtection":
			if body["TerminationProtected"] != false {
				t.Errorf("protection body = %v", body)
			}
			protected = false
			_, _ = io.WriteString(w, `{}`)
		case "ElasticMapReduce.TerminateJobFlows":
			if protected {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(w, `{"__type":"ValidationException","Message":"protected"}`)
				return
			}
			state = "TERMINATING"
			_, _ = io.WriteString(w, `{}`)
		default:
			t.Errorf("unexpected target %s", target)
		}
	})
	ctx := context.Background()
	kind := runtime.resourceKind(emrClusterType, asset.ScopeRegion)
	batch, err := runtime.List(ctx, contracts.InventoryRequest{
		ConnectionID: "connection-protocol", Source: productAPISource, ResourceKind: &kind,
		Scope: asset.Scope{Kind: asset.ScopeRegion, NativeID: "us-east-1", Location: "us-east-1"},
	})
	if err != nil || len(batch.Items) != 1 {
		t.Fatalf("list = %+v err=%v", batch, err)
	}
	normalized := batch.Items[0].Normalized
	if normalized["service_role_name"] != "EMR_DefaultRole" || normalized["instance_profile_name"] != "EMR_EC2_DefaultRole" ||
		strings.Join(stringSliceValue(normalized["security_group_ids"]), ",") != "sg-core,sg-extra,sg-master" || normalized["vswitch_id"] != "subnet-1" {
		t.Fatalf("normalized = %+v", normalized)
	}
	targets = nil
	request := nativeRequest(emrClusterType, "j-1")
	driver, err := runtime.ResolveAction(ctx, "connection-protocol", request.Asset)
	if err != nil {
		t.Fatal(err)
	}
	preflight, err := driver.Preflight(ctx, request)
	if err != nil || !preflight.Allowed || preflight.Evidence["deletion_protection"] != true {
		t.Fatalf("preflight = %+v err=%v", preflight, err)
	}
	result, err := driver.Execute(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if wait, err := driver.Wait(ctx, request, result); err != nil || wait.Done || wait.State != "TERMINATING" {
		t.Fatalf("terminating wait = %+v err=%v", wait, err)
	}
	state = "TERMINATED"
	if wait, err := driver.Wait(ctx, request, result); err != nil || !wait.Done {
		t.Fatalf("terminated wait = %+v err=%v", wait, err)
	}
	if got := strings.Join(targets, ","); got != "DescribeCluster,DescribeCluster,SetTerminationProtection,DescribeCluster,TerminateJobFlows,DescribeCluster,DescribeCluster" {
		t.Fatalf("targets = %s", got)
	}
	if preflight, err := driver.Preflight(ctx, request); err != nil || !preflight.Absent {
		t.Fatalf("terminated preflight = %+v err=%v", preflight, err)
	}
}

// WorkSpaces uses awsJson1_1; a deregistered directory counts as absent.
func TestWorkSpacesDirectoryProtocolDeregistersUntilAbsent(t *testing.T) {
	var mu sync.Mutex
	state := "REGISTERED"
	runtime := protocolNativeRuntime(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		target := r.Header.Get("X-Amz-Target")
		w.Header().Set("Content-Type", "application/x-amz-json-1.1")
		switch target {
		case "WorkspacesService.DescribeWorkspaceDirectories":
			_, _ = io.WriteString(w, `{"Directories":[{"DirectoryId":"d-1","DirectoryName":"corp.example.com","State":"`+state+`","SubnetIds":["subnet-1","subnet-2"],"WorkspaceSecurityGroupId":"sg-ws"}]}`)
		case "WorkspacesService.DeregisterWorkspaceDirectory":
			state = "DEREGISTERING"
			_, _ = io.WriteString(w, `{}`)
		default:
			t.Errorf("unexpected target %s", target)
		}
	})
	ctx := context.Background()
	request := nativeRequest(workspaceDirectoryType, "d-1")
	driver, err := runtime.ResolveAction(ctx, "connection-protocol", request.Asset)
	if err != nil {
		t.Fatal(err)
	}
	result, err := driver.Execute(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if wait, err := driver.Wait(ctx, request, result); err != nil || wait.Done {
		t.Fatalf("deregistering wait = %+v err=%v", wait, err)
	}
	state = "DEREGISTERED"
	if wait, err := driver.Wait(ctx, request, result); err != nil || !wait.Done {
		t.Fatalf("deregistered wait = %+v err=%v", wait, err)
	}
}

// WAF uses awsJson1_1. Associations are listed per regional web ACL and per
// protected resource type; Firewall Manager associations are not removed.
func TestWAFAssociationProtocolListsEveryResourceTypeAndRespectsFirewallManager(t *testing.T) {
	var mu sync.Mutex
	managed := false
	associated := map[string]bool{"arn:aws:elasticloadbalancing:us-east-1:123456789012:loadbalancer/app/web/1": true, "arn:aws:apigateway:us-east-1::/restapis/abc/stages/prod": true}
	var resourceTypes []string
	runtime := protocolNativeRuntime(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		target := strings.TrimPrefix(r.Header.Get("X-Amz-Target"), "AWSWAF_20190729.")
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/x-amz-json-1.1")
		acl := map[string]any{"Name": "web", "Id": "acl-1", "ARN": "arn:aws:wafv2:us-east-1:123456789012:regional/webacl/web/acl-1", "ManagedByFirewallManager": managed,
			"DefaultAction": map[string]any{"Allow": map[string]any{}}, "VisibilityConfig": map[string]any{"SampledRequestsEnabled": true, "CloudWatchMetricsEnabled": true, "MetricName": "web"}}
		switch target {
		case "ListWebACLs":
			if body["Scope"] != "REGIONAL" {
				t.Errorf("scope = %v", body["Scope"])
			}
			// WAF returns a marker with the last page; the next request is empty.
			if body["NextMarker"] != nil {
				_, _ = io.WriteString(w, `{"WebACLs":[],"NextMarker":"web"}`)
				return
			}
			_, _ = io.WriteString(w, `{"WebACLs":[{"Name":"web","Id":"acl-1","ARN":"arn:aws:wafv2:us-east-1:123456789012:regional/webacl/web/acl-1"}],"NextMarker":"web"}`)
		case "GetWebACL":
			payload, _ := json.Marshal(map[string]any{"WebACL": acl, "LockToken": "token"})
			_, _ = w.Write(payload)
		case "ListResourcesForWebACL":
			resourceType, _ := body["ResourceType"].(string)
			resourceTypes = append(resourceTypes, resourceType)
			var arns []string
			for arn := range associated {
				if resourceType == "APPLICATION_LOAD_BALANCER" && strings.Contains(arn, "loadbalancer") || resourceType == "API_GATEWAY" && strings.Contains(arn, "restapis") {
					arns = append(arns, arn)
				}
			}
			payload, _ := json.Marshal(map[string]any{"ResourceArns": arns})
			_, _ = w.Write(payload)
		case "GetWebACLForResource":
			if !associated[body["ResourceArn"].(string)] {
				_, _ = io.WriteString(w, `{}`)
				return
			}
			payload, _ := json.Marshal(map[string]any{"WebACL": acl})
			_, _ = w.Write(payload)
		case "DisassociateWebACL":
			delete(associated, body["ResourceArn"].(string))
			_, _ = io.WriteString(w, `{}`)
		default:
			t.Errorf("unexpected target %s", target)
		}
	})
	ctx := context.Background()
	kind := runtime.resourceKind(webACLAssociationType, asset.ScopeRegion)
	batch, err := runtime.List(ctx, contracts.InventoryRequest{
		ConnectionID: "connection-protocol", Source: productAPISource, ResourceKind: &kind,
		Scope: asset.Scope{Kind: asset.ScopeRegion, NativeID: "us-east-1", Location: "us-east-1"},
	})
	if err != nil || len(batch.Items) != 2 || len(resourceTypes) != len(wafRegionalResourceTypes) {
		t.Fatalf("list = %+v types=%v err=%v", batch, resourceTypes, err)
	}
	for _, item := range batch.Items {
		if item.Normalized["web_acl_id"] != "web|acl-1|REGIONAL" {
			t.Fatalf("item = %+v", item)
		}
		if strings.Contains(item.NativeID, "loadbalancer") && item.Normalized["load_balancer_arn"] != item.NativeID {
			t.Fatalf("load balancer reference = %+v", item.Normalized)
		}
	}
	arn := "arn:aws:elasticloadbalancing:us-east-1:123456789012:loadbalancer/app/web/1"
	request := nativeRequest(webACLAssociationType, arn)
	driver, err := runtime.ResolveAction(ctx, "connection-protocol", request.Asset)
	if err != nil {
		t.Fatal(err)
	}
	managed = true
	if preflight, err := driver.Preflight(ctx, request); err != nil || preflight.Allowed {
		t.Fatalf("managed preflight = %+v err=%v", preflight, err)
	}
	managed = false
	result, err := driver.Execute(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if wait, err := driver.Wait(ctx, request, result); err != nil || !wait.Done || associated[arn] {
		t.Fatalf("wait = %+v err=%v", wait, err)
	}
}
