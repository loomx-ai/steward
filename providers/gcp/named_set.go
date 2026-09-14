package gcp

const namedSetType = "compute.googleapis.com/NamedSet"
const namedSetDelete = "compute.routers.deleteNamedSet"
const namedSetGet = "compute.routers.getNamedSet"
const namedSetList = "compute.routers.listNamedSets"
const namedSetRouterID = "_named_set_router_id"

func routerComponentData(kind string, data map[string]any, name string) error {
	if kind == cloudNatType {
		return cloudNatData(data, name)
	}
	if kind == routePolicyType {
		return routePolicyData(data, name)
	}
	if kind != namedSetType || data == nil || data["name"] != name || !routePolicySegment.MatchString(name) {
		return groupDenied("named_set_identity_invalid")
	}
	for _, key := range []string{"type", "description", "fingerprint"} {
		if value, exists := data[key]; exists {
			if _, ok := value.(string); !ok {
				return groupDenied("named_set_field_invalid")
			}
		}
	}
	if value, exists := data["elements"]; exists {
		elements, ok := value.([]any)
		if !ok {
			return groupDenied("named_set_elements_invalid")
		}
		for _, value := range elements {
			expression, ok := value.(map[string]any)
			if !ok {
				return groupDenied("named_set_expression_invalid")
			}
			for _, key := range []string{"expression", "title", "description", "location"} {
				if value, exists := expression[key]; exists {
					if _, ok := value.(string); !ok {
						return groupDenied("named_set_expression_invalid")
					}
				}
			}
		}
	}
	return nil
}
