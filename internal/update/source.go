// Package update upgrades Steward through the channel that installed it.
package update

import (
	"os/exec"
	"strings"
)

// Source is the channel that installed the running executable.
type Source string

const (
	SourceHomebrew Source = "homebrew"
	SourceScoop    Source = "scoop"
	SourceAPT      Source = "apt"
	SourceRPM      Source = "rpm"
	SourceBinary   Source = "binary"
)

// Environment abstracts the host so detection can be tested.
type Environment struct {
	GOOS string
	// Executable is the running binary with symlinks resolved.
	Executable string
	// Owns reports whether a system package manager tool ("dpkg-query" or
	// "rpm") lists the path as part of an installed steward package.
	Owns     func(tool string, path string) bool
	LookPath func(name string) (string, error)
}

// Detect identifies the installation source of the running executable.
func Detect(env Environment) Source {
	path := strings.ToLower(strings.ReplaceAll(env.Executable, `\`, "/"))
	switch {
	case strings.Contains(path, "/cellar/steward/"):
		return SourceHomebrew
	case strings.Contains(path, "/scoop/apps/steward/"):
		return SourceScoop
	}
	if env.GOOS == "linux" && env.Owns != nil {
		if env.Owns("dpkg-query", env.Executable) {
			return SourceAPT
		}
		if env.Owns("rpm", env.Executable) {
			return SourceRPM
		}
	}
	return SourceBinary
}

// Command returns the package manager invocation for a source, or nil when
// Steward replaces its own binary.
func Command(source Source, env Environment) []string {
	switch source {
	case SourceHomebrew:
		return []string{"brew", "upgrade", "loomx-ai/tap/steward"}
	case SourceScoop:
		return []string{"scoop", "update", "steward"}
	case SourceAPT:
		return []string{"sh", "-c", "sudo apt-get update && sudo apt-get install --only-upgrade -y steward"}
	case SourceRPM:
		if env.LookPath != nil {
			if _, err := env.LookPath("dnf"); err != nil {
				if _, err := env.LookPath("yum"); err == nil {
					return []string{"sudo", "yum", "upgrade", "-y", "steward"}
				}
			}
		}
		return []string{"sudo", "dnf", "upgrade", "-y", "--refresh", "steward"}
	default:
		return nil
	}
}

// HostOwns asks dpkg-query or rpm whether a steward package owns path.
func HostOwns(tool string, path string) bool {
	if _, err := exec.LookPath(tool); err != nil {
		return false
	}
	var output []byte
	var err error
	switch tool {
	case "dpkg-query":
		output, err = exec.Command(tool, "-S", path).Output()
		// Output is "steward: /usr/bin/steward".
		return err == nil && strings.HasPrefix(strings.TrimSpace(string(output)), "steward:")
	case "rpm":
		output, err = exec.Command(tool, "-qf", "--queryformat", "%{NAME}", path).Output()
		return err == nil && strings.TrimSpace(string(output)) == "steward"
	default:
		return false
	}
}
