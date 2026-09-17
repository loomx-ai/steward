package aws

import (
	"encoding/json"
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

// A key policy that grants key administration to exactly one IAM user or role,
// without delegating to the account root, loses all administrators when that
// principal is deleted; AWS Support must then recover the key. The key uses
// that principal, so the principal cannot be deleted while the key is live.
func contributeKMSPolicyAdministrators(result *governance.Contribution, assets []asset.Asset) {
	principals := map[string][]asset.Asset{}
	for _, value := range assets {
		if value.Identity.Provider != asset.ProviderAWS || value.ClosedAt != nil {
			continue
		}
		if value.Identity.NativeType == "AWS::IAM::Role" || value.Identity.NativeType == "AWS::IAM::User" {
			key := string(value.Identity.ConnectionID) + "\x00" + value.Identity.NativeType + "\x00" + value.Identity.NativeID
			principals[key] = append(principals[key], value)
		}
	}
	for _, key := range assets {
		if key.Identity.Provider != asset.ProviderAWS || key.ClosedAt != nil || key.Identity.NativeType != "AWS::KMS::Key" {
			continue
		}
		admins, delegated, ok := kmsPolicyAdministrators(key.Normalized["KeyPolicy"])
		if !ok || delegated || len(admins) == 0 {
			continue
		}
		// Every administrator must resolve to a scanned principal: an unscanned
		// or external administrator may still manage the key.
		var targets []asset.Asset
		for _, admin := range admins {
			match := kmsPrincipalARN.FindStringSubmatch(admin)
			if match == nil {
				targets = nil
				break
			}
			nativeType := map[string]string{"role": "AWS::IAM::Role", "user": "AWS::IAM::User"}[match[1]]
			name := match[2][strings.LastIndex(match[2], "/")+1:]
			candidates := principals[string(key.Identity.ConnectionID)+"\x00"+nativeType+"\x00"+name]
			if len(candidates) != 1 {
				targets = nil
				break
			}
			targets = append(targets, candidates[0])
		}
		for _, target := range targets {
			evidence := map[string]any{"source": "KeyPolicy", "key_administrator": target.Identity.NativeID}
			if len(targets) == 1 {
				evidence["sole_key_administrator"] = admins[0]
			} else {
				// The planner blocks only when every administrator is deleted.
				evidence[graph.RelationshipEvidenceAlternativeGroup] = "kms-key-administrators:" + string(key.ID)
			}
			result.Relationships = append(result.Relationships, graph.Relationship{
				SourceAssetID: key.ID, TargetAssetID: target.ID, Type: graph.RelationshipUses, Source: kmsReferenceSource, Confidence: 1, Evidence: evidence,
			})
		}
	}
}

var kmsPrincipalARN = regexp.MustCompile(`^arn:aws(?:-[a-z]+)*:iam::[0-9]{12}:(role|user)/(.+)$`)
var kmsAccountRoot = regexp.MustCompile(`^(?:arn:aws(?:-[a-z]+)*:iam::)?[0-9]{12}(?::root)?$`)

// kmsPolicyAdministrators returns the principals of unconditional Allow
// statements that can change the key policy. delegated is true when the
// account root or any principal can, which lets IAM policies restore access.
// A policy that cannot be read as a document reports ok false.
func kmsPolicyAdministrators(value any) ([]string, bool, bool) {
	document, ok := value.(map[string]any)
	if text, isText := value.(string); isText {
		ok = json.Unmarshal([]byte(text), &document) == nil
	}
	if !ok || document == nil {
		return nil, false, false
	}
	statements := anySlice(document["Statement"])
	if statement, single := document["Statement"].(map[string]any); single {
		statements = []any{statement}
	}
	admins := map[string]bool{}
	delegated := false
	for _, raw := range statements {
		statement, _ := raw.(map[string]any)
		if statement == nil || statement["Effect"] != "Allow" || statement["Condition"] != nil || statement["NotPrincipal"] != nil || statement["NotAction"] != nil {
			continue
		}
		administers := false
		for _, action := range policyStrings(statement["Action"]) {
			switch strings.ToLower(action) {
			case "*", "kms:*", "kms:putkeypolicy":
				administers = true
			}
		}
		if !administers {
			continue
		}
		principal := statement["Principal"]
		if principal == "*" {
			delegated = true
			continue
		}
		for _, arn := range policyStrings(object(principal)["AWS"]) {
			switch {
			case arn == "*" || kmsAccountRoot.MatchString(arn):
				delegated = true
			default:
				admins[arn] = true
			}
		}
	}
	result := make([]string, 0, len(admins))
	for arn := range admins {
		result = append(result, arn)
	}
	sort.Strings(result)
	return result, delegated, true
}

func policyStrings(value any) []string {
	if text, ok := value.(string); ok {
		return []string{text}
	}
	return stringSliceValue(value)
}

func object(value any) map[string]any {
	result, _ := value.(map[string]any)
	return result
}
