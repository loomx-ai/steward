package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
)

func TestRunRejectsRemovedProviders(t *testing.T) {
	temporaryDirectory := t.TempDir()
	sourcePath := filepath.Join(temporaryDirectory, "source.json")
	if err := os.WriteFile(sourcePath, []byte(`{"paths":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, provider := range []asset.Provider{"azure", "gcp"} {
		t.Run(string(provider), func(t *testing.T) {
			err := run(provider, "openapi", sourcePath, filepath.Join(temporaryDirectory, string(provider)+".json"))
			if err == nil || !strings.Contains(err.Error(), "unsupported provider") {
				t.Fatalf("run(%q) error = %v, want unsupported provider", provider, err)
			}
		})
	}
}
