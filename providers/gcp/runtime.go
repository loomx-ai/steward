package gcp

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

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
	// caches outlive a client replaced by a concurrent first resolve, so the
	// shards of one scan share them; a new credential starts empty ones.
	caches map[asset.ConnectionID]*clientCache
	// sources identifies the credential each client was last resolved from.
	sources map[asset.ConnectionID][32]byte
}

func NewRuntime(credentials contracts.CredentialSource) (*Runtime, error) {
	if credentials == nil {
		return nil, fmt.Errorf("GCP credential source is required")
	}
	bundle, err := compileBundle()
	if err != nil {
		return nil, err
	}
	return &Runtime{credentials: credentials, transport: http.DefaultTransport, bundle: bundle, clients: map[asset.ConnectionID]*client{}, caches: map[asset.ConnectionID]*clientCache{}}, nil
}
func (r *Runtime) Provider() asset.Provider { return asset.ProviderGCP }
func (r *Runtime) Bundle() spec.Bundle {
	payload, _ := json.Marshal(r.bundle)
	var cloned spec.Bundle
	_ = json.Unmarshal(payload, &cloned)
	return cloned
}
func (r *Runtime) CredentialSchemas() []contracts.CredentialSchema {
	return []contracts.CredentialSchema{
		{Type: asset.CredentialGCPServiceAccount, LabelKey: "credentials.gcpServiceAccount", Fields: []contracts.CredentialField{
			{Key: "project_id", LabelKey: "credentials.projectId", InputType: "text", Required: true},
			{Key: "service_account_json", LabelKey: "credentials.serviceAccountJson", InputType: "textarea", Required: true, Secret: true},
			{Key: "identity_group_parent", LabelKey: "credentials.identityGroupParent", InputType: "text"},
			{Key: "firewall_policy_parent", LabelKey: "credentials.firewallPolicyParent", InputType: "text"},
		}},
		// The project is not a field here: a browser authorization lists the
		// projects the signed-in identity reaches and the operator picks one.
		{Type: asset.CredentialGCPOAuth, LabelKey: "credentials.gcpOAuth", Flow: "browser_oauth", Fields: []contracts.CredentialField{
			{Key: "identity_group_parent", LabelKey: "credentials.identityGroupParent", InputType: "text"},
			{Key: "firewall_policy_parent", LabelKey: "credentials.firewallPolicyParent", InputType: "text"},
		}},
	}
}
func (r *Runtime) InventorySources() []contracts.InventorySource {
	return []contracts.InventorySource{
		// Billing visibility can change; only each saved budget's own GET proves absence.
		{Name: billingBudgetSource, RootScopeKinds: []asset.ScopeKind{asset.ScopeProject, asset.ScopeGlobal}, KindSpecific: true, ReconcileKnownIDs: true},
		{Name: inventorySource, RootScopeKinds: []asset.ScopeKind{asset.ScopeProject, asset.ScopeRegion, asset.ScopeGlobal}, NetworkClosure: true},
		{Name: productInventorySource, RootScopeKinds: []asset.ScopeKind{asset.ScopeProject, asset.ScopeRegion, asset.ScopeGlobal}, AuthoritativeDefault: true, KindSpecific: true, NetworkClosure: true},
		// Native folder searches are filtered by caller visibility. Losing access
		// must not close a previously observed folder as if it had been deleted.
		{Name: dataformInventorySource, RootScopeKinds: []asset.ScopeKind{asset.ScopeProject, asset.ScopeRegion}, KindSpecific: true, NetworkClosure: true},
		// An optional hierarchy scope can change independently of the project.
		// Losing that scope must not close previously observed policies or links.
		{Name: firewallInventorySource, RootScopeKinds: []asset.ScopeKind{asset.ScopeProject, asset.ScopeGlobal}, KindSpecific: true},
		// Directory lists are visibility filtered and the optional root can change.
		{Name: identityInventorySource, RootScopeKinds: []asset.ScopeKind{asset.ScopeProject, asset.ScopeGlobal}, KindSpecific: true},
		// Service/location visibility does not prove security settings were deleted.
		{Name: securityServiceSource, RootScopeKinds: []asset.ScopeKind{asset.ScopeProject, asset.ScopeRegion, asset.ScopeGlobal}, KindSpecific: true},
		// Billing settings cannot be deleted; location visibility does not prove absence.
		{Name: securityBillingSource, RootScopeKinds: []asset.ScopeKind{asset.ScopeProject, asset.ScopeRegion, asset.ScopeGlobal}, KindSpecific: true},
		// Project moves and lost ancestor visibility do not delete organizations.
		{Name: organizationInventorySource, RootScopeKinds: []asset.ScopeKind{asset.ScopeProject, asset.ScopeGlobal}, KindSpecific: true},
		// Only the connection identity's own profile is listed; replacing the
		// credential must not close another identity's keys without a direct read.
		{Name: osLoginSource, RootScopeKinds: []asset.ScopeKind{asset.ScopeProject, asset.ScopeGlobal}, KindSpecific: true, ReconcileKnownIDs: true},
	}
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
	if c.firewallParent != "" {
		if _, err := c.firewallContainer(ctx, c.firewallParent); err != nil {
			return nil, err
		}
	}
	if err := c.validateIdentityDirectory(ctx); err != nil {
		return nil, err
	}
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
	// Every inventory page resolves its client. An unchanged, unexpired
	// credential reuses the resolved client instead of re-parsing its key.
	source := credentialSource(credential)
	r.mu.Lock()
	existing := r.clients[id]
	known := r.sources[id] == source
	r.mu.Unlock()
	if existing != nil && known && (credential.ExpiresAt == nil || credential.ExpiresAt.After(time.Now())) {
		return existing, nil
	}
	candidate, err := newClient(credential, r.transport)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	existing = r.clients[id]
	cache := r.caches[id]
	if cache == nil || cache.fingerprint != candidate.fingerprint {
		cache = &clientCache{fingerprint: candidate.fingerprint, inflight: sharedReads{inflightOnly: true}}
		r.caches[id] = cache
	}
	candidate.cache = cache
	if existing != nil && existing.fingerprint == candidate.fingerprint {
		// A renewed credential for the same identity keeps the client; record
		// it so later pages take the fast path again.
		r.setSource(id, source)
		r.mu.Unlock()
		return existing, nil
	}
	r.mu.Unlock()
	if _, err = candidate.projectIdentity(ctx); err != nil {
		return nil, err
	}
	r.mu.Lock()
	r.clients[id] = candidate
	r.setSource(id, source)
	r.mu.Unlock()
	return candidate, nil
}

// setSource records a client's credential; the caller holds r.mu.
func (r *Runtime) setSource(id asset.ConnectionID, source [32]byte) {
	if r.sources == nil {
		r.sources = map[asset.ConnectionID][32]byte{}
	}
	r.sources[id] = source
}
func (r *Runtime) DiscoverRegions(ctx context.Context, id asset.ConnectionID) ([]contracts.DiscoveredRegion, error) {
	c, err := r.resolve(ctx, id)
	if err != nil {
		return nil, err
	}
	// Discovery Engine supports US/EU multi-regions but has no Locations.list.
	// Offer their documented scopes even before CAI observes the first resource.
	regions := map[string]bool{"us": true, "eu": true}
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

// compiledSpec finds a kind's spec by index rather than scanning every spec:
// the runtime bundle is the provider bundle the index was built from.
func (r *Runtime) compiledSpec(nativeType string) (spec.CompiledSpec, bool) {
	metadata, _ := providerData()
	if index, ok := metadata.specIndex[nativeType]; ok && index < len(r.bundle.Specs) && r.bundle.Specs[index].ResourceKind.NativeType == nativeType {
		return r.bundle.Specs[index], true
	}
	return spec.CompiledSpec{}, false
}
func (r *Runtime) resourceKind(nativeType string) asset.ResourceKind {
	if compiled, ok := r.compiledSpec(nativeType); ok {
		return compiled.ResourceKind
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

// credentialSource identifies the stored credential material a client was
// built from.
func credentialSource(credential contracts.Credential) [32]byte {
	dynamic := ""
	if credential.Dynamic != nil {
		dynamic = credential.Dynamic.Key
	}
	encoded, _ := json.Marshal(struct {
		Type             asset.CredentialType
		Values           map[string]string
		ExpiresAt        *time.Time
		Version, Dynamic string
	}{credential.Type, credential.Values, credential.ExpiresAt, credential.Version, dynamic})
	return sha256.Sum256(encoded)
}
