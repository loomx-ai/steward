package azure

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func TestServiceBusForwardingUsesNativeTargetIdentity(t *testing.T) {
	for _, sourceKind := range []string{serviceBusQueueType, serviceBusSubscriptionType} {
		for _, targetKind := range []string{serviceBusQueueType, serviceBusTopicType} {
			for _, address := range []string{"orders/final", "orders~final", "sb://messages.servicebus.windows.net/orders/final", "https://messages.servicebus.windows.net/orders/final"} {
				t.Run(sourceKind+"/"+targetKind+"/"+address, func(t *testing.T) {
					s, r, assets := messagingScenario(t, serviceBusNamespaceType)
					var source asset.Asset
					for _, value := range assets {
						if value.Identity.NativeType == sourceKind {
							source = value
						}
					}
					root := assets[0].Identity.NativeID
					id := root + "/" + last(targetKind) + "/orders~final"
					raw := s.records[source.Identity.NativeID]
					delete(object(raw["properties"]), "provisioningState")
					object(raw["properties"])["status"] = "Active"
					object(raw["properties"])["forwardTo"] = address
					object(raw["properties"])["forwardDeadLetteredMessagesTo"] = address
					s.records[id] = map[string]any{"id": id, "type": targetKind, "properties": map[string]any{"status": "Active"}}
					otherKind := serviceBusQueueType
					if targetKind == otherKind {
						otherKind = serviceBusTopicType
					}
					s.status[root+"/"+last(otherKind)+"/orders~final"] = 404
					c, _ := r.resolve(context.Background(), "connection")
					item, err := r.inventoryItem(context.Background(), c, raw, nil, nil)
					want := []string{id}
					var wantOther []string
					if sourceKind == serviceBusSubscriptionType {
						if targetKind == serviceBusTopicType {
							want = append(want, root+"/topics/topic")
						} else {
							wantOther = []string{root + "/topics/topic"}
						}
					}
					got, _ := item.Normalized[referenceKey(targetKind)].([]string)
					gotOther, _ := item.Normalized[referenceKey(otherKind)].([]string)
					if err != nil || item.State != "Active" || !slices.Equal(got, want) || !slices.Equal(gotOther, wantOther) {
						t.Fatalf("forwarding dependencies=%v %v state=%s error=%v", got, gotOther, item.State, err)
					}
				})
			}
		}
	}
}

func TestServiceBusForwardingRejectsUnprovenTargets(t *testing.T) {
	for _, mode := range []string{"foreign-host", "userinfo", "query", "fragment", "encoded-path", "malformed", "traversal", "ambiguous", "get-403", "get-206", "foreign-id", "foreign-type", "self", "absent"} {
		t.Run(mode, func(t *testing.T) {
			s, r, assets := messagingScenario(t, serviceBusNamespaceType)
			root := assets[0].Identity.NativeID
			var source asset.Asset
			for _, value := range assets {
				if value.Identity.NativeType == serviceBusQueueType {
					source = value
				}
			}
			raw := s.records[source.Identity.NativeID]
			queueID, topicID := root+"/queues/destination", root+"/topics/destination"
			s.records[queueID] = map[string]any{"id": queueID, "type": serviceBusQueueType}
			s.status[topicID] = 404
			value := any("destination")
			switch mode {
			case "foreign-host":
				value = "sb://other.servicebus.windows.net/destination"
			case "userinfo":
				value = "sb://user:secret@messages.servicebus.windows.net/destination"
			case "query":
				value = "sb://messages.servicebus.windows.net/destination?value=secret"
			case "fragment":
				value = "sb://messages.servicebus.windows.net/destination#fragment"
			case "encoded-path":
				value = "sb://messages.servicebus.windows.net/destination%2fother"
			case "malformed":
				value = []any{"destination"}
			case "traversal":
				value = "../destination"
			case "ambiguous":
				delete(s.status, topicID)
				s.records[topicID] = map[string]any{"id": topicID, "type": serviceBusTopicType}
			case "get-403":
				s.status[topicID] = 403
			case "get-206":
				s.status[queueID] = 206
			case "foreign-id":
				s.records[queueID]["id"] = strings.Replace(queueID, "/messages/", "/foreign/", 1)
			case "foreign-type":
				s.records[queueID]["type"] = nicType
			case "self":
				value = "queue"
			case "absent":
				s.status[queueID] = 404
			}
			object(raw["properties"])["forwardTo"] = value
			c, _ := r.resolve(context.Background(), "connection")
			item, err := r.inventoryItem(context.Background(), c, raw, nil, nil)
			if mode == "absent" {
				if err != nil || item.Normalized[referenceKey(serviceBusQueueType)] != nil || item.Normalized[referenceKey(serviceBusTopicType)] != nil {
					t.Fatalf("confirmed absent target: %+v %v", item, err)
				}
			} else if err == nil {
				t.Fatal("invalid forwarding dependency accepted")
			}
			if len(s.deletes) != 0 {
				t.Fatal("dependency discovery performed a write")
			}
		})
	}
}

func TestMessagingCaptureAndPrivateEndpointAreExternalDependencies(t *testing.T) {
	s, r, assets := messagingScenario(t, eventHubNamespaceType)
	storageID := strings.ToLower(resourceID(storageType, "capturestorage"))
	containerID := storageID + "/blobservices/default/containers/events"
	endpointID := strings.ToLower(resourceID(privateEndpointType, "endpoint"))
	identityID := strings.ToLower(resourceID("Microsoft.ManagedIdentity/userAssignedIdentities", "capture"))
	for i, value := range assets {
		raw := s.records[value.Identity.NativeID]
		switch value.Identity.NativeType {
		case eventHubNamespaceType:
			raw["identity"] = map[string]any{"userAssignedIdentities": map[string]any{identityID: map[string]any{}}}
		case eventHubType:
			object(raw["properties"])["captureDescription"] = map[string]any{"enabled": true, "destination": map[string]any{"name": "EventHubArchive.AzureBlockBlob", "properties": map[string]any{"storageAccountResourceId": storageID, "blobContainer": "events"}}}
		case eventHubNamespaceType + "/privateEndpointConnections":
			object(raw["properties"])["privateEndpoint"] = map[string]any{"id": endpointID}
		default:
			continue
		}
		assets[i] = dnsAsset(t, r, raw)
	}
	for _, tc := range []struct{ kind, target, id string }{{eventHubType, storageType, storageID}, {eventHubType, containerType, containerID}, {eventHubNamespaceType + "/privateEndpointConnections", privateEndpointType, endpointID}, {eventHubNamespaceType, "Microsoft.ManagedIdentity/userAssignedIdentities", identityID}} {
		for _, value := range assets {
			if value.Identity.NativeType == tc.kind && !slices.Contains(value.Normalized[referenceKey(tc.target)].([]string), tc.id) {
				t.Fatalf("missing external dependency: %+v", value)
			}
		}
		definition, _ := r.productDefinition(tc.kind)
		found := false
		for _, relation := range definition.Relationships {
			found = found || relation.TargetType == tc.target
		}
		if !found {
			t.Fatalf("missing executable relationship %s -> %s", tc.kind, tc.target)
		}
	}
	request, _ := dnsRequest(t, r, assets, assets[0])
	for _, impact := range request.LifecycleImpacts {
		if slices.Contains([]string{storageID, containerID, endpointID, identityID}, impact.Asset.Identity.NativeID) {
			t.Fatal("external capture/endpoint resource became a cascade member")
		}
	}
	driver, _ := r.ResolveAction(context.Background(), "connection", assets[0])
	if _, err := driver.Execute(context.Background(), request); err != nil || !slices.Equal(s.deletes, []string{assets[0].Identity.NativeID}) {
		t.Fatalf("external resources not preserved: %v %v", s.deletes, err)
	}
}

func TestNativeStandaloneCreationIdentitySurvivesOrdinaryUpdates(t *testing.T) {
	for _, tc := range []struct{ kind, id, field, version string }{
		{hostType, resourceID(hostGroupType, "hosts") + "/hosts/host", "hostId", "2024-07-01"},
		{eventHubConsumerGroupType, resourceID(eventHubNamespaceType, "messages") + "/eventhubs/hub/consumergroups/consumer", "createdAt", "2024-01-01"},
		{eventHubNamespaceType + "/schemagroups", resourceID(eventHubNamespaceType, "messages") + "/schemagroups/schema", "createdAtUtc", "2024-01-01"},
	} {
		for _, mode := range []string{"ordinary-update", "recreated", "missing-creation"} {
			t.Run(tc.kind+"/"+mode, func(t *testing.T) {
				s := newDNSScenario()
				raw := map[string]any{"id": tc.id, "type": tc.kind, "etag": "original", "properties": map[string]any{tc.field: "original-creation", "provisioningState": "Succeeded", "eTag": "original-schema"}}
				s.add(raw, tc.version)
				if strings.HasPrefix(tc.kind, eventHubNamespaceType+"/") {
					parentID := strings.Join(strings.Split(strings.ToLower(tc.id), "/")[:9], "/")
					s.add(map[string]any{"id": parentID, "type": eventHubNamespaceType, "properties": map[string]any{"createdAt": "namespace-creation", "provisioningState": "Succeeded"}}, "2024-01-01")
					s.lists[parentID+"/disasterrecoveryconfigs"] = []any{}
				}
				r := s.runtime(t)
				value := dnsAsset(t, r, raw)
				encoded, _ := json.Marshal(value)
				if err := json.Unmarshal(encoded, &value); err != nil {
					t.Fatal(err)
				}
				raw["etag"] = "updated"
				object(raw["properties"])["eTag"] = "updated-schema"
				object(raw["properties"])["userMetadata"] = "ordinary-update"
				if mode == "recreated" {
					object(raw["properties"])[tc.field] = "new-creation"
				}
				if mode == "missing-creation" {
					delete(object(raw["properties"]), tc.field)
				}
				driver, _ := r.ResolveAction(context.Background(), "connection", value)
				_, err := driver.Execute(context.Background(), contracts.ActionRequest{Asset: value, Action: "delete"})
				if mode == "ordinary-update" {
					if err != nil || len(s.deletes) != 1 {
						t.Fatalf("ordinary update rejected: %v", err)
					}
				} else if err == nil || len(s.deletes) != 0 {
					t.Fatal("recreated resource deleted")
				}
			})
		}
	}
}

func TestMessagingPairingAndMigrationCannotBeImplicitlyDiscarded(t *testing.T) {
	for _, kind := range []string{serviceBusRecoveryType, eventHubRecoveryType, serviceBusMigrationType} {
		for _, mode := range []string{"paired", "transition", "pending", "malformed-pending", "malformed-partner"} {
			t.Run(kind+"/"+mode, func(t *testing.T) {
				namespace := serviceBusNamespaceType
				if kind == eventHubRecoveryType {
					namespace = eventHubNamespaceType
				}
				s, r, assets := messagingScenario(t, namespace)
				request, _ := dnsRequest(t, r, assets, assets[0])
				var child asset.Asset
				for _, value := range assets {
					if value.Identity.NativeType == kind {
						child = value
					}
				}
				properties := object(s.records[child.Identity.NativeID]["properties"])
				switch mode {
				case "paired":
					if kind == serviceBusMigrationType {
						properties["migrationState"] = "Syncing"
					} else {
						properties["partnerNamespace"] = resourceID(namespace, "secondary")
					}
				case "transition":
					if kind == serviceBusMigrationType {
						properties["migrationState"] = "Completing"
					} else {
						properties["role"] = "Secondary"
					}
				case "pending":
					properties["pendingReplicationOperationsCount"] = 1
				case "malformed-pending":
					properties["pendingReplicationOperationsCount"] = "0"
				case "malformed-partner":
					field := "partnerNamespace"
					if kind == serviceBusMigrationType {
						field = "targetNamespace"
					}
					properties[field] = map[string]any{"unreadable": "namespace"}
				}
				for _, action := range []contracts.ActionRequest{request, {Asset: child, Action: "delete"}} {
					driver, _ := r.ResolveAction(context.Background(), "connection", action.Asset)
					if _, err := driver.Execute(context.Background(), action); err == nil || len(s.deletes) != 0 {
						t.Fatal("active relationship silently discarded")
					}
				}
			})
		}
	}
}

func TestMessagingReadbackRejectsFailedOrForeignAliasedRead(t *testing.T) {
	for _, mode := range []string{"live", "403", "206", "foreign"} {
		t.Run(mode, func(t *testing.T) {
			s, r, assets := messagingScenario(t, serviceBusNamespaceType)
			var target asset.Asset
			for _, value := range assets {
				if value.Identity.NativeType == serviceBusMigrationType {
					target = value
				}
			}
			if mode == "403" {
				s.status[target.Identity.NativeID] = 403
			}
			if mode == "206" {
				s.status[target.Identity.NativeID] = 206
			}
			if mode == "foreign" {
				s.records[target.Identity.NativeID]["id"] = strings.Replace(text(s.records[target.Identity.NativeID]["id"]), "/messages/", "/other/", 1)
			}
			driver, _ := r.ResolveAction(context.Background(), "connection", target)
			wait, err := driver.Wait(context.Background(), contracts.ActionRequest{Asset: target, Action: "delete"}, contracts.ActionResult{})
			if wait.Done || (mode == "live" && err != nil) || (mode != "live" && err == nil) {
				t.Fatalf("readback=%+v %v", wait, err)
			}
		})
	}
}
