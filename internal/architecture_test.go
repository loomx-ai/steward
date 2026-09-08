package internal_test

import (
	"bufio"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
)

var (
	architectureGenerationName = regexp.MustCompile(`(?i)(?:^|[_./-])(v[0-9]+|legacy|compat)(?:$|[_./-])`)
	architectureGenerationType = regexp.MustCompile(`(?i)v[0-9]+|legacy|compat`)
	versionedAPIRoute          = regexp.MustCompile(`/api/v[0-9]+(?:/|$)`)
	versionedTable             = regexp.MustCompile(`(?i)CREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?[a-z0-9_]*(?:^|_)v[0-9]+(?:_|\s|\()`)
)

func TestTerminalArchitecture(t *testing.T) {
	root := repositoryRoot(t)

	assertForbiddenDirectoriesAbsent(t, root)
	assertCoreAndApplicationBoundaries(t, root)
	assertNoArchitectureGenerationNames(t, root)
	assertForbiddenDependenciesAbsent(t, root)
	assertForbiddenSourcePatternsAbsent(t, root)
	assertTopologyCutover(t, root)
	assertTerminalProductContract(t, root)
}

func assertTerminalProductContract(t *testing.T, root string) {
	t.Helper()
	requirements := map[string][]string{
		"internal/transport/http/router.go":   {`router.Get("/topology"`, `router.Post("/cleanup"`, `router.Delete("/connections/{id}"`},
		"internal/transport/http/cleanup.go":  {`json:"selectors"`},
		"internal/transport/http/topology.go": {`FocusKey:`, `request.URL.Query().Get("focus_key")`, `optionalInteger(request, "limit")`},
		"internal/app/topology/service.go":    {`ListRegionsByConnection`, `CountActiveAssetsByScope`, `ListRelationshipsByAssetIDs`, `ListLifecycleBindingsByAssetIDs`},
		"internal/core/topology/model.go":     {`type Response struct`, `type VPCView struct`, `type ResourceGraphView struct`},
		"internal/core/topology/key.go":       {`func VPCFocusKey`, `func ParseFocusKey`},
		"internal/app/cleanup/selector.go":    {`topology.ParseFocusKey`, `topology.VPCGroupAssetIDs`},
		"web/src/api/client.ts":               {`/api/topology`, `selectors: CleanupSelector[]`},
		"web/src/main.tsx":                    {`<LocaleProvider>`},
		"web/src/routes.tsx":                  {`Navigate to="/panorama"`, `path="panorama"`, `path="settings"`},
	}
	for relative, values := range requirements {
		content, err := os.ReadFile(filepath.Join(root, relative))
		if err != nil {
			t.Errorf("read terminal product contract %s: %v", relative, err)
			continue
		}
		for _, value := range values {
			if !strings.Contains(string(content), value) {
				t.Errorf("terminal product contract %s is missing %q", relative, value)
			}
		}
	}
	for relative, forbidden := range map[string][]string{
		"internal/transport/http/connections.go":           {`credential_ref`},
		"internal/transport/http/cleanup.go":               {`json:"asset_ids"`},
		"internal/app/cleanup/selector.go":                 {`GroupKey(`, `ParseKey(`, `key.GroupKind`},
		"internal/app/topology/service.go":                 {`maxDepth`, `.ListRelationships(ctx`, `.ListLifecycleBindings(ctx`},
		"internal/core/topology/model.go":                  {`type Node struct`, `type Key struct`},
		"internal/provider/spec/model.go":                  {`TopologySpec`, `groupKind`, `groupIdentityFields`, `parentScope`},
		"internal/provider/spec/compiler.go":               {`normalizedTopologyField`, `GroupKind`, `GroupIdentityFields`, `ParentScope`},
		"internal/provider/spec/resource-kind.schema.json": {`"topology"`, `"groupKind"`, `"groupIdentityFields"`, `"parentScope"`},
		"web/src/api/client.ts":                            {`asset_ids:`, `/api/regions`, `/api/settings`},
		"web/src/routes.tsx":                               {`path="regions"`},
	} {
		content, err := os.ReadFile(filepath.Join(root, relative))
		if err != nil {
			t.Errorf("read terminal product contract %s: %v", relative, err)
			continue
		}
		for _, value := range forbidden {
			if strings.Contains(string(content), value) {
				t.Errorf("terminal product contract %s still contains %q", relative, value)
			}
		}
	}
	accountClosure := regexp.MustCompile(`(?i)\b(?:close|delete|cancel|unregister)(?:cloud)?account\b`)
	specs, err := filepath.Glob(filepath.Join(root, "providers", "*", "specs", "*.yaml"))
	if err != nil {
		t.Errorf("list provider specs: %v", err)
	}
	for _, path := range specs {
		content, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Errorf("read provider spec %s: %v", relativePath(root, path), readErr)
			continue
		}
		if accountClosure.Match(content) {
			t.Errorf("cloud account closure action is forbidden in %s", relativePath(root, path))
		}
		for _, forbidden := range []string{"topology:", "groupKind:", "groupIdentityFields:", "parentScope:"} {
			if strings.Contains(string(content), forbidden) {
				t.Errorf("generic topology grouping contract %q is forbidden in %s", forbidden, relativePath(root, path))
			}
		}
	}
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve architecture test location")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), ".."))
}

func assertForbiddenDirectoriesAbsent(t *testing.T, root string) {
	t.Helper()
	for _, relative := range []string{
		"internal/domain",
		"internal/store",
		"internal/scanner",
		"internal/governance",
		"internal/api",
		"internal/alicloud",
		"internal/joblog",
	} {
		if info, err := os.Stat(filepath.Join(root, relative)); err == nil && info.IsDir() {
			t.Errorf("forbidden pre-terminal package remains: %s", relative)
		}
	}
}

func assertCoreAndApplicationBoundaries(t *testing.T, root string) {
	t.Helper()
	cloudSDKPrefixes := []string{
		"cloud.google.com/go/",
		"github.com/Azure/azure-sdk-for-go/",
		"github.com/alibabacloud-go/",
		"github.com/aws/aws-sdk-go-",
		"google.golang.org/api/",
	}
	for _, relative := range []string{"internal/core", "internal/app"} {
		walkFiles(t, filepath.Join(root, relative), func(path string) {
			if filepath.Ext(path) != ".go" {
				return
			}
			parsed, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
			if err != nil {
				t.Errorf("parse %s: %v", path, err)
				return
			}
			for _, imported := range parsed.Imports {
				value, err := strconv.Unquote(imported.Path.Value)
				if err != nil {
					continue
				}
				if strings.Contains(value, "/providers/") || slices.ContainsFunc(cloudSDKPrefixes, func(prefix string) bool {
					return strings.HasPrefix(value, prefix)
				}) {
					t.Errorf("%s crosses the provider boundary with import %q", relativePath(root, path), value)
				}
			}
		})
	}
}

func assertNoArchitectureGenerationNames(t *testing.T, root string) {
	t.Helper()
	for _, relative := range []string{"cmd", "internal", "web/src", "migrations"} {
		walkFiles(t, filepath.Join(root, relative), func(path string) {
			if filepath.Ext(path) != ".go" || strings.Contains(filepath.ToSlash(path), "/providers/") || filepath.Base(path) == "architecture_test.go" {
				return
			}
			parsed, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
			if err != nil {
				t.Errorf("parse %s: %v", path, err)
				return
			}
			if architectureGenerationName.MatchString(parsed.Name.Name) {
				t.Errorf("architecture-generation package name in %s: %s", relativePath(root, path), parsed.Name.Name)
			}
			ast.Inspect(parsed, func(node ast.Node) bool {
				switch value := node.(type) {
				case *ast.TypeSpec:
					if architectureGenerationType.MatchString(value.Name.Name) {
						t.Errorf("architecture-generation type name in %s: %s", relativePath(root, path), value.Name.Name)
					}
				case *ast.BasicLit:
					if value.Kind != token.STRING {
						break
					}
					text, err := strconv.Unquote(value.Value)
					if err == nil && versionedAPIRoute.MatchString(text) {
						t.Errorf("versioned API route in %s: %s", relativePath(root, path), text)
					}
				}
				return true
			})
		})
	}

	for _, relative := range []string{"migrations"} {
		walkFiles(t, filepath.Join(root, relative), func(path string) {
			content, err := os.ReadFile(path)
			if err != nil {
				t.Errorf("read %s: %v", path, err)
				return
			}
			if versionedTable.Match(content) {
				t.Errorf("architecture-generation table name in %s", relativePath(root, path))
			}
		})
	}
}

func assertForbiddenDependenciesAbsent(t *testing.T, root string) {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	for _, dependency := range []string{
		"cloud.google.com/go/asset",
		"github.com/Azure/azure-sdk-for-go/",
		"gorm.io/datatypes",
		"gorm.io/driver/mysql",
	} {
		if strings.Contains(string(content), dependency) {
			t.Errorf("forbidden dependency remains: %s", dependency)
		}
	}
}

func assertForbiddenSourcePatternsAbsent(t *testing.T, root string) {
	t.Helper()
	patterns := map[string]*regexp.Regexp{
		"API inline credential secret": regexp.MustCompile(`(?i)(json:\"(?:access[_-]?key[_-]?secret|client[_-]?secret|private[_-]?key|password))|AccessKeySecret`),
		"automatic schema migration":   regexp.MustCompile(`\bAuto` + `Migrate\b`),
		"MySQL opener":                 regexp.MustCompile(`\bOpen` + `MySQL\b`),
		"old plan route":               regexp.MustCompile(`/api/` + `plans(?:/|\b)`),
		"old resource route":           regexp.MustCompile(`/api/` + `resources(?:/|\b)`),
		"raw VPC planning access":      regexp.MustCompile(`\.Raw\.VpcId\b`),
		"region scanner":               regexp.MustCompile(`\bScan` + `Region\b`),
		"resource type enum":           regexp.MustCompile(`\bResourceType(?:ECS|VPC|Disk)`),
		"native HTML select":           regexp.MustCompile(`<select(?:\s|>)`),
	}
	for _, relative := range []string{"cmd", "internal", "web/src", "web/test", "migrations"} {
		walkFiles(t, filepath.Join(root, relative), func(path string) {
			if filepath.Base(path) == "architecture_test.go" || strings.Contains(filepath.ToSlash(path), "/webui/dist/") {
				return
			}
			extension := filepath.Ext(path)
			if extension != ".go" && extension != ".ts" && extension != ".tsx" && extension != ".js" && extension != ".mjs" && extension != ".sql" {
				return
			}
			file, err := os.Open(path)
			if err != nil {
				t.Errorf("open %s: %v", path, err)
				return
			}
			defer file.Close()
			scanner := bufio.NewScanner(file)
			for line := 1; scanner.Scan(); line++ {
				text := scanner.Text()
				for name, pattern := range patterns {
					if pattern.MatchString(text) {
						t.Errorf("%s remains at %s:%d", name, relativePath(root, path), line)
					}
				}
			}
			if err := scanner.Err(); err != nil {
				t.Errorf("scan %s: %v", path, err)
			}
		})
	}
}

func TestTopologyCutoverGuardAllowsOrdinaryDepthIdentifiersAndComments(t *testing.T) {
	root := t.TempDir()
	queryDepth := "dep" + "th"
	writeTopologyGuardFixture(t, root, "internal/example.go", "package fixture\n\n"+
		"// "+queryDepth+" is an ordinary traversal concept, not an HTTP query.\n"+
		"func advance(currentDepth int) int {\n"+
		"\t"+queryDepth+" := currentDepth + 1\n"+
		"\treturn "+queryDepth+"\n"+
		"}\n")

	violations, err := topologyCutoverViolations(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) != 0 {
		t.Fatalf("ordinary identifier/comment produced violations: %v", violations)
	}
}

func TestTopologyCutoverGuardRejectsLegacyProtocolShapes(t *testing.T) {
	queryDepth := "dep" + "th"
	cases := map[string]string{
		"double quoted literal": "const value = \"" + queryDepth + "\";\n",
		"single quoted literal": "const value = '" + queryDepth + "';\n",
		"template literal":      "const value = `" + queryDepth + "`;\n",
		"JSON tag":              "type Query struct { Depth int `json:\"" + queryDepth + "\"` }\n",
		"request field":         "interface Query { " + queryDepth + "?: number }\n",
		"query field access":    "if (query." + queryDepth + ") reject();\n",
		"URL parameter access":  "params.get(\"" + queryDepth + "\");\n",
	}
	for name, source := range cases {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			writeTopologyGuardFixture(t, root, "web/src/example.ts", source)
			violations, err := topologyCutoverViolations(root)
			if err != nil {
				t.Fatal(err)
			}
			if len(violations) == 0 {
				t.Fatalf("legacy protocol shape was accepted: %q", source)
			}
		})
	}
}

func TestTopologyCutoverGuardUsesExactNegativeTestAllowlist(t *testing.T) {
	root := t.TempDir()
	parentQuery := "parent_" + "key"
	queryDepth := "dep" + "th"
	nodeLimit := "node_" + "limit"
	writeTopologyGuardFixture(t, root, "internal/transport/http/topology.go",
		"for _, legacy := range []string{\""+parentQuery+"\", \""+queryDepth+"\", \""+nodeLimit+"\"} {\n")
	writeTopologyGuardFixture(t, root, "internal/transport/http/router_test.go",
		"for _, parameter := range []string{\""+parentQuery+"=x\", \""+queryDepth+"=1\", \""+nodeLimit+"=50\"} {\n")
	writeTopologyGuardFixture(t, root, "web/src/api/client.test.ts",
		"expect(path).not.toContain(\""+parentQuery+"\");\n"+
			"expect(path).not.toContain(\""+queryDepth+"\");\n"+
			"expect(path).not.toContain(\""+nodeLimit+"\");\n")
	writeTopologyGuardFixture(t, root, "internal/core/topology/key_test.go",
		"if strings.Contains(string(encoded), `\""+parentQuery+"\"`) {\n")

	violations, err := topologyCutoverViolations(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) != 0 {
		t.Fatalf("exact rejection/negative assertions produced violations: %v", violations)
	}

	writeTopologyGuardFixture(t, root, "internal/core/topology/key_test.go",
		"const legacyField = \""+parentQuery+"\"\n")
	violations, err = topologyCutoverViolations(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) == 0 {
		t.Fatal("dynamic legacy field in allowlisted test file was accepted")
	}
}

func writeTopologyGuardFixture(t *testing.T, root, relative, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func assertTopologyCutover(t *testing.T, root string) {
	t.Helper()
	violations, err := topologyCutoverViolations(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, violation := range violations {
		t.Error(violation)
	}
}

func topologyCutoverViolations(root string) ([]string, error) {
	obsolete := map[string]*regexp.Regexp{
		"old parent query field":         regexp.MustCompile(`Parent` + `Key`),
		"old node-limit query field":     regexp.MustCompile(`Node` + `Limit`),
		"old recursion validation error": regexp.MustCompile(`topology\.` + `depth_invalid`),
		"generic topology node":          regexp.MustCompile(`\bTopology` + `Node\b`),
		"generic topology group rule":    regexp.MustCompile(`\bGroup` + `Rule\b`),
		"old flow conversion":            regexp.MustCompile(`toFlow` + `Elements`),
		"old connection node selector":   regexp.MustCompile(`topology-node-` + `connection`),
		"old scope node selector":        regexp.MustCompile(`topology-node-` + `scope`),
	}
	legacyQueryNames := []string{"parent_" + "key", "dep" + "th", "node_" + "limit"}
	legacyPatterns := make(map[string][]*regexp.Regexp, len(legacyQueryNames))
	for _, queryName := range legacyQueryNames {
		quoted := regexp.QuoteMeta(queryName)
		legacyPatterns[queryName] = []*regexp.Regexp{
			regexp.MustCompile("[\"'`]" + quoted + "[\"'`]"),
			regexp.MustCompile(`\b(?:query|params|searchParams|url|request)\s*(?:\?\.|\.)\s*` + quoted + `\b`),
		}
	}

	var violations []string
	for _, relative := range []string{"cmd", "internal", "providers", "web/src"} {
		scanRoot := filepath.Join(root, relative)
		if _, err := os.Stat(scanRoot); os.IsNotExist(err) {
			continue
		} else if err != nil {
			return nil, err
		}
		err := filepath.WalkDir(scanRoot, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				if entry.Name() == "node_modules" || entry.Name() == "dist" {
					return filepath.SkipDir
				}
				return nil
			}
			if filepath.Base(path) == "architecture_test.go" {
				return nil
			}
			file, err := os.Open(path)
			if err != nil {
				return err
			}
			defer file.Close()
			scanner := bufio.NewScanner(file)
			for line := 1; scanner.Scan(); line++ {
				text := scanner.Text()
				for name, pattern := range obsolete {
					if pattern.MatchString(text) {
						violations = append(violations, fmt.Sprintf("%s remains at %s:%d", name, relativePath(root, path), line))
					}
				}
				if isSourceCommentLine(text) {
					continue
				}
				for _, queryName := range legacyQueryNames {
					if matchesLegacyQueryUsage(text, queryName, legacyPatterns[queryName]) &&
						!allowedLegacyQueryRejection(root, path, text, queryName) {
						violations = append(violations, fmt.Sprintf(
							"legacy topology query name %q remains outside its explicit rejection boundary at %s:%d",
							queryName, relativePath(root, path), line,
						))
					}
				}
			}
			if err := scanner.Err(); err != nil {
				return err
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return violations, nil
}

func isSourceCommentLine(line string) bool {
	trimmed := strings.TrimSpace(line)
	return strings.HasPrefix(trimmed, "//") ||
		strings.HasPrefix(trimmed, "/*") ||
		strings.HasPrefix(trimmed, "*") ||
		strings.HasPrefix(trimmed, "#")
}

func matchesAny(text string, patterns []*regexp.Regexp) bool {
	return slices.ContainsFunc(patterns, func(pattern *regexp.Regexp) bool {
		return pattern.MatchString(text)
	})
}

func matchesLegacyQueryUsage(text, queryName string, patterns []*regexp.Regexp) bool {
	if matchesAny(text, patterns) {
		return true
	}
	fieldPattern := regexp.MustCompile(`\b` + regexp.QuoteMeta(queryName) + `\s*(?:\?|!)?\s*:`)
	for _, location := range fieldPattern.FindAllStringIndex(text, -1) {
		if location[1] == len(text) || text[location[1]] != '=' {
			return true
		}
	}
	return false
}

func allowedLegacyQueryRejection(root, path, line, queryName string) bool {
	parentQuery := "parent_" + "key"
	queryDepth := "dep" + "th"
	nodeLimit := "node_" + "limit"
	trimmed := strings.TrimSpace(line)
	switch relativePath(root, path) {
	case "internal/transport/http/topology.go":
		return trimmed == `for _, legacy := range []string{"`+parentQuery+`", "`+queryDepth+`", "`+nodeLimit+`"} {`
	case "internal/transport/http/router_test.go":
		return trimmed == `for _, parameter := range []string{"`+parentQuery+`=x", "`+queryDepth+`=1", "`+nodeLimit+`=50"} {`
	case "web/src/api/client.test.ts":
		return trimmed == `expect(path).not.toContain("`+queryName+`");`
	case "internal/core/topology/key_test.go":
		return queryName == parentQuery &&
			trimmed == "if strings.Contains(string(encoded), `\""+parentQuery+"\"`) {"
	default:
		return false
	}
}

func walkFiles(t *testing.T, root string, visit func(string)) {
	t.Helper()
	if _, err := os.Stat(root); os.IsNotExist(err) {
		return
	}
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if entry.Name() == "node_modules" || entry.Name() == "dist" {
				return filepath.SkipDir
			}
			return nil
		}
		visit(path)
		return nil
	}); err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
}

func relativePath(root, path string) string {
	relative, err := filepath.Rel(root, path)
	if err != nil {
		return path
	}
	return filepath.ToSlash(relative)
}
