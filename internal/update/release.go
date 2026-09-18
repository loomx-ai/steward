package update

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// DefaultRepository is the public release location.
const DefaultRepository = "https://github.com/loomx-ai/steward"

var versionPattern = regexp.MustCompile(`^(\d+)\.(\d+)\.(\d+)(?:-([0-9A-Za-z]+(?:[.-][0-9A-Za-z]+)*))?$`)

// Releases reads published GitHub releases.
type Releases struct {
	Repository string
	Client     *http.Client
}

func (r Releases) client() *http.Client {
	if r.Client != nil {
		return r.Client
	}
	return http.DefaultClient
}

func (r Releases) repository() string {
	if r.Repository != "" {
		return strings.TrimRight(r.Repository, "/")
	}
	return DefaultRepository
}

// Latest returns the latest stable version from the releases/latest redirect,
// which avoids GitHub API rate limits.
func (r Releases) Latest(ctx context.Context) (string, error) {
	client := *r.client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	request, err := http.NewRequestWithContext(ctx, http.MethodHead, r.repository()+"/releases/latest", nil)
	if err != nil {
		return "", err
	}
	response, err := client.Do(request)
	if err != nil {
		return "", fmt.Errorf("check latest release: %w", err)
	}
	response.Body.Close()
	location := response.Header.Get("Location")
	if response.StatusCode < 300 || response.StatusCode >= 400 || location == "" {
		return "", fmt.Errorf("check latest release: unexpected response %s", response.Status)
	}
	version := strings.TrimPrefix(location[strings.LastIndex(location, "/")+1:], "v")
	if !versionPattern.MatchString(version) {
		return "", fmt.Errorf("check latest release: invalid version %q", version)
	}
	return version, nil
}

// AssetName returns the raw executable published for a platform.
func AssetName(version string, goos string, goarch string) string {
	name := fmt.Sprintf("steward_%s_%s_%s", version, goos, goarch)
	if goos == "windows" {
		name += ".exe"
	}
	return name
}

// Download fetches the executable for a version into a temporary file beside
// target and verifies it against the release's checksums.txt. The caller owns
// the returned file.
func (r Releases) Download(ctx context.Context, version string, goos string, goarch string, target string) (string, error) {
	if !versionPattern.MatchString(version) {
		return "", fmt.Errorf("invalid version %q", version)
	}
	asset := AssetName(version, goos, goarch)
	base := r.repository() + "/releases/download/v" + version + "/"
	expected, err := r.checksum(ctx, base+"checksums.txt", asset)
	if err != nil {
		return "", err
	}
	response, err := r.get(ctx, base+asset)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	staged, err := os.CreateTemp(filepath.Dir(target), ".steward-update-*")
	if err != nil {
		return "", fmt.Errorf("prepare update beside %s: %w", target, err)
	}
	keep := false
	defer func() {
		if !keep {
			staged.Close()
			os.Remove(staged.Name())
		}
	}()
	hash := sha256.New()
	if _, err := io.Copy(io.MultiWriter(staged, hash), response.Body); err != nil {
		return "", fmt.Errorf("download %s: %w", asset, err)
	}
	if actual := hex.EncodeToString(hash.Sum(nil)); actual != expected {
		return "", fmt.Errorf("SHA-256 verification failed for %s; nothing was installed", asset)
	}
	if err := staged.Chmod(0o755); err != nil && goos != "windows" {
		return "", err
	}
	if err := staged.Close(); err != nil {
		return "", err
	}
	keep = true
	return staged.Name(), nil
}

func (r Releases) checksum(ctx context.Context, url string, asset string) (string, error) {
	response, err := r.get(ctx, url)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	scanner := bufio.NewScanner(response.Body)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) == 2 && fields[1] == asset && len(fields[0]) == 64 {
			return strings.ToLower(fields[0]), nil
		}
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("read checksums: %w", err)
	}
	return "", fmt.Errorf("release has no checksum for %s", asset)
}

func (r Releases) get(ctx context.Context, url string) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	response, err := r.client().Do(request)
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", url, err)
	}
	if response.StatusCode != http.StatusOK {
		response.Body.Close()
		return nil, fmt.Errorf("download %s: %s", url, response.Status)
	}
	return response, nil
}

// Replace atomically swaps target for staged. Windows cannot overwrite a
// running executable, so the old file is first moved aside.
func Replace(staged string, target string, goos string) error {
	if goos != "windows" {
		return os.Rename(staged, target)
	}
	aside := target + ".old"
	_ = os.Remove(aside)
	if err := os.Rename(target, aside); err != nil {
		return err
	}
	if err := os.Rename(staged, target); err != nil {
		if restoreErr := os.Rename(aside, target); restoreErr != nil {
			return errors.Join(err, restoreErr)
		}
		return err
	}
	// A running executable cannot be deleted on Windows; a later update removes it.
	_ = os.Remove(aside)
	return nil
}

// Newer reports whether candidate is a higher SemVer than current. Any release
// is newer than a build without a SemVer version, such as "dev".
func Newer(candidate string, current string) bool {
	a, okA := parse(candidate)
	b, okB := parse(current)
	if !okA || !okB {
		return okA && !okB
	}
	for i := range 3 {
		if a.numbers[i] != b.numbers[i] {
			return a.numbers[i] > b.numbers[i]
		}
	}
	switch {
	case a.prerelease == b.prerelease:
		return false
	case a.prerelease == "":
		return true
	case b.prerelease == "":
		return false
	}
	return comparePrerelease(a.prerelease, b.prerelease) > 0
}

type semver struct {
	numbers    [3]int
	prerelease string
}

func parse(version string) (semver, bool) {
	match := versionPattern.FindStringSubmatch(strings.TrimPrefix(version, "v"))
	if match == nil {
		return semver{}, false
	}
	var parsed semver
	for i := range 3 {
		parsed.numbers[i], _ = strconv.Atoi(match[i+1])
	}
	parsed.prerelease = match[4]
	return parsed, true
}

func comparePrerelease(a string, b string) int {
	left, right := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(left) && i < len(right); i++ {
		x, errX := strconv.Atoi(left[i])
		y, errY := strconv.Atoi(right[i])
		switch {
		case errX == nil && errY == nil:
			if x != y {
				return x - y
			}
		case errX == nil:
			return -1
		case errY == nil:
			return 1
		default:
			if c := strings.Compare(left[i], right[i]); c != 0 {
				return c
			}
		}
	}
	return len(left) - len(right)
}
