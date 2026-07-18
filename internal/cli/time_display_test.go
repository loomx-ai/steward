package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestServerStatusDisplaysStartedAtToSeconds(t *testing.T) {
	path := filepath.Join(t.TempDir(), "server.json")
	status := ServerStatus{
		PID:       os.Getpid(),
		Addr:      "127.0.0.1:8585",
		Version:   "dev",
		StartedAt: time.Date(2026, 7, 2, 16, 8, 37, 0, time.UTC),
	}
	if err := writeServerStatus(path, status); err != nil {
		t.Fatalf("writeServerStatus() error = %v", err)
	}

	cmd := newServerStatusCommand()
	if err := cmd.Flags().Set("status-file", path); err != nil {
		t.Fatalf("set status-file flag: %v", err)
	}
	var output bytes.Buffer
	cmd.SetOut(&output)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("status command error = %v", err)
	}
	if got := output.String(); !strings.Contains(got, "started_at=2026-07-02T16:08:37Z") {
		t.Fatalf("status output missing seconds: %q", got)
	}
}
