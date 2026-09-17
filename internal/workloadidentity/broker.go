package workloadidentity

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/requestmeta"
	"github.com/loomx-ai/steward/internal/persistence"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

type validationKey struct{}

func ForValidation(ctx context.Context) context.Context {
	return context.WithValue(ctx, validationKey{}, true)
}

type Broker struct {
	Issuer       *Issuer
	repositories persistence.Repositories
	http         *http.Client
	mu           sync.Mutex
	sessions     map[string]*session
}
type session struct {
	mu     sync.Mutex
	tokens map[string]contracts.TemporaryCredential
}

func NewBroker(issuer *Issuer, repositories persistence.Repositories) *Broker {
	return &Broker{Issuer: issuer, repositories: repositories, sessions: map[string]*session{}, http: &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

func field(key, label string, required bool) contracts.CredentialField {
	return contracts.CredentialField{Key: key, LabelKey: "credentials." + label, InputType: "text", Required: required}
}

func Schema(provider asset.Provider) contracts.CredentialSchema {
	s := contracts.CredentialSchema{Type: asset.CredentialOIDC, LabelKey: "credentials.oidc", Flow: "workload_identity"}
	switch provider {
	case asset.ProviderAWS:
		s.Fields = []contracts.CredentialField{field("role_arn", "roleArn", true), field("write_role_arn", "writeRoleArn", false)}
	case asset.ProviderAliCloud:
		s.Fields = []contracts.CredentialField{field("role_arn", "roleArn", true), field("oidc_provider_arn", "oidcProviderArn", true), field("write_role_arn", "writeRoleArn", false)}
	case asset.ProviderGCP:
		s.Fields = []contracts.CredentialField{field("project_id", "projectId", true), field("workload_provider", "workloadProvider", true), field("service_account_email", "serviceAccountEmail", true), field("write_service_account_email", "writeServiceAccountEmail", false), field("identity_group_parent", "identityGroupParent", false), field("firewall_policy_parent", "firewallPolicyParent", false)}
	case asset.ProviderAzure:
		s.Fields = []contracts.CredentialField{field("subscription_id", "subscriptionId", true), field("tenant_id", "tenantId", true), field("client_id", "clientId", true), field("write_client_id", "writeClientId", false)}
	default:
		return contracts.CredentialSchema{}
	}
	s.Fields = append(s.Fields, field("audience", "oidcAudience", false))
	return s
}

var awsRole = regexp.MustCompile(`^arn:aws:iam::[0-9]{12}:role/[A-Za-z0-9_+=,.@/-]+$`)
var aliRole = regexp.MustCompile(`^acs:ram::[0-9]+:role/[A-Za-z0-9_+=,.@/-]+$`)
var aliProvider = regexp.MustCompile(`^acs:ram::[0-9]+:oidc-provider/[A-Za-z0-9_.-]+$`)
var gcpProvider = regexp.MustCompile(`^projects/[0-9]+/locations/global/workloadIdentityPools/[a-z0-9-]+/providers/[a-z0-9-]+$`)
var serviceAccount = regexp.MustCompile(`^[a-zA-Z0-9_-]+@[a-zA-Z0-9.-]+\.iam\.gserviceaccount\.com$`)
var uuid = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func ValidateConfig(provider asset.Provider, c contracts.Credential) error {
	bad := func() error {
		return contracts.NewCredentialValidationError("credential_fields_invalid", "The OIDC connection configuration is incomplete or invalid.", nil)
	}
	schema := Schema(provider)
	if schema.Type == "" || c.ExpiresAt != nil {
		return bad()
	}
	allowed := map[string]bool{}
	for _, f := range schema.Fields {
		allowed[f.Key] = true
		if f.Required && strings.TrimSpace(c.Values[f.Key]) == "" {
			return bad()
		}
	}
	for k, v := range c.Values {
		if !allowed[k] || len(v) > 1024 || strings.TrimSpace(v) != v || strings.ContainsAny(v, "\r\n\x00") {
			return bad()
		}
	}
	v := c.Values
	matchOptional := func(re *regexp.Regexp, key string) bool { return v[key] == "" || re.MatchString(v[key]) }
	switch provider {
	case asset.ProviderAWS:
		if !awsRole.MatchString(v["role_arn"]) || !matchOptional(awsRole, "write_role_arn") {
			return bad()
		}
		if v["write_role_arn"] != "" && strings.Split(v["role_arn"], ":")[4] != strings.Split(v["write_role_arn"], ":")[4] {
			return bad()
		}
	case asset.ProviderAliCloud:
		if !aliRole.MatchString(v["role_arn"]) || !aliProvider.MatchString(v["oidc_provider_arn"]) || !matchOptional(aliRole, "write_role_arn") {
			return bad()
		}
		account := strings.Split(v["role_arn"], ":")[3]
		if account != strings.Split(v["oidc_provider_arn"], ":")[3] || (v["write_role_arn"] != "" && account != strings.Split(v["write_role_arn"], ":")[3]) {
			return bad()
		}
	case asset.ProviderGCP:
		if !gcpProvider.MatchString(v["workload_provider"]) || !serviceAccount.MatchString(v["service_account_email"]) || !matchOptional(serviceAccount, "write_service_account_email") {
			return bad()
		}
	case asset.ProviderAzure:
		if !uuid.MatchString(v["tenant_id"]) || !uuid.MatchString(v["subscription_id"]) || !uuid.MatchString(v["client_id"]) || !matchOptional(uuid, "write_client_id") {
			return bad()
		}
	}
	return nil
}

func Audience(provider asset.Provider, values map[string]string) string {
	if values["audience"] != "" {
		return values["audience"]
	}
	switch provider {
	case asset.ProviderAWS:
		return "sts.amazonaws.com"
	case asset.ProviderAliCloud:
		return "sts.aliyuncs.com"
	case asset.ProviderAzure:
		return "api://AzureADTokenExchange"
	default:
		return "steward.workload.identity"
	}
}

func (b *Broker) Bind(ctx context.Context, record asset.ConnectionCredential, c contracts.Credential) (contracts.Credential, error) {
	if err := ValidateConfig(record.Provider, c); err != nil {
		return contracts.Credential{}, err
	}
	work := requestmeta.WorkloadFrom(ctx)
	validation, _ := ctx.Value(validationKey{}).(bool)
	if (work.Phase != "read" && work.Phase != "write") || (validation && work.Phase != "read") {
		return contracts.Credential{}, fmt.Errorf("invalid workload phase")
	}
	values := map[string]string{}
	for k, v := range c.Values {
		values[k] = v
	}
	if work.Phase == "write" {
		for _, key := range []string{"role_arn", "client_id", "service_account_email"} {
			if values["write_"+key] != "" {
				values[key] = values["write_"+key]
			}
		}
	}
	sum := sha256.Sum256([]byte(record.Ciphertext))
	key := fmt.Sprintf("%s:%x:%s:%s:%t", record.ConnectionID, sum, work.Phase, work.RunID, validation)
	b.mu.Lock()
	s := b.sessions[key]
	if s == nil {
		// Bounded cache: evict one lookup entry; in-flight callers keep their session.
		if len(b.sessions) >= 256 {
			for k := range b.sessions {
				delete(b.sessions, k)
				break
			}
		}
		s = &session{tokens: map[string]contracts.TemporaryCredential{}}
		b.sessions[key] = s
	}
	b.mu.Unlock()
	c.Values = values
	check := func(callCtx context.Context) error {
		connection, err := b.repositories.Connections().GetConnection(callCtx, record.ConnectionID)
		if err != nil || connection.Provider != record.Provider || connection.Status == asset.ConnectionDeleted || (!validation && connection.Status != asset.ConnectionActive) {
			return fmt.Errorf("OIDC connection is no longer authorized")
		}
		current, err := b.repositories.Credentials().GetCredential(callCtx, record.ConnectionID)
		if err != nil || current.Ciphertext != record.Ciphertext || current.Type != asset.CredentialOIDC {
			return fmt.Errorf("OIDC connection configuration changed")
		}
		return nil
	}
	c.Dynamic = &contracts.DynamicCredential{Key: key, Resolve: func(callCtx context.Context, scope string) (contracts.TemporaryCredential, error) {
		if err := callCtx.Err(); err != nil {
			return contracts.TemporaryCredential{}, err
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		if err := check(callCtx); err != nil {
			return contracts.TemporaryCredential{}, err
		}
		if cached, ok := s.tokens[scope]; ok && cached.ExpiresAt.After(b.Issuer.now().Add(time.Minute)) {
			return cached, nil
		}
		assertion, err := b.Issuer.sign(callCtx, string(record.ConnectionID), string(record.Provider), Audience(record.Provider, values), work.Phase, work.RunID)
		if err != nil {
			return contracts.TemporaryCredential{}, err
		}
		token, err := b.exchange(callCtx, record.Provider, values, assertion, scope)
		if err != nil {
			return contracts.TemporaryCredential{}, err
		}
		if !token.ExpiresAt.After(b.Issuer.now().Add(time.Minute)) {
			return contracts.TemporaryCredential{}, fmt.Errorf("OIDC exchange returned an expired credential")
		}
		// A replacement or revocation may have happened during the network exchange.
		if err := check(callCtx); err != nil {
			return contracts.TemporaryCredential{}, err
		}
		s.tokens[scope] = token
		return token, nil
	}}
	return c, nil
}

type Trust struct {
	Issuer       string `json:"issuer"`
	JWKSURI      string `json:"jwks_uri"`
	Audience     string `json:"audience"`
	ReadSubject  string `json:"read_subject"`
	WriteSubject string `json:"write_subject"`
}

func (b *Broker) Trust(id asset.ConnectionID, provider asset.Provider, c contracts.Credential) Trust {
	return Trust{Issuer: b.Issuer.URL, JWKSURI: b.Issuer.URL + "/.well-known/jwks", Audience: Audience(provider, c.Values), ReadSubject: b.Issuer.Subject(string(id), "read"), WriteSubject: b.Issuer.Subject(string(id), "write")}
}
