package idgen

import (
	"errors"
	"regexp"
	"testing"
)

func TestNewUsesPrefixedSixteenCharacterAlphabet(t *testing.T) {
	pattern := regexp.MustCompile(`^scn-[23456789abcdefghjkmnpqrstuvwxyz]{16}$`)
	seen := make(map[string]struct{}, 10_000)
	for range 10_000 {
		id, err := New("scn")
		if err != nil {
			t.Fatalf("New() error = %v", err)
		}
		if !pattern.MatchString(id) {
			t.Fatalf("New() = %q, does not match prefixed ID format", id)
		}
		if _, duplicate := seen[id]; duplicate {
			t.Fatalf("New() returned duplicate ID %q", id)
		}
		seen[id] = struct{}{}
	}
}

func TestNewRejectsInvalidPrefix(t *testing.T) {
	for _, prefix := range []string{"", "ab", "abcd", "A1b", "a-b", "中文"} {
		if _, err := New(prefix); err == nil {
			t.Fatalf("New(%q) expected error", prefix)
		}
	}
}

func TestGeneratorPropagatesEntropyFailure(t *testing.T) {
	want := errors.New("entropy unavailable")
	generator := generator{reader: errorReader{err: want}}
	if _, err := generator.new("scn"); !errors.Is(err, want) {
		t.Fatalf("new() error = %v, want %v", err, want)
	}
}

type errorReader struct{ err error }

func (r errorReader) Read([]byte) (int, error) { return 0, r.err }
