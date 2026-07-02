package cli

import (
	"path/filepath"
	"testing"
	"time"
)

func TestServerStatusFileRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "server.json")
	startedAt := time.Date(2026, 7, 2, 16, 0, 0, 0, time.UTC)
	want := ServerStatus{
		PID:       12345,
		Addr:      "127.0.0.1:8080",
		Version:   "dev",
		StartedAt: startedAt,
	}

	if err := writeServerStatus(path, want); err != nil {
		t.Fatalf("writeServerStatus() error = %v", err)
	}
	got, err := readServerStatus(path)
	if err != nil {
		t.Fatalf("readServerStatus() error = %v", err)
	}
	if got.PID != want.PID {
		t.Fatalf("pid = %d, want %d", got.PID, want.PID)
	}
	if got.Addr != want.Addr {
		t.Fatalf("addr = %q, want %q", got.Addr, want.Addr)
	}
	if got.Version != want.Version {
		t.Fatalf("version = %q, want %q", got.Version, want.Version)
	}
	if !got.StartedAt.Equal(startedAt) {
		t.Fatalf("started_at = %s, want %s", got.StartedAt, startedAt)
	}
}

func TestReadMissingServerStatusReturnsNotRunning(t *testing.T) {
	_, err := readServerStatus(filepath.Join(t.TempDir(), "missing.json"))
	if err == nil {
		t.Fatal("readServerStatus() error = nil, want missing status error")
	}
}

func TestServerURLAddsSchemeForClickableTerminalOutput(t *testing.T) {
	tests := []struct {
		name string
		addr string
		want string
	}{
		{name: "host and port", addr: "127.0.0.1:8585", want: "http://127.0.0.1:8585"},
		{name: "bare port", addr: ":8585", want: "http://127.0.0.1:8585"},
		{name: "existing scheme", addr: "http://127.0.0.1:8585", want: "http://127.0.0.1:8585"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := serverURL(tt.addr); got != tt.want {
				t.Fatalf("serverURL(%q) = %q, want %q", tt.addr, got, tt.want)
			}
		})
	}
}
