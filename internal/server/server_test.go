package server

import (
	"testing"

	"github.com/prodesire/cloud-steward/internal/governance"
	"github.com/prodesire/cloud-steward/internal/store"
)

func TestPlanExecutorDefaultsToDryRun(t *testing.T) {
	executor, err := planExecutor(store.NewMemoryStore(), "")
	if err != nil {
		t.Fatalf("planExecutor() error = %v", err)
	}
	if _, ok := executor.(governance.DryRunExecutor); !ok {
		t.Fatalf("executor = %T, want governance.DryRunExecutor", executor)
	}
}

func TestPlanExecutorCanEnableAliCloudTagExecutor(t *testing.T) {
	executor, err := planExecutor(store.NewMemoryStore(), "alicloud-tag")
	if err != nil {
		t.Fatalf("planExecutor() error = %v", err)
	}
	tagExecutor, ok := executor.(governance.TagExecutor)
	if !ok {
		t.Fatalf("executor = %T, want governance.TagExecutor", executor)
	}
	if _, ok := tagExecutor.Client.(governance.AliCloudTagClient); !ok {
		t.Fatalf("tag client = %T, want governance.AliCloudTagClient", tagExecutor.Client)
	}
}

func TestPlanExecutorRejectsUnknownExecutor(t *testing.T) {
	if _, err := planExecutor(store.NewMemoryStore(), "delete-everything"); err == nil {
		t.Fatal("planExecutor() error = nil, want error")
	}
}
