package asset

import "maps"

func CloneResourceProperties(values []ResourceProperty) []ResourceProperty {
	if values == nil {
		return nil
	}
	cloned := make([]ResourceProperty, len(values))
	for index, value := range values {
		cloned[index] = value
		cloned[index].DisplayNames = maps.Clone(value.DisplayNames)
		cloned[index].Enum = append([]any(nil), value.Enum...)
		cloned[index].Operators = append([]string(nil), value.Operators...)
	}
	return cloned
}
