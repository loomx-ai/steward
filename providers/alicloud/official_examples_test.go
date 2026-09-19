package alicloud

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// Read operations whose pinned official metadata publishes no usable JSON
// response example. Their paths are still checked against the response schema.
var officialExampleGaps = map[string]string{
	"Oss@2019-05-17#ListBuckets":           "OSS publishes XML response examples only.",
	"Oss@2019-05-17#GetBucketInfo":         "OSS publishes XML response examples only.",
	"Vpc@2016-04-28#ListIpv4Gateways":      "The portal publishes no response example.",
	"Vpc@2016-04-28#DescribeIpv6Addresses": "The portal publishes no response example.",
	"OpenSearch@2017-12-25#ListAppGroups":  "The JSON example leaves id blank; the XML example of the same operation carries 110116134.",
}

// Specification types whose inventory is assembled by Steward from several
// native calls, so their normalized fields are not paths in one response.
func customInventoryFields(nativeType string) bool {
	return strings.HasPrefix(nativeType, "ACS::CEN::")
}

type responseBlock struct {
	location     string
	operation    catalogOperation
	itemsPath    string
	identityPath string
}

func officialResponsePaths(t *testing.T) (map[string][]string, map[string]any) {
	t.Helper()
	var pinned struct {
		Products []struct {
			Product string `json:"product"`
			Version string `json:"version"`
			APIs    map[string]struct {
				ResponsePaths []string `json:"response_paths"`
			} `json:"apis"`
		} `json:"products"`
	}
	raw, err := os.ReadFile(filepath.Join("catalog", "source", "official.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &pinned); err != nil {
		t.Fatal(err)
	}
	paths := map[string][]string{}
	for _, product := range pinned.Products {
		for name, api := range product.APIs {
			paths[product.Product+"@"+product.Version+"#"+name] = api.ResponsePaths
		}
	}
	var fixture struct {
		Examples map[string]any `json:"examples"`
	}
	raw, err = os.ReadFile(filepath.Join("fixtures", "official-examples.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	return paths, fixture.Examples
}

func joinResponsePath(itemsPath, field string) string {
	// Arrays are transparent in response paths, so a * segment adds nothing.
	itemsPath = strings.ReplaceAll(strings.TrimSpace(itemsPath), ".*", "")
	if itemsPath == "" || itemsPath == "$" {
		return field
	}
	if field == "" {
		return itemsPath
	}
	return itemsPath + "." + field
}

// exampleItems extracts records the way product API inventory does: an
// array, or a single object for detail responses.
func exampleItems(example any, itemsPath string) []any {
	value := valueAtPath(example, itemsPath)
	if items, ok := productAPIListValue(value); ok {
		return items
	}
	if object, ok := value.(map[string]any); ok && len(object) > 0 {
		return []any{object}
	}
	return nil
}

func TestSpecResponsePathsMatchOfficialMetadata(t *testing.T) {
	products, exceptions := loadOfficial(t)
	responsePaths, examples := officialResponsePaths(t)
	operations := map[string]catalogOperation{}
	short := map[string][]catalogOperation{}
	for _, operation := range loadCatalogOperations(t) {
		operations[operation.ID] = operation
		operations["AlibabaCloud."+operation.ID] = operation
		short[operation.Name] = append(short[operation.Name], operation)
	}
	resolve := func(reference string) (catalogOperation, bool) {
		if operation, ok := operations[reference]; ok {
			return operation, true
		}
		if matches := short[reference]; len(matches) == 1 {
			return matches[0], true
		}
		return catalogOperation{}, false
	}
	files, err := filepath.Glob(filepath.Join("specs", "*.yaml"))
	if err != nil || len(files) == 0 {
		t.Fatal("no specifications", err)
	}
	usedGaps := map[string]bool{}
	verifiedExamples := 0
	for _, path := range files {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var document map[string]any
		if err := yaml.Unmarshal(raw, &document); err != nil {
			t.Fatal(err)
		}
		var blocks []responseBlock
		var visit func(any, string)
		visit = func(value any, location string) {
			switch typed := value.(type) {
			case map[string]any:
				reference, _ := typed["operation"].(string)
				itemsPath, hasItems := typed["itemsPath"].(string)
				identityPath, hasIdentity := typed["identityPath"].(string)
				if reference != "" && hasItems && hasIdentity {
					operation, ok := resolve(reference)
					if !ok {
						t.Errorf("%s%s: operation %s is not in the catalog", path, location, reference)
					} else {
						blocks = append(blocks, responseBlock{location, operation, itemsPath, identityPath})
					}
				}
				for key, child := range typed {
					visit(child, location+"."+key)
				}
			case []any:
				for _, child := range typed {
					visit(child, location+"[]")
				}
			}
		}
		visit(document, "")
		metadata, _ := document["metadata"].(map[string]any)
		nativeType, _ := metadata["nativeType"].(string)
		discovery, _ := document["discovery"].(map[string]any)
		source, _ := discovery["source"].(string)
		fieldPaths := map[string][]string{}
		for _, block := range blocks {
			call := block.operation.Call
			key := call.Product + "@" + call.Version + "#" + block.operation.Name
			if _, excepted := exceptions[key]; excepted {
				continue
			}
			if _, pinned := products[call.Product+"@"+call.Version].APIs[block.operation.Name]; !pinned {
				t.Errorf("%s%s: %s is not pinned", path, block.location, key)
				continue
			}
			paths := responsePaths[key]
			required := []string{joinResponsePath(block.itemsPath, "")}
			for _, part := range strings.Split(block.identityPath, "+") {
				// _parent and _item are added by inventory, not returned.
				if part = strings.TrimSpace(part); !strings.HasPrefix(part, "_") {
					required = append(required, joinResponsePath(block.itemsPath, part))
				}
			}
			for _, required := range required {
				if required != "" && !slices.Contains(paths, required) {
					t.Errorf("%s%s: %s is not in the official %s response", path, block.location, required, key)
				}
			}
			if block.location == ".discovery.list" || block.location == ".discovery.enrich" {
				fieldPaths[block.location] = paths
			}
			example, published := examples[key]
			if reason, gap := officialExampleGaps[key]; gap {
				usedGaps[key] = true
				if strings.TrimSpace(reason) == "" {
					t.Errorf("%s: an example gap needs a reason", key)
				}
				continue
			}
			if !published {
				t.Errorf("%s%s: no official JSON example is pinned for %s", path, block.location, key)
				continue
			}
			items := exampleItems(example, block.itemsPath)
			if len(items) == 0 {
				t.Errorf("%s%s: the official %s example has no records at %q", path, block.location, key, block.itemsPath)
				continue
			}
			for index, item := range items {
				record, ok := productAPIListRecord(item)
				if ok {
					record["_parent"] = map[string]any{"nativeId": "parent"}
				}
				if !ok || recordIdentity(record, block.identityPath) == "" {
					t.Errorf("%s%s: record %d of the official %s example has no identity at %q", path, block.location, index, key, block.identityPath)
				}
			}
			verifiedExamples++
		}
		// A product API field must be a path of the list or enrichment record.
		list, _ := discovery["list"].(map[string]any)
		enrich, _ := discovery["enrich"].(map[string]any)
		fields, _ := document["fields"].(map[string]any)
		if source != "product-api" || customInventoryFields(nativeType) || fieldPaths[".discovery.list"] == nil {
			continue
		}
		for name, value := range fields {
			fieldPath, _ := value.(string)
			if property, ok := value.(map[string]any); ok {
				fieldPath, _ = property["path"].(string)
			}
			if fieldPath == "" || strings.HasPrefix(fieldPath, "_") {
				continue
			}
			listItems, _ := list["itemsPath"].(string)
			found := slices.Contains(fieldPaths[".discovery.list"], joinResponsePath(listItems, fieldPath))
			if !found && enrich != nil {
				enrichItems, _ := enrich["itemsPath"].(string)
				found = slices.Contains(fieldPaths[".discovery.enrich"], joinResponsePath(enrichItems, fieldPath))
			}
			if !found {
				t.Errorf("%s: field %s reads %s, which the official list or enrichment response does not return", path, name, fieldPath)
			}
		}
	}
	for key := range officialExampleGaps {
		if !usedGaps[key] {
			t.Errorf("stale official example gap %s", key)
		}
	}
	if verifiedExamples < 250 {
		t.Fatalf("verified only %d specification reads against official examples", verifiedExamples)
	}
}
