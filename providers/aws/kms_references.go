package aws

import (
	"regexp"
	"sort"
	"strings"

	"github.com/loomx-ai/steward/internal/app/governance"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
)

const kmsReferenceSource = "aws:kms_reference"

// Encrypted resources name their customer managed key by key ID, key ARN,
// alias name or alias ARN, at any depth of their model. Deleting such a key
// makes the resource's data unreadable, so each reference to a scanned key
// becomes a uses edge and the key is deleted only after its dependents.
var kmsKeyARN = regexp.MustCompile(`^arn:aws(?:-[a-z]+)*:kms:([a-z0-9-]+):[0-9]{12}:(key|alias)/(.+)$`)

func kmsReferenceProperty(name string) bool {
	lower := strings.ToLower(name)
	if !strings.Contains(lower, "kms") {
		return false
	}
	for _, suffix := range []string{"keyid", "keyarn", "keyidentifier", "masterkeyid", "encryptionkey"} {
		if strings.HasSuffix(lower, suffix) {
			return true
		}
	}
	return false
}

type kmsReference struct {
	path, value string
}

func collectKMSReferences(value any, path string, result *[]kmsReference) {
	switch typed := value.(type) {
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			child := typed[key]
			next := key
			if path != "" {
				next = path + "." + key
			}
			if kmsReferenceProperty(key) {
				for _, text := range stringSliceValue(child) {
					if text = strings.TrimSpace(text); text != "" {
						*result = append(*result, kmsReference{path: next, value: text})
					}
				}
				if text, ok := child.(string); ok && strings.TrimSpace(text) != "" {
					*result = append(*result, kmsReference{path: next, value: strings.TrimSpace(text)})
				}
				continue
			}
			collectKMSReferences(child, next, result)
		}
	case []any:
		for _, child := range typed {
			collectKMSReferences(child, path, result)
		}
	}
}

type kmsIndex struct {
	keys    map[string][]asset.Asset
	aliases map[string][]asset.Asset
}

func kmsLookupKey(value asset.Asset, region, name string) string {
	return strings.Join([]string{string(value.Identity.ConnectionID), value.Identity.Partition, region, name}, "\x00")
}

func indexKMS(assets []asset.Asset) kmsIndex {
	index := kmsIndex{keys: map[string][]asset.Asset{}, aliases: map[string][]asset.Asset{}}
	for _, value := range assets {
		if value.Identity.Provider != asset.ProviderAWS || value.ClosedAt != nil {
			continue
		}
		switch value.Identity.NativeType {
		case "AWS::KMS::Key":
			key := kmsLookupKey(value, value.Location, value.Identity.NativeID)
			index.keys[key] = append(index.keys[key], value)
		case "AWS::KMS::Alias":
			name := strings.TrimSpace(stringValue(value.Normalized["AliasName"]))
			if name == "" {
				name = value.Identity.NativeID
			}
			key := kmsLookupKey(value, value.Location, name)
			index.aliases[key] = append(index.aliases[key], value)
		}
	}
	return index
}

// resolve returns the scanned key named by a reference in the source's
// connection. AWS managed aliases and keys outside the inventory resolve to
// nothing; an ambiguous alias or key never produces an edge.
func (i kmsIndex) resolve(source asset.Asset, reference string) (asset.Asset, bool) {
	region, kind, name := source.Location, "", reference
	if match := kmsKeyARN.FindStringSubmatch(reference); match != nil {
		region, kind, name = match[1], match[2], match[3]
		if kind == "alias" {
			name = "alias/" + name
		}
	} else if strings.HasPrefix(reference, "alias/") {
		kind = "alias"
	} else if strings.HasPrefix(reference, "arn:") {
		return asset.Asset{}, false
	}
	if kind == "alias" {
		if strings.HasPrefix(name, "alias/aws/") {
			return asset.Asset{}, false
		}
		aliases := i.aliases[kmsLookupKey(source, region, name)]
		if len(aliases) != 1 {
			return asset.Asset{}, false
		}
		target := strings.TrimSpace(stringValue(aliases[0].Normalized["TargetKeyId"]))
		if match := kmsKeyARN.FindStringSubmatch(target); match != nil && match[2] == "key" {
			region, target = match[1], match[3]
		}
		name = target
	}
	keys := i.keys[kmsLookupKey(source, region, name)]
	if len(keys) != 1 {
		return asset.Asset{}, false
	}
	return keys[0], true
}

func contributeKMSReferences(result *governance.Contribution, assets []asset.Asset) {
	index := indexKMS(assets)
	for _, source := range assets {
		if source.Identity.Provider != asset.ProviderAWS || source.ClosedAt != nil {
			continue
		}
		switch source.Identity.NativeType {
		case "AWS::KMS::Key", "AWS::KMS::Alias", "AWS::KMS::ReplicaKey":
			continue
		}
		var references []kmsReference
		collectKMSReferences(source.Normalized, "", &references)
		seen := map[asset.AssetID]bool{}
		for _, reference := range references {
			key, ok := index.resolve(source, reference.value)
			if !ok || key.ID == source.ID || seen[key.ID] {
				continue
			}
			seen[key.ID] = true
			result.Relationships = append(result.Relationships, graph.Relationship{
				SourceAssetID: source.ID, TargetAssetID: key.ID, Type: graph.RelationshipUses, Source: kmsReferenceSource, Confidence: 1,
				Evidence: map[string]any{"source": reference.path, "kms_reference": reference.value},
			})
		}
	}
}
