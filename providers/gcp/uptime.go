package gcp

import (
	"context"
	"encoding/base64"
	"strings"
	"time"

	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const uptimeType = "monitoring.googleapis.com/UptimeCheckConfig"
const uptimeReview = "_uptime_check_configuration"

func (c *client) uptimeData(id string, data map[string]any) error {
	if err := checkListCompleteness(data); err != nil {
		return err
	}
	name := text(data["name"])
	if name != data["name"] || !strings.HasPrefix(name, "projects/") || c.canonicalName("//monitoring.googleapis.com/"+name) != id {
		return groupDenied("uptime_identity_changed")
	}
	kind, _ := findType(uptimeType)
	if _, err := c.resourceURL(kind, id); err != nil {
		return err
	}
	if err := cloudNatScalars(data, []string{"name", "displayName", "period", "timeout", "checkerType"}, []string{"disabled", "logCheckFailures", "isInternal"}, nil, []string{"selectedRegions"}); err != nil {
		return err
	}
	timeout, err := time.ParseDuration(text(data["timeout"]))
	if text(data["displayName"]) == "" || err != nil || timeout < time.Second || timeout > time.Minute {
		return groupDenied("uptime_required_configuration_invalid")
	}
	targets := 0
	for _, field := range []string{"monitoredResource", "resourceGroup", "syntheticMonitor"} {
		if raw, ok := data[field]; ok {
			if object(raw) == nil {
				return groupDenied("uptime_target_invalid")
			}
			targets++
		}
	}
	if targets != 1 {
		return groupDenied("uptime_target_invalid")
	}
	if raw, ok := data["monitoredResource"]; ok {
		target := object(raw)
		if err := cloudNatScalars(target, []string{"type"}, nil, nil, nil); err != nil {
			return err
		}
		if text(target["type"]) == "" {
			return groupDenied("uptime_target_invalid")
		}
		if err := uptimeStringMap(target, "labels"); err != nil {
			return err
		}
	}
	if raw, ok := data["resourceGroup"]; ok {
		target := object(raw)
		if err := cloudNatScalars(target, []string{"groupId", "resourceType"}, nil, nil, nil); err != nil {
			return err
		}
		if text(target["groupId"]) == "" || text(target["resourceType"]) == "" {
			return groupDenied("uptime_target_invalid")
		}
	}
	if raw, ok := data["syntheticMonitor"]; ok {
		target := object(object(raw)["cloudFunctionV2"])
		if target == nil || text(target["name"]) == "" {
			return groupDenied("uptime_target_invalid")
		}
		if err := cloudNatScalars(target, []string{"name"}, nil, nil, nil); err != nil {
			return err
		}
		if value, present := target["cloudRunRevision"]; present && object(value) == nil {
			return groupDenied("uptime_target_invalid")
		}
	}
	if err := uptimeStringMap(data, "userLabels"); err != nil {
		return err
	}
	checks := 0
	for _, field := range []string{"httpCheck", "tcpCheck"} {
		raw, present := data[field]
		if !present {
			continue
		}
		checks++
		check := object(raw)
		if check == nil {
			return groupDenied("uptime_check_invalid")
		}
		if err := cloudNatScalars(check, []string{"requestMethod", "path", "contentType", "customContentType", "body"}, []string{"useSsl", "maskHeaders", "validateSsl"}, []string{"port"}, nil); err != nil {
			return err
		}
		if body, present := check["body"]; present {
			if _, err := base64.StdEncoding.DecodeString(text(body)); err != nil {
				return groupDenied("uptime_body_invalid")
			}
		}
		if err := uptimeStringMap(check, "headers"); err != nil {
			return err
		}
		if auth, present := check["authInfo"]; present {
			if object(auth) == nil {
				return groupDenied("uptime_auth_invalid")
			}
			if err := cloudNatScalars(object(auth), []string{"username", "password"}, nil, nil, nil); err != nil {
				return err
			}
		}
		for _, field := range []string{"pingConfig", "serviceAgentAuthentication"} {
			if raw, present := check[field]; present && object(raw) == nil {
				return groupDenied("uptime_check_invalid")
			}
		}
		if _, err := cloudNatObjects(check, "acceptedResponseStatusCodes"); err != nil {
			return err
		}
	}
	if checks > 1 || checks == 0 && data["syntheticMonitor"] == nil {
		return groupDenied("uptime_check_invalid")
	}
	for _, field := range []string{"contentMatchers", "internalCheckers"} {
		values, err := cloudNatObjects(data, field)
		if err != nil {
			return err
		}
		for _, value := range values {
			if err := cloudNatScalars(value, []string{"content", "matcher", "name", "displayName", "network", "gcpZone", "peerProjectId", "state"}, nil, nil, nil); err != nil {
				return err
			}
			if raw, present := value["jsonPathMatcher"]; present && object(raw) == nil {
				return groupDenied("uptime_matcher_invalid")
			}
		}
	}
	return nil
}

func uptimeStringMap(data map[string]any, field string) error {
	raw, present := data[field]
	if !present {
		return nil
	}
	values := object(raw)
	if values == nil {
		return groupDenied("uptime_map_invalid")
	}
	for _, value := range values {
		if _, ok := value.(string); !ok {
			return groupDenied("uptime_map_invalid")
		}
	}
	return nil
}

// Hash the observable native configuration before redaction. Monitoring masks
// encrypted headers itself and exposes no etag, so invisible secret changes and
// races after the final GET cannot be detected. Unknown native fields stay bound.
func uptimeConfiguration(id string, data map[string]any) string {
	value := cloneParameters(data)
	value["name"] = id
	if synthetic := object(value["syntheticMonitor"]); synthetic != nil {
		if function := object(synthetic["cloudFunctionV2"]); function != nil {
			synthetic = cloneParameters(synthetic)
			function = cloneParameters(function)
			delete(function, "cloudRunRevision")
			synthetic["cloudFunctionV2"] = function
			value["syntheticMonitor"] = synthetic
		}
	}
	return firewallDigest(value)
}

func (c *client) uptimeRead(ctx context.Context, id string) (map[string]any, error) {
	kind, _ := findType(uptimeType)
	endpoint, err := c.resourceURL(kind, id)
	if err != nil {
		return nil, err
	}
	data, err := c.request(ctx, "GET", endpoint, nil)
	if err != nil {
		return nil, err
	}
	if err := c.uptimeData(id, data); err != nil {
		return nil, err
	}
	return data, nil
}

func (c *client) uptimeInventory(ctx context.Context, id string, listed map[string]any) (map[string]any, error) {
	if err := c.uptimeData(id, listed); err != nil {
		return nil, err
	}
	live, err := c.uptimeRead(ctx, id)
	if err != nil {
		return nil, contracts.DependencyReadError(err)
	}
	if uptimeConfiguration(id, listed) != uptimeConfiguration(id, live) {
		return nil, groupDenied("uptime_configuration_changed")
	}
	return live, nil
}
