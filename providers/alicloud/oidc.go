package alicloud

import (
	"context"
	"time"

	"github.com/alibabacloud-go/tea/tea"
	cloudcredentials "github.com/aliyun/credentials-go/credentials"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

// oidcResolveTimeout bounds one workload identity resolution. The SDK asks for
// credentials without a context and cached clients outlive the request that
// built them, so resolution owns its lifecycle instead of borrowing a
// request's: a cancelled request must not fail the next call that signs.
const oidcResolveTimeout = 30 * time.Second

type oidcCredential struct {
	source *contracts.DynamicCredential
}

func (c *oidcCredential) GetCredential() (*cloudcredentials.CredentialModel, error) {
	ctx, cancel := context.WithTimeout(context.Background(), oidcResolveTimeout)
	defer cancel()
	v, err := c.source.Resolve(ctx, "")
	if err != nil {
		return nil, err
	}
	return &cloudcredentials.CredentialModel{Type: tea.String("sts"), AccessKeyId: tea.String(v.AccessKeyID), AccessKeySecret: tea.String(v.SecretAccessKey), SecurityToken: tea.String(v.SessionToken), ProviderName: tea.String("steward_oidc")}, nil
}
func (c *oidcCredential) GetAccessKeyId() (*string, error) {
	v, e := c.GetCredential()
	if e != nil {
		return nil, e
	}
	return v.AccessKeyId, nil
}
func (c *oidcCredential) GetAccessKeySecret() (*string, error) {
	v, e := c.GetCredential()
	if e != nil {
		return nil, e
	}
	return v.AccessKeySecret, nil
}
func (c *oidcCredential) GetSecurityToken() (*string, error) {
	v, e := c.GetCredential()
	if e != nil {
		return nil, e
	}
	return v.SecurityToken, nil
}
func (c *oidcCredential) GetBearerToken() *string { return nil }
func (c *oidcCredential) GetType() *string        { return tea.String("sts") }
