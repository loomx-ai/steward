package azure

import (
	"net/http"
	"strings"
	"testing"
)

func TestRecoveryGuardOfficialIdentities(t *testing.T) {
	c := &client{subscription: testSubscription}
	vault := c.root() + "/resourcegroups/test-rg/providers/microsoft.recoveryservices/vaults/samplevault"
	own := recoveryServicesExample(t, "ResourceGuardProxy_Get")
	listed := object(array(recoveryServicesExample(t, "ResourceGuardProxies_Get")["value"])[0])
	for _, raw := range []map[string]any{own, listed} {
		id, err := c.recoveryGuardMetadata(raw, vault)
		if err != nil || id != vault+"/backupresourceguardproxies/swaggerexample" {
			t.Fatal("native internal guard identity", err)
		}
	}
	if nativeConfigurationContains(listed, own) {
		t.Fatal("distinct official examples were treated as synchronized observations")
	}
	for _, mode := range []string{"foreign-vault", "foreign-name", "foreign-arm", "bad-guard", "duplicate-operation", "foreign-delete-request"} {
		t.Run(mode, func(t *testing.T) {
			raw := batchClone(own)
			p := object(raw["properties"])
			switch mode {
			case "foreign-vault":
				raw["id"] = strings.Replace(text(raw["id"]), "sampleVault", "foreign", 1)
			case "foreign-name":
				raw["name"] = "other"
			case "foreign-arm":
				raw["id"] = vault + "/backupresourceguardproxies/other"
			case "bad-guard":
				p["resourceGuardResourceId"] = "https://foreign.invalid/guard"
			case "duplicate-operation":
				entries := array(p["resourceGuardOperationDetails"])
				p["resourceGuardOperationDetails"] = append(entries, entries[0])
			case "foreign-delete-request":
				p["resourceGuardOperationDetails"] = []any{map[string]any{"vaultCriticalOperation": recoveryDeleteProtection, "defaultResourceRequest": text(p["resourceGuardResourceId"]) + "-foreign/deleteProtectionRequests/default"}}
			}
			if _, err := c.recoveryGuardMetadata(raw, vault); err == nil {
				t.Fatal("untrusted mapping accepted")
			}
		})
	}
}

func TestRecoveryGuardReadAndKnownReconciliation(t *testing.T) {
	for _, mode := range []string{"unrelated-protection", "delete-protected", "listed-and-own-arm", "known-omitted", "known-absent", "known-parent-absent", "listed-missing", "forbidden", "changed", "duplicate", "filtered-page", "foreign-page", "foreign-known"} {
		t.Run(mode, func(t *testing.T) {
			c := &client{subscription: testSubscription}
			vault := c.root() + "/resourcegroups/test-rg/providers/microsoft.recoveryservices/vaults/samplevault"
			path := vault + "/backupresourceguardproxies"
			id := path + "/swaggerexample"
			raw := recoveryServicesExample(t, "ResourceGuardProxy_Get")
			if mode == "delete-protected" {
				p := object(raw["properties"])
				p["resourceGuardOperationDetails"] = []any{map[string]any{"vaultCriticalOperation": recoveryDeleteProtection, "defaultResourceRequest": text(p["resourceGuardResourceId"]) + "/deleteProtectionRequests/default"}}
			}
			known := map[string]any{}
			if strings.HasPrefix(mode, "known-") {
				known[id] = map[string]any{}
			}
			if mode == "foreign-known" {
				known[id+"/other/child"] = map[string]any{}
			}
			calls := 0
			runtime := protocolRuntime(t, func(q *http.Request) (*http.Response, error) {
				calls++
				if q.Method != "GET" || q.URL.Query().Get("api-version") != recoveryServicesBackupVersion || len(q.URL.Query()) != 1 {
					t.Fatal("unvalidated guard query reached transport")
				}
				if strings.EqualFold(q.URL.Path, path) {
					values := []any{raw}
					if strings.HasPrefix(mode, "known-") {
						values = []any{}
					}
					if mode == "duplicate" {
						values = append(values, raw)
					}
					body := map[string]any{"value": values}
					if mode == "filtered-page" {
						body["nextLink"] = apiURL(path, recoveryServicesBackupVersion) + "&$filter=name"
					}
					if mode == "foreign-page" {
						body["nextLink"] = "https://foreign.invalid/guards?api-version=" + recoveryServicesBackupVersion
					}
					return jsonResponse(200, body, nil), nil
				}
				if !strings.EqualFold(q.URL.Path, id) {
					t.Fatal("guard endpoint changed")
				}
				switch mode {
				case "known-absent", "listed-missing":
					return jsonResponse(404, map[string]any{"error": map[string]any{"code": "ResourceNotFound"}}, nil), nil
				case "known-parent-absent":
					return jsonResponse(404, map[string]any{"error": map[string]any{"code": "ParentResourceNotFound"}}, nil), nil
				case "forbidden":
					return jsonResponse(403, map[string]any{"error": map[string]any{"code": "Forbidden"}}, nil), nil
				}
				own := batchClone(raw)
				if mode == "changed" {
					object(own["properties"])["description"] = "changed"
				}
				if mode == "listed-and-own-arm" {
					own["id"] = id
				}
				return jsonResponse(200, own, nil), nil
			})
			c, err := runtime.resolve(t.Context(), "connection")
			if err != nil {
				t.Fatal(err)
			}
			result, err := c.recoveryGuardProxies(t.Context(), vault, known)
			switch mode {
			case "unrelated-protection", "delete-protected", "listed-and-own-arm", "known-omitted":
				if err != nil || len(result) != 1 || object(result[id])["delete_requires_authorization"] != (mode == "delete-protected") {
					t.Fatal("incorrect operation-specific guard review", err)
				}
			case "known-absent":
				if err != nil || len(result) != 0 {
					t.Fatal("known guard absence", err)
				}
			default:
				if err == nil {
					t.Fatal("incomplete guard boundary accepted")
				}
			}
			if mode == "foreign-known" && calls != 0 {
				t.Fatal("foreign hint reached transport")
			}
		})
	}
}

func TestRecoveryPolicyOfficialOwnRead(t *testing.T) {
	raw := recoveryServicesExample(t, "ProtectionPolicies_Get")
	id := strings.ToLower(text(raw["id"]))
	vault := redisParentID(id)
	for _, mode := range []string{"valid", "foreign-policy", "absent", "forbidden", "empty-async"} {
		t.Run(mode, func(t *testing.T) {
			runtime := protocolRuntime(t, func(q *http.Request) (*http.Response, error) {
				if q.Method != "GET" || !strings.EqualFold(q.URL.Path, id) || q.URL.Query().Get("api-version") != recoveryServicesBackupVersion {
					t.Fatal("native policy request changed")
				}
				code := 200
				body := batchClone(raw)
				h := http.Header{}
				switch mode {
				case "foreign-policy":
					body["id"] = id + "-other"
				case "absent":
					code = 404
					body = map[string]any{"error": map[string]any{"code": "ResourceNotFound"}}
				case "forbidden":
					code = 403
					body = map[string]any{"error": map[string]any{"code": "Forbidden"}}
				case "empty-async":
					h.Set("Location", "")
				}
				return jsonResponse(code, body, h), nil
			})
			c, err := runtime.resolve(t.Context(), "connection")
			if err != nil {
				t.Fatal(err)
			}
			c.subscription = strings.Split(id, "/")[2]
			result, err := c.recoveryPolicyRead(t.Context(), vault, id)
			if mode == "valid" {
				if err != nil || len(result) == 0 {
					t.Fatal("native policy rejected", err)
				}
			} else if err == nil {
				t.Fatal("invalid dependency accepted")
			}
		})
	}
}
