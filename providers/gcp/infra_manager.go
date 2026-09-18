package gcp

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const (
	infraHost          = "config.googleapis.com"
	infraDeployment    = infraHost + "/Deployment"
	infraRevision      = infraHost + "/Revision"
	infraResource      = infraHost + "/Resource"
	infraPreview       = infraHost + "/Preview"
	infraChange        = infraHost + "/ResourceChange"
	infraDrift         = infraHost + "/ResourceDrift"
	infraGroup         = infraHost + "/DeploymentGroup"
	infraGroupRevision = infraHost + "/DeploymentGroupRevision"
	infraProof         = "_infra_configuration"
	infraParentProof   = "_infra_parent_configuration"
	infraRootProof     = "_infra_root_configuration"
)

var infraCollections = map[string][]string{
	infraDeployment: {"deployments"}, infraRevision: {"deployments", "revisions"},
	infraResource: {"deployments", "revisions", "resources"}, infraPreview: {"previews"},
	infraChange: {"previews", "resourceChanges"}, infraDrift: {"previews", "resourceDrifts"},
	infraGroup: {"deploymentGroups"}, infraGroupRevision: {"deploymentGroups", "revisions"},
}

func isInfra(kind string) bool { _, ok := infraCollections[kind]; return ok }

func isInfraController(kind string) bool {
	return kind == infraDeployment || kind == infraPreview || kind == infraGroup
}

func infraListShape(data map[string]any, path string) error {
	// The official response schema permits omission of an empty repeated field,
	// but an explicitly returned field must keep its native array/string type.
	for _, field := range []string{"items", "locations", "deployments", "revisions", "resources", "previews", "resourceChanges", "resourceDrifts", "deploymentGroups", "deploymentGroupRevisions"} {
		if _, present := data[field]; present && field != path {
			return groupDenied("infra_list_collection_changed")
		}
	}
	for _, field := range []string{path, "unreachable"} {
		if value, present := data[field]; present {
			if _, ok := value.([]any); !ok {
				return groupDenied("infra_list_shape_invalid")
			}
		}
	}
	if value, present := data["nextPageToken"]; present {
		if _, ok := value.(string); !ok {
			return groupDenied("infra_list_token_invalid")
		}
	}
	return nil
}

func infraCanonical(value string) string {
	if !strings.HasPrefix(value, "https://"+infraHost+"/") {
		return value
	}
	u, err := url.Parse(value)
	if err != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.RawPath != "" || !strings.HasPrefix(u.Path, "/v1/projects/") {
		return value
	}
	return "//" + infraHost + strings.TrimPrefix(u.Path, "/v1")
}

func (c *client) infraName(kind, id string) (string, error) {
	name := strings.TrimPrefix(id, "//"+infraHost+"/")
	p := strings.Split(name, "/")
	collections, ok := infraCollections[kind]
	if !ok || name == id || len(p) != 4+2*len(collections) || p[0] != "projects" || (p[1] != c.project && p[1] != c.number) || p[2] != "locations" {
		return "", groupDenied("infra_resource_identity_invalid")
	}
	for _, part := range p {
		if !segmentPattern.MatchString(part) || part == "." || part == ".." || part == "-" {
			return "", groupDenied("infra_resource_path_invalid")
		}
	}
	for i, collection := range collections {
		if p[4+2*i] != collection {
			return "", groupDenied("infra_resource_collection_changed")
		}
	}
	return name, nil
}

func (c *client) infraID(kind, value string) (string, error) {
	if strings.HasPrefix(value, "projects/") {
		value = "//" + infraHost + "/" + value
	}
	id := c.canonicalName(value)
	_, err := c.infraName(kind, id)
	return id, err
}

func infraParent(kind, id string) (string, string) {
	parent := map[string]string{infraRevision: infraDeployment, infraResource: infraRevision, infraChange: infraPreview, infraDrift: infraPreview, infraGroupRevision: infraGroup}[kind]
	if parent == "" {
		return "", ""
	}
	p := strings.Split(id, "/")
	return parent, strings.Join(p[:len(p)-2], "/")
}

func infraConfiguration(raw map[string]any) string {
	data := cloneParameters(raw)
	if strings.Contains(text(data["name"]), "/deploymentGroups/") {
		for _, field := range []string{"provisioningState", "provisioningStateDescription", "provisioningError", "stateDescription", "alternativeIds"} {
			delete(data, field)
		}
	}
	for key := range data {
		if strings.HasPrefix(key, "_") || strings.HasPrefix(key, "refs_") || slices.Contains([]string{
			"state", "stateDetail", "updateTime", "lockState", "errorCode", "errorStatus", "tfErrors", "errorLogs", "logs", "build", "deleteBuild", "deleteLogs", "deleteResults", "applyResults", "previewArtifacts", "quotaValidationResults", "project_id", "project_number",
		}, key) {
			delete(data, key)
		}
	}
	encoded, _ := json.Marshal(data)
	return fmt.Sprintf("%x", sha256.Sum256(encoded))
}

func infraSame(planned, live map[string]any) error {
	proof := text(planned[infraProof])
	if proof == "" {
		proof = infraConfiguration(planned)
	}
	if proof != infraConfiguration(live) {
		return groupDenied("infra_configuration_changed")
	}
	return nil
}

func (c *client) infraIdentity(kind, id string, data map[string]any) error {
	actual, err := c.infraID(kind, text(data["name"]))
	if err != nil || actual != id {
		return groupDenied("infra_resource_identity_changed")
	}
	if slices.Contains([]string{infraDeployment, infraRevision, infraPreview, infraGroup, infraGroupRevision}, kind) {
		if _, err := time.Parse(time.RFC3339Nano, text(data["createTime"])); err != nil {
			return groupDenied("infra_creation_time_missing")
		}
	}
	if kind == infraGroup || kind == infraGroupRevision {
		if err := c.infraGroupIdentity(kind, id, data); err != nil {
			return err
		}
	}
	if kind == infraDeployment && text(data["latestRevision"]) != "" {
		revision, err := c.infraID(infraRevision, text(data["latestRevision"]))
		if err != nil || !strings.HasPrefix(revision, id+"/revisions/") {
			return groupDenied("infra_latest_revision_invalid")
		}
	}
	if kind == infraResource || kind == infraChange || kind == infraDrift {
		info, ok := data["terraformInfo"].(map[string]any)
		if !ok || text(info["address"]) == "" || text(info["type"]) == "" {
			return groupDenied("infra_deployment_identity_missing")
		}
	}
	return nil
}

func (c *client) infraRead(ctx context.Context, kind, id string) (map[string]any, error) {
	name, err := c.infraName(kind, id)
	if err != nil {
		return nil, err
	}
	data, err := c.request(ctx, "GET", "https://"+infraHost+"/v1/"+name, nil)
	if err != nil {
		return nil, err
	}
	if _, present := data["error"]; present {
		return nil, groupDenied("infra_error_payload")
	}
	if err := c.infraIdentity(kind, id, data); err != nil {
		return nil, err
	}
	return data, nil
}

func (c *client) verifyInfraParent(ctx context.Context, target productTarget) error {
	if !isInfra(target.ParentType) {
		return nil
	}
	live, err := c.infraRead(ctx, target.ParentType, target.ParentID)
	if err != nil {
		return contracts.DependencyReadError(err)
	}
	if target.ParentConfiguration == "" || infraConfiguration(live) != target.ParentConfiguration {
		return groupDenied("infra_parent_changed")
	}
	// Revisions are immutable history, but their containing deployment can be
	// replaced while resource details are being read.
	if target.ParentType == infraRevision {
		_, root := infraParent(infraRevision, target.ParentID)
		live, err := c.infraRead(ctx, infraDeployment, root)
		if err != nil {
			return contracts.DependencyReadError(err)
		}
		if target.ParentContainerChain == "" || infraConfiguration(live) != target.ParentContainerChain {
			return groupDenied("infra_root_changed")
		}
	}
	return nil
}

func (c *client) infraReferences(kind, id string, data map[string]any) map[string][]string {
	refs := map[string][]string{}
	if parent, name := infraParent(kind, id); parent != "" {
		refs[parent] = []string{name}
	}
	if kind == infraGroup || kind == infraGroupRevision {
		group := data
		if kind == infraGroupRevision {
			group = object(data["snapshot"])
		}
		if units, err := c.infraGroupUnits(group); err == nil {
			for _, unit := range units {
				if unit.Deployment != "" {
					refs[infraDeployment] = append(refs[infraDeployment], unit.Deployment)
				}
			}
		}
		return refs
	}
	// Revision/preview records describe past or proposed resources, not an
	// ownership transfer. CAI identities are interpreted separately for a live
	// deployment's current state. Never infer dependencies from arbitrary input.
	if kind != infraDeployment && kind != infraPreview {
		return refs
	}
	for _, kind := range []string{"iam.googleapis.com/ServiceAccount", "storage.googleapis.com/Bucket"} {
		values, _ := discoveryStrings(data[referenceKey(kind)])
		refs[kind] = append(refs[kind], values...)
	}
	if name := text(data["serviceAccount"]); strings.HasPrefix(name, "projects/") {
		refs["iam.googleapis.com/ServiceAccount"] = append(refs["iam.googleapis.com/ServiceAccount"], c.canonicalName("//iam.googleapis.com/"+name))
	}
	for _, field := range []string{"artifactsGcsBucket", "terraformBlueprint.gcsSource"} {
		value := text(productValue(data, field))
		if strings.HasPrefix(value, "gs://") {
			bucket := strings.Split(strings.TrimPrefix(value, "gs://"), "/")[0]
			if bucket != "" && !strings.ContainsAny(bucket, "?#@") {
				refs["storage.googleapis.com/Bucket"] = append(refs["storage.googleapis.com/Bucket"], "//storage.googleapis.com/"+bucket)
			}
		}
	}
	for kind, values := range refs {
		if len(values) == 0 {
			delete(refs, kind)
			continue
		}
		slices.Sort(values)
		refs[kind] = slices.Compact(values)
	}
	return refs
}

func safeInfraPayload(raw map[string]any) map[string]any {
	result := safePayload(raw)
	var visit func(any)
	visit = func(value any) {
		switch value := value.(type) {
		case map[string]any:
			for key, child := range value {
				if slices.Contains([]string{"terraformBlueprint", "providerConfig", "applyResults", "deleteResults", "previewArtifacts", "stateDetail", "statusMessage", "errorStatus", "tfErrors", "logs", "errorLogs", "deleteLogs", "annotations", "before", "after", "provisioningError", "provisioningStateDescription", "stateDescription", "deploymentOperationSummary"}, key) {
					value[key] = "[REDACTED]"
				} else {
					visit(child)
				}
			}
		case []any:
			for _, child := range value {
				visit(child)
			}
		}
	}
	visit(result)
	return result
}
