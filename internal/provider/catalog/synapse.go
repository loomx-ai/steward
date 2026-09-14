package catalog

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Binding a workspace URL does not authorize a request. The provider must also
// establish workspace ownership and use the Synapse OAuth audience.
var azureSynapseOrigin = regexp.MustCompile(`^https://[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.dev\.azuresynapse\.net$`)

func bindSynapseParameters(call *OperationCall, properties, values map[string]any) error {
	version := "api-version"
	if strings.Contains(call.Path, "/versions/{livyApiVersion}/") {
		version = "livyApiVersion"
	}
	if v, ok := values[version]; ok && v != call.Version {
		return fmt.Errorf("Synapse API version differs from catalog")
	}
	values[version] = call.Version
	for name, value := range values {
		schema, _ := properties[name].(map[string]any)
		switch schema["type"] {
		case "integer":
			var decimal string
			switch v := value.(type) {
			case int, int32, int64, json.Number:
				decimal = fmt.Sprint(v)
			case float64:
				decimal = strconv.FormatFloat(v, 'f', -1, 64)
			default:
				return fmt.Errorf("invalid Synapse integer %q", name)
			}
			number, err := strconv.ParseInt(decimal, 10, 32)
			if err != nil || number < 0 || name == "size" && (number == 0 || number > 20) {
				return fmt.Errorf("invalid Synapse integer %q", name)
			}
			if schema["in"] == "path" {
				values[name] = strconv.FormatInt(number, 10)
			} else {
				values[name] = number
			}
		case "boolean":
			if _, ok := value.(bool); !ok {
				return fmt.Errorf("invalid Synapse boolean %q", name)
			}
		case "string":
			v, ok := value.(string)
			if !ok {
				return fmt.Errorf("invalid Synapse string %q", name)
			}
			if schema["in"] == "path" && name != "endpoint" && (v == "" || strings.TrimSpace(v) != v || strings.ContainsAny(v, "/\\%\x00\r\n") || v == "." || v == "..") {
				return fmt.Errorf("invalid Synapse path parameter %q", name)
			}
		}
	}
	return nil
}
