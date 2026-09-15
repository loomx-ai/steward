package azure

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func TestDeploymentStackExecutionReceiptFrozenPlan(t *testing.T) {
	for _, fault := range []string{"resume", "job", "missing_job", "retain", "member_birth", "member_asset", "options", "prerequisite", "region", "native", "extra", "invalid_json"} {
		t.Run(fault, func(t *testing.T) {
			c, req := stackDeletePlanFixture(t, false)
			req.IdempotencyKey = "reviewed-delete-job"
			saved, err := c.deploymentStackExecutionReceipt(req, "eastus", response{status: 204})
			if err != nil {
				t.Fatal(err)
			}
			wire, err := json.Marshal(saved)
			if err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(wire, &saved); err != nil {
				t.Fatal(err)
			}
			wire, err = json.Marshal(req)
			if err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(wire, &req); err != nil {
				t.Fatal(err)
			}
			region := "eastus"
			switch fault {
			case "job":
				req.IdempotencyKey = "another-job"
			case "missing_job":
				req.IdempotencyKey = ""
			case "retain":
				req.LifecycleImpacts[0].Delete = false
			case "member_birth":
				req.LifecycleImpacts[0].Asset.Normalized = map[string]any{"_arm_creation_generation": "replacement"}
			case "member_asset":
				req.LifecycleImpacts[0].Asset.ID = "replacement"
			case "options":
				req.Parameters = map[string]any{"retain_all_resources": false}
			case "prerequisite":
				req.PrerequisiteDeletions = append(req.PrerequisiteDeletions, req.LifecycleImpacts[0])
			case "region":
				region = "westus"
			case "native":
				object(saved["native"])["complete"] = true
			case "extra":
				saved["complete"] = true
			case "invalid_json":
				req.Asset.Normalized["invalid"] = make(chan int)
			}
			c.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
				t.Fatal("receipt must be checked before HTTP")
				return nil, nil
			})
			out, err := c.deploymentStackResumeOperation(t.Context(), req, region, saved)
			if fault == "resume" {
				if err != nil || !out.Done {
					t.Fatal(out, err)
				}
				req.ExecutionResult = &contracts.ActionResult{Data: out.Data}
				out, err = c.deploymentStackResumeOperation(t.Context(), req, region, out.Data)
				if err != nil || !out.Done {
					t.Fatal(out, err)
				}
			} else if err == nil || out.Done || out.Data != nil {
				t.Fatal("changed plan resumed", out, err)
			}
		})
	}
}

func TestDeploymentStackExecutionReceiptAsyncResume(t *testing.T) {
	c, req := stackDeletePlanFixture(t, false)
	req.IdempotencyKey = "reviewed-delete-job"
	_, endpoint, _ := stackPollFixture()
	endpoint += "&unmanageAction.ResourcesWithoutDeleteSupport=fail"
	saved, err := c.deploymentStackExecutionReceipt(req, "eastus", response{status: 202, header: http.Header{"Azure-Asyncoperation": {endpoint}}})
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	c.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != "GET" || r.URL.String() != endpoint {
			t.Fatal("unexpected operation", r.Method, r.URL)
		}
		calls++
		state := "deletingResources"
		if calls > 1 {
			state = "succeeded"
		}
		return jsonResponse(200, map[string]any{"id": r.URL.Path, "name": testTenant, "status": state}, nil), nil
	})
	for i := 0; i < 3; i++ {
		wire, err := json.Marshal(saved)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(wire, &saved); err != nil {
			t.Fatal(err)
		}
		out, err := c.deploymentStackResumeOperation(t.Context(), req, "eastus", saved)
		if err != nil || out.Done != (i > 0) {
			t.Fatal(i, out, err)
		}
		saved = out.Data
	}
	if calls != 2 {
		t.Fatal("completed operation polled again", calls)
	}
}

func TestDeploymentStackExecutionReceiptImpactOrder(t *testing.T) {
	_, initial := stackDeletePlanFixture(t, false)
	first := initial.LifecycleImpacts[0]
	second := first
	second.Asset.ID = "another-member"
	second.Asset.Identity.NativeID += "-another"
	c, req := stackDeletePlanFixture(t, false, first, second)
	req.IdempotencyKey = "reviewed-delete-job"
	req.PrerequisiteDeletions = []contracts.ActionImpact{first, second}
	before, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := c.deploymentStackExecutionReceipt(req, "eastus", response{status: 204})
	if err != nil {
		t.Fatal(err)
	}
	after, err := json.Marshal(req)
	if err != nil || string(before) != string(after) {
		t.Fatal("binding mutated the frozen request")
	}
	req.LifecycleImpacts[0], req.LifecycleImpacts[1] = req.LifecycleImpacts[1], req.LifecycleImpacts[0]
	req.PrerequisiteDeletions[0], req.PrerequisiteDeletions[1] = req.PrerequisiteDeletions[1], req.PrerequisiteDeletions[0]
	out, err := c.deploymentStackResumeOperation(t.Context(), req, "eastus", saved)
	if err != nil || !out.Done {
		t.Fatal("equivalent reordered plan rejected", out, err)
	}
}
