package aws

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/loomx-ai/steward/internal/provider/spec"
)

const (
	cloudControlPhaseDisableProtection = "disable_deletion_protection"
	cloudControlPhaseDelete            = "delete"
)

// keyedPathPattern selects one element of a list of key/value attribute
// objects, for example LoadBalancerAttributes[Key=deletion_protection.enabled].Value.
var keyedPathPattern = regexp.MustCompile(`^([A-Za-z0-9]+)\[([A-Za-z0-9]+)=([^\]]+)\]\.([A-Za-z0-9]+)$`)

// cloudControlProtection is the compiled deletion protection rule of one kind.
type cloudControlProtection struct {
	path     string
	enabled  []string
	disabled any
}

func cloudControlProtectionFromSpec(nativeType string, action spec.ActionSpec) (*cloudControlProtection, error) {
	protection := action.DeletionProtection
	if protection == nil {
		return nil, nil
	}
	if protection.Disable.Operation != cloudControlUpdateOperation {
		return nil, fmt.Errorf("AWS kind %s deletion protection must be disabled through Cloud Control UpdateResource", nativeType)
	}
	parameters := protection.Disable.Parameters
	if parameters["TypeName"] != nativeType || parameters["Identifier"] != "resource.nativeId" || parameters["PatchOperation"] != "replace" {
		return nil, fmt.Errorf("AWS kind %s deletion protection disable parameters are invalid", nativeType)
	}
	value, ok := parameters["PatchValue"]
	if !ok {
		return nil, fmt.Errorf("AWS kind %s deletion protection requires PatchValue", nativeType)
	}
	if strings.TrimSpace(protection.Path) == "" || len(protection.EnabledValues) == 0 {
		return nil, fmt.Errorf("AWS kind %s deletion protection requires path and enabledValues", nativeType)
	}
	return &cloudControlProtection{path: protection.Path, enabled: protection.EnabledValues, disabled: value}, nil
}

// evaluate returns whether protection is enabled and the JSON Patch that turns
// it off. A missing property means the resource default, which is unprotected.
func (p *cloudControlProtection) evaluate(model map[string]any) (bool, string, error) {
	if p == nil {
		return false, "", nil
	}
	pointer := ""
	var current any
	if match := keyedPathPattern.FindStringSubmatch(p.path); match != nil {
		items, _ := model[match[1]].([]any)
		found := false
		for index, item := range items {
			entry, _ := item.(map[string]any)
			if fmt.Sprint(entry[match[2]]) == match[3] {
				current, found = entry[match[4]], true
				pointer = fmt.Sprintf("/%s/%d/%s", match[1], index, match[4])
				break
			}
		}
		if !found {
			return false, "", nil
		}
	} else {
		value, found := valueAtDottedPath(model, p.path)
		if !found {
			return false, "", nil
		}
		current = value
		pointer = "/" + strings.ReplaceAll(p.path, ".", "/")
	}
	actual := strings.TrimSpace(fmt.Sprint(current))
	enabled := false
	for _, value := range p.enabled {
		if strings.EqualFold(actual, value) {
			enabled = true
			break
		}
	}
	if !enabled {
		return false, "", nil
	}
	patch, err := json.Marshal([]map[string]any{{"op": "replace", "path": pointer, "value": p.disabled}})
	if err != nil {
		return false, "", err
	}
	return true, string(patch), nil
}

// deriveCloudControlReferences projects nested identifiers onto flat
// normalized fields so resource relationships can match Cloud Control primary
// identifiers of the target kinds.
func deriveCloudControlReferences(nativeType string, model map[string]any) {
	collect := func(target string, paths ...string) {
		values := map[string]bool{}
		for _, path := range paths {
			for _, value := range cloudControlNetworkValues(model, strings.Split(path, ".")) {
				if value = strings.TrimSpace(value); value != "" {
					values[value] = true
				}
			}
		}
		if len(values) == 0 {
			return
		}
		result := make([]string, 0, len(values))
		for value := range values {
			result = append(result, value)
		}
		sort.Strings(result)
		model[target] = result
	}
	switch nativeType {
	case "AWS::EC2::Instance":
		collect("volume_ids", "Volumes.VolumeId", "BlockDeviceMappings.Ebs.VolumeId")
		collect("network_interface_ids", "NetworkInterfaces.NetworkInterfaceId")
	case "AWS::NetworkFirewall::FirewallPolicy":
		collect("rule_group_arns", "FirewallPolicy.StatefulRuleGroupReferences.ResourceArn", "FirewallPolicy.StatelessRuleGroupReferences.ResourceArn")
	case "AWS::ECS::Service":
		if cluster := strings.TrimSpace(stringValue(model["Cluster"])); cluster != "" {
			model["cluster_name"] = cluster[strings.LastIndex(cluster, "/")+1:]
		}
	case "AWS::ElasticLoadBalancingV2::Listener":
		if arn := strings.TrimSpace(stringValue(nestedValue(model, "MutualAuthentication", "TrustStoreArn"))); arn != "" {
			model["trust_store_arn"] = arn
		}
	case "AWS::ApiGateway::UsagePlan":
		collect("rest_api_ids", "ApiStages.ApiId")
	case "AWS::EC2::SecurityGroup":
		collect("prefix_list_ids", "SecurityGroupIngress.SourcePrefixListId", "SecurityGroupEgress.DestinationPrefixListId")
	case "AWS::Bedrock::Agent":
		collect("knowledge_base_ids", "KnowledgeBases.KnowledgeBaseId")
		// Agents name a guardrail by ID or ARN; only the ARN is its identifier.
		if guardrail := stringValue(nestedValue(model, "GuardrailConfiguration", "GuardrailIdentifier")); strings.HasPrefix(guardrail, "arn:") {
			model["guardrail_arn"] = guardrail
		}
	case "AWS::Scheduler::Schedule":
		// A schedule without a group belongs to the default group.
		group := strings.TrimSpace(stringValue(model["GroupName"]))
		if group == "" {
			group = "default"
		}
		model["schedule_group_name"] = group
	case "AWS::EC2::VPCEndpoint":
		name := strings.TrimSpace(stringValue(model["ServiceName"]))
		if index := strings.LastIndex(name, "."); index >= 0 && strings.HasPrefix(name[index+1:], "vpce-svc-") {
			model["service_id"] = name[index+1:]
		}
	}
}
