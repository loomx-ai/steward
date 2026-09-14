package azure

import (
	"context"
	"maps"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/catalog"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const synapseDataVersion = "2020-12-01"
const synapseOAuthScope = "https://dev.azuresynapse.net/.default"
const synapseDataOperationPrefix = "Azure.Microsoft.Synapse.DataPlane."

var synapseEndpointPattern = regexp.MustCompile(`^https://[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.dev\.azuresynapse\.net$`)

type synapseDataClient struct {
	arm  *client
	http *http.Client
}
type synapseWorkspace struct {
	id, endpoint string
	raw          map[string]any
}

// Cache a separate OAuth transport per resolved connection incarnation. Never
// copy an ARM access token into the data-plane client or use ambient credentials.
func (r *Runtime) synapseClient(ctx context.Context, id asset.ConnectionID) (*synapseDataClient, error) {
	arm, err := r.resolve(ctx, id)
	if err != nil {
		return nil, err
	}
	return r.synapseResolvedClient(id, arm)
}

func (r *Runtime) synapseResolvedClient(id asset.ConnectionID, arm *client) (*synapseDataClient, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if c := r.synapseClients[id]; c != nil && c.arm == arm {
		return c, nil
	}
	token, ok := arm.http.Transport.(*tokenTransport)
	if !ok {
		return nil, serviceDenied("synapse_credential_transport_unsupported")
	}
	config := token.config
	config.Scopes = []string{synapseOAuthScope}
	c := &synapseDataClient{arm: arm, http: &http.Client{Transport: &tokenTransport{base: token.base, config: config}, Timeout: 60 * time.Second, CheckRedirect: noRedirect}}
	if r.synapseClients == nil {
		r.synapseClients = map[asset.ConnectionID]*synapseDataClient{}
	}
	r.synapseClients[id] = c
	return c, nil
}

func synapseWorkspaceEndpoint(raw map[string]any) (string, error) {
	endpoint, ok := object(object(raw["properties"])["connectivityEndpoints"])["dev"].(string)
	if !ok || !synapseEndpointPattern.MatchString(endpoint) || endpoint != "https://"+strings.ToLower(text(raw["name"]))+".dev.azuresynapse.net" {
		return "", serviceDenied("invalid_synapse_workspace_endpoint")
	}
	return endpoint, nil
}

func (c *client) synapseWorkspaceRead(ctx context.Context, id string) (synapseWorkspace, error) {
	parsed, kind, err := parseID(id)
	if err != nil || synapseKind(kind) != synapseType || !strings.HasPrefix(parsed, c.root()+"/") || len(strings.Split(parsed, "/")) != 9 {
		return synapseWorkspace{}, serviceDenied("invalid_synapse_workspace_scope")
	}
	res, err := c.request(ctx, "GET", apiURL(parsed, synapseVersion))
	if err != nil {
		return synapseWorkspace{}, err
	}
	if err = c.synapseReadResponse(res, parsed, synapseType); err != nil {
		return synapseWorkspace{}, err
	}
	endpoint, err := synapseWorkspaceEndpoint(res.data)
	if err != nil {
		return synapseWorkspace{}, err
	}
	return synapseWorkspace{id: parsed, endpoint: endpoint, raw: res.data}, nil
}

// The endpoint is a lookup key. An unfiltered native subscription list and fresh
// workspace GET establish its authority; suffix matching alone never does.
func (c *client) synapseWorkspaceForEndpoint(ctx context.Context, endpoint string) (synapseWorkspace, error) {
	if !synapseEndpointPattern.MatchString(endpoint) {
		return synapseWorkspace{}, serviceDenied("invalid_synapse_workspace_endpoint")
	}
	collection := c.root() + "/providers/Microsoft.Synapse/workspaces"
	next := apiURL(collection, synapseVersion)
	pages, ids := map[string]bool{}, map[string]bool{}
	var match synapseWorkspace
	for next != "" {
		if pages[next] {
			return synapseWorkspace{}, serviceDenied("synapse_workspace_pagination_cycle")
		}
		pages[next] = true
		rows, following, _, err := c.synapsePage(ctx, next, collection, synapseType)
		if err != nil {
			return synapseWorkspace{}, err
		}
		for _, row := range rows {
			raw := object(row)
			id := strings.ToLower(text(raw["id"]))
			if ids[id] {
				return synapseWorkspace{}, serviceDenied("duplicate_synapse_workspace")
			}
			ids[id] = true
			// An unrelated workspace need not expose its data-plane endpoint. Only
			// candidates matching the requested endpoint's workspace name are joined.
			if "https://"+last(id)+".dev.azuresynapse.net" != endpoint {
				continue
			}
			current, err := c.synapseWorkspaceRead(ctx, id)
			if err != nil {
				return synapseWorkspace{}, err
			}
			if current.endpoint != endpoint || !nativeConfigurationContains(synapseSnapshot(raw), synapseSnapshot(current.raw)) {
				return synapseWorkspace{}, serviceDenied("synapse_workspace_changed")
			}
			if match.id != "" {
				return synapseWorkspace{}, serviceDenied("ambiguous_synapse_workspace")
			}
			match = current
		}
		next = following
	}
	if match.id == "" {
		return synapseWorkspace{}, serviceDenied("synapse_workspace_outside_subscription")
	}
	return match, nil
}

func (c *synapseDataClient) invokeRead(ctx context.Context, op catalog.Operation, inv contracts.Invocation) (contracts.InvocationResult, error) {
	request, err := bindAzureREST(op, inv.Parameters)
	if err != nil {
		return contracts.InvocationResult{}, err
	}
	workspace, err := c.arm.synapseWorkspaceForEndpoint(ctx, text(inv.Parameters["endpoint"]))
	if err != nil {
		return contracts.InvocationResult{}, contracts.DependencyReadError(err)
	}
	var pool response
	poolID := ""
	if name, ok := inv.Parameters["sparkPoolName"].(string); ok {
		poolID = workspace.id + "/bigDataPools/" + name
		pool, err = c.arm.request(ctx, "GET", apiURL(poolID, synapseVersion))
		if err == nil {
			err = c.arm.synapseReadResponse(pool, poolID, synapseSparkType)
		}
		if err != nil {
			return contracts.InvocationResult{}, contracts.DependencyReadError(err)
		}
	}
	if inv.IdempotencyKey != "" {
		request.Headers = maps.Clone(request.Headers)
		if request.Headers == nil {
			request.Headers = map[string]string{}
		}
		request.Headers["x-ms-client-request-id"] = azureRequestID(inv.IdempotencyKey)
	}
	res, err := c.request(ctx, workspace, request)
	if err != nil {
		return contracts.InvocationResult{}, err
	}
	next, err := synapseDataResponse(workspace, op, inv.Parameters, res)
	if err != nil {
		return contracts.InvocationResult{}, err
	}
	// The owning resources must still be the same after the data-plane read.
	// No result escapes if an endpoint, identity or authored configuration moved.
	if poolID != "" {
		after, e := c.arm.request(ctx, "GET", apiURL(poolID, synapseVersion))
		if e == nil {
			e = c.arm.synapseReadResponse(after, poolID, synapseSparkType)
		}
		if e != nil {
			return contracts.InvocationResult{}, contracts.DependencyReadError(e)
		}
		if c.arm.privateConfiguration(synapseSnapshot(pool.data)) != c.arm.privateConfiguration(synapseSnapshot(after.data)) {
			return contracts.InvocationResult{}, contracts.DependencyReadError(serviceDenied("synapse_pool_changed"))
		}
	}
	after, err := c.arm.synapseWorkspaceRead(ctx, workspace.id)
	if err != nil {
		return contracts.InvocationResult{}, contracts.DependencyReadError(err)
	}
	if c.arm.privateConfiguration(synapseSnapshot(workspace.raw)) != c.arm.privateConfiguration(synapseSnapshot(after.raw)) {
		return contracts.InvocationResult{}, contracts.DependencyReadError(serviceDenied("synapse_workspace_changed"))
	}
	return contracts.InvocationResult{Data: safePayload(object(synapseDataSafeValue(res.data))), RequestID: res.requestID, NextToken: next}, nil
}

func (c *synapseDataClient) request(ctx context.Context, workspace synapseWorkspace, request catalog.RESTRequest) (response, error) {
	if request.Method != "GET" && request.Method != "DELETE" || len(request.Body) != 0 {
		return response{}, serviceDenied("synapse_data_mutation_not_implemented")
	}
	if request.Method == "DELETE" {
		valid := false
		for _, kind := range []string{synapseBatchType, synapseSessionType} {
			if _, _, err := c.arm.synapseDataIdentity(request.URL, kind); err == nil {
				valid = true
			}
		}
		if !valid {
			return response{}, serviceDenied("invalid_synapse_cancel_target")
		}
	}
	endpoint, err := synapseWorkspaceEndpoint(workspace.raw)
	if err != nil || endpoint != workspace.endpoint || c.arm.synapseMetadata(workspace.raw, synapseType) != nil || !strings.EqualFold(text(workspace.raw["id"]), workspace.id) {
		return response{}, serviceDenied("invalid_synapse_workspace_context")
	}
	validate := func(target string) error {
		u, err := url.Parse(target)
		if err != nil || u.Scheme+"://"+u.Host != workspace.endpoint || u.User != nil || u.Port() != "" || u.Fragment != "" || u.RawPath != "" {
			return serviceDenied("synapse_request_changed_workspace")
		}
		return nil
	}
	return c.arm.requestUsing(ctx, request.Method, request.URL, request.Body, request.Headers, validate, c.http, false)
}
