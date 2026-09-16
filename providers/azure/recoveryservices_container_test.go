package azure

import (
	"net/http"
	"strings"
	"testing"
)

func recoveryContainerFixture(t *testing.T) *recoveryServicesFixture {
	t.Helper()
	return newRecoveryServicesFixture(t)
}

func TestRecoveryContainerConsumersAndStableReview(t *testing.T) {
	for _, mode := range []string{"active", "retained", "omitted-known", "absent-known", "empty", "changing-item", "changing-container", "parent-missing", "foreign-hint", "consumer-forbidden", "protected-vault", "managed-container"} {
		t.Run(mode, func(t *testing.T) {
			f := recoveryContainerFixture(t)
			known := map[string]any{}
			switch mode {
			case "retained":
				object(f.objects[f.item]["properties"])["isScheduledForDeferredDelete"] = true
			case "omitted-known":
				f.omitted[f.item] = true
				known[f.item] = map[string]any{}
			case "absent-known":
				delete(f.objects, f.item)
				known[f.item] = map[string]any{}
			case "empty":
				delete(f.objects, f.item)
			case "changing-item":
				count := 0
				f.override = func(q *http.Request) (*http.Response, bool) {
					if strings.EqualFold(q.URL.Path, f.item) {
						count++
						raw := batchClone(f.objects[f.item])
						object(raw["properties"])["privateRevision"] = count
						return jsonResponse(200, raw, nil), true
					}
					return nil, false
				}
			case "changing-container":
				count := 0
				f.override = func(q *http.Request) (*http.Response, bool) {
					if strings.EqualFold(q.URL.Path, f.container) {
						count++
						raw := batchClone(f.objects[f.container])
						object(raw["properties"])["privateRevision"] = count
						return jsonResponse(200, raw, nil), true
					}
					return nil, false
				}
			case "parent-missing":
				delete(f.objects, f.vault)
			case "foreign-hint":
				known[f.item+"/unexpected/child"] = map[string]any{}
			case "consumer-forbidden":
				f.override = func(q *http.Request) (*http.Response, bool) {
					if strings.EqualFold(q.URL.Path, f.item) {
						return jsonResponse(403, map[string]any{"error": map[string]any{"code": "Forbidden"}}, nil), true
					}
					return nil, false
				}
			case "protected-vault":
				f.objects[f.vault]["tags"] = map[string]any{"steward:protected": "true"}
			case "managed-container":
				f.objects[f.container]["managedBy"] = "external-controller"
			}
			c, err := f.runtime.resolve(t.Context(), "connection")
			if err != nil {
				t.Fatal(err)
			}
			review, absent, err := c.recoveryContainerReviewFor(t.Context(), f.container, known)
			if mode == "changing-item" || mode == "changing-container" || mode == "parent-missing" || mode == "foreign-hint" || mode == "consumer-forbidden" {
				if err == nil {
					t.Fatal("uncertain review accepted")
				}
				return
			}
			if err != nil || absent || len(review) != 10 || review["state"] != "Registered" {
				t.Fatal("native review failed", err, len(review))
			}
			want := 1
			if mode == "empty" || mode == "absent-known" {
				want = 0
			}
			if len(object(review["consumers"])) != want {
				t.Fatal("consumer evidence lost", mode)
			}
			if mode == "retained" && object(object(review["consumers"])[f.item])["retained"] != true {
				t.Fatal("retained item omitted")
			}
			if (mode == "protected-vault" || mode == "managed-container") && review["protected"] != true {
				t.Fatal("inherited protection lost")
			}
			proof := c.recoveryContainerProofFor(f.container, "connection", review)
			changed := batchClone(review)
			changed["consumers"] = map[string]any{"invented": true}
			if proof == c.recoveryContainerProofFor(f.container, "connection", changed) || proof == c.recoveryContainerProofFor(f.container, "different", review) {
				t.Fatal("review proof not scoped")
			}
		})
	}
}

func TestRecoveryContainerConfigurationKeepsAuthoredFields(t *testing.T) {
	f := recoveryContainerFixture(t)
	raw := f.objects[f.container]
	original := recoveryContainerConfiguration(raw)
	changed := batchClone(raw)
	p := object(changed["properties"])
	p["registrationStatus"] = "Unregistering"
	p["healthStatus"] = "Unknown"
	p["lastUpdatedTime"] = "2026-09-17T00:00:00Z"
	c, err := f.runtime.resolve(t.Context(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	if c.privateConfiguration(original) != c.privateConfiguration(recoveryContainerConfiguration(changed)) {
		t.Fatal("volatile registration state changed authored configuration")
	}
	p["privateFutureSetting"] = "changed"
	if c.privateConfiguration(original) == c.privateConfiguration(recoveryContainerConfiguration(changed)) {
		t.Fatal("unknown authored setting ignored")
	}
	if object(raw["properties"])["registrationStatus"] != "Registered" {
		t.Fatal("snapshot mutated source object")
	}
}
