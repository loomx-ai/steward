package azure

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
)

// principalObserver records the object ID of the principal behind the ARM
// token this connection sends. Role assignments to that principal are Steward's
// own access, so cleanup must never delete them.
type principalObserver struct {
	base     http.RoundTripper
	mu       sync.Mutex
	objectID string
	observed bool
}

func (o *principalObserver) RoundTrip(request *http.Request) (*http.Response, error) {
	if token, ok := strings.CutPrefix(request.Header.Get("Authorization"), "Bearer "); ok {
		if parts := strings.Split(token, "."); len(parts) == 3 {
			objectID := ""
			if payload, err := base64.RawURLEncoding.DecodeString(parts[1]); err == nil {
				var claims struct {
					OID string `json:"oid"`
				}
				if json.Unmarshal(payload, &claims) == nil && uuidPattern.MatchString(claims.OID) {
					objectID = strings.ToLower(claims.OID)
				}
			}
			o.mu.Lock()
			o.objectID, o.observed = objectID, true
			o.mu.Unlock()
		}
	}
	return o.base.RoundTrip(request)
}

// connectionPrincipal reports the observed object ID. known is false when no
// JWT access token has been observed; an observed token without a valid oid
// returns known with an empty ID, which callers treat as unverifiable.
func (c *client) connectionPrincipal() (objectID string, known bool) {
	if c.principal == nil {
		return "", false
	}
	c.principal.mu.Lock()
	defer c.principal.mu.Unlock()
	return c.principal.objectID, c.principal.observed
}
