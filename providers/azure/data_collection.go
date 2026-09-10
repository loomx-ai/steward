package azure

import (
	"context"
	"fmt"
	"net/url"
	"slices"
	"strings"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/catalog"
)

const dataCollectionRuleType = "Microsoft.Insights/dataCollectionRules"
const dataCollectionEndpointType = "Microsoft.Insights/dataCollectionEndpoints"
const dataCollectionAssociationType = "Microsoft.Insights/dataCollectionRuleAssociations"

func isDataCollectionType(kind string) bool {
	return strings.EqualFold(kind, dataCollectionRuleType) || strings.EqualFold(kind, dataCollectionEndpointType) || strings.EqualFold(kind, dataCollectionAssociationType)
}

func dataCollectionMonitoredResource(value string) (string, error) {
	id, kind, err := parseID(value)
	if err != nil || !strings.EqualFold(kind, dataCollectionAssociationType) {
		return "", serviceDenied("invalid_data_collection_association")
	}
	marker := "/providers/microsoft.insights/datacollectionruleassociations/"
	index := strings.LastIndex(id, marker)
	if index < 0 || strings.Contains(id[index+len(marker):], "/") {
		return "", serviceDenied("invalid_data_collection_association")
	}
	parent, _, err := parseID(id[:index])
	return parent, err
}

// Both fields are optional in Swagger. Keep dual-target resources as ordinary
// dependencies rather than claiming exclusive ownership for either target.
func dataCollectionReferences(raw map[string]any) []string {
	properties := object(raw["properties"])
	refs := []string{}
	for _, target := range []struct{ field, kind string }{{"dataCollectionRuleId", dataCollectionRuleType}, {"dataCollectionEndpointId", dataCollectionEndpointType}} {
		value := properties[target.field]
		if value == nil || value == "" {
			continue
		}
		id, kind, err := parseID(text(value))
		if err != nil || !strings.EqualFold(kind, target.kind) {
			return nil
		}
		refs = append(refs, id)
	}
	return refs
}

func dataCollectionSameReferences(a, b map[string]any) bool {
	x, y := dataCollectionReferences(a), dataCollectionReferences(b)
	return len(x) > 0 && slices.Equal(x, y)
}

func dataCollectionAssociationMembership(raw map[string]any, parentID string) error {
	_, err := dataCollectionMonitoredResource(text(raw["id"]))
	if err != nil || !slices.Contains(dataCollectionReferences(raw), strings.ToLower(parentID)) {
		return serviceDenied("data_collection_association_changed")
	}
	return nil
}

func dataCollectionConfiguration(kind string, raw map[string]any) string {
	safe := safePayload(raw)
	delete(object(safe["properties"]), "provisioningState")
	if strings.EqualFold(kind, dataCollectionEndpointType) {
		delete(object(safe["properties"]), "privateLinkScopedResources")
	}
	if strings.EqualFold(kind, dataCollectionAssociationType) {
		delete(safe, "location") // ProxyResource inherits the collection target's region.
	}
	return serviceParentConfiguration(kind, safe)
}

func dataCollectionIncarnation(planned asset.Asset, live map[string]any) error {
	if !isDataCollectionType(planned.Identity.NativeType) {
		return nil
	}
	if expected := text(planned.Normalized["_data_collection_configuration"]); expected == "" || expected != dataCollectionConfiguration(planned.Identity.NativeType, live) {
		return serviceDenied("data_collection_configuration_changed")
	}
	if planned.Identity.NativeType == dataCollectionAssociationType {
		if _, err := dataCollectionMonitoredResource(text(live["id"])); err != nil || len(dataCollectionReferences(live)) == 0 {
			return serviceDenied("invalid_data_collection_association")
		}
		if expected := text(planned.Normalized["arm_etag"]); expected != "" && expected != text(live["etag"]) {
			return serviceDenied("data_collection_association_changed")
		}
	}
	return nil
}

func (c *client) dataCollectionAssociationList(parentID, parentType string) (catalog.RESTRequest, error) {
	kind, ok := findType(parentType)
	if !ok || (kind.NativeType != dataCollectionRuleType && kind.NativeType != dataCollectionEndpointType) {
		return catalog.RESTRequest{}, fmt.Errorf("invalid data collection association target")
	}
	_, parameters, err := c.resourceOperation(kind, parentID, "GET")
	if err != nil {
		return catalog.RESTRequest{}, err
	}
	name := "Azure.Microsoft.Insights.DataCollectionRuleAssociations_ListByRule"
	if kind.NativeType == dataCollectionEndpointType {
		name = "Azure.Microsoft.Insights.DataCollectionRuleAssociations_ListByDataCollectionEndpoint"
	}
	metadata, err := providerData()
	if err != nil {
		return catalog.RESTRequest{}, err
	}
	operation, _ := metadata.catalog.Operation(name)
	return catalog.BindREST(operation, parameters)
}

// These are reverse indexes of extension resources on monitored machines, not
// nested rule/endpoint children. Reconcile two full lists and native GETs before
// accepting a reviewed set; neither DELETE is used as an implicit unlink.
func (c *client) dataCollectionAssociations(ctx context.Context, parent asset.Identity) ([]serviceChild, error) {
	bound, err := c.dataCollectionAssociationList(parent.NativeID, parent.NativeType)
	if err != nil {
		return nil, err
	}
	u, _ := url.Parse(bound.URL)
	kind, _ := findType(dataCollectionAssociationType)
	read := func() ([]serviceChild, error) {
		records, err := c.listAllURL(ctx, bound.URL, u.Path)
		if err != nil {
			return nil, err
		}
		children := []serviceChild{}
		seen := map[string]bool{}
		for _, value := range records {
			raw := object(value)
			id, nativeType, err := parseID(text(raw["id"]))
			if err != nil || !strings.HasPrefix(id, c.root()+"/") || !strings.EqualFold(nativeType, dataCollectionAssociationType) || !validResponseType(dataCollectionAssociationType, text(raw["type"])) || seen[id] {
				return nil, serviceDenied("invalid_data_collection_association")
			}
			seen[id] = true
			if err := dataCollectionAssociationMembership(raw, parent.NativeID); err != nil {
				return nil, err
			}
			endpoint, err := c.resourceURL(kind, id)
			if err != nil {
				return nil, err
			}
			live, err := c.request(ctx, "GET", endpoint)
			if err != nil {
				return nil, err
			}
			if !validResourceResponse(live, id, dataCollectionAssociationType) || !dataCollectionSameReferences(raw, live.data) {
				return nil, serviceDenied("data_collection_association_changed")
			}
			if err := serviceListedIncarnation(raw, live.data); err != nil {
				return nil, err
			}
			children = append(children, serviceChild{kind: dataCollectionAssociationType, id: id, data: live.data, direct: true})
		}
		slices.SortFunc(children, func(a, b serviceChild) int { return strings.Compare(a.id, b.id) })
		return children, nil
	}
	first, err := read()
	if err != nil {
		return nil, err
	}
	second, err := read()
	if err != nil {
		return nil, err
	}
	if !slices.EqualFunc(first, second, func(a, b serviceChild) bool {
		return a.id == b.id && productGeneration(a.data) == productGeneration(b.data)
	}) {
		return nil, serviceDenied("data_collection_associations_changed")
	}
	return second, nil
}
