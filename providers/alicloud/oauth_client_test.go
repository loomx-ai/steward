package alicloud

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
)

func TestOAuthAuthorizationURLUsesSiteSpecificAlibabaCloudApplication(t *testing.T) {
	client := newOAuthClient(http.DefaultClient, func() time.Time {
		return time.Date(2026, 7, 27, 16, 0, 0, 0, time.UTC)
	})
	for _, test := range []struct {
		site         asset.ConnectionSite
		wantHost     string
		wantClientID string
	}{
		{asset.ConnectionSiteCN, "signin.aliyun.com", "4038181954557748008"},
		{asset.ConnectionSiteINTL, "signin.alibabacloud.com", "4103531455503354461"},
	} {
		got, err := client.AuthorizationURL(
			test.site,
			"http://127.0.0.1:12345/cli/callback",
			"state-value",
			"challenge-value",
		)
		if err != nil {
			t.Fatal(err)
		}
		parsed, err := url.Parse(got)
		if err != nil {
			t.Fatal(err)
		}
		query := parsed.Query()
		if parsed.Scheme != "https" || parsed.Host != test.wantHost || parsed.Path != "/oauth2/v1/auth" ||
			query.Get("response_type") != "code" ||
			query.Get("client_id") != test.wantClientID ||
			query.Get("redirect_uri") != "http://127.0.0.1:12345/cli/callback" ||
			query.Get("state") != "state-value" ||
			query.Get("code_challenge") != "challenge-value" ||
			query.Get("code_challenge_method") != "S256" {
			t.Fatalf("authorization URL = %q", got)
		}
	}
}

func TestOAuthTokenAndSTSRequestsMatchAliyunCLIProtocol(t *testing.T) {
	now := time.Date(2026, 7, 27, 16, 0, 0, 0, time.UTC)
	var calls []string
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/v1/token":
			if request.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
				t.Errorf("token content type = %q", request.Header.Get("Content-Type"))
			}
			if err := request.ParseForm(); err != nil {
				t.Error(err)
				return
			}
			grant := request.Form.Get("grant_type")
			calls = append(calls, grant)
			if request.Form.Get("client_id") != "4038181954557748008" {
				t.Errorf("client_id = %q", request.Form.Get("client_id"))
			}
			switch grant {
			case "authorization_code":
				if request.Form.Get("code") != "authorization-code" ||
					request.Form.Get("redirect_uri") != "http://127.0.0.1:12345/cli/callback" ||
					request.Form.Get("code_verifier") != "code-verifier" {
					t.Errorf("authorization form = %#v", request.Form)
				}
				_ = json.NewEncoder(response).Encode(map[string]any{
					"access_token": "access-from-code", "refresh_token": "refresh-from-code", "expires_in": 3600,
				})
			case "refresh_token":
				if request.Form.Get("refresh_token") != "refresh-old" {
					t.Errorf("refresh form = %#v", request.Form)
				}
				_ = json.NewEncoder(response).Encode(map[string]any{
					"access_token": "access-refreshed", "refresh_token": "refresh-rotated", "expires_in": 1800,
				})
			default:
				t.Errorf("unexpected grant %q", grant)
			}
		case "/v1/exchange":
			calls = append(calls, "exchange")
			if request.Method != http.MethodPost ||
				request.Header.Get("Authorization") != "Bearer access-refreshed" ||
				request.Header.Get("Content-Type") != "application/json" ||
				request.Header.Get("User-Agent") != "aliyun-cli" {
				t.Errorf("exchange request headers = %#v", request.Header)
			}
			body, _ := io.ReadAll(request.Body)
			if len(body) != 0 {
				t.Errorf("exchange body = %q", body)
			}
			_ = json.NewEncoder(response).Encode(map[string]any{
				"accessKeyId": "sts-id", "accessKeySecret": "sts-secret", "securityToken": "sts-token",
				"expiration": "2026-07-27T17:00:00Z",
			})
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()

	client := newOAuthClient(server.Client(), func() time.Time { return now })
	config := client.sites[asset.ConnectionSiteCN]
	config.oauthBaseURL = server.URL
	client.sites[asset.ConnectionSiteCN] = config

	fromCode, err := client.ExchangeAuthorizationCode(
		context.Background(),
		asset.ConnectionSiteCN,
		"authorization-code",
		"http://127.0.0.1:12345/cli/callback",
		"code-verifier",
	)
	if err != nil {
		t.Fatal(err)
	}
	if fromCode.AccessToken != "access-from-code" || fromCode.RefreshToken != "refresh-from-code" || !fromCode.ExpiresAt.Equal(now.Add(time.Hour)) {
		t.Fatalf("authorization tokens = %#v", fromCode)
	}
	refreshed, err := client.Refresh(context.Background(), asset.ConnectionSiteCN, "refresh-old")
	if err != nil {
		t.Fatal(err)
	}
	if refreshed.AccessToken != "access-refreshed" || refreshed.RefreshToken != "refresh-rotated" || !refreshed.ExpiresAt.Equal(now.Add(30*time.Minute)) {
		t.Fatalf("refreshed tokens = %#v", refreshed)
	}
	sts, err := client.ExchangeSTS(context.Background(), asset.ConnectionSiteCN, refreshed.AccessToken)
	if err != nil {
		t.Fatal(err)
	}
	if sts.AccessKeyID != "sts-id" || sts.AccessKeySecret != "sts-secret" || sts.SecurityToken != "sts-token" ||
		!sts.ExpiresAt.Equal(time.Date(2026, 7, 27, 17, 0, 0, 0, time.UTC)) {
		t.Fatalf("STS = %#v", sts)
	}
	if strings.Join(calls, ",") != "authorization_code,refresh_token,exchange" {
		t.Fatalf("calls = %#v", calls)
	}
}

func TestOAuthClientRejectsUnsafeOrIncompleteResponses(t *testing.T) {
	for _, test := range []struct {
		name string
		body string
		code int
		call func(*oauthClient) error
	}{
		{
			name: "token status",
			code: http.StatusUnauthorized,
			body: `{"access_token":"must-not-leak"}`,
			call: func(client *oauthClient) error {
				_, err := client.Refresh(context.Background(), asset.ConnectionSiteCN, "refresh")
				return err
			},
		},
		{
			name: "empty access token",
			code: http.StatusOK,
			body: `{"refresh_token":"refresh","expires_in":3600}`,
			call: func(client *oauthClient) error {
				_, err := client.Refresh(context.Background(), asset.ConnectionSiteCN, "refresh")
				return err
			},
		},
		{
			name: "incomplete sts",
			code: http.StatusOK,
			body: `{"accessKeyId":"id","expiration":"2026-07-27T17:00:00Z"}`,
			call: func(client *oauthClient) error {
				_, err := client.ExchangeSTS(context.Background(), asset.ConnectionSiteCN, "access")
				return err
			},
		},
		{
			name: "invalid sts expiration",
			code: http.StatusOK,
			body: `{"accessKeyId":"id","accessKeySecret":"secret","securityToken":"token","expiration":"not-rfc3339"}`,
			call: func(client *oauthClient) error {
				_, err := client.ExchangeSTS(context.Background(), asset.ConnectionSiteCN, "access")
				return err
			},
		},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				response.WriteHeader(test.code)
				_, _ = response.Write([]byte(test.body))
			}))
			defer server.Close()
			client := newOAuthClient(server.Client(), time.Now)
			config := client.sites[asset.ConnectionSiteCN]
			config.oauthBaseURL = server.URL
			client.sites[asset.ConnectionSiteCN] = config
			err := test.call(client)
			if err == nil {
				t.Fatal("expected error")
			}
			if strings.Contains(err.Error(), "must-not-leak") {
				t.Fatalf("error exposed response body: %v", err)
			}
		})
	}
}
