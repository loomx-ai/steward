package hooks

import (
	"sort"
	"strings"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
)

const kmsKeyNativeType = "ACS::KMS::Key"

// kmsReferenceFields are the canonical names under which Alibaba Cloud product
// responses report the customer master key that encrypts a resource, taken from
// the official API metadata: KMSKeyId (ECS disks, snapshots and automatic
// snapshot policies, NAS), KmsKeyId (MNS, Kafka, HBR), EncryptionKey
// (MongoDB, HBase, RDS, PolarDB, Redis), EncryptKeyId (ENS) and KMSMasterKeyID
// (OSS server-side encryption). TDEEncryptionKey is where inventory records the
// transparent data encryption key of RDS, PolarDB and Redis.
var kmsReferenceFields = map[string]bool{
	"kmskeyid":         true,
	"encryptionkey":    true,
	"encryptkeyid":     true,
	"kmsmasterkeyid":   true,
	"tdeencryptionkey": true,
}

// kmsKeyIdentifier accepts a key ID or a KMS key ARN
// (acs:kms:<region>:<account>:key/<key-id>) and returns the key ID.
func kmsKeyIdentifier(value string) string {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "acs:kms:") {
		index := strings.LastIndex(value, ":key/")
		if index < 0 {
			return ""
		}
		return value[index+len(":key/"):]
	}
	if strings.ContainsAny(value, ":/ ") {
		return ""
	}
	return value
}

func collectKMSReferences(value any, result map[string]struct{}) {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if kmsReferenceFields[canonicalHookKey(key)] {
				for _, item := range normalizedStrings(child) {
					if id := kmsKeyIdentifier(item); id != "" {
						result[id] = struct{}{}
					}
				}
				continue
			}
			collectKMSReferences(child, result)
		}
	case []any:
		for _, child := range typed {
			collectKMSReferences(child, result)
		}
	}
}

// kmsKeyReferences returns the scanned key IDs a resource's configuration or
// reviewed normalized fields name as its encryption key. Deleting such a key
// makes the resource's data unreadable, so the resource uses the key.
func kmsKeyReferences(value asset.Asset) []string {
	if value.Identity.NativeType == kmsKeyNativeType {
		return nil
	}
	found := map[string]struct{}{}
	collectKMSReferences(value.Normalized, found)
	result := make([]string, 0, len(found))
	for id := range found {
		result = append(result, id)
	}
	sort.Strings(result)
	return result
}

func contributeKMSReferences(
	source asset.Asset,
	ordered []asset.Asset,
	seen map[configurationRelationshipKey]struct{},
) []graph.Relationship {
	var result []graph.Relationship
	for _, keyID := range kmsKeyReferences(source) {
		// Key IDs are unique within an account, so a key in another region
		// resolves when exactly one scanned key carries that ID.
		target, found := resolveConfigurationTarget(source, []string{kmsKeyNativeType}, keyID, ordered, true)
		if !found || target.ID == source.ID {
			continue
		}
		key := configurationRelationshipKey{source: source.ID, target: target.ID, kind: graph.RelationshipUses}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, graph.Relationship{
			SourceAssetID: source.ID,
			TargetAssetID: target.ID,
			Type:          graph.RelationshipUses,
			Source:        configurationTopologyEvidence,
			Confidence:    1,
			Evidence: map[string]any{
				"source":           configurationTopologyEvidence,
				"relationship":     "kms_key",
				"target_native_id": keyID,
			},
		})
	}
	return result
}
