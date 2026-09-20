package aws

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	awssso "github.com/aws/aws-sdk-go-v2/service/sso"
	awsssooidc "github.com/aws/aws-sdk-go-v2/service/ssooidc"
	ssooidctypes "github.com/aws/aws-sdk-go-v2/service/ssooidc/types"
	"github.com/aws/smithy-go"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/credential/oauth"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const (
	oauthLabel = "AWS"
	// IAM Identity Center scopes a public client to the accounts and roles the
	// signed-in user is assigned; there is no broader grant to ask for.
	oauthScope       = "sso:account:access"
	oauthClientName  = "steward"
	startURLKey      = "start_url"
	ssoRegionKey     = "sso_region"
	ssoClientIDKey   = "sso_client_id"
	ssoClientSecret  = "sso_client_secret"
	ssoClientExpire  = "sso_client_secret_expire"
	ssoTokenEndpoint = "sso_token_endpoint"
	accountIDKey     = "account_id"
	roleNameKey      = "role_name"
	stsExpireKey     = "sts_expiration"
)

var (
	regionPattern    = regexp.MustCompile(`^[a-z]{2}(-[a-z]+)+-[0-9]$`)
	accountIDPattern = regexp.MustCompile(`^[0-9]{12}$`)
	roleNamePattern  = regexp.MustCompile(`^[A-Za-z0-9+=,.@_-]{1,64}$`)
)

// oauthDriver authorizes an AWS account through IAM Identity Center. AWS has no
// account-wide OAuth login: the browser flow signs in to an Identity Center
// instance, and the connection is one account and role the user is assigned.
// The client is registered on the spot, so nothing about Steward has to be
// pre-registered with the customer's directory.
type oauthDriver struct {
	httpClient  awssdk.HTTPClient
	endpoint    string
	now         func() time.Time
	newOIDC     func(region, endpoint string, httpClient awssdk.HTTPClient) ssoOIDCAPI
	newPortal   func(region, endpoint string, httpClient awssdk.HTTPClient) ssoPortalAPI
	redirectURI string
}

type ssoOIDCAPI interface {
	RegisterClient(context.Context, *awsssooidc.RegisterClientInput, ...func(*awsssooidc.Options)) (*awsssooidc.RegisterClientOutput, error)
	CreateToken(context.Context, *awsssooidc.CreateTokenInput, ...func(*awsssooidc.Options)) (*awsssooidc.CreateTokenOutput, error)
}

type ssoPortalAPI interface {
	ListAccounts(context.Context, *awssso.ListAccountsInput, ...func(*awssso.Options)) (*awssso.ListAccountsOutput, error)
	ListAccountRoles(context.Context, *awssso.ListAccountRolesInput, ...func(*awssso.Options)) (*awssso.ListAccountRolesOutput, error)
	GetRoleCredentials(context.Context, *awssso.GetRoleCredentialsInput, ...func(*awssso.Options)) (*awssso.GetRoleCredentialsOutput, error)
}

// NewOAuthDriver builds the AWS half of a browser authorization.
func NewOAuthDriver() oauth.Driver {
	return newOAuthDriver(&http.Client{Timeout: 30 * time.Second}, "")
}

func newOAuthDriver(httpClient awssdk.HTTPClient, endpoint string) *oauthDriver {
	return &oauthDriver{
		httpClient: httpClient,
		endpoint:   endpoint,
		now:        time.Now,
		newOIDC: func(region, endpoint string, client awssdk.HTTPClient) ssoOIDCAPI {
			options := awsssooidc.Options{Region: region, HTTPClient: client}
			if endpoint != "" {
				options.BaseEndpoint = awssdk.String(endpoint)
			}
			return awsssooidc.New(options)
		},
		newPortal: func(region, endpoint string, client awssdk.HTTPClient) ssoPortalAPI {
			options := awssso.Options{Region: region, HTTPClient: client}
			if endpoint != "" {
				options.BaseEndpoint = awssdk.String(endpoint)
			}
			return awssso.New(options)
		},
	}
}

func (d *oauthDriver) Provider() asset.Provider { return asset.ProviderAWS }

// The client is registered with this redirect URI moments before the browser
// opens, so the path is Steward's own choice. It matches the AWS CLI's.
func (d *oauthDriver) Callback() oauth.Callback { return oauth.Callback{Path: "/oauth/callback"} }

func (d *oauthDriver) Authorize(
	ctx context.Context,
	params map[string]string,
	request oauth.Request,
) (oauth.Authorization, error) {
	startURL := strings.TrimSpace(params[startURLKey])
	region := strings.ToLower(strings.TrimSpace(params[ssoRegionKey]))
	parsed, err := url.Parse(startURL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return oauth.Authorization{}, oauth.FlowError(
			"invalid_oauth_parameters",
			"An IAM Identity Center start URL is required, for example https://d-0123456789.awsapps.com/start",
		)
	}
	if !regionPattern.MatchString(region) {
		return oauth.Authorization{}, oauth.FlowError(
			"invalid_oauth_parameters",
			"An IAM Identity Center region is required, for example us-east-1",
		)
	}
	oidc := d.newOIDC(region, d.endpoint, d.httpClient)
	// Registering before the browser opens means a directory that refuses this
	// client is reported as a failure to start rather than a dead browser tab.
	registration, err := oidc.RegisterClient(ctx, &awsssooidc.RegisterClientInput{
		ClientName:   awssdk.String(oauthClientName),
		ClientType:   awssdk.String("public"),
		Scopes:       []string{oauthScope},
		RedirectUris: []string{request.RedirectURI},
		GrantTypes:   []string{"authorization_code", "refresh_token"},
		IssuerUrl:    awssdk.String(startURL),
	})
	if err != nil {
		// The directory's own reason is the only thing that separates a typo in
		// the start URL from one in the region, so it is carried through rather
		// than discarded.
		return oauth.Authorization{}, oauth.FlowError(
			"oauth_client_registration_failed",
			"IAM Identity Center refused to register a client for this start URL and region: "+registrationReason(err),
		)
	}
	authorizationURL, err := d.authorizationURL(registration, region, request)
	if err != nil {
		return oauth.Authorization{}, err
	}
	return oauth.Authorization{
		URL: authorizationURL,
		Exchange: func(ctx context.Context, code string) (oauth.Session, error) {
			tokens, err := oidc.CreateToken(ctx, &awsssooidc.CreateTokenInput{
				ClientId:     registration.ClientId,
				ClientSecret: registration.ClientSecret,
				GrantType:    awssdk.String("authorization_code"),
				Code:         awssdk.String(code),
				CodeVerifier: awssdk.String(request.CodeVerifier),
				RedirectUri:  awssdk.String(request.RedirectURI),
			})
			if err != nil {
				return nil, err
			}
			if awssdk.ToString(tokens.AccessToken) == "" {
				return nil, fmt.Errorf("IAM Identity Center returned no access token")
			}
			return &oauthSession{
				driver:       d,
				startURL:     startURL,
				region:       region,
				registration: registration,
				tokens:       tokens,
				issuedAt:     d.now().UTC(),
			}, nil
		},
	}, nil
}

func (d *oauthDriver) authorizationURL(
	registration *awsssooidc.RegisterClientOutput,
	region string,
	request oauth.Request,
) (string, error) {
	endpoint := strings.TrimSpace(awssdk.ToString(registration.AuthorizationEndpoint))
	if endpoint == "" {
		endpoint = "https://oidc." + region + ".amazonaws.com/authorize"
	}
	authorizationURL, err := url.Parse(endpoint)
	if err != nil {
		return "", fmt.Errorf("build IAM Identity Center authorization URL: %w", err)
	}
	query := authorizationURL.Query()
	query.Set("response_type", "code")
	query.Set("client_id", awssdk.ToString(registration.ClientId))
	query.Set("redirect_uri", request.RedirectURI)
	query.Set("state", request.State)
	query.Set("code_challenge", request.CodeChallenge)
	query.Set("code_challenge_method", "S256")
	query.Set("scopes", oauthScope)
	authorizationURL.RawQuery = query.Encode()
	return authorizationURL.String(), nil
}

// oauthSession holds one completed IAM Identity Center sign-in. The access
// token reaches every account and role the user is assigned, so the connection
// is not decided until the operator picks one pair.
type oauthSession struct {
	driver       *oauthDriver
	startURL     string
	region       string
	registration *awsssooidc.RegisterClientOutput
	tokens       *awsssooidc.CreateTokenOutput
	issuedAt     time.Time
	targets      []contracts.OAuthTarget
}

func (s *oauthSession) Targets(ctx context.Context) ([]contracts.OAuthTarget, error) {
	if s.targets != nil {
		return s.targets, nil
	}
	portal := s.driver.newPortal(s.region, s.driver.endpoint, s.driver.httpClient)
	targets := []contracts.OAuthTarget{}
	accountToken := (*string)(nil)
	for page := 0; page < 50; page++ {
		accounts, err := portal.ListAccounts(ctx, &awssso.ListAccountsInput{
			AccessToken: s.tokens.AccessToken, NextToken: accountToken,
		})
		if err != nil {
			return nil, err
		}
		for _, account := range accounts.AccountList {
			accountID := strings.TrimSpace(awssdk.ToString(account.AccountId))
			if !accountIDPattern.MatchString(accountID) {
				continue
			}
			roles, err := portal.ListAccountRoles(ctx, &awssso.ListAccountRolesInput{
				AccessToken: s.tokens.AccessToken, AccountId: account.AccountId,
			})
			if err != nil {
				return nil, err
			}
			accountName := strings.TrimSpace(awssdk.ToString(account.AccountName))
			if accountName == "" {
				accountName = accountID
			}
			for _, role := range roles.RoleList {
				roleName := strings.TrimSpace(awssdk.ToString(role.RoleName))
				if !roleNamePattern.MatchString(roleName) {
					continue
				}
				targets = append(targets, contracts.OAuthTarget{
					ID:          accountID + "/" + roleName,
					Name:        accountName + " · " + roleName,
					Description: accountID,
				})
			}
		}
		accountToken = accounts.NextToken
		if awssdk.ToString(accountToken) == "" {
			break
		}
	}
	s.targets = targets
	return targets, nil
}

func (s *oauthSession) Credential(ctx context.Context, targetID string) (contracts.Credential, error) {
	targets, err := s.Targets(ctx)
	if err != nil {
		return contracts.Credential{}, err
	}
	found := false
	for _, target := range targets {
		if target.ID == targetID {
			found = true
			break
		}
	}
	if !found {
		return contracts.Credential{}, oauth.FlowError(
			"oauth_target_invalid",
			"The selected AWS account and role are not assigned to this identity",
		)
	}
	accountID, roleName, _ := strings.Cut(targetID, "/")
	values := map[string]string{
		startURLKey:      s.startURL,
		ssoRegionKey:     s.region,
		ssoClientIDKey:   awssdk.ToString(s.registration.ClientId),
		ssoClientSecret:  awssdk.ToString(s.registration.ClientSecret),
		ssoClientExpire:  oauth.FormatUnix(time.Unix(s.registration.ClientSecretExpiresAt, 0).UTC()),
		ssoTokenEndpoint: awssdk.ToString(s.registration.TokenEndpoint),
		accountIDKey:     accountID,
		roleNameKey:      roleName,

		oauth.AccessTokenKey:       awssdk.ToString(s.tokens.AccessToken),
		oauth.AccessTokenExpireKey: oauth.FormatUnix(s.issuedAt.Add(time.Duration(s.tokens.ExpiresIn) * time.Second)),
		oauth.RefreshTokenKey:      awssdk.ToString(s.tokens.RefreshToken),
	}
	// Exchanging the sign-in for role credentials here proves the assignment
	// works before a connection is stored against it.
	role, err := (&oauthRefresher{driver: s.driver}).roleCredentials(
		ctx, s.region, awssdk.ToString(s.tokens.AccessToken), accountID, roleName,
	)
	if err != nil {
		return contracts.Credential{}, err
	}
	for key, value := range role {
		values[key] = value
	}
	return contracts.Credential{Type: asset.CredentialAWSOAuth, Values: values}, nil
}

// registrationReason reduces a registration failure to the directory's own
// error code. The full SDK error carries request identifiers and endpoints that
// do not help the operator fix a mistyped start URL.
func registrationReason(err error) string {
	var invalidMetadata *ssooidctypes.InvalidClientMetadataException
	if errors.As(err, &invalidMetadata) {
		return "the start URL does not name a usable Identity Center instance"
	}
	var invalidRequest *ssooidctypes.InvalidRequestException
	if errors.As(err, &invalidRequest) {
		return "the registration request was rejected as invalid"
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		return apiErr.ErrorCode()
	}
	return "the directory could not be reached"
}
