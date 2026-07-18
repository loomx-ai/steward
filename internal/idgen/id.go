// Package idgen generates opaque entity identifiers owned by Steward.
package idgen

import (
	"crypto/rand"
	"fmt"
	"io"
)

const (
	alphabet     = "23456789abcdefghjkmnpqrstuvwxyz"
	randomLength = 16
)

var defaultGenerator = generator{reader: rand.Reader}

type generator struct {
	reader io.Reader
}

// New returns an opaque identifier in the form <three-character-prefix>-<16
// random characters>. The reduced alphabet avoids visually ambiguous values.
func New(prefix string) (string, error) {
	return defaultGenerator.new(prefix)
}

// MustNew is intended for entity construction paths where entropy failure is
// unrecoverable. Request-validation paths should prefer New and return errors.
func MustNew(prefix string) string {
	id, err := New(prefix)
	if err != nil {
		panic(err)
	}
	return id
}

func (g generator) new(prefix string) (string, error) {
	if !validPrefix(prefix) {
		return "", fmt.Errorf("ID prefix %q must contain exactly three lowercase ASCII letters or digits", prefix)
	}
	if g.reader == nil {
		return "", fmt.Errorf("ID entropy reader is required")
	}

	result := make([]byte, 0, len(prefix)+1+randomLength)
	result = append(result, prefix...)
	result = append(result, '-')
	byteBuffer := []byte{0}
	// Ignore values at or above the largest multiple of the alphabet length so
	// modulo reduction cannot bias the output distribution.
	limit := byte(256 - (256 % len(alphabet)))
	for len(result) < len(prefix)+1+randomLength {
		if _, err := io.ReadFull(g.reader, byteBuffer); err != nil {
			return "", fmt.Errorf("read ID entropy: %w", err)
		}
		if byteBuffer[0] >= limit {
			continue
		}
		result = append(result, alphabet[int(byteBuffer[0])%len(alphabet)])
	}
	return string(result), nil
}

func validPrefix(prefix string) bool {
	if len(prefix) != 3 {
		return false
	}
	for index := range len(prefix) {
		value := prefix[index]
		if (value < 'a' || value > 'z') && (value < '0' || value > '9') {
			return false
		}
	}
	return true
}
