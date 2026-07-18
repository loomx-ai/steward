package catalog

import (
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
)

func TestImportOfficialRejectsRemovedProviderFormats(t *testing.T) {
	for _, format := range []string{"discovery", "resource-provider", "azure-resource-provider"} {
		t.Run(format, func(t *testing.T) {
			_, err := ImportOfficial(format, asset.ProviderAWS, "fixture.json", []byte(`{}`))
			if err == nil || !strings.Contains(err.Error(), "unsupported official metadata format") {
				t.Fatalf("ImportOfficial(%q) error = %v, want unsupported format", format, err)
			}
		})
	}
}

func TestOpenAPIImporterSeparatesCatalogIDFromProductAction(t *testing.T) {
	source := []byte(`{
		"info":{"title":"AlibabaCloud"},
		"x-product-endpoints":{
			"ALB":{"cn-hangzhou":"alb.cn-hangzhou.aliyuncs.com"}
		},
		"paths":{
			"/load-balancers":{
				"get":{
					"operationId":"ALB.ListLoadBalancers",
					"x-operation-name":"ListLoadBalancers",
					"x-operation-call":{
						"product":"ALB",
						"version":"2020-06-16",
						"style":"RPC",
						"protocol":"HTTPS",
						"method":"POST",
						"path":"/",
						"endpoint":"alb.{region}.aliyuncs.com",
						"endpoint_overrides":{
							"cn-beijing":"alb.cn-beijing.aliyuncs.com"
						},
						"parameter_position":"query"
					}
				}
			}
		}
	}`)

	imported, err := OpenAPIImporter{}.Import(
		asset.ProviderAliCloud,
		"fixture.json",
		source,
	)
	if err != nil {
		t.Fatalf("Import() error = %v", err)
	}
	if len(imported.Operations) != 1 {
		t.Fatalf("operations = %d, want 1", len(imported.Operations))
	}
	operation := imported.Operations[0]
	if operation.ID != "AlibabaCloud.ALB.ListLoadBalancers" {
		t.Fatalf("operation.ID = %q", operation.ID)
	}
	if operation.Name != "ListLoadBalancers" {
		t.Fatalf("operation.Name = %q", operation.Name)
	}
	if operation.Call == nil || operation.Call.Product != "ALB" {
		t.Fatalf("operation.Call = %#v", operation.Call)
	}
	if operation.Call.EndpointOverrides["cn-hangzhou"] !=
		"alb.cn-hangzhou.aliyuncs.com" {
		t.Fatalf("operation endpoint overrides = %#v", operation.Call.EndpointOverrides)
	}
	if operation.Call.EndpointOverrides["cn-beijing"] !=
		"alb.cn-beijing.aliyuncs.com" {
		t.Fatalf("operation inline endpoint overrides = %#v", operation.Call.EndpointOverrides)
	}
}
