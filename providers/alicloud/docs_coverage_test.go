package alicloud

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"

	"gopkg.in/yaml.v3"
)

// The provider page states how many types Steward identifies and how many it
// can clean up; the numbers must follow the specifications and the Resource
// Center catalog.
func TestProviderPageStatesTheCoverage(t *testing.T) {
	identified := map[string]bool{}
	cleanable := 0
	files, err := filepath.Glob(filepath.Join("specs", "*.yaml"))
	if err != nil || len(files) == 0 {
		t.Fatal("no specifications", err)
	}
	for _, path := range files {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var document struct {
			Metadata struct {
				NativeType string `yaml:"nativeType"`
			} `yaml:"metadata"`
			Actions map[string]any `yaml:"actions"`
		}
		if err := yaml.Unmarshal(raw, &document); err != nil {
			t.Fatal(err)
		}
		identified[document.Metadata.NativeType] = true
		if document.Actions["delete"] != nil {
			cleanable++
		}
	}
	raw, err := os.ReadFile(filepath.Join("resourcecenter", "resources.json"))
	if err != nil {
		t.Fatal(err)
	}
	var catalog struct {
		Resources []struct {
			NativeType string `json:"native_type"`
		} `json:"resources"`
	}
	if err := json.Unmarshal(raw, &catalog); err != nil {
		t.Fatal(err)
	}
	for _, resource := range catalog.Resources {
		identified[resource.NativeType] = true
	}
	for language, pattern := range map[string]*regexp.Regexp{
		"en": regexp.MustCompile(`Steward identifies (\d+) Alibaba Cloud resource types, (\d+) of which`),
		"zh": regexp.MustCompile(`Steward 识别 (\d+) 类阿里云资源，其中 (\d+) 类`),
	} {
		page, err := os.ReadFile(filepath.Join("..", "..", "docs", "content", language, "alicloud.md"))
		if err != nil {
			t.Fatal(err)
		}
		match := pattern.FindSubmatch(page)
		if match == nil {
			t.Fatalf("%s page has no coverage statement", language)
		}
		stated, _ := strconv.Atoi(string(match[1]))
		statedCleanable, _ := strconv.Atoi(string(match[2]))
		if stated != len(identified) || statedCleanable != cleanable {
			t.Errorf("%s page states %d and %d; the provider identifies %d and cleans up %d", language, stated, statedCleanable, len(identified), cleanable)
		}
	}
}
