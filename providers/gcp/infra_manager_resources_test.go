package gcp

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"
)

type infraPhysicalCase struct {
	DeploymentType    string `json:"deployment_type"`
	DeploymentStateID string `json:"deployment_state_id"`
	CAIType           string `json:"cai_type"`
	CAIName           string `json:"cai_name"`
	NativeType        string `json:"native_type"`
	NativeID          string `json:"native_id"`
	GetURL            string `json:"get_url"`
}

func TestInfraManagerKeepsLatestRevisionExecutionDependencies(t *testing.T) {
	s := newInfraScenario(t)
	const account = "projects/sample-project/serviceAccounts/latest@sample-project.iam.gserviceaccount.com"
	s.resources[infraTestRevision]["serviceAccount"] = account
	object(s.resources[infraTestRevision]["terraformBlueprint"])["gcsSource"] = "gs://latest-source/revision.zip"
	s.resources[infraTestDeployment+"/revisions/r-0"]["serviceAccount"] = "projects/sample-project/serviceAccounts/historical@sample-project.iam.gserviceaccount.com"
	r := protocolRuntime(t, s.transport(t))
	batch, err := r.List(context.Background(), productRequest(r, infraDeployment, "us-central1"))
	if err != nil || len(batch.Items) != 1 {
		t.Fatalf("deployment dependencies: %+v %v", batch, err)
	}
	accounts, err := discoveryStrings(batch.Items[0].Normalized[referenceKey("iam.googleapis.com/ServiceAccount")])
	if err != nil || len(accounts) != 2 || !slices.Contains(accounts, "//iam.googleapis.com/"+account) {
		t.Fatalf("latest execution service account lost: %v %v", accounts, err)
	}
	buckets, err := discoveryStrings(batch.Items[0].Normalized[referenceKey("storage.googleapis.com/Bucket")])
	if err != nil || len(buckets) != 2 || !slices.Contains(buckets, "//storage.googleapis.com/latest-source") {
		t.Fatalf("latest deployment source dependency lost: %v %v", buckets, err)
	}
	if len(s.writes) != 0 {
		t.Fatal("execution dependencies were mutated")
	}
}

func (v infraPhysicalCase) record() map[string]any {
	return map[string]any{"intent": "CREATE", "state": "RECONCILED", "terraformInfo": map[string]any{"type": v.DeploymentType, "id": v.DeploymentStateID, "address": v.DeploymentType + ".item"}, "caiAssets": map[string]any{v.CAIType: map[string]any{"fullResourceName": v.CAIName}}}
}

func TestInfraManagerPhysicalNativeIdentityContracts(t *testing.T) {
	raw, err := os.ReadFile("fixtures/infra-manager/physical-identities.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []infraPhysicalCase
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) != len(infraDeploymentKinds) {
		t.Fatal("deployment mapping has no corresponding source-backed identity contract")
	}
	for _, test := range cases {
		t.Run(test.DeploymentType, func(t *testing.T) {
			calls := 0
			c := &client{project: "sample-project", number: "123456", http: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				calls++
				if req.Method != "GET" || req.URL.String() != test.GetURL {
					t.Fatalf("wrong native physical GET: %s %s; want %s", req.Method, req.URL, test.GetURL)
				}
				return dataformResponse(req, 404, map[string]any{}), nil
			})}}
			member, known, err := c.infraPhysicalMember(context.Background(), test.record())
			if err != nil || !known || !member.Absent || member.ID != test.NativeID || member.Kind != test.NativeType || calls != 1 {
				t.Fatalf("native state ID and CAI correspondence: %+v %t %v calls=%d", member, known, err, calls)
			}
			aliases := map[string]string{
				"google_compute_region_disk":            "compute.googleapis.com/Disk",
				"google_compute_global_address":         "compute.googleapis.com/Address",
				"google_compute_region_backend_service": "compute.googleapis.com/BackendService",
				"google_compute_global_forwarding_rule": "compute.googleapis.com/ForwardingRule",
			}
			if alias := aliases[test.DeploymentType]; alias != "" {
				row := test.record()
				row["caiAssets"] = map[string]any{alias: object(row["caiAssets"])[test.CAIType]}
				member, known, err := c.infraPhysicalMember(context.Background(), row)
				if err != nil || !known || !member.Absent || member.ID != test.NativeID || member.Kind != test.NativeType || calls != 2 {
					t.Fatalf("documented CAI search alias: %+v %t %v calls=%d", member, known, err, calls)
				}
			}
			for _, mutate := range []func(map[string]any){
				func(row map[string]any) { object(row["terraformInfo"])["type"] = test.DeploymentType + "_iam_member" },
				func(row map[string]any) { object(row["terraformInfo"])["id"] = "other-resource" },
				func(row map[string]any) {
					row["caiAssets"] = map[string]any{"unsupported.googleapis.com/Asset": map[string]any{"fullResourceName": test.CAIName}}
				},
				func(row map[string]any) {
					object(object(row["caiAssets"])[test.CAIType])["fullResourceName"] = strings.Replace(test.CAIName, "/projects/sample-project/", "/projects/foreign-project/", 1) + "-other"
				},
			} {
				row := test.record()
				mutate(row)
				if member, known, err := c.infraPhysicalMember(context.Background(), row); err == nil && known {
					t.Fatalf("ambiguous/non-owning resource became an owner: %+v", member)
				}
			}
		})
	}
}

func TestInfraManagerServiceAccountCAIAliases(t *testing.T) {
	const name = "projects/sample-project/serviceAccounts/managed@sample-project.iam.gserviceaccount.com"
	const unique = "projects/sample-project/serviceAccounts/100000000000000001"
	for _, mode := range []string{"email", "unique", "project-number", "absent", "alias-alive", "recreated", "foreign-project", "wrong-email"} {
		t.Run(mode, func(t *testing.T) {
			cai := "//iam.googleapis.com/" + unique
			if mode == "email" {
				cai = "//iam.googleapis.com/" + name
			}
			if mode == "project-number" {
				cai = strings.Replace(cai, "/sample-project/", "/123456/", 1)
			}
			row := infraPhysicalCase{DeploymentType: "google_service_account", DeploymentStateID: name, CAIType: "iam.googleapis.com/ServiceAccount", CAIName: cai}.record()
			reads := 0
			c := &client{project: "sample-project", number: "123456", http: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				reads++
				if req.Method != "GET" || req.URL.Host != "iam.googleapis.com" || (req.URL.Path != "/v1/"+name && req.URL.Path != "/v1/"+unique) {
					t.Fatalf("foreign IAM request: %s %s", req.Method, req.URL)
				}
				if mode == "absent" || mode == "alias-alive" && req.URL.Path == "/v1/"+name {
					return dataformResponse(req, 404, map[string]any{}), nil
				}
				data := map[string]any{"name": name, "projectId": "sample-project", "uniqueId": "100000000000000001", "email": "managed@sample-project.iam.gserviceaccount.com"}
				switch mode {
				case "recreated":
					data["uniqueId"] = "100000000000000002"
				case "foreign-project":
					data["projectId"] = "foreign-project"
				case "wrong-email":
					data["email"] = "someone-else@sample-project.iam.gserviceaccount.com"
				}
				return dataformResponse(req, 200, data), nil
			})}}
			member, known, err := c.infraPhysicalMember(context.Background(), row)
			if mode == "recreated" || mode == "alias-alive" || mode == "foreign-project" || mode == "wrong-email" {
				if err == nil || known {
					t.Fatalf("changed IAM alias accepted: %+v %t %v", member, known, err)
				}
				return
			}
			if err != nil || !known || member.ID != "//iam.googleapis.com/"+name || member.Absent != (mode == "absent") {
				t.Fatalf("valid IAM alias failed: %+v %t %v", member, known, err)
			}
			if mode == "absent" && reads != 2 {
				t.Fatal("absence checked only one IAM alias")
			}
		})
	}
}
