package spec

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/loomx-ai/steward/internal/provider/catalog"
)

func LoadBundle(directory string, providerCatalog catalog.Catalog, hooks HookRegistry) (Bundle, error) {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return Bundle{}, fmt.Errorf("read spec directory: %w", err)
	}
	paths := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		extension := strings.ToLower(filepath.Ext(entry.Name()))
		if extension == ".yaml" || extension == ".yml" {
			paths = append(paths, filepath.Join(directory, entry.Name()))
		}
	}
	sort.Strings(paths)
	sources := make([][]byte, 0, len(paths))
	for _, path := range paths {
		source, err := os.ReadFile(path)
		if err != nil {
			return Bundle{}, fmt.Errorf("read spec %s: %w", path, err)
		}
		sources = append(sources, source)
	}
	return CompileBundle(sources, providerCatalog, hooks)
}
