package gcp

import (
	"encoding/json"
)

const cloudNatType = "compute.googleapis.com/RouterNat"
const cloudNatRouterID = "_cloud_nat_router_id"

// NATs are embedded configurations, not independently addressable REST resources.
// A single routers.get response supplies the complete set and its parent identity.
func (c *client) cloudNatRouter(data map[string]any, parent, incarnation string) ([]map[string]any, error) {
	if data["name"] != last(parent) || c.canonicalName(text(data["selfLink"])) != c.canonicalName(parent) || !firewallNumericID(text(data["id"])) || incarnation != "" && data["id"] != incarnation {
		return nil, groupDenied("cloud_nat_router_changed")
	}
	if value, present := data["nextPageToken"]; present && value != "" {
		return nil, groupDenied("cloud_nat_unexpected_pagination")
	}
	values, err := cloudNatObjects(data, "nats")
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, value := range values {
		name := text(value["name"])
		if err := cloudNatData(value, name); err != nil {
			return nil, err
		}
		if seen[name] {
			return nil, groupDenied("cloud_nat_duplicate")
		}
		seen[name] = true
	}
	return values, nil
}

func cloudNatData(data map[string]any, name string) error {
	if data == nil || data["name"] != name || !routePolicySegment.MatchString(name) {
		return groupDenied("cloud_nat_identity_invalid")
	}
	if err := cloudNatScalars(data,
		[]string{"name", "type", "natIpAllocateOption", "autoNetworkTier", "sourceSubnetworkIpRangesToNat", "sourceSubnetworkIpRangesToNat64"},
		[]string{"enableDynamicPortAllocation", "enableEndpointIndependentMapping"},
		[]string{"minPortsPerVm", "maxPortsPerVm", "icmpIdleTimeoutSec", "udpIdleTimeoutSec", "tcpEstablishedIdleTimeoutSec", "tcpTransitoryIdleTimeoutSec", "tcpTimeWaitTimeoutSec", "effectiveTcpTimeWaitTimeoutSec"},
		[]string{"natIps", "drainNatIps", "endpointTypes"}); err != nil {
		return err
	}
	if value, present := data["logConfig"]; present {
		log, ok := value.(map[string]any)
		if !ok || log == nil {
			return groupDenied("cloud_nat_log_invalid")
		}
		if err := cloudNatScalars(log, []string{"filter"}, []string{"enable"}, nil, nil); err != nil {
			return err
		}
	}
	for _, field := range []string{"subnetworks", "nat64Subnetworks"} {
		values, err := cloudNatObjects(data, field)
		if err != nil {
			return err
		}
		for _, value := range values {
			if text(value["name"]) == "" {
				return groupDenied("cloud_nat_subnetwork_invalid")
			}
			if err := cloudNatScalars(value, []string{"name"}, nil, nil, []string{"sourceIpRangesToNat", "secondaryIpRangeNames"}); err != nil {
				return err
			}
		}
	}
	rules, err := cloudNatObjects(data, "rules")
	if err != nil {
		return err
	}
	numbers := map[uint32]bool{}
	for _, rule := range rules {
		if err := cloudNatScalars(rule, []string{"match", "description"}, nil, nil, nil); err != nil {
			return err
		}
		var number uint32
		if value, present := rule["ruleNumber"]; present {
			raw, err := json.Marshal(value)
			if err != nil || value == nil || json.Unmarshal(raw, &number) != nil {
				return groupDenied("cloud_nat_rule_number_invalid")
			}
		}
		if number > 65000 || numbers[number] {
			return groupDenied("cloud_nat_rule_number_invalid")
		}
		numbers[number] = true
		if value, present := rule["action"]; present {
			action, ok := value.(map[string]any)
			if !ok || action == nil {
				return groupDenied("cloud_nat_rule_action_invalid")
			}
			if err := cloudNatScalars(action, nil, nil, nil, []string{"sourceNatActiveIps", "sourceNatDrainIps", "sourceNatActiveRanges", "sourceNatDrainRanges"}); err != nil {
				return err
			}
		}
	}
	return nil
}

func cloudNatObjects(data map[string]any, field string) ([]map[string]any, error) {
	value, present := data[field]
	if !present {
		return nil, nil
	}
	values, ok := value.([]any)
	if !ok {
		return nil, groupDenied("cloud_nat_array_invalid")
	}
	result := make([]map[string]any, 0, len(values))
	for _, value := range values {
		item, ok := value.(map[string]any)
		if !ok || item == nil {
			return nil, groupDenied("cloud_nat_object_invalid")
		}
		result = append(result, item)
	}
	return result, nil
}

// Validate native field shapes while preserving unknown fields/enum values.
func cloudNatScalars(data map[string]any, strings, booleans, integers, arrays []string) error {
	for _, key := range strings {
		if value, ok := data[key]; ok {
			if _, ok := value.(string); !ok {
				return groupDenied("cloud_nat_string_invalid")
			}
		}
	}
	for _, key := range booleans {
		if value, ok := data[key]; ok {
			if _, ok := value.(bool); !ok {
				return groupDenied("cloud_nat_boolean_invalid")
			}
		}
	}
	for _, key := range integers {
		if value, present := data[key]; present {
			var number int32
			raw, err := json.Marshal(value)
			if err != nil || value == nil || json.Unmarshal(raw, &number) != nil || number < 0 {
				return groupDenied("cloud_nat_integer_invalid")
			}
		}
	}
	for _, key := range arrays {
		if value, present := data[key]; present {
			values, ok := value.([]any)
			if !ok {
				return groupDenied("cloud_nat_array_invalid")
			}
			for _, value := range values {
				if _, ok := value.(string); !ok {
					return groupDenied("cloud_nat_string_invalid")
				}
			}
		}
	}
	return nil
}

// Native IP/subnetwork fields complement the separately parsed CEL Hub references.
func (c *client) cloudNatReferences(data map[string]any) map[string][]string {
	subnets, ips := []any{}, []any{}
	for _, key := range []string{"subnetworks", "nat64Subnetworks"} {
		for _, value := range array(data[key]) {
			subnets = append(subnets, object(value)["name"])
		}
	}
	for _, key := range []string{"natIps", "drainNatIps"} {
		ips = append(ips, array(data[key])...)
	}
	for _, value := range array(data["rules"]) {
		action := object(object(value)["action"])
		for _, key := range []string{"sourceNatActiveRanges", "sourceNatDrainRanges"} {
			subnets = append(subnets, array(action[key])...)
		}
		for _, key := range []string{"sourceNatActiveIps", "sourceNatDrainIps"} {
			ips = append(ips, array(action[key])...)
		}
	}
	return references(c, map[string]any{"subnetwork": subnets, "target": ips})
}
