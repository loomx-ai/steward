package gcp

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
)

const (
	fusionHost          = "datafusion.googleapis.com"
	fusionInstanceType  = fusionHost + "/Instance"
	fusionDNSType       = fusionHost + "/DnsPeering"
	fusionNamespaceType = fusionHost + "/Namespace"
	fusionProof         = "_datafusion_configuration"
	fusionParentProof   = "_datafusion_parent_configuration"
)

func isFusion(kind string) bool { return strings.HasPrefix(kind, fusionHost+"/") }

func fusionCanonical(value string) string {
	if !strings.HasPrefix(value, "https://"+fusionHost+"/") {
		return value
	}
	u, err := url.Parse(value)
	if err != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Host != fusionHost {
		return value
	}
	for _, version := range []string{"v1", "v1beta1"} {
		if strings.HasPrefix(u.Path, "/"+version+"/projects/") {
			return "//" + fusionHost + strings.TrimPrefix(u.Path, "/"+version)
		}
	}
	return value
}

func (c *client) fusionName(kind, id string) (string, error) {
	name := strings.TrimPrefix(id, "//"+fusionHost+"/")
	p := strings.Split(name, "/")
	if !strings.HasPrefix(id, "//"+fusionHost+"/") || (len(p) != 6 && len(p) != 8) || p[0] != "projects" || (p[1] != c.project && p[1] != c.number) || p[2] != "locations" || p[3] == "global" || regionOf(p[3]) != p[3] || p[4] != "instances" {
		return "", groupDenied("datafusion_identity_invalid")
	}
	collection := map[string]string{fusionDNSType: "dnsPeerings", fusionNamespaceType: "namespaces"}[kind]
	if (kind == fusionInstanceType && len(p) != 6) || (kind != fusionInstanceType && (len(p) != 8 || collection == "" || p[6] != collection)) {
		return "", groupDenied("datafusion_collection_invalid")
	}
	for _, part := range p {
		if !segmentPattern.MatchString(part) || part == "." || part == ".." {
			return "", groupDenied("datafusion_path_invalid")
		}
	}
	return name, nil
}

func (c *client) fusionID(kind, value, parent string) (string, error) {
	// Namespace.name is documented as a namespace name; the list parent is
	// authoritative for a bare name. A full name must still match that parent.
	if kind == fusionNamespaceType && !strings.Contains(value, "/") && parent != "" {
		value = parent + "/namespaces/" + value
	}
	if strings.HasPrefix(value, "projects/") {
		value = "//" + fusionHost + "/" + value
	}
	id := c.canonicalName(value)
	_, err := c.fusionName(kind, id)
	if err == nil && parent != "" {
		prefix := c.canonicalName("//"+fusionHost+"/"+strings.TrimPrefix(parent, "//"+fusionHost+"/")) + "/"
		if !strings.HasPrefix(id, prefix) {
			err = groupDenied("datafusion_list_parent_changed")
		}
	}
	return id, err
}

func fusionConfiguration(raw map[string]any) string {
	value := cloneParameters(raw)
	for key := range value {
		if slices.Contains([]string{"name", "state", "stateMessage", "updateTime", "availableVersion", "maintenanceEvents", "disabledReason", "project_id", "project_number"}, key) || strings.HasPrefix(key, "_") || strings.HasPrefix(key, "refs_") {
			delete(value, key)
		}
	}
	encoded, _ := json.Marshal(value)
	return fmt.Sprintf("%x", sha256.Sum256(encoded))
}

func fusionSameResource(kind string, planned, live map[string]any) error {
	if !isFusion(kind) {
		return nil
	}
	expected := text(planned[fusionProof])
	if expected == "" {
		expected = fusionConfiguration(planned)
	}
	if expected != fusionConfiguration(live) {
		return groupDenied("datafusion_configuration_changed")
	}
	return nil
}

func (c *client) fusionIdentity(kind, id string, data map[string]any) error {
	parent := ""
	if kind != fusionInstanceType {
		if _, err := c.fusionName(kind, id); err != nil {
			return err
		}
		parent = strings.Join(strings.Split(strings.TrimPrefix(id, "//"+fusionHost+"/"), "/")[:6], "/")
	}
	actual, err := c.fusionID(kind, text(data["name"]), parent)
	if err != nil || actual != id {
		return groupDenied("datafusion_identity_changed")
	}
	if kind == fusionInstanceType {
		if _, err := time.Parse(time.RFC3339Nano, text(data["createTime"])); err != nil {
			return groupDenied("datafusion_creation_time_missing")
		}
	}
	if kind == fusionDNSType && text(data["domain"]) == "" {
		return groupDenied("datafusion_dns_domain_missing")
	}
	if kind == fusionNamespaceType {
		policy, ok := data["iamPolicy"].(map[string]any)
		if !ok {
			return groupDenied("datafusion_namespace_policy_missing")
		}
		if status, present := policy["status"]; present {
			status, ok := status.(map[string]any)
			code := status["code"]
			if !ok || (code != nil && code != json.Number("0") && code != float64(0) && code != 0) {
				return groupDenied("datafusion_namespace_policy_unreadable")
			}
		}
		if _, ok := policy["policy"].(map[string]any); !ok {
			return groupDenied("datafusion_namespace_policy_missing")
		}
	}
	return nil
}

func fusionListParameters(kind, parent string) (string, string, map[string]any) {
	collection := "dnsPeerings"
	parameters := map[string]any{"parent": strings.TrimPrefix(parent, "//"+fusionHost+"/")}
	if kind == fusionNamespaceType {
		collection = "namespaces"
		parameters["view"] = "NAMESPACE_VIEW_FULL"
	}
	return "datafusion.projects.locations.instances." + collection + ".list", collection, parameters
}

func (c *client) fusionList(ctx context.Context, kind, parent string) ([]serviceChild, error) {
	if _, err := c.fusionName(fusionInstanceType, parent); err != nil {
		return nil, err
	}
	method, collection, parameters := fusionListParameters(kind, parent)
	records, err := c.batchList(ctx, method, parameters, collection)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var children []serviceChild
	for _, data := range records {
		id, err := c.fusionID(kind, text(data["name"]), parent)
		if err != nil {
			return nil, err
		}
		if seen[id] {
			return nil, groupDenied("datafusion_duplicate_child")
		}
		seen[id] = true
		if err := c.fusionIdentity(kind, id, data); err != nil {
			return nil, err
		}
		children = append(children, serviceChild{kind: kind, id: id, data: data})
	}
	slices.SortFunc(children, func(a, b serviceChild) int { return strings.Compare(a.id, b.id) })
	return children, nil
}

func (c *client) fusionRead(ctx context.Context, kind, id string) (map[string]any, error) {
	name, err := c.fusionName(kind, id)
	if err != nil {
		return nil, err
	}
	if kind != fusionInstanceType {
		parent := "//" + fusionHost + "/" + strings.Join(strings.Split(name, "/")[:6], "/")
		children, err := c.fusionList(ctx, kind, parent)
		if err != nil {
			return nil, err
		}
		for _, child := range children {
			if child.id == id {
				return child.data, nil
			}
		}
		return nil, apiError(http.StatusNotFound, "not_found", nil, "")
	}
	native, _ := findType(kind)
	endpoint, err := c.resourceURL(native, id)
	if err != nil {
		return nil, err
	}
	data, err := c.request(ctx, "GET", endpoint, nil)
	if err != nil {
		return nil, err
	}
	if _, present := data["error"]; present {
		return nil, groupDenied("datafusion_error_response")
	}
	if err := c.fusionIdentity(kind, id, data); err != nil {
		return nil, err
	}
	return data, nil
}

func (c *client) verifyFusionParent(ctx context.Context, target productTarget) error {
	if target.ParentType != fusionInstanceType {
		return nil
	}
	if target.ParentConfiguration == "" {
		return groupDenied("datafusion_parent_proof_missing")
	}
	live, err := c.fusionRead(ctx, fusionInstanceType, target.ParentID)
	if err != nil {
		return err
	}
	if fusionConfiguration(live) != target.ParentConfiguration {
		return groupDenied("datafusion_parent_changed")
	}
	return nil
}

func (c *client) fusionChildren(ctx context.Context, parent asset.Identity, data map[string]any) ([]serviceChild, error) {
	if parent.NativeType != fusionInstanceType {
		return nil, nil
	}
	proof := text(data[fusionProof])
	if proof == "" {
		proof = fusionConfiguration(data)
	}
	target := productTarget{ParentType: fusionInstanceType, ParentID: parent.NativeID, ParentConfiguration: proof}
	if err := c.verifyFusionParent(ctx, target); err != nil {
		return nil, err
	}
	result, err := c.fusionSnapshot(ctx, parent.NativeID, []string{fusionDNSType, fusionNamespaceType})
	if err != nil {
		return nil, err
	}
	if err := c.verifyFusionParent(ctx, target); err != nil {
		return nil, err
	}
	return result, nil
}

func (c *client) fusionSnapshot(ctx context.Context, parent string, kinds []string) ([]serviceChild, error) {
	var result []serviceChild
	// Neither child API offers GET. Reconcile two complete sets, including IAM
	// policy status, instead of treating a list record as an immutable child.
	for pass := 0; pass < 2; pass++ {
		var children []serviceChild
		for _, kind := range kinds {
			found, err := c.fusionList(ctx, kind, parent)
			if err != nil {
				return nil, err
			}
			children = append(children, found...)
		}
		if pass == 1 {
			if len(children) != len(result) {
				return nil, groupDenied("datafusion_membership_changed")
			}
			for i, child := range children {
				if result[i].id != child.id || fusionConfiguration(result[i].data) != fusionConfiguration(child.data) {
					return nil, groupDenied("datafusion_membership_changed")
				}
			}
		}
		result = children
	}
	return result, nil
}

func (c *client) fusionReferences(kind, id string, data map[string]any) (map[string][]string, error) {
	refs := map[string][]string{}
	add := func(kind, value string) error {
		if value == "" {
			return nil
		}
		host := strings.Split(kind, "/")[0]
		if strings.HasPrefix(value, "projects/") || kind == "storage.googleapis.com/Bucket" && !strings.Contains(value, "/") {
			value = "//" + host + "/" + value
		}
		value = c.canonicalName(value)
		// Shared VPC and service accounts can belong to another project. Validate
		// their native shape without authorizing calls into that project.
		validation := *c
		parts := strings.Split(strings.TrimPrefix(value, "//"+host+"/"), "/")
		if len(parts) > 1 && parts[0] == "projects" {
			validation.project, validation.number = parts[1], ""
		}
		native, _ := findType(kind)
		if _, err := validation.resourceURL(native, value); err != nil {
			return groupDenied("datafusion_dependency_invalid")
		}
		if !slices.Contains(refs[kind], value) {
			refs[kind] = append(refs[kind], value)
		}
		return nil
	}
	network := text(object(data["networkConfig"])["network"])
	networkProject := c.project
	if kind == fusionDNSType {
		network = text(data["targetNetwork"])
		if target := text(data["targetProject"]); target != "" {
			networkProject = target
		}
	}
	if network != "" && !strings.Contains(network, "/") {
		network = "projects/" + networkProject + "/global/networks/" + network
	}
	for target, value := range map[string]string{
		"compute.googleapis.com/Network":           network,
		"compute.googleapis.com/NetworkAttachment": text(object(object(data["networkConfig"])["privateServiceConnectConfig"])["networkAttachment"]),
		"storage.googleapis.com/Bucket":            strings.TrimSuffix(strings.TrimPrefix(text(data["gcsBucket"]), "gs://"), "/"),
		"cloudkms.googleapis.com/CryptoKey":        text(object(data["cryptoKeyConfig"])["keyReference"]),
		"pubsub.googleapis.com/Topic":              text(object(data["eventPublishConfig"])["topic"]),
	} {
		if err := add(target, value); err != nil {
			return nil, err
		}
	}
	for _, key := range []string{"serviceAccount", "p4ServiceAccount", "dataprocServiceAccount"} {
		value := text(data[key])
		if value != "" && !strings.Contains(value, "/") {
			project := c.project
			if parts := strings.Split(value, "@"); len(parts) == 2 && strings.HasSuffix(parts[1], ".iam.gserviceaccount.com") && parts[1] != "gcp-sa-datafusion.iam.gserviceaccount.com" {
				project = strings.TrimSuffix(parts[1], ".iam.gserviceaccount.com")
			}
			value = "projects/" + project + "/serviceAccounts/" + value
		}
		if err := add("iam.googleapis.com/ServiceAccount", value); err != nil {
			return nil, err
		}
	}
	if kind != fusionInstanceType {
		refs[fusionInstanceType] = []string{strings.Join(strings.Split(id, "/")[:9], "/")}
	}
	return refs, nil
}

func safeFusionPayload(raw map[string]any) map[string]any {
	value := safePayload(raw)
	var redact func(any)
	redact = func(value any) {
		switch data := value.(type) {
		case map[string]any:
			for key, child := range data {
				if slices.Contains([]string{"options", "description", "stateMessage", "statusDetail", "additionalStatus", "iamPolicy"}, key) {
					data[key] = "[REDACTED]"
				} else {
					redact(child)
				}
			}
		case []any:
			for _, child := range data {
				redact(child)
			}
		}
	}
	redact(value)
	return value
}
