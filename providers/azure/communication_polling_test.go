package azure

import (
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func TestCommunicationARMOperationBoundaries(t *testing.T) {
	id := strings.ToLower(resourceID(communicationType, "comm"))
	global := "https://management.azure.com/providers/Microsoft.Communication/locations/westus/operationStatuses/ed5d502c-acaa-42ec-ad61-0d8488a9fd1d"
	signed := communicationSignedPollURL(id, "signature")
	for _, test := range []struct {
		name, url string
		valid     bool
	}{
		{"native-global", global, true},
		{"global-version", global + "?api-version=" + communicationARMVersion, true},
		{"native-signed", signed, true},
		{"foreign-host", strings.Replace(global, "management.azure.com", "unrelated.invalid", 1), false},
		{"http", strings.Replace(global, "https:", "http:", 1), false},
		{"userinfo", strings.Replace(global, "https://", "https://user@", 1), false},
		{"port", strings.Replace(global, "management.azure.com", "management.azure.com:443", 1), false},
		{"fragment", global + "#part", false},
		{"relative", strings.TrimPrefix(global, "https://management.azure.com"), false},
		{"unrelated-provider", strings.Replace(global, "Microsoft.Communication", "Microsoft.Compute", 1), false},
		{"resource-get", apiURL(id, communicationARMVersion), false},
		{"wrong-operation-collection", strings.Replace(global, "operationStatuses", "operationResults", 1), false},
		{"encoded-path", strings.Replace(global, "/providers/", "/%70roviders/", 1), false},
		{"encoded-uuid", strings.Replace(global, "ed5d", "%65d5d", 1), false},
		{"trailing-slash", global + "/", false},
		{"missing-id", global[:strings.LastIndex(global, "/")], false},
		{"arbitrary-id", global[:strings.LastIndex(global, "/")+1] + "latest", false},
		{"unknown-query", global + "?api-version=" + communicationARMVersion + "&scope=all", false},
		{"duplicate-version", global + "?api-version=" + communicationARMVersion + "&api-version=" + communicationARMVersion, false},
		{"changed-version", global + "?api-version=2023-04-01", false},
		{"empty-query", global + "?", false},
		{"foreign-subscription", strings.Replace(signed, testSubscription, "00000000-0000-0000-0000-000000000000", 1), false},
		{"wrong-signature-digest", strings.Replace(signed, "*"+strings.Repeat("A", 64), "*abc", 1), false},
		{"missing-signature-query", strings.Split(signed, "?")[0], false},
		{"duplicate-signature", signed + "&h=second", false},
		{"empty-signature", strings.Replace(signed, "h=hash", "h=", 1), false},
		{"control-signature", strings.Replace(signed, "h=hash", "h=%0A", 1), false},
	} {
		t.Run(test.name, func(t *testing.T) {
			actual, err := communicationARMOperationURL(testSubscription, id, communicationARMVersion, test.url)
			if (err == nil) != test.valid {
				t.Fatal("native operation URL boundary changed", err)
			}
			if test.valid {
				u, _ := url.Parse(actual)
				if u.Query().Get("api-version") != communicationARMVersion || test.name == "native-signed" && actual != signed {
					t.Fatal("operation URL lost native version or signature")
				}
			}
		})
	}
}

func TestCommunicationDeleteAndPollEnvelopes(t *testing.T) {
	id := strings.ToLower(resourceID(communicationType, "comm"))
	op := communicationSignedPollURL(id, "primary")
	u, _ := url.Parse(op)
	for _, mode := range []string{"native-dual-headers", "global-location", "duplicate-primary", "conflicting-secondary", "foreign-secondary", "unexpected-operation-location", "missing-receipt", "synchronous-receipt", "async-child", "error-body", "unexpected-delete-body", "native-deleting", "native-accepted", "native-succeeded", "poll-wrong-resource", "poll-wrong-operation", "poll-wrong-name", "poll-unknown-state", "poll-failed", "poll-202-without-state", "poll-204-status", "poll-204-location", "poll-invalid-time"} {
		t.Run(mode, func(t *testing.T) {
			headers := http.Header{"Azure-Asyncoperation": {op}, "Location": {communicationSignedPollURL(id, "secondary")}}
			res := response{status: 202, header: headers}
			kind := communicationType
			switch mode {
			case "global-location":
				headers.Del("Azure-AsyncOperation")
				headers.Set("Location", "https://management.azure.com/providers/Microsoft.Communication/locations/westus/operationStatuses/"+azureRequestID(id))
			case "duplicate-primary":
				headers.Add("Azure-AsyncOperation", op)
			case "conflicting-secondary":
				headers.Set("Location", communicationSignedPollURL(id+"other", "secondary"))
			case "foreign-secondary":
				headers.Set("Location", strings.Replace(op, "management.azure.com", "unrelated.invalid", 1))
			case "unexpected-operation-location":
				headers.Set("Operation-Location", op)
			case "missing-receipt":
				res.header = nil
			case "synchronous-receipt":
				res.status = 204
			case "async-child":
				kind = communicationSMTPType
			case "error-body":
				res.data = map[string]any{"error": map[string]any{"code": "Failed"}}
			case "unexpected-delete-body":
				res.data = map[string]any{"unknown": "private"}
			}
			if strings.HasPrefix(mode, "poll-") || strings.HasPrefix(mode, "native-") && mode != "native-dual-headers" {
				res.status, res.header, res.data = 200, nil, map[string]any{"id": u.Path, "name": last(u.Path), "resourceId": id, "status": "Succeeded"}
				polling := "status"
				switch mode {
				case "native-deleting":
					res.status = 202
					res.data["status"] = "Deleting"
				case "native-accepted":
					res.data["status"] = "Accepted"
				case "poll-wrong-resource":
					res.data["resourceId"] = id + "other"
				case "poll-wrong-operation":
					res.data["id"] = u.Path + "other"
				case "poll-wrong-name":
					res.data["name"] = "another"
				case "poll-unknown-state":
					res.data["status"] = "Unknown"
				case "poll-failed":
					res.data["status"] = "Failed"
				case "poll-202-without-state":
					res.status = 202
					res.data = nil
				case "poll-204-status":
					res.status = 204
					res.data = nil
				case "poll-204-location":
					res.status = 204
					res.data = nil
					polling = "location"
				case "poll-invalid-time":
					res.data["startTime"] = "yesterday"
				}
				done, _, err := communicationARMOperationState(id, op, polling, res)
				valid := strings.HasPrefix(mode, "native-") || mode == "poll-204-location"
				if (err == nil) != valid || valid && done != (mode == "native-succeeded" || mode == "poll-204-location") {
					t.Fatal("native poll envelope rejected or falsely completed", done, err)
				}
				return
			}
			_, _, err := communicationDeleteReceipt(testSubscription, id, kind, "", res)
			valid := mode == "native-dual-headers" || mode == "global-location"
			if (err == nil) != valid {
				t.Fatal("native delete receipt boundary changed", err)
			}
		})
	}
}

func TestCommunicationRecordedARMLongRunningOperations(t *testing.T) {
	data, err := os.ReadFile("fixtures/communication/cli-recordings.json")
	var rows []struct {
		Method, URL, Body string
		Status            int
		Headers           map[string][]string
	}
	if err != nil || json.Unmarshal(data, &rows) != nil {
		t.Fatal("missing official CLI recordings", err)
	}
	deletes, polls, pending := 0, 0, 0
	for _, row := range rows {
		u, _ := url.Parse(row.URL)
		headers := http.Header{}
		for key, values := range row.Headers {
			for _, value := range values {
				headers.Add(key, value)
			}
		}
		if row.Method == "DELETE" && row.Status == 202 {
			operation, polling, err := communicationARMOperationHeaders("00000000-0000-0000-0000-000000000000", u.Path, "2023-04-01", headers)
			if err != nil || operation != headers.Get("Azure-AsyncOperation") || polling != "status" {
				t.Fatal("unchanged native DELETE headers rejected", err)
			}
			deletes++
		}
		if row.Method != "GET" || !strings.Contains(strings.ToLower(u.Path), "/operationstatuses/") {
			continue
		}
		var body map[string]any
		if json.Unmarshal([]byte(row.Body), &body) != nil {
			t.Fatal("invalid recorded poll body")
		}
		// Only the upstream sanitized origin is replaced for the URL shape
		// check. Native header URLs and every response body remain unchanged.
		u.Scheme, u.Host = "https", "management.azure.com"
		id := text(body["resourceId"])
		if _, err := communicationARMOperationURL("00000000-0000-0000-0000-000000000000", id, "2023-04-01", u.String()); err != nil {
			t.Fatal("recorded signed poll URL rejected", err)
		}
		if _, _, err := communicationARMOperationHeaders("00000000-0000-0000-0000-000000000000", id, "2023-04-01", headers); err != nil {
			t.Fatal("recorded rotated headers rejected", err)
		}
		done, _, err := communicationARMOperationState(id, u.String(), "status", response{data: body, status: row.Status, header: headers})
		if err != nil {
			t.Fatal("unchanged recorded poll body rejected", err)
		}
		polls++
		if !done {
			pending++
		}
	}
	if deletes != 3 || polls == 0 || pending == 0 {
		t.Fatal("native recorded LRO coverage missing", deletes, polls, pending)
	}
	t.Logf("validated %d native DELETE receipts and %d native polls (%d nonterminal)", deletes, polls, pending)
}

func TestCommunicationARMInvokeUsesNativeGlobalReceipt(t *testing.T) {
	f := newCommunicationFixture(t)
	c, _ := f.runtime.resolve(t.Context(), "connection")
	kind, _ := findType(communicationType)
	op, params, err := c.resourceOperation(kind, f.ids[communicationType], "DELETE")
	if err != nil {
		t.Fatal(err)
	}
	location := "https://management.azure.com/providers/Microsoft.Communication/locations/westus/operationStatuses/" + azureRequestID("invoked-delete")
	f.override = func(req *http.Request) (*http.Response, bool) {
		if req.Method != "DELETE" || !strings.EqualFold(req.URL.Path, f.ids[communicationType]) {
			t.Fatal("ARM invocation changed native deletion")
		}
		return jsonResponse(202, nil, http.Header{"Location": {location}}), true
	}
	result, err := f.runtime.Invoke(t.Context(), contracts.Invocation{ConnectionID: "connection", Operation: op.ID, Parameters: params})
	if err != nil || result.OperationID != location+"?api-version="+communicationARMVersion {
		t.Fatal("native global DELETE receipt rejected", result, err)
	}
}
