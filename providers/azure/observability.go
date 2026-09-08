package azure

import "github.com/loomx-ai/steward/internal/provider/contracts"

func safePayload(value map[string]any) map[string]any {
	cleaned, err := contracts.CloudRawPayload(object(safeResource(value)))
	if err != nil {
		return map[string]any{}
	}
	return cleaned
}
