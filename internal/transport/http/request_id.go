package httptransport

import (
	"crypto/rand"
	"fmt"
	"net/http"
	"strings"

	"github.com/loomx-ai/steward/internal/core/requestmeta"
)

const requestIDHeader = "X-Request-ID"

func newRequestID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("generate request ID: %w", err)
	}
	value[6] = value[6]&0x0f | 0x40
	value[8] = value[8]&0x3f | 0x80
	return fmt.Sprintf(
		"%x-%x-%x-%x-%x",
		value[0:4],
		value[4:6],
		value[6:8],
		value[8:10],
		value[10:16],
	), nil
}

func withRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api" && !strings.HasPrefix(request.URL.Path, "/api/") {
			next.ServeHTTP(response, request)
			return
		}
		requestID, err := newRequestID()
		if err != nil {
			panic(err)
		}
		response.Header().Set(requestIDHeader, requestID)
		next.ServeHTTP(
			response,
			request.WithContext(requestmeta.WithRequestID(request.Context(), requestID)),
		)
	})
}
