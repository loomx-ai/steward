package azure

import (
	"context"
	_ "embed"
	"encoding/json"
	"net/url"
	"slices"
	"strings"
	"sync"
)

//go:generate python3 ../../scripts/sync-datafactory-references.py
//go:embed catalog/generated/datafactory-references.json
var dataFactoryReferenceJSON []byte

var dataFactoryReferenceShapes = sync.OnceValues(func() (map[string]map[string]any, error) {
	var document struct {
		Shapes map[string]map[string]any `json:"shapes"`
	}
	if err := json.Unmarshal(dataFactoryReferenceJSON, &document); err != nil {
		return nil, err
	}
	if len(document.Shapes) == 0 {
		return nil, serviceDenied("datafactory_reference_shapes_missing")
	}
	return document.Shapes, nil
})

var dataFactoryResourceSchemas = map[string]string{
	dataFactoryType: "Factory", dataFactoryCDCType: "ChangeDataCaptureResource", dataFactoryCredentialType: "CredentialResource",
	dataFactoryFlowType: "DataFlowResource", dataFactoryDatasetType: "DatasetResource", dataFactoryParametersType: "GlobalParameterResource",
	dataFactoryIRType: "IntegrationRuntimeResource", dataFactoryNodeType: "SelfHostedIntegrationRuntimeNode", dataFactoryLinkedType: "LinkedServiceResource",
	dataFactoryNetworkType: "ManagedVirtualNetworkResource", dataFactoryEndpointType: "ManagedPrivateEndpointResource", dataFactoryPipelineType: "PipelineResource",
	dataFactoryPECType: "PrivateEndpointConnectionResource", dataFactoryTriggerType: "TriggerResource",
}

func dataFactoryNamedReference(root, collection, name string) (string, error) {
	id := root + "/" + collection + "/" + name
	parsed, kind, err := parseID(id)
	expected := dataFactoryKind(dataFactoryType + "/" + collection)
	if err != nil || name == "" || name != strings.TrimSpace(name) || strings.ContainsAny(name, "/\t@{}") || expected == "" || !strings.EqualFold(kind, expected) || len(strings.Split(parsed, "/")) != 11 {
		return "", serviceDenied("invalid_datafactory_named_reference")
	}
	return parsed, nil
}

func dataFactoryTypedReferences(id, kind string, raw map[string]any) (map[string][]string, error) {
	shapes, err := dataFactoryReferenceShapes()
	if err != nil {
		return nil, err
	}
	refs := map[string][]string{}
	root := dataFactoryRoot(id)
	remaining := 100000
	var walk func(map[string]any, any, int) error
	walk = func(shape map[string]any, value any, depth int) error {
		if len(shape) == 0 || value == nil {
			return nil
		}
		remaining--
		if remaining < 0 || depth > 128 {
			return serviceDenied("datafactory_reference_shape_limit")
		}
		if name := text(shape["s"]); name != "" {
			target, ok := shapes[name]
			if !ok {
				return serviceDenied("datafactory_reference_shape_missing")
			}
			return walk(target, value, depth+1)
		}
		if element := object(shape["a"]); element != nil {
			values, ok := value.([]any)
			if !ok {
				return serviceDenied("invalid_datafactory_reference_array")
			}
			for _, v := range values {
				if err := walk(element, v, depth+1); err != nil {
					return err
				}
			}
			return nil
		}
		raw, ok := value.(map[string]any)
		if !ok {
			return serviceDenied("invalid_datafactory_reference_object")
		}
		if collection := text(shape["r"]); collection != "" {
			targetKind := dataFactoryKind(dataFactoryType + "/" + collection)
			expected := map[string]string{"datasets": "DatasetReference", "linkedservices": "LinkedServiceReference", "integrationRuntimes": "IntegrationRuntimeReference", "dataflows": "DataFlowReference", "pipelines": "PipelineReference", "triggers": "TriggerReference", "credentials": "CredentialReference", "managedVirtualNetworks": "ManagedVirtualNetworkReference"}[collection]
			name, ok := raw["referenceName"].(string)
			if !ok || raw["type"] != expected {
				return serviceDenied("invalid_datafactory_reference_type")
			}
			target, err := dataFactoryNamedReference(root, collection, name)
			if err != nil {
				return err
			}
			addReference(refs, targetKind, target)
			return nil
		}
		if discriminator := text(shape["d"]); discriminator != "" {
			variant, ok := object(shape["v"])[text(raw[discriminator])]
			if !ok {
				return serviceDenied("unknown_datafactory_reference_variant")
			}
			if err := walk(object(variant), raw, depth+1); err != nil {
				return err
			}
		}
		for field, child := range object(shape["f"]) {
			if value, present := raw[field]; present {
				if err := walk(object(child), value, depth+1); err != nil {
					return err
				}
			}
		}
		if element := object(shape["m"]); element != nil {
			for _, value := range raw {
				if err := walk(element, value, depth+1); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := walk(shapes[dataFactoryResourceSchemas[kind]], raw, 0); err != nil {
		return nil, err
	}
	if parent := dataFactoryParent(id, kind); parent != "" {
		_, parentKind, _ := parseID(parent)
		addReference(refs, dataFactoryKind(parentKind), parent)
	}
	for key := range refs {
		slices.Sort(refs[key])
	}
	return refs, nil
}

func dataFactoryARMReference(refs map[string][]string, expected string, value any) error {
	if value == nil || value == "" {
		return nil
	}
	wire, ok := value.(string)
	id, kind, err := parseID(wire)
	if !ok || err != nil || wire != strings.TrimSpace(wire) || expected != "" && !strings.EqualFold(kind, expected) {
		return serviceDenied("invalid_datafactory_arm_reference")
	}
	if known, ok := findType(kind); ok {
		kind = known.NativeType
	}
	addReference(refs, kind, id)
	return nil
}

func (c *client) dataFactoryReferences(ctx context.Context, id, kind string, raw map[string]any, indexes map[string][]serviceChild) (map[string][]string, error) {
	refs, err := dataFactoryTypedReferences(id, kind, raw)
	if err != nil {
		return nil, err
	}
	add := func(kind string, value any) {
		if err == nil {
			err = dataFactoryARMReference(refs, kind, value)
		}
	}
	props := object(raw["properties"])
	switch kind {
	case dataFactoryType:
		identities := object(object(raw["identity"])["userAssignedIdentities"])
		if value := object(raw["identity"])["userAssignedIdentities"]; value != nil && identities == nil {
			return nil, serviceDenied("invalid_datafactory_user_identities")
		}
		for identity := range identities {
			add(rbacUserIdentityType, identity)
		}
		add("Microsoft.Purview/accounts", object(props["purviewConfiguration"])["purviewResourceId"])
		encryption := object(props["encryption"])
		add(rbacUserIdentityType, object(encryption["identity"])["userAssignedIdentity"])
		if value := encryption["vaultBaseUrl"]; value != nil && value != "" {
			endpoint, e := url.Parse(text(value))
			if e != nil || endpoint.Scheme != "https" || endpoint.Host == "" || endpoint.User != nil || endpoint.Port() != "" || endpoint.RawQuery != "" || endpoint.ForceQuery || endpoint.Fragment != "" || endpoint.RawPath != "" || endpoint.Path != "" && endpoint.Path != "/" {
				return nil, serviceDenied("invalid_datafactory_key_vault_url")
			}
			target, e := c.apimExternalReference(ctx, apimVaultType, "https://"+strings.ToLower(endpoint.Host), indexes)
			if e != nil {
				return nil, e
			}
			addReference(refs, apimVaultType, target)
		}
	case dataFactoryCredentialType:
		if props["type"] == "ManagedIdentity" {
			add(rbacUserIdentityType, object(props["typeProperties"])["resourceId"])
		}
	case dataFactoryEndpointType:
		add("", props["privateLinkResourceId"])
	case dataFactoryPECType:
		add("Microsoft.Network/privateEndpoints", object(props["privateEndpoint"])["id"])
	case dataFactoryIRType:
		typ := object(props["typeProperties"])
		linked := object(typ["linkedInfo"])
		if linked["authorizationType"] == "RBAC" {
			if text(linked["resourceId"]) == "" {
				return nil, serviceDenied("datafactory_shared_runtime_resource_id_missing")
			}
			add(dataFactoryIRType, linked["resourceId"])
		}
		add(subnetType, object(typ["customerVirtualNetwork"])["subnetId"])
		network := object(object(typ["computeProperties"])["vNetProperties"])
		add(vnetType, network["vNetId"])
		add(subnetType, network["subnetId"])
		if network["vNetId"] != nil && network["subnet"] != nil {
			subnet, ok := network["subnet"].(string)
			if !ok || subnet == "" || strings.ContainsAny(subnet, "/\t\r\n") || subnet != strings.TrimSpace(subnet) {
				return nil, serviceDenied("invalid_datafactory_subnet_reference")
			}
			add(subnetType, text(network["vNetId"])+"/subnets/"+subnet)
		}
		if ips, present := network["publicIPs"]; present && ips != nil {
			values, ok := ips.([]any)
			if !ok {
				return nil, serviceDenied("invalid_datafactory_public_ips")
			}
			for _, ip := range values {
				add("Microsoft.Network/publicIPAddresses", ip)
			}
		}
	}
	for key := range refs {
		slices.Sort(refs[key])
	}
	return refs, err
}
