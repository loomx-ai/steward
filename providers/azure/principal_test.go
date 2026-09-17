package azure

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func testAccessToken(claims map[string]any) string {
	header, _ := json.Marshal(map[string]any{"alg": "RS256", "typ": "JWT"})
	payload, _ := json.Marshal(claims)
	return base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload) + ".signature"
}

func TestRBACProtectsTheConnectionsOwnRoleAssignments(t *testing.T) {
	for _, mode := range []string{"own", "other", "missing-oid"} {
		t.Run(mode, func(t *testing.T) {
			f := newRBACFixture(t)
			assignment := f.resources[rbacTestAssignmentID()]
			principal := text(object(assignment["properties"])["principalId"])
			claims := map[string]any{"oid": strings.ToUpper(principal), "tid": testTenant}
			switch mode {
			case "other":
				claims["oid"] = "99999999-8888-7777-6666-555555555555"
			case "missing-oid":
				delete(claims, "oid")
			}
			product := f.runtime.transport
			f.runtime.transport = roundTripFunc(func(q *http.Request) (*http.Response, error) {
				if q.URL.Host == "login.microsoftonline.com" {
					return jsonResponse(200, map[string]any{"access_token": testAccessToken(claims), "token_type": "Bearer", "expires_in": 3600}, nil), nil
				}
				if strings.HasPrefix(q.Header.Get("Authorization"), "Bearer ") {
					clone := q.Clone(q.Context())
					clone.Header.Set("Authorization", "Bearer token")
					return product.RoundTrip(clone)
				}
				return product.RoundTrip(q)
			})
			value := f.asset(t, rbacAssignmentType, rbacTestAssignmentID())
			protected := value.Normalized["cleanup_protection_reason"] == "azure_rbac_connection_principal"
			if protected != (mode != "other") {
				t.Fatal("connection principal protection", mode, value.Normalized["cleanup_protection_reason"])
			}
			if mode == "other" {
				return
			}
			delete(value.Normalized, "cleanup_protected")
			delete(value.Normalized, "cleanup_protection_reason")
			check, err := f.action(t, value).Preflight(t.Context(), contracts.ActionRequest{Asset: value, Action: "delete"})
			if err != nil || check.Allowed || len(f.deleted) != 0 {
				t.Fatal("own role assignment deletion was allowed", check, err)
			}
		})
	}
}
