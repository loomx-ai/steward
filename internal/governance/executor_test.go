package governance

import (
	"context"
	"testing"

	"github.com/prodesire/cloud-steward/internal/domain"
	"github.com/prodesire/cloud-steward/internal/store"
)

func TestTagExecutorAppliesPlanTags(t *testing.T) {
	ctx := context.Background()
	client := &recordingTagClient{requestID: "req-tag-1"}
	executor := TagExecutor{Client: client}
	request := ExecutionRequest{
		Plan: domain.CleanupPlan{ID: "plan-1"},
		Item: domain.CleanupPlanItem{
			ID:          "item-1",
			ResourceID:  "resource-1",
			CandidateID: "candidate-1",
			Action:      "tag",
		},
		Resource: domain.Resource{
			ID:       "resource-1",
			Provider: domain.ProviderAliCloud,
			Region:   "cn-hangzhou",
			Type:     domain.ResourceTypeECSInstance,
			NativeID: "i-demo",
		},
		Actor: "operator",
	}

	result, err := executor.ExecutePlanItem(ctx, request)
	if err != nil {
		t.Fatalf("ExecutePlanItem() error = %v", err)
	}
	if result.Result != "tagged" {
		t.Fatalf("result = %q, want tagged", result.Result)
	}
	if result.RequestID != "req-tag-1" {
		t.Fatalf("request id = %q, want req-tag-1", result.RequestID)
	}
	if client.called != 1 {
		t.Fatalf("client calls = %d, want 1", client.called)
	}
	if client.resource.NativeID != "i-demo" {
		t.Fatalf("tagged resource = %q, want i-demo", client.resource.NativeID)
	}
	wantTags := map[string]string{
		"cloud-steward:managed-by":   "cloud-steward",
		"cloud-steward:plan-id":      "plan-1",
		"cloud-steward:plan-item-id": "item-1",
		"cloud-steward:action":       "tag",
	}
	for key, want := range wantTags {
		if got := client.tags[key]; got != want {
			t.Fatalf("tag %s = %q, want %q", key, got, want)
		}
	}
}

func TestTagExecutorSkipsUnsupportedActions(t *testing.T) {
	client := &recordingTagClient{requestID: "req-tag-1"}
	executor := TagExecutor{Client: client}

	result, err := executor.ExecutePlanItem(context.Background(), ExecutionRequest{
		Plan: domain.CleanupPlan{ID: "plan-1"},
		Item: domain.CleanupPlanItem{ID: "item-1", Action: "delete"},
		Resource: domain.Resource{
			ID:       "resource-1",
			Provider: domain.ProviderAliCloud,
			Region:   "cn-hangzhou",
			Type:     domain.ResourceTypeDisk,
			NativeID: "d-demo",
		},
	})
	if err != nil {
		t.Fatalf("ExecutePlanItem() error = %v", err)
	}
	if result.Result != "skipped" {
		t.Fatalf("result = %q, want skipped", result.Result)
	}
	if result.RequestID == "" {
		t.Fatal("request id is empty")
	}
	if client.called != 0 {
		t.Fatalf("client calls = %d, want 0", client.called)
	}
}

type recordingTagClient struct {
	called    int
	resource  domain.Resource
	tags      map[string]string
	requestID string
}

func (c *recordingTagClient) TagResource(_ context.Context, resource domain.Resource, tags map[string]string) (string, error) {
	c.called++
	c.resource = resource
	c.tags = map[string]string{}
	for key, value := range tags {
		c.tags[key] = value
	}
	return c.requestID, nil
}

func TestAliCloudTagClientRoutesECSResourceTags(t *testing.T) {
	ctx := context.Background()
	repo := store.NewMemoryStore()
	account, err := repo.UpsertAccount(ctx, domain.Account{
		Name:            "prod",
		Provider:        domain.ProviderAliCloud,
		AccessKeyID:     "ak",
		AccessKeySecret: "secret",
	})
	if err != nil {
		t.Fatalf("UpsertAccount() error = %v", err)
	}
	tagger := &recordingAliCloudTagger{requestID: "aliyun-req-1"}
	client := AliCloudTagClient{Repo: repo, Tagger: tagger}

	requestID, err := client.TagResource(ctx, domain.Resource{
		Provider:  domain.ProviderAliCloud,
		AccountID: account.ID,
		Region:    "cn-hangzhou",
		Type:      domain.ResourceTypeSecurityGroup,
		NativeID:  "sg-demo",
	}, map[string]string{"cloud-steward:plan-id": "plan-1"})
	if err != nil {
		t.Fatalf("TagResource() error = %v", err)
	}
	if requestID != "aliyun-req-1" {
		t.Fatalf("request id = %q, want aliyun-req-1", requestID)
	}
	if tagger.ecsCalls != 1 {
		t.Fatalf("ECS calls = %d, want 1", tagger.ecsCalls)
	}
	if tagger.vpcCalls != 0 {
		t.Fatalf("VPC calls = %d, want 0", tagger.vpcCalls)
	}
	if tagger.account.AccessKeyID != "ak" {
		t.Fatalf("access key id = %q, want ak", tagger.account.AccessKeyID)
	}
	if tagger.region != "cn-hangzhou" {
		t.Fatalf("region = %q, want cn-hangzhou", tagger.region)
	}
	if tagger.resourceType != "securitygroup" {
		t.Fatalf("resource type = %q, want securitygroup", tagger.resourceType)
	}
	if tagger.nativeID != "sg-demo" {
		t.Fatalf("native id = %q, want sg-demo", tagger.nativeID)
	}
}

func TestAliCloudTagClientRoutesVPCResourceTags(t *testing.T) {
	ctx := context.Background()
	repo := store.NewMemoryStore()
	account, err := repo.UpsertAccount(ctx, domain.Account{
		Name:            "prod",
		Provider:        domain.ProviderAliCloud,
		AccessKeyID:     "ak",
		AccessKeySecret: "secret",
	})
	if err != nil {
		t.Fatalf("UpsertAccount() error = %v", err)
	}
	tagger := &recordingAliCloudTagger{requestID: "aliyun-req-2"}
	client := AliCloudTagClient{Repo: repo, Tagger: tagger}

	if _, err := client.TagResource(ctx, domain.Resource{
		Provider:  domain.ProviderAliCloud,
		AccountID: account.ID,
		Region:    "cn-hangzhou",
		Type:      domain.ResourceTypeEIP,
		NativeID:  "eip-demo",
	}, map[string]string{"cloud-steward:plan-id": "plan-1"}); err != nil {
		t.Fatalf("TagResource() error = %v", err)
	}
	if tagger.ecsCalls != 0 {
		t.Fatalf("ECS calls = %d, want 0", tagger.ecsCalls)
	}
	if tagger.vpcCalls != 1 {
		t.Fatalf("VPC calls = %d, want 1", tagger.vpcCalls)
	}
	if tagger.resourceType != "EIP" {
		t.Fatalf("resource type = %q, want EIP", tagger.resourceType)
	}
	if tagger.nativeID != "eip-demo" {
		t.Fatalf("native id = %q, want eip-demo", tagger.nativeID)
	}
}

type recordingAliCloudTagger struct {
	ecsCalls     int
	vpcCalls     int
	account      domain.Account
	region       string
	resourceType string
	nativeID     string
	tags         map[string]string
	requestID    string
}

func (t *recordingAliCloudTagger) TagECSResource(_ context.Context, account domain.Account, region string, resourceType string, nativeID string, tags map[string]string) (string, error) {
	t.ecsCalls++
	t.capture(account, region, resourceType, nativeID, tags)
	return t.requestID, nil
}

func (t *recordingAliCloudTagger) TagVPCResource(_ context.Context, account domain.Account, region string, resourceType string, nativeID string, tags map[string]string) (string, error) {
	t.vpcCalls++
	t.capture(account, region, resourceType, nativeID, tags)
	return t.requestID, nil
}

func (t *recordingAliCloudTagger) capture(account domain.Account, region string, resourceType string, nativeID string, tags map[string]string) {
	t.account = account
	t.region = region
	t.resourceType = resourceType
	t.nativeID = nativeID
	t.tags = map[string]string{}
	for key, value := range tags {
		t.tags[key] = value
	}
}
