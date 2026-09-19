package alicloud

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/provider/catalog"
	"gopkg.in/yaml.v3"
)

// officialParameter mirrors one parameter of the pinned api.aliyun.com metadata.
type officialParameter struct {
	Name       string   `json:"name"`
	In         string   `json:"in"`
	Type       string   `json:"type"`
	Properties []string `json:"properties"`
}

type officialAPI struct {
	Methods    []string            `json:"methods"`
	Path       string              `json:"path"`
	Deprecated bool                `json:"deprecated"`
	Parameters []officialParameter `json:"parameters"`
}

type officialProduct struct {
	Product      string                 `json:"product"`
	Version      string                 `json:"version"`
	SourceURI    string                 `json:"source_uri"`
	SourceSHA256 string                 `json:"source_sha256"`
	Style        string                 `json:"style"`
	APIs         map[string]officialAPI `json:"apis"`
}

type officialException struct {
	Product   string `json:"product"`
	Version   string `json:"version"`
	Operation string `json:"operation"`
	Reason    string `json:"reason"`
	Evidence  string `json:"evidence"`
}

type catalogCall struct {
	Product              string   `json:"product"`
	Version              string   `json:"version"`
	Style                string   `json:"style"`
	Method               string   `json:"method"`
	Path                 string   `json:"path"`
	IdempotencyParameter string   `json:"idempotency_parameter"`
	EndpointParameters   []string `json:"endpoint_parameters"`
	HostParameters       []string `json:"host_parameters"`
	HeaderParameters     []string `json:"header_parameters"`
}

type catalogOperation struct {
	ID   string
	Name string
	Call catalogCall
}

// Evidence for an operation the portal omits must be Alibaba Cloud's own SDK
// or gateway source at a fixed commit.
var officialEvidence = regexp.MustCompile(`^https://github\.com/(aliyun|alibabacloud-go)/[A-Za-z0-9._-]+/blob/[0-9a-f]{40}/`)
var pathTemplate = regexp.MustCompile(`\{[^}]+\}`)

func loadCatalogOperations(t *testing.T) []catalogOperation {
	t.Helper()
	var document struct {
		Paths map[string]map[string]struct {
			OperationID string       `json:"operationId"`
			Name        string       `json:"x-operation-name"`
			Call        *catalogCall `json:"x-operation-call"`
		} `json:"paths"`
	}
	raw, err := os.ReadFile(filepath.Join("catalog", "source", "openapi.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	var result []catalogOperation
	for _, item := range document.Paths {
		for _, operation := range item {
			if operation.Call == nil {
				continue
			}
			name := operation.Name
			if name == "" {
				name = operation.OperationID[strings.LastIndex(operation.OperationID, ".")+1:]
			}
			result = append(result, catalogOperation{ID: operation.OperationID, Name: name, Call: *operation.Call})
		}
	}
	return result
}

func loadOfficial(t *testing.T) (map[string]officialProduct, map[string]officialException) {
	t.Helper()
	var pinned struct {
		Products []officialProduct `json:"products"`
	}
	raw, err := os.ReadFile(filepath.Join("catalog", "source", "official.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &pinned); err != nil {
		t.Fatal(err)
	}
	products := map[string]officialProduct{}
	for _, product := range pinned.Products {
		products[product.Product+"@"+product.Version] = product
	}
	var listed struct {
		Exceptions []officialException `json:"exceptions"`
	}
	raw, err = os.ReadFile(filepath.Join("catalog", "source", "official-exceptions.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &listed); err != nil {
		t.Fatal(err)
	}
	exceptions := map[string]officialException{}
	for _, exception := range listed.Exceptions {
		exceptions[exception.Product+"@"+exception.Version+"#"+exception.Operation] = exception
	}
	return products, exceptions
}

func TestCatalogMatchesPinnedOfficialMetadata(t *testing.T) {
	products, exceptions := loadOfficial(t)
	usedExceptions := map[string]bool{}
	usedOfficial := map[string]bool{}
	for _, operation := range loadCatalogOperations(t) {
		call := operation.Call
		key := call.Product + "@" + call.Version + "#" + operation.Name
		if exception, ok := exceptions[key]; ok {
			usedExceptions[key] = true
			if strings.TrimSpace(exception.Reason) == "" || !officialEvidence.MatchString(exception.Evidence) {
				t.Errorf("%s: exception needs a reason and pinned Alibaba Cloud evidence", operation.ID)
			}
			continue
		}
		product, ok := products[call.Product+"@"+call.Version]
		if !ok || !strings.HasPrefix(product.SourceURI, "https://api.aliyun.com/meta/v1/products/") || len(product.SourceSHA256) != 64 {
			t.Errorf("%s: %s %s is not pinned to official metadata", operation.ID, call.Product, call.Version)
			continue
		}
		api, ok := product.APIs[operation.Name]
		if !ok {
			t.Errorf("%s: %s is absent from the official %s %s metadata", operation.ID, operation.Name, call.Product, call.Version)
			continue
		}
		usedOfficial[key] = true
		if !strings.EqualFold(call.Style, product.Style) {
			t.Errorf("%s: style %s, official %s", operation.ID, call.Style, product.Style)
		}
		if !slices.Contains(api.Methods, strings.ToLower(call.Method)) {
			t.Errorf("%s: method %s, official %v", operation.ID, call.Method, api.Methods)
		}
		// Template names may differ ({object} and {key}); the structure may not.
		if strings.EqualFold(call.Style, "ROA") && api.Path != "" &&
			pathTemplate.ReplaceAllString(strings.SplitN(call.Path, "?", 2)[0], "{}") != pathTemplate.ReplaceAllString(strings.SplitN(api.Path, "?", 2)[0], "{}") {
			t.Errorf("%s: path %s, official %s", operation.ID, call.Path, api.Path)
		}
		if call.IdempotencyParameter != "" && !officialParameterAccepts(api, call, call.IdempotencyParameter) {
			t.Errorf("%s: idempotency parameter %s is not an official parameter", operation.ID, call.IdempotencyParameter)
		}
		if api.Deprecated {
			t.Errorf("%s: official metadata marks %s deprecated", operation.ID, operation.Name)
		}
	}
	for key := range exceptions {
		if !usedExceptions[key] {
			t.Errorf("stale official-metadata exception %s", key)
		}
	}
	for _, product := range products {
		for name := range product.APIs {
			if !usedOfficial[product.Product+"@"+product.Version+"#"+name] {
				t.Errorf("stale pinned official operation %s %s %s", product.Product, product.Version, name)
			}
		}
	}
}

// officialParameterAccepts reports whether a request parameter is part of the
// official contract: a declared parameter, a field of an ROA body, an element
// of a flattened RPC list or object (Filter.1.Key), or an endpoint, host,
// header or path value the call metadata routes outside the API parameters.
func officialParameterAccepts(api officialAPI, call catalogCall, name string) bool {
	base := name
	if index := strings.Index(name, "."); index > 0 {
		base = name[:index]
	}
	for _, parameter := range api.Parameters {
		if parameter.Name == name {
			return true
		}
		if parameter.In == "body" && slices.Contains(parameter.Properties, name) {
			return true
		}
		if base != name && parameter.Name == base && (parameter.Type == "array" || parameter.Type == "object") {
			return true
		}
	}
	if slices.Contains(call.EndpointParameters, name) || slices.Contains(call.HostParameters, name) || slices.Contains(call.HeaderParameters, name) {
		return true
	}
	if strings.Contains(call.Path, "{"+name+"}") {
		return true
	}
	// RegionId selects the regional RPC endpoint and is accepted by the RPC
	// gateway even where a product does not list it.
	return name == "RegionId" && strings.EqualFold(call.Style, "RPC")
}

func TestSpecParametersAreOfficialParameters(t *testing.T) {
	products, exceptions := loadOfficial(t)
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
	checked := 0
	for _, path := range files {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var document any
		if err := yaml.Unmarshal(raw, &document); err != nil {
			t.Fatal(err)
		}
		var visit func(any)
		visit = func(value any) {
			switch typed := value.(type) {
			case map[string]any:
				reference, _ := typed["operation"].(string)
				parameters, _ := typed["parameters"].(map[string]any)
				if reference != "" && parameters != nil {
					operation, ok := resolve(reference)
					if !ok {
						t.Errorf("%s: operation %s is not in the catalog", path, reference)
					} else if _, excepted := exceptions[operation.Call.Product+"@"+operation.Call.Version+"#"+operation.Name]; !excepted {
						api := products[operation.Call.Product+"@"+operation.Call.Version].APIs[operation.Name]
						for name := range parameters {
							checked++
							if !officialParameterAccepts(api, operation.Call, name) {
								t.Errorf("%s: %s sends %s, which is not an official parameter", path, reference, name)
							}
						}
					}
				}
				for _, child := range typed {
					visit(child)
				}
			case []any:
				for _, child := range typed {
					visit(child)
				}
			}
		}
		visit(document)
	}
	if checked < 500 {
		t.Fatalf("checked only %d specification parameters", checked)
	}
}

func TestCatalogRegeneratesFromSource(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("catalog", "source", "openapi.json"))
	if err != nil {
		t.Fatal(err)
	}
	generated, err := catalog.ImportOfficial("openapi", "alicloud", "catalog/source/openapi.json", source)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := catalog.MarshalGenerated(generated)
	if err != nil {
		t.Fatal(err)
	}
	embedded, err := os.ReadFile(filepath.Join("catalog", "generated", "catalog.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(payload) != string(embedded) {
		t.Fatal("catalog/generated/catalog.json is stale; run go generate ./providers/alicloud")
	}
}
