package azure

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func TestDNSRecordNativeWireAndConditionalDeletion(t *testing.T) {
	for _, tc := range []struct {
		collection, version string
		types               []string
	}{
		{"dnsZones", "2018-05-01", []string{"A", "AAAA", "CAA", "CNAME", "MX", "NS", "PTR", "SOA", "SRV", "TXT"}},
		{"privateDnsZones", "2024-06-01", []string{"A", "AAAA", "CNAME", "MX", "PTR", "SOA", "SRV", "TXT"}},
	} {
		for _, recordType := range tc.types {
			t.Run(tc.collection+"/"+recordType, func(t *testing.T) {
				root := "/subscriptions/" + testSubscription
				group := root + "/resourceGroups/test"
				zoneID := group + "/providers/Microsoft.Network/" + tc.collection + "/example.com"
				name := "*.test"
				if recordType == "SOA" {
					name = "@"
				}
				if recordType == "NS" {
					name = "delegated"
				}
				id := zoneID + "/" + recordType + "/" + name
				zone := map[string]any{"id": zoneID, "type": "Microsoft.Network/" + tc.collection, "name": "example.com", "location": "global", "etag": "zone-etag", "properties": map[string]any{}}
				raw := map[string]any{"id": id, "name": name, "etag": "record-etag", "properties": map[string]any{"metadata": map[string]any{"owner": "dns-team"}, "fqdn": name + ".example.com.", "ttl": 60}}
				deleted := false
				r := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
					path := strings.ToLower(req.URL.Path)
					if req.Method == "DELETE" {
						if recordType == "SOA" || !strings.EqualFold(path, id) || req.Header.Get("If-Match") != "record-etag" {
							t.Fatalf("wrong native conditional delete: %s %v", req.URL, req.Header)
						}
						if req.URL.Query().Get("api-version") != tc.version {
							t.Fatalf("wrong DNS delete version %s", req.URL)
						}
						deleted = true
						return jsonResponse(204, nil, http.Header{"X-Ms-Request-Id": {"dns-delete"}}), nil
					}
					if req.Method != "GET" {
						t.Fatalf("unexpected DNS write %s", req.URL)
					}
					switch {
					case strings.EqualFold(path, id):
						if !strings.Contains(req.URL.Path, "/"+recordType+"/") || req.URL.Query().Get("api-version") != tc.version {
							t.Fatalf("wrong native DNS resource path %s", req.URL)
						}
						if deleted {
							return jsonResponse(404, map[string]any{}, nil), nil
						}
						return jsonResponse(200, raw, nil), nil
					case strings.EqualFold(path, zoneID+"/"+recordType):
						if req.URL.Query().Get("api-version") != tc.version {
							t.Fatalf("wrong DNS list version %s", req.URL)
						}
						return jsonResponse(200, map[string]any{"value": []any{raw}}, http.Header{"X-Ms-Request-Id": {"dns-record-list"}}), nil
					case strings.EqualFold(path, zoneID):
						return jsonResponse(200, zone, nil), nil
					case strings.EqualFold(path, root+"/providers/Microsoft.Network/"+tc.collection), strings.EqualFold(path, group+"/providers/Microsoft.Network/"+tc.collection):
						return jsonResponse(200, map[string]any{"value": []any{zone}}, nil), nil
					case strings.EqualFold(path, root+"/resourceGroups"):
						return jsonResponse(200, map[string]any{"value": []any{map[string]any{"id": group, "name": "test", "location": "eastus", "type": groupType}}}, nil), nil
					case strings.EqualFold(path, group):
						return jsonResponse(200, map[string]any{"id": group, "name": "test"}, nil), nil
					case strings.EqualFold(path, root+"/providers/Microsoft.Authorization/locks"):
						return jsonResponse(200, map[string]any{"value": []any{}}, nil), nil
					}
					t.Fatalf("unexpected DNS request %s", req.URL)
					return nil, nil
				})
				batch, err := r.List(context.Background(), productRequest(r, "Microsoft.Network/"+tc.collection+"/"+recordType))
				if err != nil || !batch.Complete || len(batch.Items) != 1 || batch.Items[0].Location != "global" || batch.Items[0].Tags["owner"] != "dns-team" {
					t.Fatalf("DNS inventory=%+v %v", batch, err)
				}
				item := batch.Items[0]
				value := asset.Asset{ID: "record", Identity: asset.Identity{Provider: asset.ProviderAzure, ConnectionID: "connection", NativeType: item.NativeType, NativeID: item.NativeID}, Location: item.Location, Normalized: item.Normalized}
				driver, err := r.ResolveAction(context.Background(), "connection", value)
				if err != nil {
					t.Fatal(err)
				}
				request := contracts.ActionRequest{Action: "delete", Asset: value}
				result, err := driver.Execute(context.Background(), request)
				if recordType == "SOA" {
					if err == nil || deleted || item.Normalized["cleanup_controller_only"] != true {
						t.Fatal("SOA allowed direct deletion")
					}
					return
				}
				if err != nil || !deleted || result.ProviderRequestID != "dns-delete" {
					t.Fatalf("DNS deletion=%+v %v", result, err)
				}
				wait, err := driver.Wait(context.Background(), request, result)
				if err != nil || !wait.Done {
					t.Fatalf("DNS readback=%+v %v", wait, err)
				}
			})
		}
	}
}

type dnsScenario struct {
	records      map[string]map[string]any
	lists        map[string][]any
	gone         map[string]bool
	deletes      []string
	version      map[string]string
	status       map[string]int
	deleteStatus int
	handle       func(*http.Request) (*http.Response, bool)
}

func newDNSScenario() *dnsScenario {
	return &dnsScenario{records: map[string]map[string]any{}, lists: map[string][]any{}, gone: map[string]bool{}, version: map[string]string{}, status: map[string]int{}}
}
func (s *dnsScenario) add(raw map[string]any, version string) {
	id := strings.ToLower(text(raw["id"]))
	s.records[id] = raw
	s.version[id] = version
}
func (s *dnsScenario) runtime(t *testing.T) *Runtime {
	t.Helper()
	return protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
		id := strings.ToLower(req.URL.Path)
		if version := s.version[id]; version != "" && req.URL.Query().Get("api-version") != version {
			t.Fatalf("wrong native resource API version %s", req.URL)
		}
		if s.handle != nil {
			if response, handled := s.handle(req); handled {
				return response, nil
			}
		}
		if req.Method == "DELETE" {
			if s.records[id] == nil {
				t.Fatalf("unexpected DNS controller delete %s", req.URL)
			}
			if strings.Contains(id, "/virtualnetworklinks/") && req.Header.Get("If-Match") != text(s.records[id]["etag"]) {
				t.Fatal("link lost conditional deletion")
			}
			s.deletes = append(s.deletes, id)
			if s.deleteStatus != 0 {
				return jsonResponse(s.deleteStatus, map[string]any{"error": map[string]any{"code": "PreconditionFailed", "message": "generation changed"}}, nil), nil
			}
			s.gone[id] = true
			return jsonResponse(204, nil, http.Header{"X-Ms-Request-Id": {"dns-controller-delete"}}), nil
		}
		if req.Method != "GET" {
			t.Fatalf("unexpected DNS write %s %s", req.Method, req.URL)
		}
		if status := s.status[id]; status != 0 {
			return jsonResponse(status, map[string]any{}, nil), nil
		}
		if s.gone[id] {
			return jsonResponse(404, map[string]any{}, nil), nil
		}
		if raw := s.records[id]; raw != nil {
			return jsonResponse(200, raw, nil), nil
		}
		if values, exists := s.lists[id]; exists {
			records := []any{}
			for _, value := range values {
				if !s.gone[strings.ToLower(text(object(value)["id"]))] {
					records = append(records, value)
				}
			}
			return jsonResponse(200, map[string]any{"value": records}, nil), nil
		}
		root := "/subscriptions/" + testSubscription
		if id == root+"/providers/microsoft.authorization/locks" {
			return jsonResponse(200, map[string]any{"value": []any{}}, nil), nil
		}
		if len(strings.Split(id, "/")) == 5 && strings.HasPrefix(id, root+"/resourcegroups/") {
			return jsonResponse(200, map[string]any{"id": id}, nil), nil
		}
		t.Fatalf("unexpected DNS API %s", req.URL)
		return nil, nil
	})
}
func dnsAsset(t *testing.T, r *Runtime, raw map[string]any) asset.Asset {
	t.Helper()
	c, err := r.resolve(context.Background(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	// Preserve the actual API payload: native DNS child responses may omit type.
	copy := map[string]any{}
	for k, v := range raw {
		copy[k] = v
	}
	_, kind, _ := parseID(text(raw["id"]))
	rule, _ := findType(kind)
	copy["type"] = rule.NativeType
	item, err := r.inventoryItem(context.Background(), c, copy, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return asset.Asset{ID: asset.AssetID(item.NativeID), Identity: asset.Identity{Provider: asset.ProviderAzure, ConnectionID: "connection", Partition: "azure", NativeType: item.NativeType, NativeID: item.NativeID}, Location: item.Location, Capabilities: asset.CapabilitySet{asset.CapabilityActionable}, Normalized: item.Normalized}
}
func dnsRequest(t *testing.T, r *Runtime, assets []asset.Asset, root asset.Asset) (contracts.ActionRequest, plan.Input) {
	t.Helper()
	contributor, err := r.ServiceLifecycle(context.Background(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	contribution, err := contributor.Contribute(context.Background(), "scope", assets)
	if err != nil || len(contribution.Unresolved) > 0 {
		t.Fatalf("DNS contribution=%+v %v", contribution, err)
	}
	input := plan.Input{Assets: assets, ResolvedAssetIDs: []asset.AssetID{root.ID}, LifecycleBindings: contribution.Bindings, Relationships: contribution.Relationships}
	result, err := plan.Solve(input)
	if err != nil || len(result.Blockers) > 0 {
		t.Fatalf("DNS plan=%+v %v", result, err)
	}
	return servicePlanRequest(result, assets, root), input
}

func servicePlanRequest(result plan.Result, assets []asset.Asset, root asset.Asset) contracts.ActionRequest {
	request := contracts.ActionRequest{Asset: root, Action: "delete"}
	var stepID plan.StepID
	for _, step := range result.Steps {
		if step.AssetID == root.ID && step.Action == "delete" {
			stepID, request.Parameters = step.ID, step.RequestOptions
		}
	}
	for _, impact := range result.ImpactItems {
		if impact.DelegatedTo != stepID {
			continue
		}
		for _, value := range assets {
			if value.ID == impact.AssetID {
				request.LifecycleImpacts = append(request.LifecycleImpacts, contracts.ActionImpact{Asset: value, ControllerID: impact.ControllerID, Delete: impact.Expected == plan.ExpectedDelegatedDelete})
			}
		}
	}
	return request
}

func TestPublicDNSZoneOwnsSystemAndCustomRecordSets(t *testing.T) {
	s := newDNSScenario()
	zoneID := resourceID(publicDNSZoneType, "example.com")
	zone := map[string]any{"id": zoneID, "type": publicDNSZoneType, "name": "example.com", "location": "global", "etag": "zone-generation"}
	s.add(zone, "2018-05-01")
	raw := []map[string]any{zone}
	for _, recordType := range []string{"A", "AAAA", "CAA", "CNAME", "MX", "NS", "PTR", "SOA", "SRV", "TXT"} {
		name := "app"
		if recordType == "NS" || recordType == "SOA" {
			name = "@"
		}
		record := map[string]any{"id": zoneID + "/" + recordType + "/" + name, "name": name, "etag": recordType + "-etag", "properties": map[string]any{"TTL": 60}}
		s.add(record, "2018-05-01")
		raw = append(raw, record)
		s.lists[strings.ToLower(zoneID+"/"+recordType)] = []any{record}
	}
	r := s.runtime(t)
	var assets []asset.Asset
	for _, record := range raw {
		assets = append(assets, dnsAsset(t, r, record))
	}
	request, input := dnsRequest(t, r, assets, assets[0])
	if len(request.LifecycleImpacts) != 10 {
		t.Fatal("zone omitted record set impacts")
	}
	input.RequestOptions = map[asset.AssetID]map[string]any{assets[0].ID: {"retain_resources": []string{assets[1].Identity.NativeID}}}
	result, err := plan.Solve(input)
	if err != nil || len(result.Blockers) == 0 {
		t.Fatalf("zone retention ignored %+v %v", result, err)
	}
	driver, err := r.ResolveAction(context.Background(), "connection", assets[0])
	if err != nil {
		t.Fatal(err)
	}
	resultOp, err := driver.Execute(context.Background(), request)
	if err != nil || len(s.deletes) != 1 {
		t.Fatalf("zone deletion %v %v", s.deletes, err)
	}
	encoded, _ := json.Marshal(request)
	json.Unmarshal(encoded, &request)
	driver, err = r.ResolveAction(context.Background(), "connection", assets[0])
	if err != nil {
		t.Fatal(err)
	}
	wait, err := driver.Wait(context.Background(), request, resultOp)
	if err != nil || wait.Done {
		t.Fatalf("lost record readback %+v %v", wait, err)
	}
	for _, record := range raw[1:] {
		s.gone[strings.ToLower(text(record["id"]))] = true
	}
	wait, err = driver.Wait(context.Background(), request, resultOp)
	if err != nil || !wait.Done {
		t.Fatalf("zone readback %+v %v", wait, err)
	}
}

func TestDNSMetadataProtectionOverridesSystemRecordPolicy(t *testing.T) {
	kind, _ := findType(publicDNSZoneType + "/SOA")
	raw := map[string]any{"id": resourceID(publicDNSZoneType, "example.com") + "/SOA/@", "properties": map[string]any{"metadata": map[string]any{"steward:protected": "true"}}}
	if reason := protectionReason(kind, raw); reason != "azure_protected_tag" {
		t.Fatalf("metadata protection ignored: %s", reason)
	}
	if controllerOnlyReason("azure_protected_tag") {
		t.Fatal("protected record could be delegated")
	}
}

func privateDNSGroupScenario() (*dnsScenario, []map[string]any) {
	s := newDNSScenario()
	zoneID := strings.Replace(resourceID(privateDNSZoneType, "privatelink.example.com"), "/resourceGroups/test/", "/resourceGroups/shared-dns/", 1)
	groupID := resourceID("Microsoft.Network/privateEndpoints", "endpoint") + "/privateDnsZoneGroups/default"
	zone := map[string]any{"id": zoneID, "name": "privatelink.example.com", "location": "global", "etag": "zone-etag"}
	groupRecords := []any{}
	records := []map[string]any{}
	for _, family := range []string{"A", "AAAA"} {
		address, field, key := "10.1.0.4", "aRecords", "ipv4Address"
		if family == "AAAA" {
			address, field, key = "fd00:1::4", "aaaaRecords", "ipv6Address"
		}
		raw := map[string]any{"id": zoneID + "/" + family + "/endpoint", "name": "endpoint", "etag": family + "-etag", "properties": map[string]any{"ttl": 60, "fqdn": "endpoint.privatelink.example.com.", field: []any{map[string]any{key: address}}}}
		records = append(records, raw)
		groupRecords = append(groupRecords, map[string]any{"recordType": family, "recordSetName": "endpoint", "fqdn": "endpoint.privatelink.example.com.", "ttl": 60, "ipAddresses": []any{address}})
		s.add(raw, "2024-06-01")
	}
	group := map[string]any{"id": groupID, "name": "default", "etag": "group-etag", "properties": map[string]any{"privateDnsZoneConfigs": []any{map[string]any{"name": "zone", "properties": map[string]any{"privateDnsZoneId": zoneID, "recordSets": groupRecords}}}}}
	s.add(zone, "2024-06-01")
	s.add(group, "2024-05-01")
	for _, kind := range append(dnsChildTypes(privateDNSZoneType), privateDNSLinkType) {
		s.lists[strings.ToLower(zoneID+"/"+last(kind))] = []any{}
	}
	for _, record := range records {
		_, kind, _ := parseID(text(record["id"]))
		s.lists[strings.ToLower(zoneID+"/"+last(kind))] = []any{record}
	}
	return s, append([]map[string]any{group, zone}, records...)
}

func TestPrivateDNSZoneGroupOwnsExactExternalRecords(t *testing.T) {
	s, raw := privateDNSGroupScenario()
	r := s.runtime(t)
	assets := []asset.Asset{dnsAsset(t, r, raw[0]), dnsAsset(t, r, raw[1]), dnsAsset(t, r, raw[2]), dnsAsset(t, r, raw[3])}
	// Zone precedes the external controller in input; ownership must still be unique.
	request, input := dnsRequest(t, r, []asset.Asset{assets[1], assets[2], assets[0], assets[3]}, assets[0])
	if len(input.LifecycleBindings) != 2 || len(request.LifecycleImpacts) != 2 {
		t.Fatalf("DNS ownership overlap: %+v", input.LifecycleBindings)
	}
	for _, impact := range request.LifecycleImpacts {
		if impact.ControllerID != assets[0].ID {
			t.Fatal("zone took endpoint records")
		}
	}
	driver, err := r.ResolveAction(context.Background(), "connection", assets[0])
	if err != nil {
		t.Fatal(err)
	}
	result, err := driver.Execute(context.Background(), request)
	if err != nil || len(s.deletes) != 1 {
		t.Fatalf("group deletion %v %v", s.deletes, err)
	}
	data, _ := json.Marshal(request)
	if err := json.Unmarshal(data, &request); err != nil {
		t.Fatal(err)
	}
	wait, err := driver.Wait(context.Background(), request, result)
	if err != nil || wait.Done {
		t.Fatalf("external records lost: %+v %v", wait, err)
	}
	for _, value := range raw[2:] {
		s.gone[strings.ToLower(text(value["id"]))] = true
	}
	wait, err = driver.Wait(context.Background(), request, result)
	if err != nil || !wait.Done {
		t.Fatalf("external readback %+v %v", wait, err)
	}
	if s.gone[strings.ToLower(text(raw[1]["id"]))] {
		t.Fatal("shared zone deleted")
	}
}

func TestPrivateDNSGroupRejectsChangedOrUnreviewedImpacts(t *testing.T) {
	for _, mode := range []string{"missing impact", "retain", "foreign controller", "foreign connection", "foreign subscription", "address added", "address replaced", "ttl changed", "fqdn changed", "auto registration", "protected metadata", "record generation", "record forbidden", "group forbidden", "managed group", "record lock", "missing configs", "missing records", "duplicate records", "unsupported record type", "record identity", "zone target changed"} {
		t.Run(mode, func(t *testing.T) {
			s, raw := privateDNSGroupScenario()
			r := s.runtime(t)
			assets := []asset.Asset{dnsAsset(t, r, raw[0]), dnsAsset(t, r, raw[2]), dnsAsset(t, r, raw[3])}
			request, _ := dnsRequest(t, r, assets, assets[0])
			properties := object(raw[2]["properties"])
			config := object(object(object(raw[0]["properties"])["privateDnsZoneConfigs"].([]any)[0])["properties"])
			groupID := strings.Join(strings.Split(strings.ToLower(text(raw[2]["id"])), "/")[:5], "/")
			switch mode {
			case "missing impact":
				request.LifecycleImpacts = request.LifecycleImpacts[1:]
			case "retain":
				request.LifecycleImpacts[0].Delete = false
			case "foreign controller":
				request.LifecycleImpacts[0].ControllerID = "foreign"
			case "foreign connection":
				request.LifecycleImpacts[0].Asset.Identity.ConnectionID = "foreign"
			case "foreign subscription":
				request.LifecycleImpacts[0].Asset.Identity.NativeID = strings.Replace(request.LifecycleImpacts[0].Asset.Identity.NativeID, testSubscription, "foreign", 1)
			case "address added":
				properties["aRecords"] = []any{map[string]any{"ipv4Address": "10.1.0.4"}, map[string]any{"ipv4Address": "10.1.0.5"}}
			case "address replaced":
				properties["aRecords"] = []any{map[string]any{"ipv4Address": "10.1.0.5"}}
			case "ttl changed":
				properties["ttl"] = 90
			case "fqdn changed":
				properties["fqdn"] = "another.example.com."
			case "auto registration":
				properties["isAutoRegistered"] = true
			case "protected metadata":
				properties["metadata"] = map[string]any{"steward:protected": "true"}
			case "record generation":
				raw[2]["etag"] = "new-record"
			case "record forbidden":
				s.status[strings.ToLower(text(raw[2]["id"]))] = 403
			case "group forbidden":
				s.status[groupID] = 403
			case "managed group":
				s.add(map[string]any{"id": groupID, "managedBy": resourceID(aksType, "cluster")}, resourcesVersion)
			case "record lock":
				s.lists["/subscriptions/"+testSubscription+"/providers/microsoft.authorization/locks"] = []any{map[string]any{"id": text(raw[2]["id"]) + "/providers/Microsoft.Authorization/locks/keep", "type": "Microsoft.Authorization/locks", "properties": map[string]any{"level": "CanNotDelete"}}}
			case "missing configs":
				delete(object(raw[0]["properties"]), "privateDnsZoneConfigs")
			case "missing records":
				delete(config, "recordSets")
			case "duplicate records":
				config["recordSets"] = append(config["recordSets"].([]any), config["recordSets"].([]any)[0])
			case "unsupported record type":
				object(config["recordSets"].([]any)[0])["recordType"] = "CNAME"
			case "record identity":
				raw[2]["id"] = text(raw[2]["id"]) + "wrong"
			case "zone target changed":
				config["privateDnsZoneId"] = resourceID(privateDNSZoneType, "another.example.com")
				s.status[strings.ToLower(text(config["privateDnsZoneId"])+"/A/endpoint")] = 404
			}
			driver, err := r.ResolveAction(context.Background(), "connection", assets[0])
			if err != nil {
				t.Fatal(err)
			}
			_, err = driver.Execute(context.Background(), request)
			if err == nil || len(s.deletes) != 0 {
				t.Fatalf("%s allowed write %v %v", mode, s.deletes, err)
			}
		})
	}
}

func privateDNSLinkScenario(multiple bool) (*dnsScenario, []map[string]any) {
	s := newDNSScenario()
	zoneID := resourceID(privateDNSZoneType, "internal.example.com")
	zone := map[string]any{"id": zoneID, "name": "internal.example.com", "location": "global", "etag": "zone-etag"}
	raw := []map[string]any{zone}
	links, aRecords := []any{}, []any{}
	count := 1
	if multiple {
		count = 2
	}
	for i := 1; i <= count; i++ {
		name := fmt.Sprint(i)
		vnetID := resourceID(vnetType, "vnet"+name)
		vnet := map[string]any{"id": vnetID, "name": "vnet" + name, "etag": "network-etag", "properties": map[string]any{"addressSpace": map[string]any{"addressPrefixes": []any{"10." + name + ".0.0/16", "fd00:" + name + "::/64"}}}}
		link := map[string]any{"id": zoneID + "/virtualNetworkLinks/link" + name, "name": "link" + name, "location": "global", "etag": "link-etag" + name, "properties": map[string]any{"registrationEnabled": true, "virtualNetwork": map[string]any{"id": vnetID}}}
		record := map[string]any{"id": zoneID + "/A/vm" + name, "name": "vm" + name, "etag": "record-etag" + name, "properties": map[string]any{"isAutoRegistered": true, "ttl": 10, "aRecords": []any{map[string]any{"ipv4Address": "10." + name + ".0.4"}}}}
		s.add(vnet, "2024-05-01")
		links = append(links, link)
		aRecords = append(aRecords, record)
		raw = append(raw, link, record)
	}
	manual := map[string]any{"id": zoneID + "/A/manual", "etag": "manual-etag", "properties": map[string]any{"ttl": 10, "aRecords": []any{map[string]any{"ipv4Address": "10.1.0.5"}}}}
	raw = append(raw, manual)
	aRecords = append(aRecords, manual)
	for _, value := range raw {
		s.add(value, "2024-06-01")
	}
	for _, kind := range dnsChildTypes(privateDNSZoneType) {
		s.lists[strings.ToLower(zoneID+"/"+last(kind))] = []any{}
	}
	s.lists[strings.ToLower(zoneID+"/virtualNetworkLinks")] = links
	s.lists[strings.ToLower(zoneID+"/A")] = aRecords
	return s, raw
}

func TestPrivateDNSLinkOwnsOnlyItsAutoRegistrationRecords(t *testing.T) {
	for _, multiple := range []bool{false, true} {
		t.Run(fmt.Sprint(multiple), func(t *testing.T) {
			s, raw := privateDNSLinkScenario(multiple)
			r := s.runtime(t)
			assets := []asset.Asset{}
			for _, value := range raw {
				assets = append(assets, dnsAsset(t, r, value))
			}
			request, input := dnsRequest(t, r, assets, assets[1])
			if len(request.LifecycleImpacts) != 1 || request.LifecycleImpacts[0].Asset.ID != assets[2].ID {
				t.Fatalf("wrong auto-registration owner: %+v", request.LifecycleImpacts)
			}
			input.RequestOptions = map[asset.AssetID]map[string]any{assets[1].ID: {"retain_resources": []string{assets[2].Identity.NativeID}}}
			solved, err := plan.Solve(input)
			if err != nil || len(solved.Blockers) == 0 {
				t.Fatalf("registration retention ignored %+v %v", solved, err)
			}
			driver, err := r.ResolveAction(context.Background(), "connection", assets[1])
			if err != nil {
				t.Fatal(err)
			}
			result, err := driver.Execute(context.Background(), request)
			if err != nil || len(s.deletes) != 1 {
				t.Fatalf("link deletion %v %v", s.deletes, err)
			}
			wait, err := driver.Wait(context.Background(), request, result)
			if err != nil || wait.Done {
				t.Fatalf("auto record readback lost %+v %v", wait, err)
			}
			s.gone[strings.ToLower(text(raw[2]["id"]))] = true
			wait, err = driver.Wait(context.Background(), request, result)
			if err != nil || !wait.Done {
				t.Fatalf("link readback %+v %v", wait, err)
			}
			if s.gone[strings.ToLower(text(raw[len(raw)-1]["id"]))] {
				t.Fatal("manual DNS record deleted")
			}
		})
	}
}

func TestPrivateDNSRegistrationRefusesAmbiguousOrChangedOwnership(t *testing.T) {
	for _, mode := range []string{"overlapping networks", "unknown address", "split addresses", "network forbidden", "network identity", "invalid network prefix", "missing network prefixes", "link removed", "registration disabled", "link generation", "record generation", "protected record", "missing addresses", "wrong address family", "duplicate address", "record forbidden", "zone forbidden"} {
		t.Run(mode, func(t *testing.T) {
			s, raw := privateDNSLinkScenario(true)
			r := s.runtime(t)
			assets := []asset.Asset{dnsAsset(t, r, raw[1]), dnsAsset(t, r, raw[2])}
			request, _ := dnsRequest(t, r, assets, assets[0])
			props := object(raw[2]["properties"])
			network := s.records[strings.ToLower(resourceID(vnetType, "vnet2"))]
			switch mode {
			case "overlapping networks":
				object(object(network["properties"])["addressSpace"])["addressPrefixes"] = []any{"10.0.0.0/8"}
			case "unknown address":
				props["aRecords"] = []any{map[string]any{"ipv4Address": "192.168.0.4"}}
			case "split addresses":
				props["aRecords"] = []any{map[string]any{"ipv4Address": "10.1.0.4"}, map[string]any{"ipv4Address": "10.2.0.4"}}
			case "network forbidden":
				s.status[strings.ToLower(text(network["id"]))] = 403
			case "network identity":
				network["id"] = resourceID(vnetType, "foreign")
			case "invalid network prefix":
				object(object(network["properties"])["addressSpace"])["addressPrefixes"] = []any{"invalid"}
			case "missing network prefixes":
				delete(object(network["properties"]), "addressSpace")
			case "link removed":
				s.lists[strings.ToLower(text(raw[0]["id"])+"/virtualNetworkLinks")] = []any{raw[3]}
			case "registration disabled":
				object(raw[1]["properties"])["registrationEnabled"] = false
			case "link generation":
				raw[1]["etag"] = "new-link"
			case "record generation":
				raw[2]["etag"] = "new-record"
			case "protected record":
				props["metadata"] = map[string]any{"steward/protected": "true"}
			case "missing addresses":
				delete(props, "aRecords")
			case "wrong address family":
				props["aRecords"] = []any{map[string]any{"ipv4Address": "fd00:1::4"}}
			case "duplicate address":
				props["aRecords"] = append(props["aRecords"].([]any), props["aRecords"].([]any)[0])
			case "record forbidden":
				s.status[strings.ToLower(text(raw[2]["id"]))] = 403
			case "zone forbidden":
				s.status[strings.ToLower(text(raw[0]["id"]))] = 403
			}
			driver, err := r.ResolveAction(context.Background(), "connection", assets[0])
			if err != nil {
				t.Fatal(err)
			}
			_, err = driver.Execute(context.Background(), request)
			if err == nil || len(s.deletes) != 0 {
				t.Fatalf("%s allowed write %v %v", mode, s.deletes, err)
			}
		})
	}
}

func TestPrivateDNSConditionalDeleteConflictAndMissingETag(t *testing.T) {
	for _, kind := range []string{privateDNSLinkType, privateDNSZoneType + "/A"} {
		t.Run(kind, func(t *testing.T) {
			s, raw := privateDNSLinkScenario(false)
			r := s.runtime(t)
			selected := raw[1]
			if isDNSRecordType(kind) {
				selected = raw[len(raw)-1]
			} else {
				object(selected["properties"])["registrationEnabled"] = false
			}
			value := dnsAsset(t, r, selected)
			missing := value
			missing.Normalized = map[string]any{}
			if _, err := r.ResolveAction(context.Background(), "connection", missing); err == nil {
				t.Fatal("missing ETag allowed unconditional deletion")
			}
			s.deleteStatus = 412
			driver, err := r.ResolveAction(context.Background(), "connection", value)
			if err != nil {
				t.Fatal(err)
			}
			_, err = driver.Execute(context.Background(), contracts.ActionRequest{Asset: value, Action: "delete"})
			var call *contracts.ProviderCallError
			if !errors.As(err, &call) || call.Provider.Category != execution.ErrorConflict || len(s.deletes) != 1 || s.gone[strings.ToLower(value.Identity.NativeID)] {
				t.Fatalf("conditional failure lost %v %v", s.deletes, err)
			}
		})
	}
}

func TestPrivateDNSZoneRequiresVirtualNetworkLinksDeletedFirst(t *testing.T) {
	s, raw := privateDNSLinkScenario(false)
	r := s.runtime(t)
	assets := []asset.Asset{dnsAsset(t, r, raw[0]), dnsAsset(t, r, raw[2]), dnsAsset(t, r, raw[3])}
	request, _ := dnsRequest(t, r, assets, assets[0])
	driver, err := r.ResolveAction(context.Background(), "connection", assets[0])
	if err != nil {
		t.Fatal(err)
	}
	_, err = driver.Execute(context.Background(), request)
	var call *contracts.ProviderCallError
	if !errors.As(err, &call) || call.Provider.Code != "private_dns_zone_has_virtual_network_links" || len(s.deletes) != 0 {
		t.Fatalf("zone bypassed live links %v", err)
	}
	s.gone[strings.ToLower(text(raw[1]["id"]))] = true
	if _, err = driver.Execute(context.Background(), request); err != nil || len(s.deletes) != 1 {
		t.Fatalf("zone still blocked after unlink: %v", err)
	}
}

func TestVirtualNetworkChecksDNSLinksAcrossResourceGroups(t *testing.T) {
	for _, mode := range []string{"linked", "unlinked", "zone permission denied"} {
		t.Run(mode, func(t *testing.T) {
			s, raw := privateDNSLinkScenario(false)
			// The VNet lives outside the DNS zone's resource group.
			vnetID := strings.Replace(resourceID(vnetType, "target"), "/resourceGroups/test/", "/resourceGroups/application/", 1)
			vnet := map[string]any{"id": vnetID, "location": "eastus", "etag": "network-etag", "properties": map[string]any{"subnets": []any{}}}
			s.add(vnet, "2024-05-01")
			object(raw[1]["properties"])["virtualNetwork"] = map[string]any{"id": vnetID}
			root := "/subscriptions/" + testSubscription
			s.lists[strings.ToLower(root+"/providers/"+privateDNSZoneType)] = []any{raw[0]}
			s.lists[root+"/resourcegroups"] = []any{map[string]any{"id": root + "/resourceGroups/test"}, map[string]any{"id": root + "/resourceGroups/application"}}
			s.lists[strings.ToLower(vnetID+"/subnets")] = []any{}
			if mode == "unlinked" {
				s.gone[strings.ToLower(text(raw[1]["id"]))] = true
			}
			if mode == "zone permission denied" {
				s.status[strings.ToLower(text(raw[0]["id"]))] = 403
			}
			r := s.runtime(t)
			value := dnsAsset(t, r, vnet)
			driver, err := r.ResolveAction(context.Background(), "connection", value)
			if err != nil {
				t.Fatal(err)
			}
			_, err = driver.Execute(context.Background(), contracts.ActionRequest{Asset: value, Action: "delete"})
			if mode == "unlinked" {
				if err != nil || len(s.deletes) != 1 {
					t.Fatalf("unlinked VNet blocked: %v", err)
				}
				return
			}
			if err == nil || len(s.deletes) != 0 {
				t.Fatalf("DNS-link check failed: %v %v", s.deletes, err)
			}
		})
	}
}
