package providers_test

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/provider/spec"
	"gopkg.in/yaml.v3"
)

type parityTarget struct {
	Resources     []string `yaml:"resources"`
	Unimplemented []string `yaml:"unimplemented_resources"`
}

type parityBaseline struct {
	Source     string   `yaml:"inventory_source"`
	Scope      string   `yaml:"scope"`
	Actions    []string `yaml:"actions"`
	Hook       string   `yaml:"hook"`
	Enrichment bool     `yaml:"enrichment"`
	Parent     bool     `yaml:"parent_discovery"`
}

type parityRow struct {
	Alicloud string         `yaml:"alicloud"`
	Spec     string         `yaml:"spec"`
	Class    string         `yaml:"class"`
	Baseline parityBaseline `yaml:"baseline"`
	GCP      parityTarget   `yaml:"gcp"`
	Azure    parityTarget   `yaml:"azure"`
	Status   string         `yaml:"status"`
	Notes    string         `yaml:"notes"`
	Sources  []string       `yaml:"sources"`
}

// This checks scope and source consistency, not cloud feature equivalence. A
// mapped spec or passing test does not satisfy the matrix's behavioral acceptance.
func TestParityMatrixMatchesProviderSpecifications(t *testing.T) {
	data, err := os.ReadFile("parity.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var matrix struct {
		Schema           string            `yaml:"schema"`
		Purpose          string            `yaml:"purpose"`
		Evidence         map[string]string `yaml:"evidence"`
		BaselineProvider string            `yaml:"baseline_provider"`
		Resources        []parityRow       `yaml:"resources"`
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&matrix); err != nil {
		t.Fatal(err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		t.Fatal("expected one complete matrix document", err)
	}
	if matrix.Schema != "steward.io/provider-parity/v1" || matrix.BaselineProvider != "alicloud" {
		t.Fatal("unexpected parity matrix contract")
	}
	definitions := map[string]map[string]spec.ResourceKindSpec{}
	paths := map[string]string{}
	for _, provider := range []string{"alicloud", "gcp", "azure"} {
		definitions[provider] = map[string]spec.ResourceKindSpec{}
		files, err := filepath.Glob(filepath.Join(provider, "specs", "*.yaml"))
		if err != nil || len(files) == 0 {
			t.Fatal("missing provider specifications", provider, err)
		}
		for _, path := range files {
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var definition spec.ResourceKindSpec
			if err := yaml.Unmarshal(raw, &definition); err != nil {
				t.Fatalf("%s: %v", path, err)
			}
			name := definition.Metadata.NativeType
			if name == "" || string(definition.Metadata.Provider) != provider {
				t.Fatal("invalid specification identity", path)
			}
			if _, exists := definitions[provider][name]; exists {
				t.Fatal("duplicate native specification", name)
			}
			definitions[provider][name] = definition
			if provider == "alicloud" {
				paths[name] = filepath.ToSlash(filepath.Join("providers", path))
			}
		}
	}
	seen := map[string]bool{}
	for _, row := range matrix.Resources {
		t.Run(row.Alicloud, func(t *testing.T) {
			baseline, ok := definitions["alicloud"][row.Alicloud]
			if !ok || seen[row.Alicloud] {
				t.Fatal("unknown or duplicated baseline", row.Alicloud)
			}
			seen[row.Alicloud] = true
			if row.Spec != paths[row.Alicloud] || row.Class != baseline.Metadata.Class {
				t.Fatal("baseline source or class drift")
			}
			actions := make([]string, 0, len(baseline.Actions))
			for action := range baseline.Actions {
				actions = append(actions, action)
			}
			slices.Sort(actions)
			declared := slices.Clone(row.Baseline.Actions)
			slices.Sort(declared)
			if row.Baseline.Source != baseline.Discovery.Source || row.Baseline.Scope != string(baseline.Scope.Kind) || !slices.Equal(actions, declared) || row.Baseline.Hook != baseline.Extensions.Hook || row.Baseline.Enrichment != (baseline.Discovery.Enrich != nil) || row.Baseline.Parent != (baseline.Discovery.Parent != nil) {
				t.Fatal("baseline inventory, scope, actions or discovery behavior drift")
			}
			if strings.TrimSpace(row.Status) == "" {
				t.Fatal("missing verification status")
			}
			for provider, target := range map[string]parityTarget{"gcp": row.GCP, "azure": row.Azure} {
				references := map[string]bool{}
				if len(target.Resources)+len(target.Unimplemented) == 0 && strings.TrimSpace(row.Notes) == "" {
					t.Error("empty mapping has no research explanation", provider)
				}
				for _, name := range append(slices.Clone(target.Resources), target.Unimplemented...) {
					if name == "" || references[name] {
						t.Error("empty or duplicate resource reference", provider, name)
					}
					references[name] = true
				}
				for _, name := range target.Resources {
					if _, exists := definitions[provider][name]; !exists {
						t.Error("mapped resource has no specification; declare the implementation gap explicitly", provider, name)
					}
				}
				for _, name := range target.Unimplemented {
					if _, exists := definitions[provider][name]; exists {
						t.Error("implementation backlog is stale; review the new specification", provider, name)
					}
				}
			}
		})
	}
	for name := range definitions["alicloud"] {
		if !seen[name] {
			t.Error("baseline resource omitted from parity scope", name)
		}
	}
}
