package aws

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/spec"
)

const (
	cloudControlListOperation   = "com.amazonaws.cloudcontrol#ListResources"
	cloudControlGetOperation    = "com.amazonaws.cloudcontrol#GetResource"
	cloudControlUpdateOperation = "com.amazonaws.cloudcontrol#UpdateResource"
	resourceModelParameter      = "ResourceModel."
	// Parent sets are listed completely before child pages are requested. The
	// bound keeps a misbehaving paginator from looping forever.
	cloudControlMaxParentPages = 1000
)

// globalEndpointRegions records where each global AWS service is served.
// CloudFront, IAM, Route 53, Organizations and Shield Advanced use us-east-1;
// Global Accelerator and Network Manager are served from us-west-2.
var globalEndpointRegions = map[string]string{
	"AWS::GlobalAccelerator::": "us-west-2",
	"AWS::NetworkManager::":    "us-west-2",
}

// CloudControlListPlan is the spec-declared ListResources request for one kind.
// Model maps resource model properties to spec expressions or constants.
type CloudControlListPlan struct {
	TypeName     string
	Model        map[string]any
	ParentSource string
	ParentType   string
}

// cloudControlVariant is one concrete ListResources resource model, produced by
// a parent resource or by a multi-valued scope expression.
type cloudControlVariant struct {
	Key   string
	Model string
}

type cloudControlCursor struct {
	Variant     int    `json:"v"`
	Fingerprint string `json:"f"`
	Token       string `json:"t,omitempty"`
}

type cloudControlParent struct {
	Identifier string
	Properties map[string]any
}

func cloudControlListPlanFromSpec(definition spec.ResourceKindSpec) (CloudControlListPlan, error) {
	plan := CloudControlListPlan{TypeName: definition.Metadata.NativeType, Model: map[string]any{}}
	list := definition.Discovery.List
	if list == nil {
		return plan, nil
	}
	if list.Operation != cloudControlListOperation {
		return CloudControlListPlan{}, fmt.Errorf("AWS kind %s list operation %q is not Cloud Control ListResources", plan.TypeName, list.Operation)
	}
	for name, value := range list.Parameters {
		switch {
		case name == "TypeName":
			if value != plan.TypeName {
				return CloudControlListPlan{}, fmt.Errorf("AWS kind %s lists Cloud Control type %v", plan.TypeName, value)
			}
		case strings.HasPrefix(name, resourceModelParameter) && len(name) > len(resourceModelParameter):
			plan.Model[strings.TrimPrefix(name, resourceModelParameter)] = value
		default:
			return CloudControlListPlan{}, fmt.Errorf("AWS kind %s has unsupported list parameter %q", plan.TypeName, name)
		}
	}
	if parent := definition.Discovery.Parent; parent != nil {
		plan.ParentSource, plan.ParentType = parent.Source, parent.NativeType
	}
	return plan, nil
}

func (p CloudControlListPlan) usesParent() bool {
	for _, value := range p.Model {
		if text, ok := value.(string); ok && strings.HasPrefix(text, "parent.") {
			return true
		}
	}
	return false
}

// variants resolves the plan against concrete parents and scope values. Every
// model is canonical JSON so its fingerprint is stable across pages.
func (p CloudControlListPlan) variants(parents []cloudControlParent, scope asset.Scope, accountID string) ([]cloudControlVariant, error) {
	names := make([]string, 0, len(p.Model))
	for name := range p.Model {
		names = append(names, name)
	}
	sort.Strings(names)
	base := []map[string]any{{}}
	for _, name := range names {
		text, isExpression := p.Model[name].(string)
		if !isExpression || strings.HasPrefix(text, "parent.") {
			if !isExpression {
				for _, model := range base {
					model[name] = p.Model[name]
				}
			}
			continue
		}
		var values []any
		switch text {
		case "scope.accountId":
			if strings.TrimSpace(accountID) == "" {
				return nil, fmt.Errorf("AWS kind %s requires the connection account ID", p.TypeName)
			}
			values = []any{accountID}
		case "scope.wafScope":
			values = []any{"REGIONAL"}
			if cloudControlRegion(scope) == awsRegionBootstrap {
				values = append(values, "CLOUDFRONT")
			}
		default:
			values = []any{text}
		}
		next := make([]map[string]any, 0, len(base)*len(values))
		for _, model := range base {
			for _, value := range values {
				clone := make(map[string]any, len(model)+1)
				for key, item := range model {
					clone[key] = item
				}
				clone[name] = value
				next = append(next, clone)
			}
		}
		base = next
	}
	if !p.usesParent() {
		return encodeVariants(base, nil)
	}
	models := make([]map[string]any, 0, len(base)*len(parents))
	keys := make([]string, 0, cap(models))
	for _, parent := range parents {
		for _, model := range base {
			clone := make(map[string]any, len(model)+len(names))
			for key, item := range model {
				clone[key] = item
			}
			for _, name := range names {
				text, _ := p.Model[name].(string)
				switch {
				case text == "parent.nativeId":
					clone[name] = parent.Identifier
				case strings.HasPrefix(text, "parent.normalized."):
					value, found := valueAtDottedPath(parent.Properties, strings.TrimPrefix(text, "parent.normalized."))
					if !found || strings.TrimSpace(fmt.Sprint(value)) == "" {
						return nil, fmt.Errorf("AWS parent %s %q has no %s", p.ParentType, parent.Identifier, text)
					}
					clone[name] = value
				}
			}
			models = append(models, clone)
			keys = append(keys, parent.Identifier)
		}
	}
	return encodeVariants(models, keys)
}

func encodeVariants(models []map[string]any, keys []string) ([]cloudControlVariant, error) {
	result := make([]cloudControlVariant, 0, len(models))
	for index, model := range models {
		payload := ""
		if len(model) > 0 {
			encoded, err := json.Marshal(model)
			if err != nil {
				return nil, err
			}
			payload = string(encoded)
		}
		key := payload
		if keys != nil {
			key = keys[index] + "\x00" + payload
		}
		result = append(result, cloudControlVariant{Key: key, Model: payload})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Key < result[j].Key })
	return result, nil
}

func variantFingerprint(typeName string, variants []cloudControlVariant) string {
	digest := sha256.New()
	digest.Write([]byte(typeName))
	for _, variant := range variants {
		digest.Write([]byte{0})
		digest.Write([]byte(variant.Key))
	}
	return hex.EncodeToString(digest.Sum(nil))[:32]
}

// A single model-less variant keeps the provider token as the cursor for
// compatibility with shards created before list plans existed.
func decodeCloudControlCursor(raw string, variants []cloudControlVariant, fingerprint string) (cloudControlCursor, error) {
	if raw == "" {
		return cloudControlCursor{Fingerprint: fingerprint}, nil
	}
	if len(variants) == 1 && variants[0].Model == "" {
		return cloudControlCursor{Fingerprint: fingerprint, Token: raw}, nil
	}
	payload, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return cloudControlCursor{}, fmt.Errorf("AWS Cloud Control cursor is malformed")
	}
	var cursor cloudControlCursor
	if err := json.Unmarshal(payload, &cursor); err != nil {
		return cloudControlCursor{}, fmt.Errorf("AWS Cloud Control cursor is malformed")
	}
	if cursor.Fingerprint != fingerprint || cursor.Variant < 0 || cursor.Variant >= len(variants) {
		return cloudControlCursor{}, fmt.Errorf("AWS Cloud Control parent set changed during pagination; restart the shard")
	}
	return cursor, nil
}

func encodeCloudControlCursor(cursor cloudControlCursor, variants []cloudControlVariant) string {
	if len(variants) == 1 && variants[0].Model == "" {
		return cursor.Token
	}
	payload, _ := json.Marshal(cursor)
	return base64.RawURLEncoding.EncodeToString(payload)
}

func valueAtDottedPath(document map[string]any, path string) (any, bool) {
	var current any = document
	for _, segment := range strings.Split(path, ".") {
		object, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		current, ok = object[segment]
		if !ok {
			return nil, false
		}
	}
	return current, true
}

func homeRegion(nativeType string, scopeKind asset.ScopeKind, location string) string {
	location = strings.TrimSpace(location)
	if scopeKind == asset.ScopeGlobal || location == "" || strings.EqualFold(location, "global") {
		for prefix, region := range globalEndpointRegions {
			if strings.HasPrefix(nativeType, prefix) {
				return region
			}
		}
		return awsRegionBootstrap
	}
	return location
}

// listCloudControlParents reads the complete parent set in the same partition
// and region. Parents that themselves require a parent are resolved recursively.
func (r *Runtime) listCloudControlParents(ctx context.Context, client CloudControlClient, plan CloudControlListPlan, scope asset.Scope, accountID string, depth int) ([]cloudControlParent, error) {
	if depth > 4 {
		return nil, fmt.Errorf("AWS Cloud Control parent chain for %s is too deep", plan.TypeName)
	}
	if plan.ParentSource != cloudControlSource || plan.ParentType == "" {
		return nil, fmt.Errorf("AWS kind %s has unsupported parent source %q", plan.TypeName, plan.ParentSource)
	}
	parentSpec, ok := r.compiledSpec(plan.ParentType)
	if !ok {
		return nil, fmt.Errorf("AWS parent kind %s has no specification", plan.ParentType)
	}
	parentPlan, err := cloudControlListPlanFromSpec(parentSpec.Definition)
	if err != nil {
		return nil, err
	}
	var grandparents []cloudControlParent
	if parentPlan.usesParent() {
		grandparents, err = r.listCloudControlParents(ctx, client, parentPlan, scope, accountID, depth+1)
		if err != nil {
			return nil, err
		}
	}
	variants, err := parentPlan.variants(grandparents, scope, accountID)
	if err != nil {
		return nil, err
	}
	var parents []cloudControlParent
	pages := 0
	for _, variant := range variants {
		token := ""
		for {
			pages++
			if pages > cloudControlMaxParentPages {
				return nil, fmt.Errorf("AWS Cloud Control parent listing for %s exceeded %d pages", plan.ParentType, cloudControlMaxParentPages)
			}
			page, err := client.ListResources(ctx, CloudControlListRequest{TypeName: parentPlan.TypeName, NextToken: token, Limit: cloudControlPageLimit, ResourceModel: variant.Model})
			if err != nil {
				return nil, NormalizeError(err)
			}
			for _, resource := range page.Resources {
				properties, err := cloudControlModel(resource.Properties)
				if err != nil {
					return nil, fmt.Errorf("decode AWS parent %s %q: %w", plan.ParentType, resource.Identifier, err)
				}
				if strings.TrimSpace(resource.Identifier) == "" {
					return nil, fmt.Errorf("AWS Cloud Control ListResources returned an empty %s identifier", plan.ParentType)
				}
				if needsParentDetail(plan, properties) {
					detail, _, err := client.GetResource(ctx, parentPlan.TypeName, resource.Identifier)
					if err != nil {
						if cloudControlNotFound(err) {
							continue
						}
						return nil, NormalizeError(err)
					}
					if properties, err = cloudControlModel(detail.Properties); err != nil {
						return nil, err
					}
				}
				parents = append(parents, cloudControlParent{Identifier: resource.Identifier, Properties: properties})
			}
			if page.NextToken == "" {
				break
			}
			if page.NextToken == token {
				return nil, fmt.Errorf("AWS Cloud Control repeated a %s page token", plan.ParentType)
			}
			token = page.NextToken
		}
	}
	return parents, nil
}

func needsParentDetail(plan CloudControlListPlan, properties map[string]any) bool {
	for _, value := range plan.Model {
		text, _ := value.(string)
		if !strings.HasPrefix(text, "parent.normalized.") {
			continue
		}
		if _, found := valueAtDottedPath(properties, strings.TrimPrefix(text, "parent.normalized.")); !found {
			return true
		}
	}
	return false
}

func (r *Runtime) compiledSpec(nativeType string) (spec.CompiledSpec, bool) {
	for _, compiled := range r.bundle.Specs {
		if compiled.ResourceKind.NativeType == nativeType {
			return compiled, true
		}
	}
	return spec.CompiledSpec{}, false
}
