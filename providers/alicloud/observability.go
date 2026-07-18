package alicloud

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/alibabacloud-go/tea/dara"
	"github.com/alibabacloud-go/tea/tea"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func logCloudAPIRetry(
	ctx context.Context,
	service, operation string,
	request map[string]any,
) queryRetryObserver {
	return func(failedAttempt, nextAttempt int, err error) {
		LogCloudAPIError(
			ctx,
			service,
			fmt.Sprintf("%s (attempt %d)", operation, failedAttempt),
			NormalizeError(err),
		)
		execution.LogCloudAPIRequest(
			ctx,
			service,
			fmt.Sprintf("%s (attempt %d)", operation, nextAttempt),
			rawCloudPayload(request),
		)
	}
}

// LogCloudAPIError preserves the provider response boundary in job logs.
// Structured SDK errors contain the response body in Data; transport errors do
// not have a response and are logged as failures instead of fabricated
// response payloads.
func LogCloudAPIError(ctx context.Context, service, operation string, err error) {
	if payload, ok := rawSDKErrorResponse(err); ok {
		execution.LogCloudAPIResponse(ctx, service, operation, payload)
		return
	}
	execution.LogCloudAPIFailure(ctx, service, operation, err)
}

func rawSDKErrorResponse(err error) (map[string]any, bool) {
	var data string
	var teaError *tea.SDKError
	if errors.As(err, &teaError) {
		data = tea.StringValue(teaError.Data)
	} else {
		var daraError *dara.SDKError
		if !errors.As(err, &daraError) {
			return nil, false
		}
		data = dara.StringValue(daraError.Data)
	}
	payload, ok := decodeErrorData(data).(map[string]any)
	if !ok {
		return nil, false
	}
	// Darabonba adds this transport field to the decoded provider body before
	// placing it in SDKError.Data. It was not part of the original response.
	delete(payload, "statusCode")
	if len(payload) == 0 {
		return nil, false
	}
	sanitizeProviderPayload(payload)
	return rawCloudPayload(payload), true
}

func sanitizeProviderPayload(value any) {
	switch typed := value.(type) {
	case map[string]any:
		for key, item := range typed {
			switch strings.ToLower(strings.TrimSpace(key)) {
			case "authorization", "x-acs-accesskey-id", "x-acs-security-token",
				"x-fc-security-token", "securitytoken", "accesskeysecret":
				typed[key] = "[REDACTED]"
			case "message":
				if message, ok := item.(string); ok {
					typed[key] = sanitizeProviderMessage(message)
				}
			default:
				sanitizeProviderPayload(item)
			}
		}
	case []any:
		for _, item := range typed {
			sanitizeProviderPayload(item)
		}
	}
}

func rawCloudPayload(value any) map[string]any {
	payload, err := contracts.CloudRawPayload(value)
	if err != nil {
		payload = map[string]any{"StewardLogError": err.Error()}
	}
	return payload
}
