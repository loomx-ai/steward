package gcp

import (
	"strings"
	"unicode/utf8"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

// Unicode 15.0/17.0 Default_Ignorable_Code_Point (identical in both versions).
// Source: unicode.org/Public/{version}/ucd/DerivedCoreProperties.txt.
// Copyright Unicode, Inc.; see testdata/logging/UNICODE-LICENSE.txt.
func loggingIgnorable(r rune) bool {
	return r == 0xAD || r == 0x34F || r == 0x61C || r >= 0x115F && r <= 0x1160 ||
		r >= 0x17B4 && r <= 0x17B5 || r >= 0x180B && r <= 0x180F ||
		r >= 0x200B && r <= 0x200F || r >= 0x202A && r <= 0x202E ||
		r >= 0x2060 && r <= 0x206F || r == 0x3164 || r >= 0xFE00 && r <= 0xFE0F ||
		r == 0xFEFF || r == 0xFFA0 || r >= 0xFFF0 && r <= 0xFFF8 ||
		r >= 0x1BCA0 && r <= 0x1BCA3 || r >= 0x1D173 && r <= 0x1D17A ||
		r >= 0xE0000 && r <= 0xE0FFF
}

// Logging compares strings with NFKC_CF, not simple Unicode lowercasing.
// Map each code point before final NFC, as specified by toNFKC_Casefold.
// Full official mapping vectors are retained for both x/text build-tag versions.
func loggingFold(value string) (string, bool) {
	if !utf8.ValidString(value) || norm.Version != cases.UnicodeVersion || norm.Version != "15.0.0" && norm.Version != "17.0.0" {
		return "", false
	}
	var out strings.Builder
	fold := cases.Fold()
	for _, r := range value {
		if loggingIgnorable(r) {
			continue
		}
		// Unicode case folding preserves Cherokee capitals. x/text Fold
		// currently lowercases these default-identity code points instead.
		if r >= 0x13A0 && r <= 0x13F5 {
			out.WriteRune(r)
			continue
		}
		if r < utf8.RuneSelf {
			if r >= 'A' && r <= 'Z' {
				r += 'a' - 'A'
			}
			out.WriteRune(r)
			continue
		}
		mapped := norm.NFKC.String(fold.String(norm.NFKC.String(string(r))))
		for _, r := range mapped {
			if !loggingIgnorable(r) {
				out.WriteRune(r)
			}
		}
	}
	normalized := norm.NFC.String(out.String())
	// x/text inserts CGJ for stream safety after long combining sequences.
	// NFKC_CF removes CGJ; inserted markers cannot prove native inequality.
	if strings.ContainsRune(normalized, 0x34F) {
		return "", false
	}
	return normalized, true
}

// Logging does not pin its Unicode version in its public query contract.
// These 92 scalar mappings differ between the retained Unicode 15 and 17 data.
// They cannot establish equality or inequality against an unknown backend version.
func loggingVersionSensitive(r rune) bool {
	return r == 0x1C89 || r >= 0xA7CB && r <= 0xA7CC || r == 0xA7CE ||
		r == 0xA7D2 || r == 0xA7D4 || r == 0xA7DA || r == 0xA7DC || r == 0xA7F1 ||
		r >= 0x10D50 && r <= 0x10D65 || r >= 0x16EA0 && r <= 0x16EB8 ||
		r >= 0x1CCD6 && r <= 0x1CCF9
}
func loggingNativeFold(value string) (string, bool) {
	for _, r := range value {
		if loggingVersionSensitive(r) {
			return "", false
		}
	}
	return loggingFold(value)
}
