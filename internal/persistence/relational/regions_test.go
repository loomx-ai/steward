package relational

import "testing"

func TestRegionCursorRoundTrip(t *testing.T) {
	cursor := encodeRegionCursor("cn-hangzhou", "region-1")
	regionID, id, err := decodeRegionCursor(cursor)
	if err != nil || regionID != "cn-hangzhou" || id != "region-1" {
		t.Fatalf("decodeRegionCursor() = %q, %q, %v", regionID, id, err)
	}
	if _, _, err := decodeRegionCursor("invalid"); err == nil {
		t.Fatal("invalid region cursor was accepted")
	}
}

func TestEscapeRegionSearchPattern(t *testing.T) {
	if got := escapeLike(`cn_%\\test`); got != `cn\_\%\\\\test` {
		t.Fatalf("escapeLike() = %q", got)
	}
}
