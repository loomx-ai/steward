// Package datadir resolves where Steward keeps its local database, credential
// key, and server status file.
package datadir

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Name is the directory name used under the user's home directory.
const Name = ".steward"

// Resolution describes the chosen data directory.
type Resolution struct {
	Path string
	// WorkingDirectory is true when an existing ./.steward database from an
	// earlier release was found and the home directory has none yet.
	WorkingDirectory bool
}

// Resolve returns STEWARD_HOME when set, otherwise ~/.steward. A database
// created by earlier releases in ./.steward keeps being used until it is
// moved, so upgrading never presents an empty inventory.
func Resolve() (Resolution, error) {
	if value := strings.TrimSpace(os.Getenv("STEWARD_HOME")); value != "" {
		path, err := filepath.Abs(value)
		if err != nil {
			return Resolution{}, err
		}
		return Resolution{Path: path}, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return Resolution{}, fmt.Errorf("resolve home directory: %w; set STEWARD_HOME", err)
	}
	path := filepath.Join(home, Name)
	if working, err := filepath.Abs(Name); err == nil && working != path && hasDatabase(working) && !hasDatabase(path) {
		return Resolution{Path: working, WorkingDirectory: true}, nil
	}
	return Resolution{Path: path}, nil
}

// Path returns the resolved data directory.
func Path() (string, error) {
	resolution, err := Resolve()
	return resolution.Path, err
}

// DatabasePath returns the default SQLite database path.
func DatabasePath() (string, error) {
	path, err := Path()
	if err != nil {
		return "", err
	}
	return filepath.Join(path, "steward.db"), nil
}

func hasDatabase(directory string) bool {
	info, err := os.Stat(filepath.Join(directory, "steward.db"))
	return err == nil && info.Mode().IsRegular()
}
