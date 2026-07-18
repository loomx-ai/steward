package alicloud

import (
	"strings"
	"testing"
	"testing/fstest"
)

func TestEmbeddedResourceCatalogClassifiesOfficialSnapshot(t *testing.T) {
	t.Parallel()

	catalog, err := loadResourceCatalog(providerFiles)
	if err != nil {
		t.Fatalf("load embedded resource catalog: %v", err)
	}
	if got := catalog.Len(); got != 170 {
		t.Fatalf("resource catalog size = %d, want 170", got)
	}
	if got := len(catalog.InstanceNativeTypes()); got != 137 {
		t.Fatalf("instance resource count = %d, want 137", got)
	}
	for _, nativeType := range []string{
		"ACS::ACK::Cluster",
		"ACS::ALB::LoadBalancer",
		"ACS::ECS::Instance",
		"ACS::ECS::SecurityGroup",
		"ACS::OSS::Bucket",
		"ACS::VPC::VPC",
	} {
		if !catalog.IsInstance(nativeType) {
			t.Fatalf("%s must be classified as an instance", nativeType)
		}
	}
	for _, nativeType := range []string{
		"ACS::ALB::Listener",
		"ACS::ALB::ServerGroup",
		"ACS::AliKafka::Topic",
		"ACS::CR::Repository",
		"ACS::NLB::Listener",
		"ACS::RocketMQ::ConsumerGroup",
	} {
		if catalog.IsInstance(nativeType) {
			t.Fatalf("%s must be classified as a subresource", nativeType)
		}
	}
	if catalog.IsInstance("ACS::Future::Unknown") {
		t.Fatal("unknown resource type must fail closed")
	}

	nativeTypes := catalog.InstanceNativeTypes()
	nativeTypes[0] = "mutated"
	if catalog.InstanceNativeTypes()[0] == "mutated" {
		t.Fatal("instance native types are not defensively copied")
	}
}

func TestLoadResourceCatalogRejectsMalformedDocuments(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		document string
		want     string
	}{
		{
			name: "duplicate native type",
			document: resourceCatalogDocumentFixture(
				`{"native_type":"ACS::ECS::Instance","level":"instance","icon":"/icons/alicloud/acs-ecs-instance.svg","icon_origin":"official","icon_source":"https://example.test/ecs"}`,
				`{"native_type":"ACS::ECS::Instance","level":"subresource","reason":"duplicate"}`,
			),
			want: "duplicate native type",
		},
		{
			name: "invalid level",
			document: resourceCatalogDocumentFixture(
				`{"native_type":"ACS::ECS::Instance","level":"other","reason":"invalid"}`,
			),
			want: "invalid level",
		},
		{
			name: "instance missing icon",
			document: resourceCatalogDocumentFixture(
				`{"native_type":"ACS::ECS::Instance","level":"instance","icon_origin":"official","icon_source":"https://example.test/ecs"}`,
			),
			want: "requires an icon",
		},
		{
			name: "unsafe icon path",
			document: resourceCatalogDocumentFixture(
				`{"native_type":"ACS::ECS::Instance","level":"instance","icon":"https://example.test/ecs.svg","icon_origin":"official","icon_source":"https://example.test/ecs"}`,
			),
			want: "local Alibaba Cloud icon path",
		},
		{
			name: "duplicate icon path",
			document: resourceCatalogDocumentFixture(
				`{"native_type":"ACS::ECS::Instance","level":"instance","icon":"/icons/alicloud/acs-ecs-instance.svg","icon_origin":"official","icon_source":"https://example.test/ecs"}`,
				`{"native_type":"ACS::ECS::Disk","level":"instance","icon":"/icons/alicloud/acs-ecs-instance.svg","icon_origin":"official","icon_source":"https://example.test/disk"}`,
			),
			want: "duplicate icon path",
		},
		{
			name: "instance missing provenance",
			document: resourceCatalogDocumentFixture(
				`{"native_type":"ACS::ECS::Instance","level":"instance","icon":"/icons/alicloud/acs-ecs-instance.svg"}`,
			),
			want: "icon origin and source",
		},
		{
			name: "invalid icon origin",
			document: resourceCatalogDocumentFixture(
				`{"native_type":"ACS::ECS::Instance","level":"instance","icon":"/icons/alicloud/acs-ecs-instance.svg","icon_origin":"other","icon_source":"https://example.test/ecs"}`,
			),
			want: "invalid icon origin",
		},
		{
			name: "official icon extension mismatch",
			document: resourceCatalogDocumentFixture(
				`{"native_type":"ACS::ECS::Instance","level":"instance","icon":"/icons/alicloud/acs-ecs-instance.png","icon_origin":"official","icon_source":"https://example.test/ecs"}`,
			),
			want: "official SVG icon",
		},
		{
			name: "generated icon extension mismatch",
			document: resourceCatalogDocumentFixture(
				`{"native_type":"ACS::ECS::Instance","level":"instance","icon":"/icons/alicloud/acs-ecs-instance.svg","icon_origin":"generated","icon_source":"imagegen:test"}`,
			),
			want: "generated PNG icon",
		},
		{
			name: "subresource defines icon metadata",
			document: resourceCatalogDocumentFixture(
				`{"native_type":"ACS::ALB::Listener","level":"subresource","parent_native_type":"ACS::ALB::LoadBalancer","icon":"/icons/alicloud/acs-alb-listener.svg","icon_origin":"official","icon_source":"https://example.test/listener"}`,
			),
			want: "must not define icon metadata",
		},
		{
			name: "subresource missing parent and reason",
			document: resourceCatalogDocumentFixture(
				`{"native_type":"ACS::ALB::Listener","level":"subresource"}`,
			),
			want: "parent native type or reason",
		},
		{
			name: "missing revision",
			document: `{
				"source_url":"https://example.test/resources",
				"resources":[
					{"native_type":"ACS::ALB::Listener","level":"subresource","reason":"listener"}
				]
			}`,
			want: "revision",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			_, err := loadResourceCatalog(fstest.MapFS{
				"resourcecenter/resources.json": &fstest.MapFile{Data: []byte(test.document)},
			})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestRuntimeLoadsValidatedEmbeddedResourceCatalog(t *testing.T) {
	t.Parallel()

	runtime, err := newRuntime(&credentialSource{}, &runtimeFactory{})
	if err != nil {
		t.Fatalf("create Alibaba Cloud runtime: %v", err)
	}
	if runtime.resourceCatalog.Len() != 170 {
		t.Fatalf("runtime resource catalog size = %d, want 170", runtime.resourceCatalog.Len())
	}
	if got := len(runtime.resourceCatalog.InstanceNativeTypes()); got != 137 {
		t.Fatalf("runtime instance resource count = %d, want 137", got)
	}
}

func TestRuntimeResourceCatalogExposesLocalizedHumanNames(t *testing.T) {
	t.Parallel()

	runtime, err := newRuntime(&credentialSource{}, &runtimeFactory{})
	if err != nil {
		t.Fatalf("create Alibaba Cloud runtime: %v", err)
	}
	kinds, _ := runtime.ResourceKinds()
	for _, kind := range kinds {
		if !runtime.resourceCatalog.IsInstance(kind.NativeType) {
			continue
		}
		expected, exists := catalogDisplayNames(kind.NativeType)
		if !exists {
			t.Fatalf("%s is missing catalog display names", kind.NativeType)
		}
		for _, locale := range []string{"zh-CN", "en-US"} {
			name := strings.TrimSpace(kind.DisplayNames[locale])
			if name == "" {
				t.Fatalf("%s is missing %s display name", kind.NativeType, locale)
			}
			if strings.Contains(name, "ACS::") {
				t.Fatalf("%s %s display name exposes provider code %q", kind.NativeType, locale, name)
			}
			if name != expected[locale] {
				t.Fatalf("%s %s display name = %q, want catalog name %q", kind.NativeType, locale, name, expected[locale])
			}
		}
	}

	var instanceFound bool
	for _, kind := range kinds {
		if kind.NativeType != "ACS::ECS::Instance" {
			continue
		}
		instanceFound = true
		labels := kind.FieldDisplayNames
		if labels["accountId"]["zh-CN"] != "账号 ID" {
			t.Fatalf("accountId labels = %+v", labels["accountId"])
		}
		if labels["vpc_id"]["zh-CN"] != "所属专有网络" {
			t.Fatalf("vpc_id labels = %+v", labels["vpc_id"])
		}
		if _, translated := labels["instance_charge_type"]; translated {
			t.Fatal("untranslated fields must remain absent so the UI can show the raw key")
		}
		labels["accountId"]["zh-CN"] = "mutated"
		break
	}
	if !instanceFound {
		t.Fatal("ECS instance resource kind is missing")
	}

	rebuiltKinds, _ := runtime.ResourceKinds()
	for _, kind := range rebuiltKinds {
		if kind.NativeType != "ACS::ECS::Instance" {
			continue
		}
		if got := kind.FieldDisplayNames["accountId"]["zh-CN"]; got != "账号 ID" {
			t.Fatalf("rebuilt catalog retained mutated field labels: %+v", kind.FieldDisplayNames["accountId"])
		}
		return
	}
	t.Fatal("rebuilt ECS instance resource kind is missing")
}

func resourceCatalogDocumentFixture(entries ...string) string {
	return `{
		"revision":"2026-07-27",
		"source_url":"https://example.test/resources",
		"resources":[` + strings.Join(entries, ",") + `]
	}`
}
