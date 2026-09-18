package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestDetect(t *testing.T) {
	owns := func(owner string) func(string, string) bool {
		return func(tool string, _ string) bool { return tool == owner }
	}
	tests := []struct {
		name string
		env  Environment
		want Source
	}{
		{"homebrew arm", Environment{GOOS: "darwin", Executable: "/opt/homebrew/Cellar/steward/0.2.0/bin/steward"}, SourceHomebrew},
		{"linuxbrew", Environment{GOOS: "linux", Executable: "/home/linuxbrew/.linuxbrew/Cellar/steward/0.2.0/bin/steward", Owns: owns("dpkg-query")}, SourceHomebrew},
		{"scoop", Environment{GOOS: "windows", Executable: `C:\Users\me\scoop\apps\steward\current\steward.exe`}, SourceScoop},
		{"deb", Environment{GOOS: "linux", Executable: "/usr/bin/steward", Owns: owns("dpkg-query")}, SourceAPT},
		{"rpm", Environment{GOOS: "linux", Executable: "/usr/bin/steward", Owns: owns("rpm")}, SourceRPM},
		{"install script", Environment{GOOS: "linux", Executable: "/home/me/.local/bin/steward", Owns: owns("")}, SourceBinary},
		{"macOS download", Environment{GOOS: "darwin", Executable: "/usr/local/bin/steward", Owns: owns("dpkg-query")}, SourceBinary},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := Detect(test.env); got != test.want {
				t.Fatalf("Detect() = %s, want %s", got, test.want)
			}
		})
	}
}

func TestCommand(t *testing.T) {
	onlyYum := func(name string) (string, error) {
		if name == "yum" {
			return "/usr/bin/yum", nil
		}
		return "", errors.New("not found")
	}
	tests := []struct {
		source Source
		env    Environment
		want   string
	}{
		{SourceHomebrew, Environment{}, "brew upgrade loomx-ai/tap/steward"},
		{SourceScoop, Environment{}, "scoop update steward"},
		{SourceAPT, Environment{}, "sh -c sudo apt-get update && sudo apt-get install --only-upgrade -y steward"},
		{SourceRPM, Environment{}, "sudo dnf upgrade -y --refresh steward"},
		{SourceRPM, Environment{LookPath: onlyYum}, "sudo yum upgrade -y steward"},
		{SourceBinary, Environment{}, ""},
	}
	for _, test := range tests {
		if got := strings.Join(Command(test.source, test.env), " "); got != test.want {
			t.Errorf("Command(%s) = %q, want %q", test.source, got, test.want)
		}
	}
}

func TestNewer(t *testing.T) {
	for _, test := range []struct {
		candidate, current string
		want               bool
	}{
		{"0.2.0", "0.1.0", true},
		{"0.2.0", "0.2.0", false},
		{"0.1.9", "0.2.0", false},
		{"0.10.0", "0.9.0", true},
		{"0.2.0", "0.2.0-rc.1", true},
		{"0.2.0-rc.2", "0.2.0-rc.1", true},
		{"0.2.0-rc.10", "0.2.0-rc.9", true},
		{"0.2.0-rc.1", "0.2.0", false},
		{"0.2.0", "dev", true},
		{"0.2.0", "0.0.0-dev.abc123", true},
	} {
		if got := Newer(test.candidate, test.current); got != test.want {
			t.Errorf("Newer(%q, %q) = %v, want %v", test.candidate, test.current, got, test.want)
		}
	}
}

func releaseServer(t *testing.T, latest string, binary []byte, checksum string) *httptest.Server {
	t.Helper()
	asset := AssetName(latest, "linux", "amd64")
	if checksum == "" {
		sum := sha256.Sum256(binary)
		checksum = hex.EncodeToString(sum[:])
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/releases/latest":
			http.Redirect(w, r, "/releases/tag/v"+latest, http.StatusFound)
		case "/releases/download/v" + latest + "/checksums.txt":
			fmt.Fprintf(w, "%s  other\n%s  %s\n", strings.Repeat("0", 64), checksum, asset)
		case "/releases/download/v" + latest + "/" + asset:
			w.Write(binary)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func TestLatestFollowsReleaseRedirect(t *testing.T) {
	server := releaseServer(t, "1.2.3", nil, "")
	got, err := Releases{Repository: server.URL}.Latest(context.Background())
	if err != nil || got != "1.2.3" {
		t.Fatalf("Latest() = %q, %v", got, err)
	}
}

func TestDownloadVerifiesAndReplaces(t *testing.T) {
	binary := []byte("new steward")
	server := releaseServer(t, "1.2.3", binary, "")
	target := filepath.Join(t.TempDir(), "steward")
	if err := os.WriteFile(target, []byte("old steward"), 0o755); err != nil {
		t.Fatal(err)
	}
	staged, err := Releases{Repository: server.URL}.Download(context.Background(), "1.2.3", "linux", "amd64", target)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(staged) != filepath.Dir(target) {
		t.Fatalf("staged %s outside target directory", staged)
	}
	for _, goos := range []string{"linux", "windows"} {
		if goos == "windows" {
			if err := os.WriteFile(staged, binary, 0o755); err != nil {
				t.Fatal(err)
			}
		}
		if err := Replace(staged, target, goos); err != nil {
			t.Fatalf("Replace(%s) error = %v", goos, err)
		}
		content, _ := os.ReadFile(target)
		if string(content) != string(binary) {
			t.Fatalf("Replace(%s) content = %q", goos, content)
		}
	}
	entries, _ := os.ReadDir(filepath.Dir(target))
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	if !slices.Equal(names, []string{"steward"}) {
		t.Fatalf("leftover files: %v", names)
	}
}

func TestDownloadRejectsChecksumMismatch(t *testing.T) {
	server := releaseServer(t, "1.2.3", []byte("tampered"), strings.Repeat("a", 64))
	directory := t.TempDir()
	target := filepath.Join(directory, "steward")
	if _, err := (Releases{Repository: server.URL}).Download(context.Background(), "1.2.3", "linux", "amd64", target); err == nil || !strings.Contains(err.Error(), "SHA-256") {
		t.Fatalf("Download() error = %v, want checksum failure", err)
	}
	if entries, _ := os.ReadDir(directory); len(entries) != 0 {
		t.Fatalf("staged file left behind: %v", entries)
	}
}

func TestDownloadRejectsMissingChecksum(t *testing.T) {
	server := releaseServer(t, "1.2.3", []byte("x"), "")
	_, err := Releases{Repository: server.URL}.Download(context.Background(), "1.2.3", "darwin", "arm64", filepath.Join(t.TempDir(), "steward"))
	if err == nil || !strings.Contains(err.Error(), "no checksum") {
		t.Fatalf("Download() error = %v, want missing checksum", err)
	}
}
