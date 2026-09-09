package azure

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func TestMessagingEntityDeletionRechecksNativeReplicationContext(t *testing.T) {
	for _, namespace := range []string{serviceBusNamespaceType, eventHubNamespaceType} {
		for _, mode := range []string{"new-pair", "secondary", "accepted", "pending", "migration", "namespace-403", "namespace-404", "namespace-206", "namespace-foreign", "namespace-recreated", "namespace-changes-during-read", "alias-list-403", "alias-list-404", "alias-list-206", "alias-get-403", "alias-get-404", "alias-get-206", "alias-foreign"} {
			if mode == "migration" && namespace != serviceBusNamespaceType {
				continue
			}
			t.Run(namespace+"/"+mode, func(t *testing.T) {
				s, r, assets := messagingScenario(t, namespace)
				var target, recovery, migration asset.Asset
				for _, value := range assets {
					switch value.Identity.NativeType {
					case serviceBusQueueType, eventHubConsumerGroupType:
						target = value
					case serviceBusRecoveryType, eventHubRecoveryType:
						recovery = value
					case serviceBusMigrationType:
						migration = value
					}
				}
				parentID := assets[0].Identity.NativeID
				properties := object(s.records[recovery.Identity.NativeID]["properties"])
				listID := parentID + "/disasterrecoveryconfigs"
				switch mode {
				case "new-pair":
					properties["partnerNamespace"], properties["role"] = resourceID(namespace, "peer"), "Primary"
				case "secondary":
					properties["partnerNamespace"], properties["role"] = resourceID(namespace, "peer"), "Secondary"
				case "accepted":
					properties["provisioningState"] = "Accepted"
				case "pending":
					properties["pendingReplicationOperationsCount"] = 1
				case "migration":
					object(s.records[migration.Identity.NativeID]["properties"])["targetNamespace"] = resourceID(namespace, "premium")
				case "namespace-403":
					s.status[parentID] = 403
				case "namespace-404":
					s.status[parentID] = 404
				case "namespace-206":
					s.status[parentID] = 206
				case "namespace-foreign":
					s.records[parentID]["id"] = resourceID(namespace, "other")
				case "namespace-recreated":
					object(s.records[parentID]["properties"])["createdAt"] = "new"
				case "namespace-changes-during-read":
					s.handle = func(req *http.Request) (*http.Response, bool) {
						if strings.EqualFold(req.URL.Path, recovery.Identity.NativeID) {
							object(s.records[parentID]["properties"])["createdAt"] = "changed-during-read"
						}
						return nil, false
					}
				case "alias-list-403":
					s.status[listID] = 403
				case "alias-list-404":
					s.status[listID] = 404
				case "alias-list-206":
					s.status[listID] = 206
				case "alias-get-403":
					s.status[recovery.Identity.NativeID] = 403
				case "alias-get-404":
					s.status[recovery.Identity.NativeID] = 404
				case "alias-get-206":
					s.status[recovery.Identity.NativeID] = 206
				case "alias-foreign":
					s.records[recovery.Identity.NativeID]["id"] = resourceID(namespace, "other") + "/disasterRecoveryConfigs/alias"
				}
				driver, err := r.ResolveAction(context.Background(), "connection", target)
				if err != nil {
					t.Fatal(err)
				}
				request := contracts.ActionRequest{Asset: target, Action: "delete"}
				if _, err := driver.Execute(context.Background(), request); err == nil || len(s.deletes) != 0 {
					t.Fatalf("unreviewed replication allowed deletion %v", err)
				}
				if mode == "new-pair" || mode == "secondary" || mode == "accepted" || mode == "pending" || mode == "migration" {
					c, _ := r.resolve(context.Background(), "connection")
					item, err := r.inventoryItem(context.Background(), c, s.records[target.Identity.NativeID], nil, nil)
					if err != nil || item.Normalized["cleanup_controller_only"] != true || item.Normalized["cleanup_protection_reason"] != "azure_messaging_replication_requires_unpairing" {
						t.Fatalf("paired entity inventory omitted restriction %+v %v", item.Normalized, err)
					}
				}
			})
		}
	}
}

func TestMessagingNativeConfigurationWithoutReplicationCanDeleteIndependently(t *testing.T) {
	for _, namespace := range []string{serviceBusNamespaceType, eventHubNamespaceType} {
		s, r, assets := messagingScenario(t, namespace)
		var target, recovery asset.Asset
		for _, value := range assets {
			if value.Identity.NativeType == namespace+"/privateEndpointConnections" {
				target = value
			}
			if value.Identity.NativeType == namespace+"/disasterRecoveryConfigs" {
				recovery = value
			}
		}
		object(s.records[recovery.Identity.NativeID]["properties"])["partnerNamespace"] = resourceID(namespace, "peer")
		driver, err := r.ResolveAction(context.Background(), "connection", target)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := driver.Execute(context.Background(), contracts.ActionRequest{Asset: target, Action: "delete"}); err != nil || len(s.deletes) != 1 {
			t.Fatalf("nonreplicated private endpoint connection blocked %v", err)
		}
	}
}
