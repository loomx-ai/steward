package alicloud

import (
	"context"
	"github.com/alibabacloud-go/tea/tea"
	cloudcredentials "github.com/aliyun/credentials-go/credentials"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

type oidcCredential struct {
	ctx    context.Context
	source *contracts.DynamicCredential
}

func (c *oidcCredential) GetCredential() (*cloudcredentials.CredentialModel, error) {
	v, err := c.source.Resolve(c.ctx, "")
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
