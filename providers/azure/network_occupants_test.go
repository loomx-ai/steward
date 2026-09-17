package azure

import (
	"encoding/json"
	"os"
	"testing"
)

func TestNetworkOccupantsBlockDeletion(t *testing.T) {
	wire, err := os.ReadFile("fixtures/PublicIpAddressGet.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err = json.Unmarshal(wire, &doc); err != nil {
		t.Fatal(err)
	}
	official := object(object(object(doc["responses"])["200"])["body"])
	if reason := serviceAssociationReason("Microsoft.Network/publicIPAddresses", official); reason != "azure_public_ip_associated" {
		t.Fatal("official associated public IP was not blocked", reason)
	}
	sal := map[string]any{"id": "/subscriptions/s/resourceGroups/g/providers/Microsoft.Network/virtualNetworks/v/subnets/a/serviceAssociationLinks/AppServiceLink", "properties": map[string]any{"linkedResourceType": "Microsoft.Web/serverfarms"}}
	for _, tc := range []struct {
		kind  string
		props map[string]any
		want  string
	}{
		{subnetType, map[string]any{"addressPrefix": "10.0.0.0/24", "delegations": []any{map[string]any{"name": "web"}}}, ""},
		{subnetType, map[string]any{"ipConfigurations": []any{map[string]any{"id": "nic-ip"}}}, "azure_subnet_in_use"},
		{subnetType, map[string]any{"privateEndpoints": []any{map[string]any{"id": "pe"}}}, "azure_subnet_in_use"},
		{subnetType, map[string]any{"serviceAssociationLinks": []any{sal}}, "azure_subnet_in_use"},
		{subnetType, map[string]any{"resourceNavigationLinks": []any{map[string]any{"id": "rnl"}}}, "azure_subnet_in_use"},
		{subnetType, map[string]any{"ipConfigurations": []any{}, "privateEndpoints": []any{}}, ""},
		{subnetType, map[string]any{"ipConfigurations": "unexpected"}, "azure_subnet_in_use"},
		{"Microsoft.Network/networkSecurityGroups", map[string]any{"securityRules": []any{map[string]any{"name": "allow"}}}, ""},
		{"Microsoft.Network/networkSecurityGroups", map[string]any{"subnets": []any{map[string]any{"id": "subnet"}}}, "azure_network_security_group_associated"},
		{"Microsoft.Network/networkSecurityGroups", map[string]any{"networkInterfaces": []any{map[string]any{"id": "nic"}}}, "azure_network_security_group_associated"},
		{"Microsoft.Network/routeTables", map[string]any{"subnets": []any{map[string]any{"id": "subnet"}}}, "azure_route_table_associated"},
		{"Microsoft.Network/natGateways", map[string]any{"subnets": []any{map[string]any{"id": "subnet"}}}, "azure_nat_gateway_associated"},
		{"Microsoft.Network/publicIPAddresses", map[string]any{"natGateway": map[string]any{"id": "nat"}}, "azure_public_ip_associated"},
		{"Microsoft.Network/publicIPAddresses", map[string]any{"ipAddress": "203.0.113.1"}, ""},
	} {
		if got := serviceAssociationReason(tc.kind, map[string]any{"properties": tc.props}); got != tc.want {
			t.Fatal(tc.kind, tc.props, got, tc.want)
		}
	}
}
