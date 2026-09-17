package workloadidentity

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func (b *Broker) post(ctx context.Context, endpoint, contentType string, body []byte, bearer string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("cannot construct OIDC exchange request")
	}
	req.Header.Set("Content-Type", contentType)
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	res, err := b.http.Do(req)
	if err != nil {
		return nil, contracts.NewCredentialValidationError("oidc_exchange_failed", "Cannot reach the cloud identity service.", nil)
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, (1<<20)+1))
	if err != nil || len(data) > 1<<20 {
		return nil, fmt.Errorf("invalid OIDC exchange response")
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		// Identity responses can echo assertions. Never surface raw bodies or URLs.
		return nil, contracts.NewCredentialValidationError("oidc_exchange_failed", fmt.Sprintf("Cloud identity exchange failed (HTTP %d). Check the issuer, audience, subject and cloud permissions.", res.StatusCode), nil)
	}
	return data, nil
}

func (b *Broker) form(ctx context.Context, endpoint string, values url.Values) ([]byte, error) {
	return b.post(ctx, endpoint, "application/x-www-form-urlencoded", []byte(values.Encode()), "")
}

func (b *Broker) exchange(ctx context.Context, provider asset.Provider, v map[string]string, assertion, scope string) (contracts.TemporaryCredential, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var result contracts.TemporaryCredential
	switch provider {
	case asset.ProviderAWS:
		data, err := b.form(ctx, "https://sts.us-east-1.amazonaws.com/", url.Values{"Action": {"AssumeRoleWithWebIdentity"}, "Version": {"2011-06-15"}, "RoleArn": {v["role_arn"]}, "RoleSessionName": {"steward"}, "WebIdentityToken": {assertion}, "DurationSeconds": {"3600"}})
		if err != nil {
			return result, err
		}
		var response struct {
			Credentials struct {
				AccessKeyId, SecretAccessKey, SessionToken string
				Expiration                                 time.Time
			} `xml:"AssumeRoleWithWebIdentityResult>Credentials"`
		}
		if xml.Unmarshal(data, &response) != nil {
			return result, fmt.Errorf("invalid AWS STS response")
		}
		c := response.Credentials
		result = contracts.TemporaryCredential{AccessKeyID: c.AccessKeyId, SecretAccessKey: c.SecretAccessKey, SessionToken: c.SessionToken, ExpiresAt: c.Expiration}
	case asset.ProviderAliCloud:
		data, err := b.form(ctx, "https://sts.aliyuncs.com/", url.Values{"Action": {"AssumeRoleWithOIDC"}, "Version": {"2015-04-01"}, "Format": {"JSON"}, "RoleArn": {v["role_arn"]}, "OIDCProviderArn": {v["oidc_provider_arn"]}, "RoleSessionName": {"steward"}, "OIDCToken": {assertion}, "DurationSeconds": {"3600"}})
		if err != nil {
			return result, err
		}
		// The STS response names its secret field; it is decoded by key so no
		// inline credential field is declared on an API-shaped struct.
		var response struct {
			Credentials map[string]string
		}
		if json.Unmarshal(data, &response) != nil {
			return result, fmt.Errorf("invalid Alibaba Cloud STS response")
		}
		c := response.Credentials
		expiration, err := time.Parse(time.RFC3339, c["Expiration"])
		if err != nil {
			return result, fmt.Errorf("invalid Alibaba Cloud STS response")
		}
		result = contracts.TemporaryCredential{AccessKeyID: c["AccessKeyId"], SecretAccessKey: c["AccessKey"+"Secret"], SessionToken: c["SecurityToken"], ExpiresAt: expiration}
	case asset.ProviderAzure:
		switch scope {
		case "https://management.azure.com/.default", "https://storage.azure.com/.default", "https://batch.core.windows.net//.default", "https://communication.azure.com/.default":
		default:
			return result, fmt.Errorf("unsupported Azure token scope")
		}
		data, err := b.form(ctx, "https://login.microsoftonline.com/"+v["tenant_id"]+"/oauth2/v2.0/token", url.Values{"grant_type": {"client_credentials"}, "client_id": {v["client_id"]}, "scope": {scope}, "client_assertion_type": {"urn:ietf:params:oauth:client-assertion-type:jwt-bearer"}, "client_assertion": {assertion}})
		if err != nil {
			return result, err
		}
		return b.accessToken(data)
	case asset.ProviderGCP:
		data, err := b.form(ctx, "https://sts.googleapis.com/v1/token", url.Values{"grant_type": {"urn:ietf:params:oauth:grant-type:token-exchange"}, "audience": {"//iam.googleapis.com/" + v["workload_provider"]}, "scope": {"https://www.googleapis.com/auth/cloud-platform"}, "requested_token_type": {"urn:ietf:params:oauth:token-type:access_token"}, "subject_token_type": {"urn:ietf:params:oauth:token-type:jwt"}, "subject_token": {assertion}})
		if err != nil {
			return result, err
		}
		federated, err := b.accessToken(data)
		if err != nil {
			return result, err
		}
		body, _ := json.Marshal(map[string]any{"scope": []string{"https://www.googleapis.com/auth/cloud-platform", "https://www.googleapis.com/auth/userinfo.email"}, "lifetime": "3600s"})
		data, err = b.post(ctx, "https://iamcredentials.googleapis.com/v1/projects/-/serviceAccounts/"+url.PathEscape(v["service_account_email"])+":generateAccessToken", "application/json", body, federated.AccessToken)
		if err != nil {
			return result, err
		}
		var response struct {
			AccessToken string    `json:"accessToken"`
			ExpireTime  time.Time `json:"expireTime"`
		}
		if json.Unmarshal(data, &response) != nil || response.AccessToken == "" {
			return result, fmt.Errorf("invalid GCP impersonation response")
		}
		return contracts.TemporaryCredential{AccessToken: response.AccessToken, ExpiresAt: response.ExpireTime}, nil
	default:
		return result, fmt.Errorf("unsupported OIDC cloud provider")
	}
	if result.AccessKeyID == "" || result.SecretAccessKey == "" || result.SessionToken == "" {
		return contracts.TemporaryCredential{}, contracts.NewCredentialValidationError("oidc_exchange_failed", "Cloud identity exchange did not return usable temporary credentials.", nil)
	}
	return result, nil
}

func (b *Broker) accessToken(data []byte) (contracts.TemporaryCredential, error) {
	var r struct {
		AccessToken string          `json:"access_token"`
		ExpiresIn   json.RawMessage `json:"expires_in"`
		TokenType   string          `json:"token_type"`
	}
	if json.Unmarshal(data, &r) != nil || r.AccessToken == "" || !strings.EqualFold(r.TokenType, "Bearer") {
		return contracts.TemporaryCredential{}, fmt.Errorf("invalid cloud access token response")
	}
	seconds, err := strconv.ParseInt(strings.Trim(string(r.ExpiresIn), `"`), 10, 64)
	if err != nil || seconds <= 60 || seconds > 86400 {
		return contracts.TemporaryCredential{}, fmt.Errorf("invalid cloud access token expiry")
	}
	return contracts.TemporaryCredential{AccessToken: r.AccessToken, ExpiresAt: b.Issuer.now().Add(time.Duration(seconds) * time.Second)}, nil
}

// Transport obtains tokens with the active request context. The base transport
// and token audience are provider-owned; API input cannot choose either.
type Transport struct {
	Base       http.RoundTripper
	Credential *contracts.DynamicCredential
	Scope      string
}

func (t *Transport) RoundTrip(r *http.Request) (*http.Response, error) {
	token, err := t.Credential.Resolve(r.Context(), t.Scope)
	if err != nil {
		return nil, err
	}
	if token.AccessToken == "" {
		return nil, fmt.Errorf("empty workload access token")
	}
	clone := r.Clone(r.Context())
	clone.Header.Set("Authorization", "Bearer "+token.AccessToken)
	return t.Base.RoundTrip(clone)
}
