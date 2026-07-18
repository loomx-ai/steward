package connection

import (
	"testing"
	"time"
)

func TestNextConnectionUpdatedAtAdvancesAtDatabasePrecision(t *testing.T) {
	current := time.Date(2026, 7, 27, 8, 0, 0, 123456000, time.UTC)
	next := nextConnectionUpdatedAt(current.Add(time.Nanosecond), current)
	want := current.Add(time.Microsecond)
	if !next.Equal(want) {
		t.Fatalf("nextConnectionUpdatedAt() = %s, want %s", next, want)
	}
}
