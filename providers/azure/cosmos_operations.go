package azure

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

var cosmosOperationRegion = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

// Native Cosmos responses use both the global ARM host and its regional ARM
// host. The operation's processing region can differ from the data region.
// Only a resource-bound receipt may resume a returned operation URL.
func validateCosmosOperationURL(subscription, wire, version, endpoint string) error {
	u, err := url.Parse(endpoint)
	if err != nil || len(endpoint) > 32*1024 || u.Scheme != "https" || u.User != nil || u.Fragment != "" || (u.RawPath != "" && u.RawPath != u.Path) {
		return fmt.Errorf("invalid Cosmos DB operation endpoint")
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil || (len(query) != 1 && len(query) != 5) || len(query["api-version"]) != 1 || query.Get("api-version") != version {
		return fmt.Errorf("invalid Cosmos DB operation API version")
	}
	if len(query) == 5 {
		for _, key := range []string{"t", "c", "s", "h"} {
			if len(query[key]) != 1 || query.Get(key) == "" {
				return fmt.Errorf("invalid Cosmos DB signed polling parameters")
			}
		}
	}
	parts := strings.Split(u.Path, "/")
	region := ""
	if len(parts) == 9 && strings.EqualFold(parts[1], "subscriptions") && strings.EqualFold(parts[2], subscription) && strings.EqualFold(parts[3], "providers") && strings.EqualFold(parts[4], "Microsoft.DocumentDB") && strings.EqualFold(parts[5], "locations") && cosmosOperationRegion.MatchString(parts[6]) && (strings.EqualFold(parts[7], "operationsStatus") || strings.EqualFold(parts[7], "operationResults")) && uuidPattern.MatchString(parts[8]) {
		region = parts[6]
	} else {
		if len(parts) < 3 || !strings.EqualFold(parts[len(parts)-2], "operationResults") || !uuidPattern.MatchString(parts[len(parts)-1]) || !cosmosSameWireID(strings.Join(parts[:len(parts)-2], "/"), wire) {
			return fmt.Errorf("Cosmos DB operation belongs to another resource")
		}
		id, _, err := parseID(wire)
		if err != nil || !strings.HasPrefix(id, "/subscriptions/"+strings.ToLower(subscription)+"/") {
			return fmt.Errorf("Cosmos DB operation belongs to another subscription")
		}
	}
	if u.Host != "management.azure.com" && (region == "" || u.Host != region+".management.azure.com") {
		return fmt.Errorf("untrusted Cosmos DB operation host")
	}
	return nil
}
func (a *action) cosmosOperationBinding(endpoint string) string {
	return a.client.privateConfiguration(map[string]any{"wire_id": cosmosWireSignature(a.wireID), "operation": endpoint, "api_version": a.kind.Version})
}
