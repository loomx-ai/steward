package domain

import (
	"testing"
	"time"
)

func TestExtractOwnershipReadsCommonTags(t *testing.T) {
	tags := map[string]string{
		"owner":       "alice",
		"team":        "platform",
		"app":         "checkout",
		"env":         "dev",
		"cost-center": "cc-42",
	}

	ownership := ExtractOwnership(tags)

	if ownership.Owner != "alice" {
		t.Fatalf("owner = %q, want alice", ownership.Owner)
	}
	if ownership.Team != "platform" {
		t.Fatalf("team = %q, want platform", ownership.Team)
	}
	if ownership.Application != "checkout" {
		t.Fatalf("application = %q, want checkout", ownership.Application)
	}
	if ownership.Environment != "dev" {
		t.Fatalf("environment = %q, want dev", ownership.Environment)
	}
	if ownership.CostCenter != "cc-42" {
		t.Fatalf("cost center = %q, want cc-42", ownership.CostCenter)
	}
}

func TestIsProtectedResourceDetectsProductionAndExplicitProtection(t *testing.T) {
	tests := []struct {
		name string
		tags map[string]string
		want bool
	}{
		{name: "prod shorthand", tags: map[string]string{"env": "prod"}, want: true},
		{name: "production full", tags: map[string]string{"environment": "production"}, want: true},
		{name: "explicit protect", tags: map[string]string{"cloud-steward:protect": "true"}, want: true},
		{name: "dev", tags: map[string]string{"env": "dev"}, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsProtectedResource(tt.tags)
			if got != tt.want {
				t.Fatalf("IsProtectedResource() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestResourceValidateRequiresStableIdentity(t *testing.T) {
	now := time.Date(2026, 7, 2, 15, 0, 0, 0, time.UTC)
	resource := Resource{
		Provider:   ProviderAliCloud,
		AccountID:  "acct-1",
		Region:     "cn-hangzhou",
		Type:       ResourceTypeECSInstance,
		NativeID:   "i-123",
		Name:       "dev-instance",
		State:      "Running",
		Tags:       map[string]string{"env": "dev"},
		CreatedAt:  now,
		LastSeenAt: now,
	}

	if err := resource.Validate(); err != nil {
		t.Fatalf("Validate() unexpected error: %v", err)
	}

	resource.NativeID = ""
	if err := resource.Validate(); err == nil {
		t.Fatal("Validate() error = nil, want missing native id error")
	}
}
