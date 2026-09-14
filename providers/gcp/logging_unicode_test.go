package gcp

import (
	"bufio"
	"os"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/text/unicode/norm"
)

func loggingUnicodeMappings(t *testing.T, version string) map[rune]string {
	t.Helper()
	file, err := os.Open("testdata/logging/nfkc_cf_" + version + ".txt")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	mapped := map[rune]string{}
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "#") || line == "" {
			continue
		}
		fields := strings.Split(line, ";")
		endpoints := strings.Split(fields[0], "..")
		low, err := strconv.ParseInt(endpoints[0], 16, 32)
		if err != nil {
			t.Fatal(err)
		}
		high := low
		if len(endpoints) == 2 {
			high, err = strconv.ParseInt(endpoints[1], 16, 32)
			if err != nil {
				t.Fatal(err)
			}
		}
		expected := ""
		for _, code := range strings.Fields(fields[1]) {
			r, err := strconv.ParseInt(code, 16, 32)
			if err != nil {
				t.Fatal(err)
			}
			expected += string(rune(r))
		}
		for cp := low; cp <= high; cp++ {
			mapped[rune(cp)] = expected
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if len(mapped) < 10000 {
		t.Fatal("incomplete Unicode conformance vectors", len(mapped))
	}
	return mapped
}
func TestLoggingOfficialUnicodeCasefoldMappings(t *testing.T) {
	mapped := loggingUnicodeMappings(t, norm.Version)
	for cp := rune(0); cp <= 0x10FFFF; cp++ {
		if cp >= 0xD800 && cp <= 0xDFFF {
			continue
		}
		expected, ok := mapped[cp]
		if !ok {
			expected = string(cp)
		}
		got, ok := loggingFold(string(cp))
		if !ok || got != expected {
			t.Fatalf("U+%04X: got %q want %q", cp, got, expected)
		}
	}
	t.Logf("verified %d explicit mappings and all default identity scalars for Unicode %s", len(mapped), norm.Version)
}
func TestLoggingUnicodeVersionUncertainty(t *testing.T) {
	before, after := loggingUnicodeMappings(t, "15.0.0"), loggingUnicodeMappings(t, "17.0.0")
	count := 0
	for cp := rune(0); cp <= 0x10FFFF; cp++ {
		a, ok := before[cp]
		if !ok {
			a = string(cp)
		}
		b, ok := after[cp]
		if !ok {
			b = string(cp)
		}
		if loggingVersionSensitive(cp) != (a != b) {
			t.Fatalf("incorrect version boundary U+%04X", cp)
		}
		if a != b {
			count++
			if _, ok := loggingNativeFold(string(cp)); ok {
				t.Fatalf("version-specific mapping accepted U+%04X", cp)
			}
		}
	}
	if count != 92 {
		t.Fatal(count)
	}
	if got := loggingFilterReference(`labels.check_id="`+string(rune(0x1CCD6))+`"`, "A"); got != monitoringHasReference {
		t.Fatal("new compatibility letter excluded a possible match", got)
	}
}

func TestLoggingUnicodeCasefoldSequences(t *testing.T) {
	for input, want := range map[string]string{"ＰＵＢＬＩＣ-ＣＨＥＣＫ": "public-check", "Straße": "strasse", "pub\u200blic-check": "public-check", "a\u034F\u030A": "å", "\u212A": "k", "\uFB03": "ffi", "\u115F\u1160\u3164\uFFA0": "", "\u0600": "\u0600"} {
		got, ok := loggingFold(input)
		if !ok || got != want {
			t.Fatal(input, got, want)
		}
	}
	if _, ok := loggingFold(string([]byte{255})); ok {
		t.Fatal("invalid UTF8 accepted")
	}
}
