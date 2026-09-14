package gcp

import (
	"embed"
	"encoding/json"
	"strings"
	"sync"
)

// A group selector is a reference even if another predicate matches no series.
// This deliberately does not evaluate metric/resource truth or current members.
func monitoringGroupFilterReference(value, group string) monitoringReference {
	f, ok := parseMonitoringFilter(value)
	if !ok || f == nil {
		return monitoringUnresolvedReference
	}
	return f.groupReference(group)
}
func (f *monitoringFilter) groupReference(group string) monitoringReference {
	if f.op == "AND" || f.op == "OR" {
		return monitoringOr(f.left.groupReference(group), f.right.groupReference(group))
	}
	if f.field != "group.id" {
		return monitoringNoReference
	}
	if f.op != "=" || f.function != "" {
		return monitoringUnresolvedReference
	}
	value := f.value
	if strings.HasPrefix(value, `"`) {
		var err error
		value, err = monitoringString(value)
		if err != nil {
			return monitoringUnresolvedReference
		}
	} else if !firewallNumericID(value) {
		return monitoringUnresolvedReference
	}
	if !uptimeSegment(value) || strings.ContainsAny(value, "${}") {
		return monitoringUnresolvedReference
	}
	return monitoringBool(value == group)
}

func alertPolicyGroupReference(data map[string]any, group string) monitoringReference {
	result := monitoringNoReference
	for _, raw := range array(data["conditions"]) {
		for key, raw := range object(raw) {
			switch key {
			case "name", "displayName", "conditionMatchedLog":
				// Logging filters select log entries, not Monitoring group objects.
			case "conditionThreshold", "conditionAbsent":
				body := object(raw)
				result = monitoringOr(result, monitoringGroupFilterReference(text(body["filter"]), group))
				if v, ok := body["denominatorFilter"]; ok {
					result = monitoringOr(result, monitoringGroupFilterReference(text(v), group))
				}
			default:
				result = monitoringOr(result, monitoringUnresolvedReference)
			}
		}
	}
	return result
}

// Retain the native schema graph, rather than assuming that every string or
// every field named "filter" is a Monitoring query (text and log widgets aren't).
//
//go:embed catalog/native/monitoring-dashboard.json
var monitoringDashboardFiles embed.FS
var monitoringDashboardSchemas = sync.OnceValues(func() (map[string]any, error) {
	b, err := monitoringDashboardFiles.ReadFile("catalog/native/monitoring-dashboard.json")
	if err != nil {
		return nil, err
	}
	var data map[string]any
	if err := json.Unmarshal(b, &data); err != nil {
		return nil, err
	}
	return object(data["schemas"]), nil
})

func monitoringDashboardGroupReference(data map[string]any, group string) monitoringReference {
	schemas, err := monitoringDashboardSchemas()
	if err != nil {
		return monitoringUnresolvedReference
	}
	nodes := 0
	var walk func(any, map[string]any, int) monitoringReference
	walk = func(value any, schema map[string]any, depth int) monitoringReference {
		nodes++
		if nodes > 100000 || depth > 64 || schema == nil {
			return monitoringUnresolvedReference
		}
		if ref := text(schema["$ref"]); ref != "" {
			schema = object(schemas[ref])
		}
		result := monitoringNoReference
		switch text(schema["type"]) {
		case "object":
			data := object(value)
			if data == nil {
				return monitoringUnresolvedReference
			}
			switch text(schema["id"]) {
			case "Dashboard":
				count := 0
				for _, key := range []string{"gridLayout", "mosaicLayout", "rowLayout", "columnLayout"} {
					if _, ok := data[key]; ok {
						count++
					}
				}
				if count != 1 {
					result = monitoringUnresolvedReference
				}
			case "Widget":
				count := 0
				for key := range data {
					if key != "id" && key != "title" && key != "visibilityCondition" {
						count++
					}
				}
				if count != 1 {
					result = monitoringUnresolvedReference
				}
			case "TimeSeriesFilter", "RatioPart":
				result = monitoringGroupFilterReference(text(data["filter"]), group)
			case "TimeSeriesQuery":
				count := 0
				for _, key := range []string{"timeSeriesFilter", "timeSeriesFilterRatio", "prometheusQuery", "timeSeriesQueryLanguage", "opsAnalyticsQuery", "traceQuery"} {
					if _, ok := data[key]; ok {
						count++
						if key != "timeSeriesFilter" && key != "timeSeriesFilterRatio" {
							result = monitoringUnresolvedReference
						}
					}
				}
				if count != 1 {
					result = monitoringUnresolvedReference
				}
			case "DashboardFilter":
				if data["filterType"] == "GROUP" {
					// A filter can offer dynamically chosen group values. Its default is not
					// evidence that the other groups are unused.
					result = monitoringUnresolvedReference
					if data["stringValue"] == group {
						result = monitoringHasReference
					}
				}
			}
			properties := object(schema["properties"])
			for key, v := range data {
				child := object(properties[key])
				if child == nil {
					child = object(schema["additionalProperties"])
				}
				result = monitoringOr(result, walk(v, child, depth+1))
			}
		case "array":
			values, ok := value.([]any)
			if !ok {
				return monitoringUnresolvedReference
			}
			for _, v := range values {
				result = monitoringOr(result, walk(v, object(schema["items"]), depth+1))
			}
		case "string":
			s, ok := value.(string)
			if !ok {
				return monitoringUnresolvedReference
			}
			if choices, ok := schema["enum"].([]any); ok {
				found := false
				for _, v := range choices {
					found = found || v == s
				}
				if !found {
					return monitoringUnresolvedReference
				}
			}
		case "boolean":
			if _, ok := value.(bool); !ok {
				return monitoringUnresolvedReference
			}
		case "integer", "number":
			switch value.(type) {
			case float64, int, int64, json.Number:
			default:
				return monitoringUnresolvedReference
			}
		default:
			return monitoringUnresolvedReference
		}
		return result
	}
	return walk(data, object(schemas["Dashboard"]), 0)
}
