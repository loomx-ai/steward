package alicloud

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"regexp"
	"sort"
	"strings"

	"github.com/loomx-ai/steward/internal/core/asset"
	providercatalog "github.com/loomx-ai/steward/internal/provider/catalog"
	"github.com/loomx-ai/steward/internal/provider/spec"
)

const resourceCatalogPath = "resourcecenter/resources.json"

type resourceLevel string

const (
	resourceLevelInstance    resourceLevel = "instance"
	resourceLevelSubresource resourceLevel = "subresource"
)

var (
	resourceNativeTypePattern = regexp.MustCompile(`^ACS::[A-Za-z0-9_-]+::[A-Za-z0-9_.-]+$`)
	resourceIconPathPattern   = regexp.MustCompile(`^/icons/alicloud/[a-z0-9-]+\.(?:svg|png)$`)
)

type resourceCatalogEntry struct {
	NativeType       string        `json:"native_type"`
	Level            resourceLevel `json:"level"`
	Icon             string        `json:"icon,omitempty"`
	IconOrigin       string        `json:"icon_origin,omitempty"`
	IconSource       string        `json:"icon_source,omitempty"`
	ParentNativeType string        `json:"parent_native_type,omitempty"`
	Reason           string        `json:"reason,omitempty"`
}

type resourceCatalogDocument struct {
	Revision  string                 `json:"revision"`
	SourceURL string                 `json:"source_url"`
	Resources []resourceCatalogEntry `json:"resources"`
}

type resourceCatalog struct {
	revision      string
	sourceURL     string
	entries       map[string]resourceCatalogEntry
	instanceTypes []string
}

func loadResourceCatalog(source fs.FS) (resourceCatalog, error) {
	if source == nil {
		return resourceCatalog{}, fmt.Errorf("Alibaba Cloud resource catalog filesystem is required")
	}
	file, err := source.Open(resourceCatalogPath)
	if err != nil {
		return resourceCatalog{}, fmt.Errorf("open Alibaba Cloud resource catalog: %w", err)
	}
	defer file.Close()

	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	var document resourceCatalogDocument
	if err := decoder.Decode(&document); err != nil {
		return resourceCatalog{}, fmt.Errorf("decode Alibaba Cloud resource catalog: %w", err)
	}
	if err := ensureResourceCatalogEOF(decoder); err != nil {
		return resourceCatalog{}, err
	}
	document.Revision = strings.TrimSpace(document.Revision)
	document.SourceURL = strings.TrimSpace(document.SourceURL)
	if document.Revision == "" {
		return resourceCatalog{}, fmt.Errorf("Alibaba Cloud resource catalog revision is required")
	}
	if document.SourceURL == "" {
		return resourceCatalog{}, fmt.Errorf("Alibaba Cloud resource catalog source URL is required")
	}

	entries := make(map[string]resourceCatalogEntry, len(document.Resources))
	instanceTypes := make([]string, 0, len(document.Resources))
	iconOwners := make(map[string]string)
	for index, raw := range document.Resources {
		entry := normalizeResourceCatalogEntry(raw)
		if !resourceNativeTypePattern.MatchString(entry.NativeType) {
			return resourceCatalog{}, fmt.Errorf("Alibaba Cloud resource catalog entry %d has invalid native type %q", index, entry.NativeType)
		}
		if _, exists := entries[entry.NativeType]; exists {
			return resourceCatalog{}, fmt.Errorf("Alibaba Cloud resource catalog has duplicate native type %q", entry.NativeType)
		}
		switch entry.Level {
		case resourceLevelInstance:
			if _, exists := catalogDisplayNames(entry.NativeType); !exists {
				return resourceCatalog{}, fmt.Errorf("Alibaba Cloud instance resource %q requires localized display names", entry.NativeType)
			}
			if entry.Icon == "" {
				return resourceCatalog{}, fmt.Errorf("Alibaba Cloud instance resource %q requires an icon", entry.NativeType)
			}
			if !resourceIconPathPattern.MatchString(entry.Icon) {
				return resourceCatalog{}, fmt.Errorf("Alibaba Cloud instance resource %q requires a local Alibaba Cloud icon path", entry.NativeType)
			}
			if owner, exists := iconOwners[entry.Icon]; exists {
				return resourceCatalog{}, fmt.Errorf("Alibaba Cloud resource catalog has duplicate icon path %q for %q and %q", entry.Icon, owner, entry.NativeType)
			}
			if entry.IconOrigin == "" || entry.IconSource == "" {
				return resourceCatalog{}, fmt.Errorf("Alibaba Cloud instance resource %q requires icon origin and source", entry.NativeType)
			}
			switch entry.IconOrigin {
			case "official":
				if !strings.HasSuffix(entry.Icon, ".svg") {
					return resourceCatalog{}, fmt.Errorf("Alibaba Cloud instance resource %q requires an official SVG icon", entry.NativeType)
				}
			case "generated":
				if !strings.HasSuffix(entry.Icon, ".png") {
					return resourceCatalog{}, fmt.Errorf("Alibaba Cloud instance resource %q requires a generated PNG icon", entry.NativeType)
				}
			default:
				return resourceCatalog{}, fmt.Errorf("Alibaba Cloud instance resource %q has invalid icon origin %q", entry.NativeType, entry.IconOrigin)
			}
			iconOwners[entry.Icon] = entry.NativeType
			instanceTypes = append(instanceTypes, entry.NativeType)
		case resourceLevelSubresource:
			if entry.ParentNativeType == "" && entry.Reason == "" {
				return resourceCatalog{}, fmt.Errorf("Alibaba Cloud subresource %q requires a parent native type or reason", entry.NativeType)
			}
			if entry.Icon != "" || entry.IconOrigin != "" || entry.IconSource != "" {
				return resourceCatalog{}, fmt.Errorf("Alibaba Cloud subresource %q must not define icon metadata", entry.NativeType)
			}
		default:
			return resourceCatalog{}, fmt.Errorf("Alibaba Cloud resource %q has invalid level %q", entry.NativeType, entry.Level)
		}
		entries[entry.NativeType] = entry
	}
	if len(entries) != 170 {
		return resourceCatalog{}, fmt.Errorf("Alibaba Cloud resource catalog has %d resource types, want 170", len(entries))
	}
	sort.Strings(instanceTypes)
	return resourceCatalog{
		revision:      document.Revision,
		sourceURL:     document.SourceURL,
		entries:       entries,
		instanceTypes: instanceTypes,
	}, nil
}

func ensureResourceCatalogEOF(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); err == io.EOF {
		return nil
	} else if err != nil {
		return fmt.Errorf("decode Alibaba Cloud resource catalog trailing content: %w", err)
	}
	return fmt.Errorf("Alibaba Cloud resource catalog contains multiple JSON documents")
}

func normalizeResourceCatalogEntry(entry resourceCatalogEntry) resourceCatalogEntry {
	entry.NativeType = strings.TrimSpace(entry.NativeType)
	entry.Level = resourceLevel(strings.TrimSpace(string(entry.Level)))
	entry.Icon = strings.TrimSpace(entry.Icon)
	entry.IconOrigin = strings.TrimSpace(entry.IconOrigin)
	entry.IconSource = strings.TrimSpace(entry.IconSource)
	entry.ParentNativeType = strings.TrimSpace(entry.ParentNativeType)
	entry.Reason = strings.TrimSpace(entry.Reason)
	return entry
}

func (c resourceCatalog) Len() int {
	return len(c.entries)
}

func (c resourceCatalog) IsInstance(nativeType string) bool {
	entry, exists := c.entries[strings.TrimSpace(nativeType)]
	return exists && entry.Level == resourceLevelInstance
}

func (c resourceCatalog) InstanceNativeTypes() []string {
	return append([]string(nil), c.instanceTypes...)
}

func (c resourceCatalog) Entry(nativeType string) (resourceCatalogEntry, bool) {
	entry, exists := c.entries[strings.TrimSpace(nativeType)]
	return entry, exists
}

func (c resourceCatalog) Revision() string {
	return c.revision
}

func (c resourceCatalog) ResourceKinds(
	bundle spec.Bundle,
	apiCatalog providercatalog.Catalog,
) ([]asset.ResourceKind, string) {
	revisionBytes := sha256.Sum256([]byte(bundle.Revision + "\x00" + c.revision))
	revision := fmt.Sprintf("%x", revisionBytes)
	compiledByNativeType := make(map[string]asset.ResourceKind, len(bundle.Specs))
	for _, compiled := range bundle.Specs {
		compiledByNativeType[compiled.ResourceKind.NativeType] = compiled.ResourceKind
	}

	nativeTypes := append([]string(nil), c.instanceTypes...)
	for nativeType := range compiledByNativeType {
		entry, exists := c.entries[nativeType]
		if !exists || entry.Level == resourceLevelSubresource {
			nativeTypes = append(nativeTypes, nativeType)
		}
	}
	sort.Strings(nativeTypes)

	result := make([]asset.ResourceKind, 0, len(nativeTypes))
	for _, nativeType := range nativeTypes {
		entry := c.entries[nativeType]
		kind, exists := compiledByNativeType[nativeType]
		if !exists {
			var err error
			kind, err = apiCatalog.ResourceKind(nativeType, revision, nil)
			if err != nil {
				kind = asset.ResourceKind{
					ID:           asset.ResourceKindID(string(asset.ProviderAliCloud) + ":" + nativeType),
					Provider:     asset.ProviderAliCloud,
					NativeType:   nativeType,
					DisplayName:  nativeType,
					ScopeKinds:   []asset.ScopeKind{asset.ScopeRegion},
					Capabilities: asset.CapabilitySet{asset.CapabilityIndexed},
				}
			}
		}
		displayNames := make(map[string]string, len(kind.DisplayNames)+2)
		for locale, displayName := range kind.DisplayNames {
			if value := strings.TrimSpace(displayName); value != "" {
				displayNames[locale] = value
			}
		}
		catalogNames, _ := catalogDisplayNames(nativeType)
		for locale, displayName := range catalogNames {
			displayNames[locale] = displayName
		}
		kind.DisplayNames = displayNames
		kind.DisplayName = displayNames["en-US"]
		fieldDisplayNames := cloneCommonResourcePropertyDisplayNames()
		for field, localized := range kind.FieldDisplayNames {
			if fieldDisplayNames[field] == nil {
				fieldDisplayNames[field] = map[string]string{}
			}
			for locale, displayName := range localized {
				if value := strings.TrimSpace(displayName); value != "" {
					fieldDisplayNames[field][locale] = value
				}
			}
		}
		kind.FieldDisplayNames = fieldDisplayNames
		if entry.Icon != "" {
			kind.Icon = entry.Icon
		} else if entry.ParentNativeType != "" {
			kind.Icon = c.entries[entry.ParentNativeType].Icon
		}
		kind.BundleRevision = revision
		result = append(result, kind)
	}
	sort.Slice(result, func(left, right int) bool {
		return result[left].ID < result[right].ID
	})
	return result, revision
}
