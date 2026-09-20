package oauth

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Well-known value keys. Every driver stores its OAuth material under these
// names so that redaction, expiry and refresh read the same way for each cloud.
const (
	AccessTokenKey       = "oauth_access_token"
	RefreshTokenKey      = "oauth_refresh_token"
	AccessTokenExpireKey = "oauth_access_token_expire"
)

// RefreshBeforeExpiry is the validity a renewal must leave on the wire: enough
// for client construction, a retry and one cloud API request.
const RefreshBeforeExpiry = 5 * time.Minute

// CloneValues copies a credential's values so a caller can mutate its own copy.
func CloneValues(values map[string]string) map[string]string {
	cloned := make(map[string]string, len(values))
	for key, value := range values {
		cloned[key] = value
	}
	return cloned
}

// ParseUnix reads an optional Unix timestamp. A missing key is the zero time,
// which every expiry test treats as already expired.
func ParseUnix(values map[string]string, key string) (time.Time, error) {
	raw := strings.TrimSpace(values[key])
	if raw == "" {
		return time.Time{}, nil
	}
	seconds, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return time.Time{}, fmt.Errorf("%s is not a Unix timestamp", key)
	}
	return time.Unix(seconds, 0).UTC(), nil
}

// FormatUnix writes a timestamp in the form ParseUnix reads.
func FormatUnix(value time.Time) string {
	return strconv.FormatInt(value.Unix(), 10)
}

// ExpiresSoon reports whether material is too close to expiry to be handed to a
// cloud client.
func ExpiresSoon(expiresAt, now time.Time) bool {
	return !expiresAt.After(now.Add(RefreshBeforeExpiry))
}

// Present reports whether every named value is non-empty.
func Present(values map[string]string, keys ...string) bool {
	for _, key := range keys {
		if strings.TrimSpace(values[key]) == "" {
			return false
		}
	}
	return true
}
