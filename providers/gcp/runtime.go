package gcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
	"github.com/loomx-ai/steward/internal/provider/spec"
)

type Runtime struct {
	credentials contracts.CredentialSource
	transport   http.RoundTripper
	bundle      spec.Bundle
	mu          sync.Mutex
	clients     map[asset.ConnectionID]*client
}

func NewRuntime(credentials contracts.CredentialSource) (*Runtime, error) {
	if credentials == nil {
		return nil, fmt.Errorf("GCP credential source is required")
	}
	bundle, err := compileBundle()
	if err != nil {
		return nil, err
	}
	return &Runtime{credentials: credentials, transport: http.DefaultTransport, bundle: bundle, clients: map[asset.ConnectionID]*client{}}, nil
}
func (r *Runtime) Provider() asset.Provider { return asset.ProviderGCP }
func (r *Runtime) Bundle() spec.Bundle {
	payload, _ := json.Marshal(r.bundle)
	var cloned spec.Bundle
	_ = json.Unmarshal(payload, &cloned)
	return cloned
}
func (r *Runtime) CredentialSchemas() []contracts.CredentialSchema {
	return []contracts.CredentialSchema{{Type: asset.CredentialGCPServiceAccount, LabelKey: "credentials.gcpServiceAccount", Fields: []contracts.CredentialField{
		{Key: "project_id", LabelKey: "credentials.projectId", InputType: "text", Required: true},
		{Key: "service_account_json", LabelKey: "credentials.serviceAccountJson", InputType: "textarea", Required: true, Secret: true},
	}}}
}
func (r *Runtime) InventorySources() []contracts.InventorySource {
	return []contracts.InventorySource{{Name: inventorySource, RootScopeKinds: []asset.ScopeKind{asset.ScopeProject, asset.ScopeRegion, asset.ScopeGlobal}, AuthoritativeDefault: true, NetworkClosure: true}}
}
func (r *Runtime) Invoke(context.Context, contracts.Invocation) (contracts.InvocationResult, error) {
	return contracts.InvocationResult{}, fmt.Errorf("GCP uses provider-owned REST operations")
}

func (c *client) projectIdentity(ctx context.Context) (map[string]any, error) {
	data, err := c.request(ctx, "GET", "https://cloudresourcemanager.googleapis.com/v3/projects/"+c.project, nil)
	if err != nil {
		return nil, err
	}
	if text(data["state"]) != "ACTIVE" || text(data["projectId"]) == "" {
		return nil, fmt.Errorf("Google Cloud project is not active")
	}
	c.number = last(text(data["name"]))
	if c.number == "" || (c.project != text(data["projectId"]) && c.project != c.number) {
		return nil, fmt.Errorf("Google Cloud returned a different project")
	}
	c.project = text(data["projectId"])
	return data, nil
}
func (r *Runtime) ValidateConnection(ctx context.Context, credential contracts.Credential) (contracts.ConnectionIdentity, error) {
	c, err := newClient(credential, r.transport)
	if err != nil {
		return contracts.ConnectionIdentity{}, err
	}
	data, err := c.projectIdentity(ctx)
	if err != nil {
		return contracts.ConnectionIdentity{}, err
	}
	// Validation proves project visibility and the exact inventory permission.
	_, err = c.request(ctx, "GET", "https://cloudasset.googleapis.com/v1/projects/"+c.project+"/assets", url.Values{"contentType": {"RESOURCE"}, "pageSize": {"1"}})
	if err != nil {
		return contracts.ConnectionIdentity{}, err
	}
	return contracts.ConnectionIdentity{Partition: "gcp", TenantID: c.number, Principal: c.email, RootScopes: []contracts.RootScopeCandidate{{Kind: asset.ScopeProject, NativeID: text(data["projectId"]), Name: text(data["displayName"])}}}, nil
}
func (r *Runtime) resolve(ctx context.Context, id asset.ConnectionID) (*client, error) {
	if id == "" {
		return nil, fmt.Errorf("GCP connection ID is required")
	}
	credential, err := r.credentials.Resolve(ctx, id)
	if err != nil {
		return nil, err
	}
	candidate, err := newClient(credential, r.transport)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	existing := r.clients[id]
	r.mu.Unlock()
	if existing != nil && existing.fingerprint == candidate.fingerprint {
		return existing, nil
	}
	if _, err = candidate.projectIdentity(ctx); err != nil {
		return nil, err
	}
	r.mu.Lock()
	r.clients[id] = candidate
	r.mu.Unlock()
	return candidate, nil
}
func (r *Runtime) DiscoverRegions(ctx context.Context, id asset.ConnectionID) ([]contracts.DiscoveredRegion, error) {
	c, err := r.resolve(ctx, id)
	if err != nil {
		return nil, err
	}
	regions := map[string]bool{}
	cursor := ""
	for {
		data, err := c.request(ctx, "GET", "https://compute.googleapis.com/compute/v1/projects/"+c.project+"/regions", url.Values{"maxResults": {"500"}, "pageToken": {cursor}})
		if err != nil {
			return nil, err
		}
		for _, item := range array(data["items"]) {
			value := object(item)
			name := text(value["name"])
			if name != "" && text(value["status"]) != "DOWN" {
				regions[name] = true
			}
		}
		next := text(data["nextPageToken"])
		if next == "" {
			break
		}
		if next == cursor {
			return nil, fmt.Errorf("Google regions pagination did not advance")
		}
		cursor = next
	}
	// CAI also covers services whose locations are not Compute regions.
	cursor = ""
	for {
		data, err := c.assetPage(ctx, cursor, "", 1000)
		if err != nil {
			return nil, err
		}
		for _, value := range array(data["assets"]) {
			_, region := assetLocation(object(value))
			if region != "global" && region != "" {
				regions[region] = true
			}
		}
		next := text(data["nextPageToken"])
		if next == "" {
			break
		}
		if next == cursor {
			return nil, fmt.Errorf("Google asset pagination did not advance")
		}
		cursor = next
	}
	result := make([]contracts.DiscoveredRegion, 0, len(regions))
	for region := range regions {
		result = append(result, contracts.DiscoveredRegion{RegionID: region, Name: region})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].RegionID < result[j].RegionID })
	return result, nil
}
func (r *Runtime) resourceKind(nativeType string) asset.ResourceKind {
	for _, compiled := range r.bundle.Specs {
		if compiled.ResourceKind.NativeType == nativeType {
			return compiled.ResourceKind
		}
	}
	return asset.ResourceKind{ID: asset.ResourceKindID("gcp:" + nativeType), Provider: asset.ProviderGCP, NativeType: nativeType, DisplayName: last(nativeType), ScopeKinds: both, Capabilities: asset.CapabilitySet{asset.CapabilityIndexed}, BundleRevision: r.bundle.Revision}
}
func regionOf(location string) string {
	location = strings.ToLower(last(location))
	parts := strings.Split(location, "-")
	if len(parts) >= 3 && len(parts[len(parts)-1]) == 1 {
		return strings.Join(parts[:len(parts)-1], "-")
	}
	return location
}
