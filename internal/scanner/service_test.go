package scanner

import (
	"context"
	"errors"
	"testing"

	"github.com/prodesire/cloud-steward/internal/domain"
	"github.com/prodesire/cloud-steward/internal/store"
)

func TestServiceProcessesDemoScanJob(t *testing.T) {
	ctx := context.Background()
	repo := store.NewMemoryStore()
	account, err := repo.UpsertAccount(ctx, domain.Account{Name: "dev", Provider: domain.ProviderDemo})
	if err != nil {
		t.Fatalf("UpsertAccount() error = %v", err)
	}
	job, err := repo.CreateScanJob(ctx, domain.ScanJob{
		AccountID:   account.ID,
		AccountName: account.Name,
		Provider:    domain.ProviderDemo,
		Mode:        domain.ScanModeDemo,
		Regions:     []string{"cn-hangzhou"},
	})
	if err != nil {
		t.Fatalf("CreateScanJob() error = %v", err)
	}

	service := NewService(repo, Registry{
		domain.ScanModeDemo: DemoConnector{},
	})
	processed, err := service.ProcessNext(ctx)
	if err != nil {
		t.Fatalf("ProcessNext() error = %v", err)
	}
	if !processed {
		t.Fatal("ProcessNext() processed = false, want true")
	}

	got, err := repo.GetScanJob(ctx, job.ID)
	if err != nil {
		t.Fatalf("GetScanJob() error = %v", err)
	}
	if got.Status != domain.ScanJobSucceeded {
		t.Fatalf("scan status = %s, want succeeded", got.Status)
	}
	if got.ResourceCount != 7 {
		t.Fatalf("resource count = %d, want 7 demo resources", got.ResourceCount)
	}
	resources, err := repo.ListResources(ctx, domain.ResourceFilter{ScanID: job.ID})
	if err != nil {
		t.Fatalf("ListResources() error = %v", err)
	}
	if len(resources) != 7 {
		t.Fatalf("stored resources = %d, want 7", len(resources))
	}
	wantTypes := map[domain.ResourceType]bool{
		domain.ResourceTypeECSInstance:   true,
		domain.ResourceTypeDisk:          true,
		domain.ResourceTypeEIP:           true,
		domain.ResourceTypeSecurityGroup: true,
		domain.ResourceTypeSnapshot:      true,
		domain.ResourceTypeVPC:           true,
		domain.ResourceTypeVSwitch:       true,
	}
	for _, resource := range resources {
		delete(wantTypes, resource.Type)
	}
	if len(wantTypes) != 0 {
		t.Fatalf("missing demo resource types: %#v", wantTypes)
	}
}

func TestServiceMarksJobFailedWhenConnectorFails(t *testing.T) {
	ctx := context.Background()
	repo := store.NewMemoryStore()
	account, err := repo.UpsertAccount(ctx, domain.Account{Name: "dev", Provider: domain.ProviderAliCloud})
	if err != nil {
		t.Fatalf("UpsertAccount() error = %v", err)
	}
	job, err := repo.CreateScanJob(ctx, domain.ScanJob{
		AccountID:   account.ID,
		AccountName: account.Name,
		Provider:    domain.ProviderAliCloud,
		Mode:        domain.ScanModeAliCloud,
		Regions:     []string{"cn-hangzhou"},
	})
	if err != nil {
		t.Fatalf("CreateScanJob() error = %v", err)
	}
	before := []domain.Resource{{
		Provider:  domain.ProviderAliCloud,
		AccountID: account.ID,
		Region:    "cn-hangzhou",
		Type:      domain.ResourceTypeDisk,
		NativeID:  "d-existing",
		Name:      "existing",
		State:     "Available",
	}}
	if err := repo.UpsertResources(ctx, "previous-scan", before); err != nil {
		t.Fatalf("UpsertResources(previous) error = %v", err)
	}

	service := NewService(repo, Registry{
		domain.ScanModeAliCloud: failingConnector{err: errors.New("missing credentials")},
	})
	processed, err := service.ProcessNext(ctx)
	if err != nil {
		t.Fatalf("ProcessNext() error = %v", err)
	}
	if !processed {
		t.Fatal("ProcessNext() processed = false, want true")
	}

	got, err := repo.GetScanJob(ctx, job.ID)
	if err != nil {
		t.Fatalf("GetScanJob() error = %v", err)
	}
	if got.Status != domain.ScanJobFailed {
		t.Fatalf("scan status = %s, want failed", got.Status)
	}
	if got.FailureReason != "missing credentials" {
		t.Fatalf("failure reason = %q, want missing credentials", got.FailureReason)
	}
	resources, err := repo.ListResources(ctx, domain.ResourceFilter{})
	if err != nil {
		t.Fatalf("ListResources() error = %v", err)
	}
	if len(resources) != 1 || resources[0].NativeID != "d-existing" {
		t.Fatalf("resources after failure = %#v, want existing resource preserved", resources)
	}
}

func TestAliCloudConnectorRequiresCredentialsAndRegion(t *testing.T) {
	connector := NewAliCloudConnector()
	_, err := connector.ScanRegion(context.Background(), domain.Account{}, domain.ScanJob{}, "cn-hangzhou")
	if err == nil {
		t.Fatal("ScanRegion() error = nil, want missing credentials error")
	}
	_, err = connector.ScanRegion(context.Background(), domain.Account{AccessKeyID: "ak", AccessKeySecret: "secret"}, domain.ScanJob{}, "")
	if err == nil {
		t.Fatal("ScanRegion() error = nil, want missing region error")
	}
}

type failingConnector struct {
	err error
}

func (c failingConnector) ScanRegion(context.Context, domain.Account, domain.ScanJob, string) ([]domain.Resource, error) {
	return nil, c.err
}
