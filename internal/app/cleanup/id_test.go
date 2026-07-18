package cleanup

import (
	"strings"
	"testing"
)

func TestNewServiceDefaultCleanupTaskIDUsesCleanupTaskPrefix(t *testing.T) {
	service := NewService(nil, nil)

	id := service.taskIDGenerator()
	if !strings.HasPrefix(id, "cln-") {
		t.Fatalf("expected cleanup task ID prefix cln-, got %q", id)
	}
}
