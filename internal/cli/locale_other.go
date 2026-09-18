//go:build !windows

package cli

// systemLocale is empty on Unix-like systems, where the terminal's locale
// environment variables are authoritative.
func systemLocale() string { return "" }
