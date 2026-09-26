package aws

import (
	"context"
	"sort"
	"strings"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	awsconfigservice "github.com/aws/aws-sdk-go-v2/service/configservice"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

type ConfigNativeAPI interface {
	DescribeConfigRules(context.Context, *awsconfigservice.DescribeConfigRulesInput, ...func(*awsconfigservice.Options)) (*awsconfigservice.DescribeConfigRulesOutput, error)
	DescribeConformancePackCompliance(context.Context, *awsconfigservice.DescribeConformancePackComplianceInput, ...func(*awsconfigservice.Options)) (*awsconfigservice.DescribeConformancePackComplianceOutput, error)
}

const (
	configRuleCreatedByField  = "created_by"
	conformancePackRulesField = "config_rule_names"
	// The service principal AWS Config records as CreatedBy for rules a
	// conformance pack deploys.
	conformancePackService = "config-conforms.amazonaws.com"
	// Organization conformance packs deploy member packs with this prefix;
	// only the management account's organization pack can remove them.
	organizationConformancePackPrefix = "OrgConformsPack-"
	emrClusterTag                     = "aws:elasticmapreduce:job-flow-id"
	configRuleBatchSize               = 25
)

// enrichConfigRules adds CreatedBy, which the Cloud Control model omits. A
// service-linked rule (created by a conformance pack, an organization rule or
// another service) can only be removed by the service that created it.
func enrichConfigRules(ctx context.Context, client ConfigNativeAPI, items []contracts.InventoryItem, indexes []int) error {
	for start := 0; start < len(indexes); start += configRuleBatchSize {
		batch := indexes[start:min(start+configRuleBatchSize, len(indexes))]
		names := make([]string, 0, len(batch))
		for _, index := range batch {
			names = append(names, items[index].NativeID)
		}
		found, err := describeConfigRules(ctx, client, names)
		if err != nil {
			return err
		}
		for _, index := range batch {
			createdBy, ok := found[items[index].NativeID]
			if !ok {
				continue
			}
			items[index].Normalized[configRuleCreatedByField] = createdBy
			if createdBy != "" {
				actionable := false
				items[index].Actionable = &actionable
				items[index].Normalized["cleanup_protection_reason"] = "service_linked_config_rule"
			}
		}
	}
	return nil
}

func describeConfigRules(ctx context.Context, client ConfigNativeAPI, names []string) (map[string]string, error) {
	result := map[string]string{}
	token := ""
	for {
		input := &awsconfigservice.DescribeConfigRulesInput{ConfigRuleNames: names}
		if token != "" {
			input.NextToken = awssdk.String(token)
		}
		execution.LogCloudAPIRequest(ctx, "config", "DescribeConfigRules", rawCloudPayload(map[string]any{"ConfigRuleNames": names, "NextToken": token}))
		output, err := client.DescribeConfigRules(ctx, input)
		if err != nil {
			execution.LogCloudAPIFailure(ctx, "config", "DescribeConfigRules", err)
			if nativeNotFound(err, "NoSuchConfigRuleException") && len(names) > 1 {
				// One rule deleted since listing fails the batch; read the rest
				// individually instead of dropping their facts.
				for _, name := range names {
					single, err := describeConfigRules(ctx, client, []string{name})
					if err != nil {
						return nil, err
					}
					for key, value := range single {
						result[key] = value
					}
				}
				return result, nil
			}
			if nativeNotFound(err, "NoSuchConfigRuleException") {
				return result, nil
			}
			return nil, NormalizeError(err)
		}
		for _, rule := range output.ConfigRules {
			result[awssdk.ToString(rule.ConfigRuleName)] = strings.TrimSpace(awssdk.ToString(rule.CreatedBy))
		}
		if token = awssdk.ToString(output.NextToken); token == "" {
			return result, nil
		}
	}
}

// enrichConformancePacks records the rules each pack deployed, so deleting the
// pack shows them as deleted with it.
func enrichConformancePacks(ctx context.Context, client ConfigNativeAPI, items []contracts.InventoryItem, indexes []int) error {
	for _, index := range indexes {
		name := items[index].NativeID
		if strings.HasPrefix(name, organizationConformancePackPrefix) {
			actionable := false
			items[index].Actionable = &actionable
			items[index].Normalized["cleanup_protection_reason"] = "organization_conformance_pack_member"
		}
		rules := map[string]bool{}
		token := ""
		missing := false
		for {
			input := &awsconfigservice.DescribeConformancePackComplianceInput{ConformancePackName: awssdk.String(name), Limit: 1000}
			if token != "" {
				input.NextToken = awssdk.String(token)
			}
			execution.LogCloudAPIRequest(ctx, "config", "DescribeConformancePackCompliance", rawCloudPayload(map[string]any{"ConformancePackName": name, "NextToken": token}))
			output, err := client.DescribeConformancePackCompliance(ctx, input)
			if err != nil {
				execution.LogCloudAPIFailure(ctx, "config", "DescribeConformancePackCompliance", err)
				if nativeNotFound(err, "NoSuchConformancePackException") {
					missing = true
					break
				}
				return NormalizeError(err)
			}
			for _, rule := range output.ConformancePackRuleComplianceList {
				if value := awssdk.ToString(rule.ConfigRuleName); value != "" {
					rules[value] = true
				}
			}
			next := awssdk.ToString(output.NextToken)
			if next == "" || next == token {
				break
			}
			token = next
		}
		if missing {
			continue
		}
		names := make([]string, 0, len(rules))
		for rule := range rules {
			names = append(names, rule)
		}
		sort.Strings(names)
		items[index].Normalized[conformancePackRulesField] = names
	}
	return nil
}
