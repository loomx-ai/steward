package alicloud

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
	"github.com/loomx-ai/steward/internal/provider/spec"
)

func TestFCTriggersFanOutOverFunctionsByServiceAndFunctionName(t *testing.T) {
	t.Parallel()

	// Shapes follow the official FC-Open 2021-04-06 metadata: ListTriggers
	// needs both the service name and the function name.
	resourceCenter := &runtimeResourceCenterClient{page: ResourcePage{Resources: []ResourceRecord{{
		ResourceType: "ACS::FC::Service", ResourceID: "service-a", RegionID: "cn-hangzhou",
	}}}}
	runtime, factory := encryptionKeyRuntime(t, func(invocation contracts.Invocation) (contracts.InvocationResult, error) {
		switch invocation.Operation {
		case "AlibabaCloud.FC.ListFunctions":
			return contracts.InvocationResult{Data: map[string]any{"functions": []any{
				map[string]any{"functionId": "fid-a", "functionName": "function-a"},
			}}}, nil
		case "AlibabaCloud.FC.ListTriggers":
			if invocation.Parameters["serviceName"] != "service-a" || invocation.Parameters["functionName"] != "function-a" {
				return contracts.InvocationResult{}, errors.New("unexpected trigger parent")
			}
			return contracts.InvocationResult{Data: map[string]any{"triggers": []any{
				map[string]any{"triggerId": "tid-a", "triggerName": "oss-upload", "triggerType": "oss", "qualifier": "LATEST"},
			}}}, nil
		}
		return contracts.InvocationResult{}, errors.New("unexpected call " + invocation.Operation)
	})
	factory.resourceCenter = resourceCenter
	kind := runtime.resourceKindByNativeType["ACS::FC::Trigger"]
	batch, err := runtime.List(context.Background(), contracts.InventoryRequest{
		ConnectionID: "connection-a", Scope: asset.Scope{Kind: asset.ScopeRegion, NativeID: "cn-hangzhou", Location: "cn-hangzhou"},
		Source: "product-api", ResourceKind: &kind, Limit: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(batch.Items) != 1 || batch.Items[0].NativeID != "tid-a" ||
		batch.Items[0].Normalized["serviceName"] != "service-a" ||
		batch.Items[0].Normalized["functionName"] != "function-a" ||
		batch.Items[0].Normalized["functionId"] != "fid-a" ||
		batch.Items[0].Normalized["triggerName"] != "oss-upload" {
		t.Fatalf("triggers = %+v, calls = %+v", batch.Items, factory.calls)
	}
}

func TestFCDeletionOrderFollowsOfficialNotEmptyErrors(t *testing.T) {
	t.Parallel()

	// FunctionNotEmpty: a function with triggers cannot be deleted.
	// ServiceNotEmpty: a service with functions, versions or aliases cannot.
	// VersionAlreadyInUse: a version an alias routes to cannot.
	runtime, _ := encryptionKeyRuntime(t, nil)
	want := map[string][]spec.RelationshipSpec{
		"ACS::FC::Trigger":  {{Type: "member_of", TargetType: "ACS::FC::Function", TargetIDPath: "functionId"}},
		"ACS::FC::Function": {{Type: "member_of", TargetType: "ACS::FC::Service", TargetIDPath: "serviceName"}},
		"ACS::FC::Version":  {{Type: "member_of", TargetType: "ACS::FC::Service", TargetIDPath: "serviceName"}},
		"ACS::FC::Alias":    {{Type: "member_of", TargetType: "ACS::FC::Service", TargetIDPath: "serviceName"}, {Type: "uses", TargetType: "ACS::FC::Version", TargetIDPath: "versionRefs"}},
		"ACS::FC3::Trigger": {{Type: "member_of", TargetType: "ACS::FC3::Function", TargetIDPath: "functionId"}},
	}
	for nativeType, relationships := range want {
		compiled, ok := runtime.compiledSpec(nativeType)
		if _, deletable := compiled.Definition.Actions["delete"]; !ok || !deletable {
			t.Fatalf("%s has no delete action", nativeType)
		}
		if !reflect.DeepEqual(compiled.Definition.Relationships, relationships) {
			t.Fatalf("%s relationships = %+v", nativeType, compiled.Definition.Relationships)
		}
	}
	fc3, _ := runtime.compiledSpec("ACS::FC3::Function")
	preconditions := fc3.Definition.Actions["delete"].Preconditions
	if len(preconditions) != 1 || preconditions[0].Path != "lockInfo.lockedBy" {
		t.Fatalf("FC 3.0 function preconditions = %+v", preconditions)
	}
}

func TestFCAliasesReferenceEveryVersionTheyRouteTo(t *testing.T) {
	t.Parallel()

	items := enrichFCAliasVersions([]contracts.InventoryItem{{
		NativeType: "ACS::FC::Alias", NativeID: "service-a/prod",
		Normalized: map[string]any{"serviceName": "service-a"},
		Raw:        map[string]any{"aliasName": "prod", "versionId": "3", "additionalVersionWeight": map[string]any{"2": 0.1}},
	}})
	if got := items[0].Normalized["versionRefs"]; !reflect.DeepEqual(got, []any{"service-a/2", "service-a/3"}) {
		t.Fatalf("version refs = %v", got)
	}
	if identity := readbackIdentity(asset.Asset{
		Identity:   asset.Identity{NativeType: "ACS::FC::Alias", NativeID: "service-a/prod"},
		Normalized: map[string]any{"aliasName": "prod"},
	}); identity != "prod" {
		t.Fatalf("alias readback identity = %q", identity)
	}
}

func TestTokenPaginatedReadbackWithoutNextTokenIsComplete(t *testing.T) {
	t.Parallel()

	// A prefix filter can return a sibling ("fn-2" for "fn"); without a next
	// token the response holds every match, so the deleted one is absent.
	read := spec.ProductAPISpec{ItemsPath: "functions", Pagination: &spec.PaginationSpec{Type: "token", TokenPath: "nextToken"}}
	if !completeReadbackListing(map[string]any{"functions": []any{map[string]any{"functionId": "other"}}}, read) {
		t.Fatal("exhausted token listing was not complete")
	}
	if completeReadbackListing(map[string]any{"nextToken": "more", "functions": []any{}}, read) {
		t.Fatal("listing with a next token was complete")
	}
}
