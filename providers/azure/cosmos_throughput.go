package azure

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Throughput is an owner configuration, not a separately deletable resource.
// A native 404 means no dedicated offer only after the owner is re-read intact.
func (c *client) cosmosThroughput(ctx context.Context, kind, wire string, owner map[string]any) (map[string]any, error) {
	mapping, _ := findType(kind)
	metadata, err := providerData()
	if err != nil {
		return nil, err
	}
	for _, id := range mapping.ReadOperations {
		op, _ := metadata.catalog.Operation(id)
		if op.Call == nil || !strings.HasSuffix(op.Call.Path, "/throughputSettings/default") {
			continue
		}
		_, params, err := c.resourceOperation(mapping, wire, "GET")
		if err != nil {
			return nil, err
		}
		bound, err := bindAzureREST(op, params)
		if err != nil {
			return nil, err
		}
		res, err := c.request(ctx, "GET", bound.URL)
		result := map[string]any{"present": false}
		if err != nil && !isNotFound(err) {
			return nil, err
		}
		if err == nil {
			const suffix = "/throughputSettings/default"
			resourceID := text(res.data["id"])
			if res.status != 200 || len(resourceID) <= len(suffix) || !strings.EqualFold(resourceID[len(resourceID)-len(suffix):], suffix) || !cosmosSameWireID(responseID(kind, resourceID[:len(resourceID)-len(suffix)]), wire) {
				return nil, serviceDenied("cosmos_throughput_identity_mismatch")
			}
			typ := text(res.data["type"])
			if typ != "" && (len(typ) <= len("/throughputSettings") || !strings.EqualFold(typ[len(typ)-len("/throughputSettings"):], "/throughputSettings") || !validResponseType(kind, typ[:len(typ)-len("/throughputSettings")])) {
				return nil, serviceDenied("cosmos_throughput_type_mismatch")
			}
			props := object(res.data["properties"])
			resource := object(props["resource"])
			if resource == nil || (resource["throughput"] == nil && object(resource["autoscaleSettings"])["maxThroughput"] == nil) {
				return nil, serviceDenied("cosmos_missing_throughput_configuration")
			}
			// Storage use can change these informational limits independently of
			// the configured RU/s and autoscale policy.
			configuration := map[string]any{}
			for key, value := range resource {
				switch key {
				case "_etag", "_ts", "_self", "minimumThroughput", "instantMaximumThroughput", "softAllowedMaximumThroughput":
					continue
				}
				configuration[key] = value
			}
			result = map[string]any{"present": true, "resource": configuration}
		}
		options := object(object(owner["properties"])["options"])
		if result["present"] == false && (options["throughput"] != nil || options["autoscaleSettings"] != nil) {
			return nil, serviceDenied("cosmos_throughput_indexes_disagree")
		}
		current, err := c.cosmosResource(ctx, wire)
		if err != nil {
			return nil, err
		}
		if c.privateConfiguration(cosmosSnapshot(kind, owner)) != c.privateConfiguration(cosmosSnapshot(kind, current)) {
			return nil, serviceDenied("cosmos_throughput_owner_changed")
		}
		return result, nil
	}
	return map[string]any{"applicable": false}, nil
}
func cosmosThroughputProtection(settings map[string]any) string {
	value := object(settings["resource"])["offerReplacePending"]
	if value != nil && value != "false" && value != false {
		return "azure_cosmos_throughput_change_pending"
	}
	resource := object(settings["resource"])
	if value := resource["autoscaleSettings"]; value != nil && object(value)["maxThroughput"] == nil {
		return "azure_cosmos_invalid_throughput"
	}
	for _, value := range []any{resource["throughput"], object(resource["autoscaleSettings"])["maxThroughput"], object(resource["autoscaleSettings"])["targetMaxThroughput"]} {
		if value == nil {
			continue
		}
		var number float64
		switch v := value.(type) {
		case json.Number:
			var err error
			number, err = strconv.ParseFloat(string(v), 64)
			if err != nil {
				return "azure_cosmos_invalid_throughput"
			}
		case float64:
			number = v
		case int:
			number = float64(v)
		case int64:
			number = float64(v)
		default:
			return "azure_cosmos_invalid_throughput"
		}
		if math.IsNaN(number) || math.IsInf(number, 0) || number < 0 || number > math.MaxInt32 || math.Trunc(number) != number {
			return "azure_cosmos_invalid_throughput"
		}
	}
	return ""
}

func (c *client) verifyCosmosProductParent(ctx context.Context, target productTarget, raw map[string]any) error {
	if !isCosmosType(target.ParentType) {
		return nil
	}
	// Native cascade walks have the live parent, but no inventory cursor. They
	// bind ancestors through the planned asset before reaching this helper.
	if target.CosmosAncestors == nil && target.CosmosThroughput == "" {
		return nil
	}
	if target.CosmosAncestors == nil || target.CosmosThroughput == "" {
		return fmt.Errorf("Cosmos DB parent is missing its configuration binding")
	}
	if err := c.cosmosAncestors(ctx, target.ParentWireID, target.CosmosAncestors, false); err != nil {
		return err
	}
	settings, err := c.cosmosThroughput(ctx, target.ParentType, target.ParentWireID, raw)
	if err != nil {
		return err
	}
	if target.CosmosThroughput != c.privateConfiguration(settings) {
		return serviceDenied("cosmos_parent_throughput_changed")
	}
	return nil
}
