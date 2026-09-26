package aws

import (
	"context"
	"strings"
	"testing"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	awsconfigservice "github.com/aws/aws-sdk-go-v2/service/configservice"
	configtypes "github.com/aws/aws-sdk-go-v2/service/configservice/types"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func TestChildDependenciesRequireCascadeOrManageChildren(t *testing.T) {
	instance := awsAsset("emr-node", "AWS::EC2::Instance", "i-emr", nil)
	instance.Tags = map[string]string{emrClusterTag: "j-1"}
	role := awsAsset("emr-role", "AWS::IAM::Role", "EMR_DefaultRole", nil)
	role.Location = ""
	assets := []asset.Asset{
		awsAsset("endpoint", clientVPNEndpointType, "cvpn-endpoint-1", nil),
		awsAsset("association", clientVPNAssociationType, "cvpn-endpoint-1/cvpn-assoc-1", map[string]any{"ClientVpnEndpointId": "cvpn-endpoint-1", "TargetNetworkId": "subnet-1"}),
		awsAsset("rule", clientVPNRuleType, "cvpn-endpoint-1/10.0.0.0/16/*", map[string]any{"ClientVpnEndpointId": "cvpn-endpoint-1"}),
		awsAsset("manual-route", clientVPNRouteType, "cvpn-endpoint-1/0.0.0.0/0/subnet-1", map[string]any{"ClientVpnEndpointId": "cvpn-endpoint-1", "TargetSubnet": "subnet-1", "Origin": "add-route"}),
		awsAsset("subnet-route", clientVPNRouteType, "cvpn-endpoint-1/10.0.0.0/16/subnet-1", map[string]any{"ClientVpnEndpointId": "cvpn-endpoint-1", "TargetSubnet": "subnet-1", "Origin": "associate"}),
		awsAsset("topic", "AWS::SNS::Topic", "arn:aws:sns:us-east-1:123456789012:alerts", nil),
		awsAsset("subscription", "AWS::SNS::Subscription", "arn:aws:sns:us-east-1:123456789012:alerts:1", map[string]any{"TopicArn": "arn:aws:sns:us-east-1:123456789012:alerts"}),
		awsAsset("config-rule", "AWS::Config::ConfigRule", "s3-encryption", map[string]any{configRuleCreatedByField: ""}),
		awsAsset("remediation", "AWS::Config::RemediationConfiguration", "s3-encryption", map[string]any{"ConfigRuleName": "s3-encryption"}),
		awsAsset("pack", "AWS::Config::ConformancePack", "baseline", map[string]any{conformancePackRulesField: []string{"baseline-rule-abc"}}),
		awsAsset("pack-rule", "AWS::Config::ConfigRule", "baseline-rule-abc", map[string]any{configRuleCreatedByField: conformancePackService}),
		awsAsset("directory", workspaceDirectoryType, "d-1", nil),
		awsAsset("desktop", "AWS::WorkSpaces::Workspace", "ws-1", map[string]any{"DirectoryId": "d-1"}),
		awsAsset("plan", "AWS::ApiGateway::UsagePlan", "plan-1", nil),
		awsAsset("plan-key", "AWS::ApiGateway::UsagePlanKey", "key-1:plan-1", map[string]any{"UsagePlanId": "plan-1"}),
		awsAsset("trust-store", "AWS::ElasticLoadBalancingV2::TrustStore", "arn:aws:elasticloadbalancing:us-east-1:123456789012:truststore/ca/1", nil),
		awsAsset("listener", "AWS::ElasticLoadBalancingV2::Listener", "arn:listener", map[string]any{"trust_store_arn": "arn:aws:elasticloadbalancing:us-east-1:123456789012:truststore/ca/1"}),
		awsAsset("cluster", emrClusterType, "j-1", map[string]any{"service_role_name": "EMR_DefaultRole", "instance_profile_name": "EMR_EC2_DefaultRole"}),
		instance, role,
	}
	contribution, err := NewLifecycle().Contribute(context.Background(), "scope", assets)
	if err != nil {
		t.Fatal(err)
	}
	required := map[string]bool{}
	uses := map[string]bool{}
	for _, relationship := range contribution.Relationships {
		key := string(relationship.SourceAssetID) + ">" + string(relationship.TargetAssetID)
		switch {
		case relationship.Type == graph.RelationshipDependsOn && relationship.Evidence[graph.RelationshipEvidenceRequiredDeletion] == true:
			if relationship.Evidence[graph.RelationshipEvidenceDeletionOrder] != graph.DeletionOrderTargetBeforeSource || relationship.Evidence[graph.RelationshipEvidenceAuthority] != string(graph.AuthorityAuthoritative) {
				t.Errorf("required relationship %s evidence = %+v", key, relationship.Evidence)
			}
			required[key] = relationship.Evidence[graph.RelationshipEvidenceAutomaticSelection].(bool)
		case relationship.Type == graph.RelationshipUses:
			uses[key] = true
		}
	}
	wantRequired := map[string]bool{"endpoint>association": true, "config-rule>remediation": true, "directory>desktop": false, "trust-store>listener": false}
	if len(required) != len(wantRequired) {
		t.Fatalf("required = %v", required)
	}
	for key, automatic := range wantRequired {
		if value, ok := required[key]; !ok || value != automatic {
			t.Errorf("required %s = %v, %v", key, value, ok)
		}
	}
	if !uses["cluster>emr-role"] || len(uses) != 1 {
		t.Fatalf("uses = %v", uses)
	}
	if len(contribution.Unresolved) != 1 || contribution.Unresolved[0].NativeID != "EMR_EC2_DefaultRole" || contribution.Unresolved[0].BlocksCleanup {
		t.Fatalf("unresolved = %+v", contribution.Unresolved)
	}
	bindings := map[asset.AssetID]graph.LifecycleBinding{}
	for _, binding := range contribution.Bindings {
		bindings[binding.ManagedAssetID] = binding
	}
	wantBindings := map[string]struct {
		controller string
		direct     bool
	}{
		"rule": {"endpoint", true}, "manual-route": {"endpoint", true}, "subscription": {"topic", true}, "plan-key": {"plan", true},
		"subnet-route": {"association", false}, "pack-rule": {"pack", false}, "emr-node": {"cluster", false},
	}
	if len(bindings) != len(wantBindings) {
		t.Fatalf("bindings = %+v", bindings)
	}
	for managed, want := range wantBindings {
		binding, ok := bindings[asset.AssetID(managed)]
		if !ok || binding.ControllerAssetID != asset.AssetID(want.controller) || binding.DirectCleanupAllowed != want.direct ||
			binding.CleanupPolicy != graph.CleanupDelegate || binding.Ownership != graph.OwnershipExclusive ||
			binding.Evidence[graph.LifecycleEvidenceControllerDeleteGuaranteed] != true || binding.Evidence["retention_supported"] != false {
			t.Errorf("binding %s = %+v", managed, binding)
		}
		if want.direct != (binding.Evidence[graph.LifecycleEvidenceNativeDeleteEffect] == true) {
			t.Errorf("binding %s native delete effect = %+v", managed, binding.Evidence)
		}
	}
}

func TestChildDependenciesIgnoreParentsOutsideTheScan(t *testing.T) {
	assets := []asset.Asset{
		awsAsset("association", clientVPNAssociationType, "cvpn-endpoint-1/cvpn-assoc-1", map[string]any{"ClientVpnEndpointId": "cvpn-endpoint-1"}),
		awsAsset("subscription", "AWS::SNS::Subscription", "arn:sub", map[string]any{"TopicArn": "arn:other-account-topic"}),
		// Another region's endpoint of the same connection is not the parent.
		func() asset.Asset {
			endpoint := awsAsset("endpoint-west", clientVPNEndpointType, "cvpn-endpoint-1", nil)
			endpoint.Location = "us-west-2"
			return endpoint
		}(),
	}
	contribution, err := NewLifecycle().Contribute(context.Background(), "scope", assets)
	if err != nil {
		t.Fatal(err)
	}
	if len(contribution.Relationships) != 0 || len(contribution.Bindings) != 0 {
		t.Fatalf("contribution = %+v", contribution)
	}
}

type fakeConfigAPI struct {
	rules    map[string]string
	packs    map[string][]string
	requests [][]string
}

func (f *fakeConfigAPI) DescribeConfigRules(_ context.Context, input *awsconfigservice.DescribeConfigRulesInput, _ ...func(*awsconfigservice.Options)) (*awsconfigservice.DescribeConfigRulesOutput, error) {
	f.requests = append(f.requests, input.ConfigRuleNames)
	output := &awsconfigservice.DescribeConfigRulesOutput{}
	for _, name := range input.ConfigRuleNames {
		createdBy, ok := f.rules[name]
		if !ok {
			return nil, &configtypes.NoSuchConfigRuleException{Message: awssdk.String("missing " + name)}
		}
		rule := configtypes.ConfigRule{ConfigRuleName: awssdk.String(name)}
		if createdBy != "" {
			rule.CreatedBy = awssdk.String(createdBy)
		}
		output.ConfigRules = append(output.ConfigRules, rule)
	}
	return output, nil
}

func (f *fakeConfigAPI) DescribeConformancePackCompliance(_ context.Context, input *awsconfigservice.DescribeConformancePackComplianceInput, _ ...func(*awsconfigservice.Options)) (*awsconfigservice.DescribeConformancePackComplianceOutput, error) {
	rules, ok := f.packs[awssdk.ToString(input.ConformancePackName)]
	if !ok {
		return nil, &configtypes.NoSuchConformancePackException{Message: awssdk.String("missing")}
	}
	output := &awsconfigservice.DescribeConformancePackComplianceOutput{ConformancePackName: input.ConformancePackName}
	for _, rule := range rules {
		output.ConformancePackRuleComplianceList = append(output.ConformancePackRuleComplianceList, configtypes.ConformancePackRuleCompliance{ConfigRuleName: awssdk.String(rule)})
	}
	return output, nil
}

func configItem(nativeType, id string) contracts.InventoryItem {
	return contracts.InventoryItem{NativeType: nativeType, NativeID: id, Normalized: map[string]any{}}
}

// Service-linked rules can only be removed by their creator, and a rule
// deleted after listing does not drop the facts of the others.
func TestConfigEnrichmentMarksServiceLinkedRulesAndPackMembers(t *testing.T) {
	api := &fakeConfigAPI{
		rules: map[string]string{"own": "", "pack-rule": conformancePackService, "hub-rule": "securityhub.amazonaws.com"},
		packs: map[string][]string{"baseline": {"pack-rule", "pack-rule", "other"}, "OrgConformsPack-abc": {}},
	}
	items := []contracts.InventoryItem{
		configItem("AWS::Config::ConfigRule", "own"), configItem("AWS::Config::ConfigRule", "pack-rule"),
		configItem("AWS::Config::ConfigRule", "hub-rule"), configItem("AWS::Config::ConfigRule", "deleted"),
		configItem("AWS::Config::ConformancePack", "baseline"), configItem("AWS::Config::ConformancePack", "OrgConformsPack-abc"),
		configItem("AWS::Config::ConformancePack", "gone"),
	}
	clients := &NativeClients{Config: api}
	if err := enrichLifecycleFacts(context.Background(), clients, items); err != nil {
		t.Fatal(err)
	}
	if items[0].Actionable != nil || items[0].Normalized[configRuleCreatedByField] != "" {
		t.Fatalf("own rule = %+v", items[0])
	}
	for _, index := range []int{1, 2} {
		if items[index].Actionable == nil || *items[index].Actionable || items[index].Normalized["cleanup_protection_reason"] != "service_linked_config_rule" {
			t.Fatalf("service-linked rule = %+v", items[index])
		}
	}
	if _, known := items[3].Normalized[configRuleCreatedByField]; known || len(api.requests) != 5 {
		t.Fatalf("deleted rule = %+v requests=%v", items[3], api.requests)
	}
	if names := stringSliceValue(items[4].Normalized[conformancePackRulesField]); len(names) != 2 || names[0] != "other" || names[1] != "pack-rule" || items[4].Actionable != nil {
		t.Fatalf("pack = %+v", items[4])
	}
	if items[5].Actionable == nil || *items[5].Actionable {
		t.Fatalf("organization member pack = %+v", items[5])
	}
	if _, known := items[6].Normalized[conformancePackRulesField]; known {
		t.Fatalf("deleted pack = %+v", items[6])
	}
}

func TestReservedResourceGroupNamesAreServiceManaged(t *testing.T) {
	kind := asset.ResourceKind{NativeType: "AWS::ResourceGroups::Group"}
	for name, managed := range map[string]bool{"AWS_AppRegistry_Application-web": true, "aws-reserved": true, "team-web": false} {
		item, err := cloudControlItem(CloudControlResource{Identifier: name, Properties: `{"Name":"` + name + `"}`}, kind, asset.Scope{Kind: asset.ScopeRegion, NativeID: "us-east-1"})
		if err != nil {
			t.Fatal(err)
		}
		if (item.Actionable != nil && !*item.Actionable) != managed {
			t.Errorf("%s actionable = %v", name, item.Actionable)
		}
	}
}

// Internal Kafka topics are owned by Kafka and MSK; deleting a cluster deletes
// its topics.
func TestMSKTopicsFollowTheirCluster(t *testing.T) {
	kind := asset.ResourceKind{NativeType: "AWS::MSK::Topic"}
	for name, internal := range map[string]bool{"__consumer_offsets": true, "__amazon_msk_canary": true, "orders": false} {
		arn := "arn:aws:kafka:us-east-1:123456789012:topic/events/0123abcd-4567-89ef-0123-456789abcdef-1/" + name
		item, err := cloudControlItem(CloudControlResource{Identifier: arn, Properties: `{"TopicArn":"` + arn + `","TopicName":"` + name + `"}`}, kind, asset.Scope{Kind: asset.ScopeRegion, NativeID: "us-east-1"})
		if err != nil {
			t.Fatal(err)
		}
		if (item.Actionable != nil && !*item.Actionable) != internal {
			t.Errorf("%s actionable = %v", name, item.Actionable)
		}
	}
	cluster := "arn:aws:kafka:us-east-1:123456789012:cluster/events/0123abcd-4567-89ef-0123-456789abcdef-1"
	contribution, err := NewLifecycle().Contribute(context.Background(), "scope", []asset.Asset{
		awsAsset("cluster", "AWS::MSK::Cluster", cluster, nil),
		awsAsset("topic", "AWS::MSK::Topic", cluster+"/orders", map[string]any{"ClusterArn": cluster}),
	})
	if err != nil || len(contribution.Bindings) != 1 || contribution.Bindings[0].ControllerAssetID != "cluster" || !contribution.Bindings[0].DirectCleanupAllowed {
		t.Fatalf("contribution = %+v, %v", contribution, err)
	}
}

func TestCommonProductDependenciesOrderIPAMSchedulerBedrockAndBeanstalk(t *testing.T) {
	stack := awsAsset("stack", CloudFormationStackNativeType, "arn:aws:cloudformation:us-east-1:123456789012:stack/awseb-e-abc-stack/1", nil)
	stack.Tags = map[string]string{beanstalkEnvironmentTag: "web-prod"}
	assets := []asset.Asset{
		awsAsset("ipam", "AWS::EC2::IPAM", "ipam-1", nil),
		awsAsset("default-scope", "AWS::EC2::IPAMScope", "ipam-scope-default", map[string]any{"IpamId": "ipam-1", "IsDefault": true}),
		awsAsset("scope", "AWS::EC2::IPAMScope", "ipam-scope-private", map[string]any{"IpamId": "ipam-1", "IsDefault": false}),
		awsAsset("pool", "AWS::EC2::IPAMPool", "ipam-pool-top", map[string]any{"IpamScopeId": "ipam-scope-private"}),
		awsAsset("child-pool", "AWS::EC2::IPAMPool", "ipam-pool-child", map[string]any{"IpamScopeId": "ipam-scope-private", "SourceIpamPoolId": "ipam-pool-top"}),
		awsAsset("group", "AWS::Scheduler::ScheduleGroup", "nightly", nil),
		awsAsset("schedule", "AWS::Scheduler::Schedule", "backup", map[string]any{"schedule_group_name": "nightly"}),
		awsAsset("kb", "AWS::Bedrock::KnowledgeBase", "KB123", nil),
		awsAsset("source", "AWS::Bedrock::DataSource", "KB123|DS456", map[string]any{"KnowledgeBaseId": "KB123"}),
		awsAsset("app", "AWS::ElasticBeanstalk::Application", "web", nil),
		awsAsset("env", "AWS::ElasticBeanstalk::Environment", "web-prod", map[string]any{"ApplicationName": "web"}),
		stack,
	}
	contribution, err := NewLifecycle().Contribute(context.Background(), "scope", assets)
	if err != nil {
		t.Fatal(err)
	}
	required := map[string]bool{}
	for _, relationship := range contribution.Relationships {
		if relationship.Evidence[graph.RelationshipEvidenceRequiredDeletion] == true {
			required[string(relationship.SourceAssetID)+">"+string(relationship.TargetAssetID)] = relationship.Evidence[graph.RelationshipEvidenceAutomaticSelection].(bool)
		}
	}
	want := map[string]bool{"ipam>scope": true, "scope>pool": false, "scope>child-pool": false, "pool>child-pool": false, "kb>source": false, "app>env": false}
	if len(required) != len(want) {
		t.Fatalf("required = %v", required)
	}
	for key, automatic := range want {
		if value, ok := required[key]; !ok || value != automatic {
			t.Errorf("required %s = %v, %v", key, value, ok)
		}
	}
	bindings := map[asset.AssetID]graph.LifecycleBinding{}
	for _, binding := range contribution.Bindings {
		bindings[binding.ManagedAssetID] = binding
	}
	if len(bindings) != 3 || bindings["default-scope"].ControllerAssetID != "ipam" || bindings["default-scope"].DirectCleanupAllowed ||
		bindings["schedule"].ControllerAssetID != "group" || !bindings["schedule"].DirectCleanupAllowed ||
		bindings["stack"].ControllerAssetID != "env" || bindings["stack"].DirectCleanupAllowed {
		t.Fatalf("bindings = %+v", bindings)
	}
}

func TestCommonProductServiceManagedResourcesAreProtected(t *testing.T) {
	for _, tc := range []struct {
		kind, id, properties string
		managed              bool
	}{
		{"AWS::EC2::PrefixList", "pl-63a5400a", `{"OwnerId":"AWS"}`, true},
		{"AWS::EC2::PrefixList", "pl-0123456789abcdef0", `{"OwnerId":"123456789012"}`, false},
		{"AWS::EC2::IPAMScope", "ipam-scope-1", `{"IsDefault":true}`, true},
		{"AWS::EC2::IPAMScope", "ipam-scope-2", `{"IsDefault":false}`, false},
		{"AWS::Scheduler::ScheduleGroup", "default", `{}`, true},
		{"AWS::Scheduler::ScheduleGroup", "nightly", `{}`, false},
	} {
		item, err := cloudControlItem(CloudControlResource{Identifier: tc.id, Properties: tc.properties}, asset.ResourceKind{NativeType: tc.kind}, asset.Scope{Kind: asset.ScopeRegion, NativeID: "us-east-1"})
		if err != nil {
			t.Fatal(err)
		}
		if (item.Actionable != nil && !*item.Actionable) != tc.managed {
			t.Errorf("%s %s actionable = %v", tc.kind, tc.id, item.Actionable)
		}
	}
	model := map[string]any{"SecurityGroupIngress": []any{map[string]any{"SourcePrefixListId": "pl-1"}}, "SecurityGroupEgress": []any{map[string]any{"DestinationPrefixListId": "pl-2"}}}
	deriveCloudControlReferences("AWS::EC2::SecurityGroup", model)
	if strings.Join(stringSliceValue(model["prefix_list_ids"]), ",") != "pl-1,pl-2" {
		t.Fatalf("prefix lists = %v", model["prefix_list_ids"])
	}
}
