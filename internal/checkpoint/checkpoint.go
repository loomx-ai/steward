// Package checkpoint asks the LoomX checkpoint service whether a newer Steward
// release exists and whether a security bulletin applies to this build.
//
// The request carries the running version, the operating system, the
// architecture, and a random signature that identifies the installation
// without identifying its user. Nothing about the inventory, the configured
// connections, or the command being run is sent. Set STEWARD_CHECKPOINT_DISABLE
// or DO_NOT_TRACK to switch the request off entirely.
package checkpoint

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

const (
	// DefaultEndpoint is the LoomX checkpoint service.
	DefaultEndpoint = "https://checkpoint.loomx.ai"
	// Product names this program to the service.
	Product = "steward"

	// SignatureFileName holds the installation signature inside the data
	// directory, and CacheFileName the last answer.
	SignatureFileName = "checkpoint_signature"
	CacheFileName     = "checkpoint_cache"

	defaultTimeout = 3 * time.Second
	// A check is answered from the cache for a day, so a machine reaches the
	// service at most once per day however many commands it runs.
	defaultCacheDuration = 24 * time.Hour

	// Bounds on what the service is allowed to hand back for display.
	maxAlerts        = 8
	maxAlertMessage  = 500
	maxResponseBytes = 64 << 10
)

// Alert is a bulletin the service considers applicable to this build.
type Alert struct {
	ID      int    `json:"id"`
	Date    int    `json:"date"`
	Message string `json:"message"`
	URL     string `json:"url"`
	Level   string `json:"level"`
}

// Response is the answer to a check.
type Response struct {
	Product             string  `json:"product"`
	CurrentVersion      string  `json:"current_version"`
	CurrentReleaseDate  int     `json:"current_release_date"`
	CurrentDownloadURL  string  `json:"current_download_url"`
	CurrentChangelogURL string  `json:"current_changelog_url"`
	ProjectWebsite      string  `json:"project_website"`
	Outdated            bool    `json:"outdated"`
	Alerts              []Alert `json:"alerts"`
}

// Params configures a check. Only Version and Directory are required.
type Params struct {
	// Version is the running build, such as "0.4.1" or "dev".
	Version string
	// OS and Arch default to the values this binary was built for.
	OS   string
	Arch string
	// Directory holds the signature and cache files; checks run without
	// either when it is empty.
	Directory string
	// Endpoint overrides DefaultEndpoint.
	Endpoint string
	// CacheDuration overrides the one-day cache lifetime.
	CacheDuration time.Duration
	// Timeout bounds the request; checking for a release never delays a
	// command for long.
	Timeout time.Duration
	// Client, Getenv, and Now exist for tests.
	Client *http.Client
	Getenv func(string) string
	Now    func() time.Time
}

var (
	versionPattern   = regexp.MustCompile(`^(?:dev|[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z]+(?:[.-][0-9A-Za-z]+)*)?)$`)
	signaturePattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	platformPattern  = regexp.MustCompile(`^[a-z0-9]{1,16}$`)
)

func (p Params) getenv(name string) string {
	if p.Getenv != nil {
		return p.Getenv(name)
	}
	return os.Getenv(name)
}

func (p Params) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

// Disabled reports whether the operator switched checking off. DO_NOT_TRACK is
// the cross-vendor convention; STEWARD_CHECKPOINT_DISABLE is specific to
// Steward. Either one set to a non-empty value other than "0" is enough.
func Disabled(getenv func(string) string) bool {
	for _, name := range []string{"STEWARD_CHECKPOINT_DISABLE", "DO_NOT_TRACK"} {
		if value := strings.TrimSpace(getenv(name)); value != "" && value != "0" {
			return true
		}
	}
	return false
}

// Start runs a check in the background and reports the answer on the returned
// channel. The channel always receives exactly one value, nil when checking is
// disabled or the service could not be reached, so a caller may wait on it
// without risking a hang beyond the configured timeout.
func Start(ctx context.Context, params Params) <-chan *Response {
	result := make(chan *Response, 1)
	go func() {
		response, err := Check(ctx, params)
		if err != nil {
			response = nil
		}
		result <- response
	}()
	return result
}

// Check returns the service's answer, reading a recent cached answer instead of
// making a request when one is available. It returns nil without an error when
// checking is disabled.
func Check(ctx context.Context, params Params) (*Response, error) {
	if Disabled(params.getenv) {
		return nil, nil
	}
	if params.OS == "" {
		params.OS = runtime.GOOS
	}
	if params.Arch == "" {
		params.Arch = runtime.GOARCH
	}
	if !platformPattern.MatchString(params.OS) || !platformPattern.MatchString(params.Arch) {
		return nil, fmt.Errorf("invalid platform %s_%s", params.OS, params.Arch)
	}
	if !versionPattern.MatchString(params.Version) {
		return nil, fmt.Errorf("invalid version %q", params.Version)
	}

	cachePath := params.path(CacheFileName)
	if response := readCache(cachePath, params.Version, params.cacheDuration(), params.now()); response != nil {
		return response, nil
	}

	endpoint, err := params.endpoint()
	if err != nil {
		return nil, err
	}
	query := url.Values{}
	query.Set("version", params.Version)
	query.Set("os", params.OS)
	query.Set("arch", params.Arch)
	if signature, err := params.signature(); err == nil && signature != "" {
		query.Set("signature", signature)
	}
	endpoint.Path = strings.TrimRight(endpoint.Path, "/") + "/v1/check/" + Product
	endpoint.RawQuery = query.Encode()

	ctx, cancel := context.WithTimeout(ctx, params.timeout())
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "Steward/"+params.Version)

	client := params.Client
	if client == nil {
		client = &http.Client{Timeout: params.timeout()}
	}
	httpResponse, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("check for a newer release: %w", err)
	}
	defer httpResponse.Body.Close()
	if httpResponse.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("check for a newer release: %s", httpResponse.Status)
	}
	var response Response
	if err := json.NewDecoder(io.LimitReader(httpResponse.Body, maxResponseBytes)).Decode(&response); err != nil {
		return nil, fmt.Errorf("check for a newer release: %w", err)
	}
	if err := sanitize(&response); err != nil {
		return nil, err
	}
	writeCache(cachePath, params.Version, params.now(), response)
	return &response, nil
}

// sanitize rejects an answer that does not describe this product and strips
// everything a hostile or misconfigured endpoint could use to forge terminal
// output: control characters in messages, and links on any scheme but HTTPS.
func sanitize(response *Response) error {
	if response.Product != Product {
		return fmt.Errorf("checkpoint answered for %q", response.Product)
	}
	if response.CurrentVersion != "" && !versionPattern.MatchString(response.CurrentVersion) {
		return fmt.Errorf("checkpoint answered with version %q", response.CurrentVersion)
	}
	response.CurrentDownloadURL = safeURL(response.CurrentDownloadURL)
	response.CurrentChangelogURL = safeURL(response.CurrentChangelogURL)
	response.ProjectWebsite = safeURL(response.ProjectWebsite)
	if len(response.Alerts) > maxAlerts {
		response.Alerts = response.Alerts[:maxAlerts]
	}
	for i := range response.Alerts {
		response.Alerts[i].Message = safeText(response.Alerts[i].Message)
		response.Alerts[i].URL = safeURL(response.Alerts[i].URL)
		response.Alerts[i].Level = safeText(response.Alerts[i].Level)
	}
	response.Alerts = keepFunc(response.Alerts, func(alert Alert) bool { return alert.Message != "" })
	return nil
}

func keepFunc(alerts []Alert, keep func(Alert) bool) []Alert {
	kept := alerts[:0]
	for _, alert := range alerts {
		if keep(alert) {
			kept = append(kept, alert)
		}
	}
	return kept
}

func safeText(value string) string {
	value = strings.Map(func(r rune) rune {
		if r == '\t' {
			return ' '
		}
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, value)
	value = strings.TrimSpace(value)
	if len(value) > maxAlertMessage {
		value = strings.TrimSpace(value[:maxAlertMessage]) + "…"
	}
	return value
}

func safeURL(value string) string {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return ""
	}
	return parsed.String()
}

func (p Params) endpoint() (*url.URL, error) {
	value := strings.TrimSpace(p.Endpoint)
	if value == "" {
		value = strings.TrimSpace(p.getenv("STEWARD_CHECKPOINT_URL"))
	}
	if value == "" {
		value = DefaultEndpoint
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") {
		return nil, fmt.Errorf("invalid checkpoint endpoint %q", value)
	}
	return parsed, nil
}

func (p Params) timeout() time.Duration {
	if p.Timeout > 0 {
		return p.Timeout
	}
	if value := strings.TrimSpace(p.getenv("STEWARD_CHECKPOINT_TIMEOUT")); value != "" {
		if parsed, err := time.ParseDuration(value); err == nil && parsed > 0 {
			return parsed
		}
	}
	return defaultTimeout
}

func (p Params) cacheDuration() time.Duration {
	if p.CacheDuration > 0 {
		return p.CacheDuration
	}
	return defaultCacheDuration
}

func (p Params) path(name string) string {
	if p.Directory == "" {
		return ""
	}
	return filepath.Join(p.Directory, name)
}

// signature returns the installation signature, creating one when this is the
// first check. It is a random UUID: it identifies the installation across
// checks so a release is counted once, and carries nothing derived from the
// machine, the network, or the user. Deleting the file issues a new one.
func (p Params) signature() (string, error) {
	path := p.path(SignatureFileName)
	if path == "" {
		return "", nil
	}
	if value := strings.TrimSpace(p.getenv("STEWARD_CHECKPOINT_SIGNATURE_DISABLE")); value != "" && value != "0" {
		return "", nil
	}
	content, err := os.ReadFile(path)
	if err == nil {
		first, _, _ := strings.Cut(string(content), "\n")
		if first = strings.TrimSpace(first); signaturePattern.MatchString(first) {
			return first, nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	signature, err := newSignature()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(signature+"\n\n"+signatureNotice), 0o600); err != nil {
		return "", err
	}
	return signature, nil
}

func newSignature() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

const signatureNotice = `This is a randomly generated identifier Steward sends when it checks for a
newer release, so that one installation is counted once and a security
bulletin is not repeated. It is not derived from anything about you or this
machine. Delete this file to be issued a new one, or set
STEWARD_CHECKPOINT_DISABLE=1 to stop checking for releases altogether.
`

type cacheFile struct {
	Version   string    `json:"version"`
	CheckedAt time.Time `json:"checked_at"`
	Response  Response  `json:"response"`
}

func readCache(path string, version string, lifetime time.Duration, now time.Time) *Response {
	if path == "" {
		return nil
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var cached cacheFile
	if err := json.Unmarshal(content, &cached); err != nil {
		return nil
	}
	// An upgrade invalidates the answer: the previous one was about the
	// version that has just been replaced.
	if cached.Version != version || now.Sub(cached.CheckedAt) >= lifetime || cached.CheckedAt.After(now) {
		return nil
	}
	if err := sanitize(&cached.Response); err != nil {
		return nil
	}
	return &cached.Response
}

func writeCache(path string, version string, now time.Time, response Response) {
	if path == "" {
		return
	}
	content, err := json.Marshal(cacheFile{Version: version, CheckedAt: now.UTC(), Response: response})
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	// A failed write only costs another request tomorrow.
	_ = os.WriteFile(path, content, 0o600)
}
