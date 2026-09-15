package azure

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
)

// Official create tests include deletion/readback and same-name recreation that
// the dedicated delete recordings omit. Keep these distinct from member checks.
func TestDeploymentStackNativeCreationAndDetachReadback(t *testing.T) {
	wire, err := os.ReadFile("fixtures/deployment-stacks/creation-detach-readback.json")
	if err != nil || fmt.Sprintf("%x", sha256.Sum256(wire)) != "1b3159949919e43745ff25ac095a811392d02db406cff5371e14e240f37d0376" {
		t.Fatal("native fixture provenance", err)
	}
	type interaction struct {
		Index    int `json:"index"`
		Request  struct{ Method, URI string }
		Response struct {
			Status  int
			Headers http.Header
			Body    string
		}
	}
	var fixture struct {
		Source       string
		SHA          string `json:"source_sha256"`
		Interactions []interaction
	}
	if json.Unmarshal(wire, &fixture) != nil || fixture.SHA != "af2ca10418f3e8f1cb8087dbe83ac0a5bc15cabf9413e41178a4204385826fb6" || fixture.Source != "https://raw.githubusercontent.com/Azure/azure-cli/ea185727729efc032ad9d4eef9ec355ee74ebaae/src/azure-cli/azure/cli/command_modules/resource/tests/latest/recordings/test_create_deployment_stack_resource_group.yaml" || len(fixture.Interactions) != 12 {
		t.Fatal("source manifest")
	}
	rows := map[int]interaction{}
	for _, row := range fixture.Interactions {
		if _, exists := rows[row.Index]; exists {
			t.Fatal("duplicate interaction")
		}
		rows[row.Index] = row
	}
	var pending *interaction
	calls := 0
	c := directClient(func(q *http.Request) (*http.Response, error) {
		calls++
		if pending == nil || q.Method != "GET" || pending.Request.Method != "GET" {
			t.Fatal("unexpected native read or mutation")
		}
		expected, err := url.Parse(pending.Request.URI)
		if err != nil || q.URL.Host != expected.Host || !strings.EqualFold(q.URL.Path, expected.Path) || q.URL.Query().Encode() != expected.Query().Encode() {
			t.Fatal("native own-read boundary changed")
		}
		headers := http.Header{}
		for key, values := range pending.Response.Headers {
			for _, value := range values {
				headers.Add(key, value)
			}
		}
		result := &http.Response{StatusCode: pending.Response.Status, Header: headers, Body: io.NopCloser(strings.NewReader(pending.Response.Body))}
		pending = nil
		return result, nil
	})
	c.subscription = "00000000-0000-0000-0000-000000000000"
	read := func(index int) (response, error) {
		t.Helper()
		row, ok := rows[index]
		if !ok {
			t.Fatal("missing native read", index)
		}
		pending = &row
		target, err := url.Parse(row.Request.URI)
		if err != nil {
			t.Fatal(err)
		}
		before := calls
		result, err := c.deploymentStackRead(t.Context(), target.Path)
		if calls != before+1 || pending != nil {
			t.Fatal("native read not replayed", index)
		}
		return result, err
	}
	births := []string{}
	for _, index := range []int{11, 21, 31, 39, 57} {
		current, err := read(index)
		if err != nil {
			t.Fatalf("native GET %d: %v", index, err)
		}
		birth, err := c.deploymentStackBirth(current.data)
		if err != nil || birth == "" {
			t.Fatal("missing native birth", index, err)
		}
		births = append(births, birth)
	}
	if births[0] == births[1] || births[1] == births[2] || births[2] != births[3] || births[3] != births[4] {
		t.Fatal("native recreation/update lifetime distinction lost")
	}
	for _, episode := range [][3]int{{11, 13, 14}, {21, 23, 24}, {73, 74, 75}} {
		before, err := read(episode[0])
		if err != nil {
			t.Fatal(err)
		}
		deletion := rows[episode[1]]
		endpoint, err := url.Parse(deletion.Request.URI)
		if err != nil {
			t.Fatal(err)
		}
		goneURL, err := url.Parse(rows[episode[2]].Request.URI)
		if err != nil {
			t.Fatal(err)
		}
		if deletion.Request.Method != "DELETE" || deletion.Response.Status != 200 || deletion.Response.Body != "" || !strings.EqualFold(endpoint.Path, text(before.data["id"])) || !strings.EqualFold(goneURL.Path, endpoint.Path) {
			t.Fatal("native delete/readback identity mismatch")
		}
		parameters := map[string]any{}
		for key, values := range endpoint.Query() {
			if len(values) != 1 {
				t.Fatal("ambiguous native query")
			}
			parameters[key] = values[0]
		}
		for _, key := range []string{"Resources", "ResourceGroups", "ManagementGroups"} {
			if parameters["unmanageAction."+key] != "detach" {
				t.Fatal("unexpected destructive policy")
			}
		}
		// This recording has no parent-region GET for authorization. The region here
		// only binds a synchronous receipt, which has no native callback URL to follow.
		receipt, err := c.deploymentStackDeleteReceipt(endpoint.Path, "westcentralus", parameters, response{status: deletion.Response.Status, header: deletion.Response.Headers})
		if err != nil {
			t.Fatal(err)
		}
		persisted, err := json.Marshal(receipt)
		if err != nil {
			t.Fatal(err)
		}
		if err = json.Unmarshal(persisted, &receipt); err != nil {
			t.Fatal(err)
		}
		count := calls
		wait, err := c.deploymentStackPoll(t.Context(), endpoint.Path, "westcentralus", receipt)
		if err != nil || !wait.Done || calls != count {
			t.Fatal("synchronous operation completion", err)
		}
		if _, err = read(episode[2]); !isNotFound(err) {
			t.Fatal("native own-resource absence not recognized", err)
		}
	}
	// The final episode detached two managed resources. A stack 404 proves only
	// stack absence; this recording provides no retained-member own GET afterward.
	var final map[string]any
	if json.Unmarshal([]byte(rows[73].Response.Body), &final) != nil || len(array(object(final["properties"])["resources"])) != 2 {
		t.Fatal("retained-member evidence lost")
	}
}
