package asset

import (
	"errors"
	"strings"
	"time"
)

type (
	ConnectionID   string
	ScopeID        string
	ResourceKindID string
	AssetID        string
	ObservationID  string
	ScanTaskID     string
	ScanRunID      = ScanTaskID
	ScanShardID    string
)

type Provider string

const (
	ProviderAliCloud Provider = "alicloud"
	ProviderAWS      Provider = "aws"
	ProviderGCP      Provider = "gcp"
)

type ConnectionSite string

const (
	ConnectionSiteCN   ConnectionSite = "cn"
	ConnectionSiteINTL ConnectionSite = "intl"
)

type Capability string

const (
	CapabilityIndexed    Capability = "indexed"
	CapabilityDetailed   Capability = "detailed"
	CapabilityRelated    Capability = "related"
	CapabilityGoverned   Capability = "governed"
	CapabilityActionable Capability = "actionable"
)

type CapabilitySet []Capability

func (s CapabilitySet) Has(want Capability) bool {
	for _, capability := range s {
		if capability == want {
			return true
		}
	}
	return false
}

type ConnectionStatus string

var ErrConnectionNotValidated = errors.New("cloud connection has not passed validation")

const (
	ConnectionUnverified ConnectionStatus = "unverified"
	ConnectionActive     ConnectionStatus = "active"
	ConnectionInvalid    ConnectionStatus = "invalid"
	ConnectionDeleted    ConnectionStatus = "deleted"
)

type CredentialType string

const (
	CredentialAliCloudAccessKey CredentialType = "access_key"
	CredentialAliCloudSTS       CredentialType = "sts"
	CredentialAliCloudOAuth     CredentialType = "oauth"
	CredentialAWSAccessKey      CredentialType = "access_key"
	CredentialAWSSession        CredentialType = "session"
	CredentialGCPServiceAccount CredentialType = "service_account"
)

type CloudConnection struct {
	ID                  ConnectionID     `json:"id"`
	Name                string           `json:"name"`
	Provider            Provider         `json:"provider"`
	Site                ConnectionSite   `json:"site,omitempty"`
	Partition           string           `json:"partition"`
	TenantID            string           `json:"tenant_id,omitempty"`
	Principal           string           `json:"principal"`
	Status              ConnectionStatus `json:"status"`
	EnabledCapabilities CapabilitySet    `json:"enabled_capabilities"`
	CreatedAt           time.Time        `json:"created_at"`
	UpdatedAt           time.Time        `json:"updated_at"`
	DeletedAt           *time.Time       `json:"deleted_at,omitempty"`
}

type ConnectionCredential struct {
	ConnectionID    ConnectionID   `json:"connection_id"`
	Provider        Provider       `json:"provider"`
	Type            CredentialType `json:"type"`
	EnvelopeVersion int            `json:"envelope_version"`
	Nonce           string         `json:"nonce"`
	Ciphertext      string         `json:"ciphertext"`
	ExpiresAt       *time.Time     `json:"expires_at,omitempty"`
	CreatedAt       time.Time      `json:"created_at"`
	UpdatedAt       time.Time      `json:"updated_at"`
}

type RegionOrigin string

const (
	RegionOriginAPI    RegionOrigin = "api"
	RegionOriginManual RegionOrigin = "manual"
)

type RegionLifecycle string

const (
	RegionActive   RegionLifecycle = "active"
	RegionRetired  RegionLifecycle = "retired"
	RegionExcluded RegionLifecycle = "excluded"
)

type ConnectionRegion struct {
	ID             string          `json:"id"`
	Revision       uint64          `json:"-"`
	ConnectionID   ConnectionID    `json:"connection_id"`
	RegionID       string          `json:"region_id"`
	DiscoveredName string          `json:"discovered_name,omitempty"`
	NameOverride   string          `json:"name_override,omitempty"`
	Origin         RegionOrigin    `json:"origin"`
	Lifecycle      RegionLifecycle `json:"lifecycle"`
	FirstSeenAt    *time.Time      `json:"first_seen_at,omitempty"`
	LastSeenAt     *time.Time      `json:"last_seen_at,omitempty"`
	CreatedAt      time.Time       `json:"created_at"`
	UpdatedAt      time.Time       `json:"updated_at"`
}

func (r ConnectionRegion) EffectiveName() string {
	if name := strings.TrimSpace(r.NameOverride); name != "" {
		return name
	}
	if name := strings.TrimSpace(r.DiscoveredName); name != "" {
		return name
	}
	return strings.TrimSpace(r.RegionID)
}

type ScopeKind string

const (
	ScopeOrganization  ScopeKind = "organization"
	ScopeFolder        ScopeKind = "folder"
	ScopeTenant        ScopeKind = "tenant"
	ScopeAccount       ScopeKind = "account"
	ScopeSubscription  ScopeKind = "subscription"
	ScopeProject       ScopeKind = "project"
	ScopeResourceGroup ScopeKind = "resource_group"
	ScopeRegion        ScopeKind = "region"
	ScopeZone          ScopeKind = "zone"
	ScopeGlobal        ScopeKind = "global"
)

type Scope struct {
	ID             ScopeID      `json:"id"`
	ConnectionID   ConnectionID `json:"connection_id"`
	ParentID       ScopeID      `json:"parent_id,omitempty"`
	SupersededByID ScopeID      `json:"-"`
	Kind           ScopeKind    `json:"kind"`
	NativeID       string       `json:"native_id"`
	Name           string       `json:"name"`
	Location       string       `json:"location,omitempty"`
	CreatedAt      time.Time    `json:"created_at"`
	UpdatedAt      time.Time    `json:"updated_at"`
}

type ResourceKind struct {
	ID                  ResourceKindID               `json:"id"`
	Provider            Provider                     `json:"provider"`
	NativeType          string                       `json:"native_type"`
	Class               string                       `json:"class,omitempty"`
	ScopeKinds          []ScopeKind                  `json:"scope_kinds"`
	Capabilities        CapabilitySet                `json:"capabilities"`
	DisplayName         string                       `json:"display_name"`
	DisplayNames        map[string]string            `json:"display_names,omitempty"`
	FieldDisplayNames   map[string]map[string]string `json:"field_display_names,omitempty"`
	Properties          []ResourceProperty           `json:"properties,omitempty"`
	Icon                string                       `json:"icon,omitempty"`
	ConsoleLinkTemplate string                       `json:"console_link_template,omitempty"`
	SummaryFields       []string                     `json:"summary_fields,omitempty"`
	BundleRevision      string                       `json:"bundle_revision"`
}

type ResourceProperty struct {
	Path         string            `json:"path"`
	Type         string            `json:"type"`
	DisplayNames map[string]string `json:"display_names,omitempty"`
	Enum         []any             `json:"enum,omitempty"`
	Operators    []string          `json:"operators,omitempty"`
}

type Identity struct {
	Provider     Provider     `json:"provider"`
	Partition    string       `json:"partition"`
	ConnectionID ConnectionID `json:"connection_id"`
	NativeType   string       `json:"native_type"`
	NativeID     string       `json:"native_id"`
	ScopeKey     string       `json:"scope_key,omitempty"`
}

type Asset struct {
	ID                   AssetID           `json:"id"`
	Identity             Identity          `json:"identity"`
	ScopeID              ScopeID           `json:"scope_id,omitempty"`
	ResourceKindID       ResourceKindID    `json:"resource_kind_id"`
	CurrentObservationID ObservationID     `json:"current_observation_id"`
	Name                 string            `json:"name"`
	State                string            `json:"state"`
	Location             string            `json:"location,omitempty"`
	Dirty                bool              `json:"dirty"`
	Tags                 map[string]string `json:"tags"`
	Capabilities         CapabilitySet     `json:"capabilities"`
	Normalized           map[string]any    `json:"normalized"`
	FirstSeenAt          time.Time         `json:"first_seen_at"`
	LastSeenAt           time.Time         `json:"last_seen_at"`
	ClosedAt             *time.Time        `json:"closed_at,omitempty"`
	DeletedAt            *time.Time        `json:"deleted_at,omitempty"`
}

type Observation struct {
	ID             ObservationID  `json:"id"`
	AssetID        AssetID        `json:"asset_id"`
	ScanRunID      ScanRunID      `json:"scan_run_id"`
	ScanShardID    ScanShardID    `json:"scan_shard_id"`
	ObservedAt     time.Time      `json:"observed_at"`
	Source         string         `json:"source"`
	SchemaRevision string         `json:"schema_revision"`
	Normalized     map[string]any `json:"normalized"`
	Raw            map[string]any `json:"raw"`
	ContentHash    string         `json:"content_hash"`
	Authoritative  bool           `json:"authoritative"`
	Priority       int            `json:"priority"`
}

type ScanStatus string

const (
	ScanPending     ScanStatus = "pending"
	ScanRunning     ScanStatus = "running"
	ScanPausing     ScanStatus = "pausing"
	ScanPaused      ScanStatus = "paused"
	ScanCanceling   ScanStatus = "canceling"
	ScanCanceled    ScanStatus = "canceled"
	ScanReconciling ScanStatus = "reconciling"
	ScanSucceeded   ScanStatus = "succeeded"
	ScanPartial     ScanStatus = "partial"
	ScanFailed      ScanStatus = "failed"
)

type ShardStatus string

const (
	ShardPending   ShardStatus = "pending"
	ShardRunning   ShardStatus = "running"
	ShardSucceeded ShardStatus = "succeeded"
	ShardSkipped   ShardStatus = "skipped"
	ShardFailed    ShardStatus = "failed"
	ShardBlocked   ShardStatus = "blocked"
	ShardPaused    ShardStatus = "paused"
	ShardCanceled  ShardStatus = "canceled"
)

type SkipReason string

const (
	SkipProductUnsupported        SkipReason = "product_unsupported"
	SkipProviderRegionUnavailable SkipReason = "provider_region_unavailable"
	SkipDependencyRetained        SkipReason = "dependency_retained"
)

type Coverage struct {
	Source         string         `json:"source"`
	TargetKey      string         `json:"target_key,omitempty"`
	ScopeID        ScopeID        `json:"scope_id"`
	ResourceKindID ResourceKindID `json:"resource_kind_id,omitempty"`
	Authoritative  bool           `json:"authoritative"`
	Complete       bool           `json:"complete"`
	ItemCount      int            `json:"item_count"`
	FreshAt        time.Time      `json:"fresh_at"`
	SkipReason     SkipReason     `json:"skip_reason,omitempty"`
	FailureReason  string         `json:"failure_reason,omitempty"`
}

type ScanScopeMode string

const (
	ScanAllActiveRegions ScanScopeMode = "all_active_regions"
	ScanSelectedRegions  ScanScopeMode = "selected_regions"
	ScanSelectedNetworks ScanScopeMode = "selected_networks"
)

type ScanTargetKind string

const (
	ScanTargetRegion  ScanTargetKind = "region"
	ScanTargetGlobal  ScanTargetKind = "global"
	ScanTargetVPC     ScanTargetKind = "vpc"
	ScanTargetVSwitch ScanTargetKind = "vswitch"
)

type ScanTarget struct {
	Key            string         `json:"key"`
	Kind           ScanTargetKind `json:"kind"`
	RegionID       string         `json:"region_id"`
	RegionName     string         `json:"region_name,omitempty"`
	NativeID       string         `json:"native_id,omitempty"`
	Name           string         `json:"name,omitempty"`
	ParentNativeID string         `json:"parent_native_id,omitempty"`
}

type ScanTask struct {
	ID               ScanTaskID       `json:"id"`
	ConnectionID     ConnectionID     `json:"connection_id"`
	Status           ScanStatus       `json:"status"`
	ScopeMode        ScanScopeMode    `json:"scope_mode"`
	RequestedBy      string           `json:"requested_by"`
	Targets          []ScanTarget     `json:"targets"`
	ResourceKindIDs  []ResourceKindID `json:"resource_kind_ids,omitempty"`
	RetryGeneration  int              `json:"retry_generation"`
	RetryCount       int              `json:"retry_count"`
	ControlVersion   uint64           `json:"control_version"`
	CompletionStatus ScanStatus       `json:"completion_status,omitempty"`
	CreatedAt        time.Time        `json:"created_at"`
	StartedAt        *time.Time       `json:"started_at,omitempty"`
	FinishedAt       *time.Time       `json:"finished_at,omitempty"`
	PausedAt         *time.Time       `json:"paused_at,omitempty"`
	CanceledAt       *time.Time       `json:"canceled_at,omitempty"`
}

type ScanRun = ScanTask

type ScanShard struct {
	ID              ScanShardID    `json:"id"`
	ScanTaskID      ScanTaskID     `json:"scan_task_id"`
	ScanRunID       ScanRunID      `json:"-"`
	TargetKey       string         `json:"target_key"`
	RetryGeneration int            `json:"retry_generation"`
	Provider        Provider       `json:"provider"`
	Source          string         `json:"source"`
	RegionID        string         `json:"region_id"`
	ScopeID         ScopeID        `json:"scope_id"`
	ResourceKindID  ResourceKindID `json:"resource_kind_id,omitempty"`
	Authoritative   bool           `json:"authoritative"`
	Status          ShardStatus    `json:"status"`
	Coverage        Coverage       `json:"coverage"`
	CreatedAt       time.Time      `json:"created_at"`
	StartedAt       *time.Time     `json:"started_at,omitempty"`
	FinishedAt      *time.Time     `json:"finished_at,omitempty"`
}
