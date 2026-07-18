package requestmeta

import (
	"context"
	"strings"
)

type requestIDContextKey struct{}

// WithRequestID associates the server-generated application request ID with
// all work performed for the request.
func WithRequestID(ctx context.Context, requestID string) context.Context {
	return context.WithValue(ctx, requestIDContextKey{}, strings.TrimSpace(requestID))
}

// RequestID returns the application request ID associated with ctx.
func RequestID(ctx context.Context) string {
	requestID, _ := ctx.Value(requestIDContextKey{}).(string)
	return requestID
}
