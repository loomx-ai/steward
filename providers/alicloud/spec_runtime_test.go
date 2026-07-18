package alicloud

import (
	"context"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
	"github.com/loomx-ai/steward/internal/provider/spec"
)

func TestNextSpecCursorAcceptsNumericStringTotal(t *testing.T) {
	t.Parallel()

	next, err := nextSpecCursor(
		map[string]any{"Data": map[string]any{"TotalCount": "51"}},
		&spec.PaginationSpec{
			Type:              "page-number",
			PageParameter:     "PageNumber",
			PageSizeParameter: "PageSize",
			TotalPath:         "Data.TotalCount",
		},
		1,
		50,
		50,
	)
	if err != nil || next != "2" {
		t.Fatalf("next cursor=%q err=%v", next, err)
	}
}

func TestOffsetSpecPaginationStartsAtZeroAndAdvancesByResults(t *testing.T) {
	t.Parallel()

	parameters := map[string]any{}
	offset, pageSize, err := applySpecPagination(
		parameters,
		&spec.PaginationSpec{
			Type:              "offset",
			OffsetParameter:   "offset",
			PageSizeParameter: "size",
			TotalPath:         "total",
			MaxPageSize:       100,
		},
		"",
		250,
	)
	if err != nil || offset != 0 || pageSize != 100 ||
		parameters["offset"] != 0 || parameters["size"] != 100 {
		t.Fatalf(
			"offset=%d pageSize=%d parameters=%+v err=%v",
			offset,
			pageSize,
			parameters,
			err,
		)
	}
	next, err := nextSpecCursor(
		map[string]any{"total": 250},
		&spec.PaginationSpec{Type: "offset", TotalPath: "total"},
		offset,
		pageSize,
		100,
	)
	if err != nil || next != "100" {
		t.Fatalf("next cursor=%q err=%v", next, err)
	}
}

func TestResolveSpecParametersReadsNormalizedResourceField(t *testing.T) {
	t.Parallel()

	parameters, err := resolveSpecParameters(
		map[string]any{"ServiceName": "resource.normalized.serviceName"},
		specParameterContext{
			normalized: map[string]any{"serviceName": "service-a"},
		},
	)
	if err != nil || parameters["ServiceName"] != "service-a" {
		t.Fatalf("parameters=%+v err=%v", parameters, err)
	}
	_, err = resolveSpecParameters(
		map[string]any{"ServiceName": "resource.normalized.missing"},
		specParameterContext{normalized: map[string]any{}},
	)
	if err == nil {
		t.Fatal("missing normalized resource field must fail")
	}
}

func TestProductAPIRegionUsesGlobalScopeEndpointRegion(t *testing.T) {
	t.Parallel()

	region, err := productAPIRegion(contracts.InventoryRequest{
		Scope: asset.Scope{
			Kind: asset.ScopeGlobal, NativeID: "global", Location: "cn-hangzhou",
		},
	})
	if err != nil || region != "cn-hangzhou" {
		t.Fatalf("region=%q err=%v", region, err)
	}

	_, err = productAPIRegion(contracts.InventoryRequest{
		Scope: asset.Scope{Kind: asset.ScopeGlobal, NativeID: "global"},
	})
	if err == nil {
		t.Fatal("global product API inventory without endpoint region must fail")
	}
}

func TestProductAPIEmptyObjectResponseIsAnEmptyTerminalPage(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		data map[string]any
	}{
		{name: "root", data: map[string]any{}},
		{name: "nested", data: map[string]any{"data": map[string]any{}}},
		{name: "null list", data: map[string]any{"data": map[string]any{"list": nil}}},
		{name: "empty string list", data: map[string]any{"data": map[string]any{"list": ""}}},
		{name: "null parent", data: map[string]any{"data": nil}},
		{name: "typed null list", data: map[string]any{"data": map[string]any{"list": []map[string]any(nil)}}},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			source := &credentialSource{wantConnection: "connection-a", value: contracts.Credential{
				Type:   asset.CredentialAliCloudAccessKey,
				Values: map[string]string{"access_key_id": "id", "access_key_secret": "secret"},
			}}
			factory := &runtimeFactory{invokeResult: contracts.InvocationResult{Data: test.data}}
			runtime, err := newRuntime(source, factory)
			if err != nil {
				t.Fatal(err)
			}
			page, err := runtime.invokeProductAPIPage(
				context.Background(),
				contracts.InventoryRequest{ConnectionID: "connection-a"},
				spec.ProductAPISpec{
					Operation: "AlibabaCloud.RocketMQ.ListInstances",
					ItemsPath: "data.list",
					Pagination: &spec.PaginationSpec{
						Type: "page-number", PageParameter: "pageNumber",
						PageSizeParameter: "pageSize", TotalPath: "data.totalCount",
					},
				},
				specParameterContext{region: "cn-hangzhou"},
				"",
				100,
			)
			if err != nil {
				t.Fatal(err)
			}
			if len(page.items) != 0 || page.next != "" {
				t.Fatalf("empty object page=%+v", page)
			}
		})
	}
}

func TestProductAPISkipsFreeCloudFirewallWithoutInstanceID(t *testing.T) {
	t.Parallel()

	items, err := inventoryItemsFromProductAPI(
		[]any{map[string]any{"UserStatus": false}},
		spec.CompiledSpec{
			Definition: spec.ResourceKindSpec{
				Discovery: spec.DiscoverySpec{
					List: &spec.ProductAPISpec{
						Operation:    "AlibabaCloud.CloudFirewall.DescribeUserBuyVersion",
						IdentityPath: "InstanceId",
					},
				},
			},
		},
		contracts.InventoryRequest{},
		"cn-hangzhou",
		"",
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Fatalf("free Cloud Firewall items=%+v", items)
	}
}

func TestProductAPIAcceptsTypedObjectSlice(t *testing.T) {
	t.Parallel()

	source := &credentialSource{wantConnection: "connection-a", value: contracts.Credential{
		Type:   asset.CredentialAliCloudAccessKey,
		Values: map[string]string{"access_key_id": "id", "access_key_secret": "secret"},
	}}
	factory := &runtimeFactory{invokeResult: contracts.InvocationResult{Data: map[string]any{
		"data": map[string]any{"list": &[]map[string]any{{"id": "bucket-a"}}},
	}}}
	runtime, err := newRuntime(source, factory)
	if err != nil {
		t.Fatal(err)
	}
	page, err := runtime.invokeProductAPIPage(
		context.Background(),
		contracts.InventoryRequest{ConnectionID: "connection-a"},
		spec.ProductAPISpec{Operation: "AlibabaCloud.OSS.ListBuckets", ItemsPath: "data.list"},
		specParameterContext{region: "cn-hangzhou"},
		"",
		100,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.items) != 1 {
		t.Fatalf("typed slice page=%+v", page)
	}
}

func TestProductAPIRegionShardDropsCrossRegionRecords(t *testing.T) {
	t.Parallel()

	compiled := spec.CompiledSpec{
		Definition: spec.ResourceKindSpec{Discovery: spec.DiscoverySpec{List: &spec.ProductAPISpec{
			Operation: "AlibabaCloud.ARMS.ListEnvironments", IdentityPath: "EnvironmentId",
		}}},
	}
	raw := []any{
		map[string]any{"EnvironmentId": "env-beijing", "RegionId": "cn-beijing"},
		map[string]any{"EnvironmentId": "env-hangzhou", "RegionId": "cn-hangzhou"},
		map[string]any{"EnvironmentId": "env-legacy"},
	}
	regionItems, err := inventoryItemsFromProductAPI(
		raw,
		compiled,
		contracts.InventoryRequest{Scope: asset.Scope{Kind: asset.ScopeRegion, NativeID: "cn-hangzhou"}},
		"cn-hangzhou",
		"",
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(regionItems) != 2 || regionItems[0].NativeID != "env-hangzhou" || regionItems[1].NativeID != "env-legacy" {
		t.Fatalf("region-scoped product API items = %+v", regionItems)
	}
	globalItems, err := inventoryItemsFromProductAPI(
		raw,
		compiled,
		contracts.InventoryRequest{Scope: asset.Scope{Kind: asset.ScopeGlobal, NativeID: "global"}},
		"cn-hangzhou",
		"",
	)
	if err != nil || len(globalItems) != 3 {
		t.Fatalf("global product API items = %+v, err=%v", globalItems, err)
	}
}

func TestValueAtPathTraversesNamedNestedMap(t *testing.T) {
	t.Parallel()

	type namedMap map[string]any
	items := []map[string]any{{"id": "bucket-a"}}
	value := map[string]any{"Buckets": namedMap{"Bucket": &items}}
	if got := valueAtPath(value, "Buckets.Bucket"); got == nil {
		t.Fatal("named nested map path was not resolved")
	}
}

func TestInventoryItemsAcceptTypedProductAPIResource(t *testing.T) {
	t.Parallel()

	type bucket struct {
		Name string `json:"Name"`
	}
	items, err := inventoryItemsFromProductAPI(
		[]any{&bucket{Name: "bucket-a"}},
		spec.CompiledSpec{
			Definition: spec.ResourceKindSpec{
				Discovery: spec.DiscoverySpec{
					List: &spec.ProductAPISpec{
						Operation:    "AlibabaCloud.OSS.ListBuckets",
						IdentityPath: "Name",
					},
				},
			},
		},
		contracts.InventoryRequest{},
		"cn-hangzhou",
		"",
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].NativeID != "bucket-a" {
		t.Fatalf("typed product API items=%+v", items)
	}
}

func TestProductAPIMissingItemsWithZeroTotalIsAnEmptyTerminalPage(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name      string
		operation string
		itemsPath string
		totalPath string
		data      map[string]any
	}{
		{
			name: "ACK", operation: "AlibabaCloud.DescribeClustersForRegion", itemsPath: "clusters", totalPath: "page_info.total_count",
			data: map[string]any{"page_info": map[string]any{"page_number": 1, "page_size": 100, "total_count": 0}},
		},
		{
			name: "RocketMQ", operation: "AlibabaCloud.RocketMQ.ListInstances", itemsPath: "data.list", totalPath: "data.totalCount",
			data: map[string]any{"data": map[string]any{"pageNumber": 1, "pageSize": 100, "totalCount": 0}},
		},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			source := &credentialSource{wantConnection: "connection-a", value: contracts.Credential{
				Type:   asset.CredentialAliCloudAccessKey,
				Values: map[string]string{"access_key_id": "id", "access_key_secret": "secret"},
			}}
			factory := &runtimeFactory{invokeResult: contracts.InvocationResult{Data: test.data}}
			runtime, err := newRuntime(source, factory)
			if err != nil {
				t.Fatal(err)
			}
			page, err := runtime.invokeProductAPIPage(
				context.Background(),
				contracts.InventoryRequest{ConnectionID: "connection-a"},
				spec.ProductAPISpec{
					Operation: test.operation, ItemsPath: test.itemsPath,
					Pagination: &spec.PaginationSpec{
						Type: "page-number", PageParameter: "pageNumber",
						PageSizeParameter: "pageSize", TotalPath: test.totalPath,
					},
				},
				specParameterContext{region: "cn-hangzhou"},
				"",
				100,
			)
			if err != nil {
				t.Fatal(err)
			}
			if len(page.items) != 0 || page.next != "" {
				t.Fatalf("empty page=%+v", page)
			}
		})
	}
}
