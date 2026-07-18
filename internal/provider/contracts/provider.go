package contracts

import (
	"context"
	"fmt"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
)

// CredentialSource decrypts credentials by connection identity. Provider DTOs
// keep secrets and SDK-specific credential objects outside the domain model.
type CredentialSource interface {
	Resolve(ctx context.Context, connectionID asset.ConnectionID) (Credential, error)
}

type Credential struct {
	Type         asset.CredentialType `json:"type"`
	Values       map[string]string    `json:"values"`
	ExpiresAt    *time.Time           `json:"expires_at,omitempty"`
	ConnectionID asset.ConnectionID   `json:"-"`
	Site         asset.ConnectionSite `json:"-"`
	Version      string               `json:"-"`
}

type CredentialUpdater interface {
	CompareAndSwap(context.Context, Credential, Credential) (Credential, error)
}

type OAuthFlowStatus string

const (
	OAuthFlowPending    OAuthFlowStatus = "pending"
	OAuthFlowAuthorized OAuthFlowStatus = "authorized"
	OAuthFlowFailed     OAuthFlowStatus = "failed"
	OAuthFlowExpired    OAuthFlowStatus = "expired"
	OAuthFlowConsumed   OAuthFlowStatus = "consumed"
)

type OAuthFlowView struct {
	ID               string          `json:"id"`
	Status           OAuthFlowStatus `json:"status"`
	AuthorizationURL string          `json:"authorization_url,omitempty"`
	ExpiresAt        time.Time       `json:"expires_at"`
	ErrorCode        string          `json:"error_code,omitempty"`
}

type OAuthFlowService interface {
	Start(context.Context, string, asset.ConnectionSite) (OAuthFlowView, error)
	Get(context.Context, string, string) (OAuthFlowView, error)
	Consume(context.Context, string, string, asset.ConnectionSite, func(Credential) error) error
}

type OAuthFlowError struct {
	Code    string
	Message string
}

func (e *OAuthFlowError) Error() string {
	if e == nil {
		return "OAuth flow failed"
	}
	if e.Message != "" {
		return e.Message
	}
	return fmt.Sprintf("OAuth flow failed: %s", e.Code)
}

// Invocation is the serializable boundary between the core and a provider SDK.
type Invocation struct {
	ConnectionID   asset.ConnectionID `json:"connection_id"`
	Operation      string             `json:"operation"`
	Scope          map[string]string  `json:"scope,omitempty"`
	Parameters     map[string]any     `json:"parameters,omitempty"`
	IdempotencyKey string             `json:"idempotency_key,omitempty"`
}

type InvocationResult struct {
	RequestID   string         `json:"request_id,omitempty"`
	OperationID string         `json:"operation_id,omitempty"`
	Data        map[string]any `json:"data,omitempty"`
	NextToken   string         `json:"next_token,omitempty"`
}

type Provider interface {
	Provider() asset.Provider
	Invoke(ctx context.Context, invocation Invocation) (InvocationResult, error)
}

type InventorySource struct {
	Name                 string            `json:"name"`
	RootScopeKinds       []asset.ScopeKind `json:"root_scope_kinds"`
	AuthoritativeDefault bool              `json:"authoritative_default"`
	// KindSpecific sources require a ResourceKind. Broad scans expand one
	// authoritative shard per matching resource spec instead of invoking the
	// source without a kind.
	KindSpecific bool `json:"kind_specific,omitempty"`
	// NetworkClosure means a selected VPC/vSwitch scan must include every
	// resource kind from this source so relationships can be closed over the
	// selected network, not only the VPC and vSwitch kinds themselves.
	NetworkClosure bool `json:"network_closure,omitempty"`
}

type ProviderDescriptor struct {
	Provider          asset.Provider     `json:"provider"`
	Sites             []ProviderSite     `json:"sites"`
	InventorySources  []InventorySource  `json:"inventory_sources"`
	CredentialSchemas []CredentialSchema `json:"credential_schemas"`
}

type ProviderSite struct {
	Value    asset.ConnectionSite `json:"value"`
	LabelKey string               `json:"label_key"`
}

type ConnectionSiteMetadata interface {
	ConnectionSites() []ProviderSite
}

// ResourceKindMetadata exposes provider-owned presentation and admission
// metadata without requiring every resource kind to have a full executable
// Provider Spec.
type ResourceKindMetadata interface {
	ResourceKinds() ([]asset.ResourceKind, string)
}

func ProviderSupportsRootScope(
	descriptors []ProviderDescriptor,
	provider asset.Provider,
	scope asset.ScopeKind,
) (supported bool, provable bool) {
	var matched *ProviderDescriptor
	for index := range descriptors {
		if descriptors[index].Provider != provider {
			continue
		}
		if matched != nil {
			return false, false
		}
		matched = &descriptors[index]
	}
	if matched == nil || len(matched.InventorySources) == 0 {
		return false, false
	}
	for _, source := range matched.InventorySources {
		if len(source.RootScopeKinds) == 0 {
			return true, true
		}
		for _, supportedScope := range source.RootScopeKinds {
			if supportedScope == scope {
				return true, true
			}
		}
	}
	return false, true
}

type CredentialField struct {
	Key       string `json:"key"`
	LabelKey  string `json:"label_key"`
	InputType string `json:"input_type"`
	Secret    bool   `json:"secret"`
	Required  bool   `json:"required"`
}

type CredentialSchema struct {
	Type     asset.CredentialType `json:"type"`
	LabelKey string               `json:"label_key"`
	Flow     string               `json:"flow,omitempty"`
	Fields   []CredentialField    `json:"fields"`
}

type CredentialMetadata interface {
	CredentialSchemas() []CredentialSchema
}

type RootScopeCandidate struct {
	Kind     asset.ScopeKind `json:"kind"`
	NativeID string          `json:"native_id"`
	Name     string          `json:"name"`
	Location string          `json:"location,omitempty"`
}

type ConnectionIdentity struct {
	Partition         string               `json:"partition"`
	TenantID          string               `json:"tenant_id,omitempty"`
	Principal         string               `json:"principal"`
	RootScopes        []RootScopeCandidate `json:"root_scopes"`
	CredentialVersion string               `json:"-"`
}

type ConnectionBootstrapper interface {
	ValidateConnection(context.Context, Credential) (ConnectionIdentity, error)
}

type DiscoveredRegion struct {
	RegionID string            `json:"region_id"`
	Name     string            `json:"name,omitempty"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

type RegionDiscoverer interface {
	DiscoverRegions(context.Context, asset.ConnectionID) ([]DiscoveredRegion, error)
}

type InventoryMetadata interface {
	InventorySources() []InventorySource
}

// InventorySourceSelector allows a provider to override a spec's declared
// discovery source for scanning. This keeps direct product API metadata
// available for unsupported-resource fallback and lifecycle actions.
type InventorySourceSelector interface {
	InventorySourceForResourceKind(asset.ResourceKind, string) string
}

type ErrorClassifier interface {
	Classify(err error) execution.ProviderError
}

type ProviderCallError struct {
	Provider   execution.ProviderError
	RetryAfter time.Duration
	Cause      error
}

type CredentialValidationError struct {
	Code    string
	Message string
	Cause   error
}

func NewCredentialValidationError(code, message string, cause error) *CredentialValidationError {
	return &CredentialValidationError{Code: code, Message: message, Cause: cause}
}

func (e *CredentialValidationError) Error() string {
	if e == nil || e.Message == "" {
		return "cloud credential validation failed"
	}
	return e.Message
}

func (e *CredentialValidationError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

const SafeProviderValidationMessage = "The cloud provider could not complete the credential validation request."

const SafeProviderTransportMessage = SafeProviderValidationMessage

// SanitizeProviderError keeps only allowlisted structured diagnostics.
// Provider messages are arbitrary SDK input and must be sanitized before audit,
// log, persistence, or API use. The sole exception is the authenticated,
// explicit connection-validation response, which may return the original
// message synchronously for diagnosis without storing it.
func SanitizeProviderError(value execution.ProviderError) execution.ProviderError {
	value.Message = SafeProviderValidationMessage
	return value
}

func (e *ProviderCallError) Error() string {
	if e == nil {
		return "provider call failed"
	}
	message := e.Provider.Message
	switch {
	case e.Provider.Code != "" && message != "":
		message = e.Provider.Code + ": " + message
	case e.Provider.Code != "":
		message = e.Provider.Code
	case message == "":
		message = "provider call failed"
	}
	if e.Provider.RequestID != "" {
		message += "; request_id=" + e.Provider.RequestID
	}
	return message
}

func (e *ProviderCallError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}
