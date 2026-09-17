package aws

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/provider/catalog"
)

var sha256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// Generated operations must come from pinned official models, and regenerating
// from the checked-in source must reproduce the embedded catalog byte for byte.
func TestAWSCatalogIsReproducibleFromPinnedOfficialMetadata(t *testing.T) {
	source, err := os.ReadFile("catalog/source/smithy.json")
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Metadata struct {
			Sources []struct {
				URI    string `json:"uri"`
				SHA256 string `json:"sha256"`
			} `json:"steward.sources"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(source, &document); err != nil {
		t.Fatal(err)
	}
	if len(document.Metadata.Sources) == 0 {
		t.Fatal("Smithy source provenance is missing")
	}
	for _, item := range document.Metadata.Sources {
		if !strings.HasPrefix(item.URI, "https://raw.githubusercontent.com/aws/api-models-aws/") || !sha256Pattern.MatchString(item.SHA256) {
			t.Fatalf("unpinned model source %#v", item)
		}
	}
	generated, err := catalog.ImportOfficial("smithy", "aws", "catalog/source/smithy.json", source)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := catalog.MarshalGenerated(generated)
	if err != nil {
		t.Fatal(err)
	}
	embedded, err := os.ReadFile("catalog/generated/catalog.json")
	if err != nil {
		t.Fatal(err)
	}
	if string(payload) != string(embedded) {
		t.Fatal("generated AWS catalog is stale; run go generate ./providers/aws")
	}
	for _, operation := range generated.Operations {
		if operation.Call == nil || operation.Call.Style != "aws-smithy" || operation.SourceFormat != "aws-smithy" {
			t.Fatalf("operation %s lacks official call metadata", operation.ID)
		}
	}
}
