package azure

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/app/governance"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
)

func batchReferenceResources(t *testing.T, s *batchScenario) (map[string]any, map[string]any) {
	t.Helper()
	group := "/subscriptions/" + testSubscription + "/resourcegroups/data-in-another-region"
	storage := nativeResource(storageType, "inputstore", "westus", map[string]any{"provisioningState": "Succeeded", "primaryEndpoints": map[string]any{"blob": "https://inputstore.z17.blob.storage.azure.net/", "file": "https://inputstore.file.core.windows.net/"}, "secondaryEndpoints": map[string]any{"blob": "https://inputstore-secondary.blob.core.windows.net/"}, "customDomain": map[string]any{"name": "batch-files.example.test"}})
	storage["id"], storage["kind"] = group+"/providers/microsoft.storage/storageaccounts/inputstore", "StorageV2"
	vault := nativeResource("Microsoft.KeyVault/vaults", "batchkeys", "westus", map[string]any{"vaultUri": "https://batchkeys.vault.azure.net/"})
	vault["id"] = group + "/providers/microsoft.keyvault/vaults/batchkeys"
	for _, raw := range []map[string]any{storage, vault} {
		kind, _ := findType(text(raw["type"]))
		s.arm.add(raw, kind.Version)
		path := "/subscriptions/" + testSubscription + "/providers/" + strings.ToLower(text(raw["type"]))
		s.arm.lists[path], s.arm.version[path] = []any{raw}, kind.Version
	}
	return storage, vault
}

type batchReferenceGraph struct{ assets []asset.Asset }

func (g batchReferenceGraph) ListActiveAssetsByConnection(context.Context, asset.ConnectionID, asset.ResourceKindID) ([]asset.Asset, error) {
	return g.assets, nil
}
func (batchReferenceGraph) ReplaceGraph(context.Context, asset.ScopeID, string, []graph.Relationship, []graph.LifecycleBinding, ...graph.UnresolvedReference) error {
	return nil
}

func TestBatchExternalReferencesResolveActualARMResourcesAndGraph(t *testing.T) {
	s, r, _ := newBatchScenario(t)
	storage, vault := batchReferenceResources(t, s)
	storageID, vaultID := text(storage["id"]), text(vault["id"])
	task := s.records["/jobs/jobid/tasks/taskid"]
	task["resourceFiles"] = []any{
		map[string]any{"httpUrl": "https://inputstore.z17.blob.storage.azure.net/incoming/path/file?sig=BATCH_INPUT_SECRET"},
		map[string]any{"storageContainerUrl": "https://inputstore-secondary.blob.core.windows.net/secondary?sig=BATCH_SECONDARY_SECRET"},
		map[string]any{"httpUrl": "https://batch-files.example.test/custom/file?sig=BATCH_CUSTOM_SECRET"},
		map[string]any{"httpUrl": "https://batch-files.example.test/root.txt?sig=BATCH_ROOT_SECRET"},
		map[string]any{"httpUrl": "https://downloads.example.test:8443/file?sig=BATCH_OTHER_SECRET"},
		map[string]any{"autoStorageContainerName": "auto-input"},
	}
	task["outputFiles"] = []any{map[string]any{"destination": map[string]any{"container": map[string]any{"containerUrl": "https://inputstore.z17.blob.storage.azure.net/output?sig=BATCH_OUTPUT_SECRET"}}}}
	page, err := r.List(t.Context(), productRequest(r, batchTaskType))
	if err != nil || len(page.Items) != 1 {
		t.Fatal("Batch external task inventory", err)
	}
	item := page.Items[0]
	for _, name := range []string{"incoming", "secondary", "custom", "output", "$root"} {
		id := storageID + "/blobservices/default/containers/" + name
		if !slices.Contains(item.Normalized[referenceKey(containerType)].([]string), id) || !slices.Contains(item.NetworkReferences, id) {
			t.Fatal("Batch URL was not resolved to its actual cross-group container", name)
		}
	}
	auto := text(object(object(s.arm.records[s.account]["properties"])["autoStorage"])["storageAccountId"]) + "/blobservices/default/containers/auto-input"
	if !slices.Contains(item.Normalized[referenceKey(containerType)].([]string), auto) {
		t.Fatal("account auto storage did not supply its container identity")
	}
	pool := s.arm.records[s.account+"/pools/poolid"]
	props := object(pool["properties"])
	props["mountConfiguration"] = []any{
		map[string]any{"azureBlobFileSystemConfiguration": map[string]any{"accountName": "inputstore", "containerName": "mounted", "sasKey": "BATCH_MOUNT_SECRET"}},
		map[string]any{"azureFileShareConfiguration": map[string]any{"accountName": "inputstore", "azureFileUrl": "https://inputstore.file.core.windows.net/shared", "accountKey": "BATCH_SHARE_SECRET"}},
	}
	object(props["deploymentConfiguration"])["virtualMachineConfiguration"] = map[string]any{"diskEncryptionConfiguration": map[string]any{"customerManagedKey": map[string]any{"keyUrl": "https://batchkeys.vault.azure.net/keys/disk/version", "identityReference": map[string]any{"resourceId": "/subscriptions/" + testSubscription + "/resourcegroups/data-in-another-region/providers/microsoft.managedidentity/userassignedidentities/encryptor"}}}}
	poolAsset := dnsAsset(t, r, pool)
	shareType := "Microsoft.Storage/storageAccounts/fileServices/shares"
	if !slices.Equal(poolAsset.Normalized[referenceKey(shareType)].([]string), []string{storageID + "/fileservices/default/shares/shared"}) || !slices.Equal(poolAsset.Normalized[referenceKey("Microsoft.KeyVault/vaults")].([]string), []string{vaultID}) {
		t.Fatal("mount or disk key reference was lost")
	}
	values := []asset.Asset{poolAsset, dnsAsset(t, r, storage), dnsAsset(t, r, vault)}
	values = append(values, asset.Asset{ID: asset.AssetID(item.NativeID), Identity: asset.Identity{Provider: asset.ProviderAzure, ConnectionID: "connection", Partition: "azure", NativeID: item.NativeID, NativeType: batchTaskType}, Location: item.Location, Normalized: item.Normalized})
	for _, name := range []string{"incoming", "secondary", "custom", "output", "mounted"} {
		id := storageID + "/blobservices/default/containers/" + name
		values = append(values, dnsAsset(t, r, map[string]any{"id": id, "type": containerType, "name": name, "location": "westus", "properties": map[string]any{}}))
	}
	store := batchReferenceGraph{assets: values}
	result, err := governance.NewService(store, store).RebuildGraph(t.Context(), "scope", "connection", "batch-references", r.bundle, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{storageID, vaultID, storageID + "/blobservices/default/containers/incoming", storageID + "/blobservices/default/containers/mounted"} {
		if !slices.ContainsFunc(result.Relationships, func(ref graph.Relationship) bool {
			return ref.TargetAssetID == asset.AssetID(target) && ref.Type == graph.RelationshipUses
		}) {
			t.Fatal("native reference did not reach the application graph", target)
		}
	}
	payload, _ := json.Marshal([]any{item, poolAsset, result})
	if strings.Contains(string(payload), "_SECRET") || strings.Contains(string(payload), "sig=") {
		t.Fatal("external Batch reference leaked credentials")
	}
}

func TestBatchExternalReferenceBoundaries(t *testing.T) {
	for _, mode := range []string{"foreign_subscription", "wrong_kind", "duplicate", "ambiguous_endpoint", "ambiguous_service", "changed_endpoint", "changed_generation", "missing_detail", "denied_detail", "denied_list", "partial_page", "foreign_page", "wrong_service", "wrong_account_name", "encoded_container", "credentials", "missing_auto_storage", "unknown_external", "opaque_values"} {
		t.Run(mode, func(t *testing.T) {
			s, r, _ := newBatchScenario(t)
			storage, _ := batchReferenceResources(t, s)
			id := text(storage["id"])
			path := "/subscriptions/" + testSubscription + "/providers/microsoft.storage/storageaccounts"
			raw := map[string]any{"resourceFiles": []any{map[string]any{"storageContainerUrl": "https://inputstore.z17.blob.storage.azure.net/input?sig=PRIVATE_SAS"}}}
			switch mode {
			case "foreign_subscription":
				storage["id"] = strings.Replace(id, testSubscription, "99999999-2222-4333-8444-555555555555", 1)
			case "wrong_kind":
				storage["type"] = vmType
			case "duplicate":
				s.arm.lists[path] = []any{storage, storage}
			case "ambiguous_endpoint":
				other := batchClone(storage)
				other["id"], other["name"] = strings.Replace(id, "inputstore", "anotherstore", 1), "anotherstore"
				s.arm.add(other, "2023-05-01")
				s.arm.lists[path] = append(s.arm.lists[path], other)
			case "ambiguous_service":
				object(object(storage["properties"])["primaryEndpoints"])["file"] = "https://inputstore.z17.blob.storage.azure.net/"
			case "changed_endpoint", "changed_generation":
				storage["etag"] = "old"
				s.arm.lists[path] = []any{batchClone(storage)}
				if mode == "changed_generation" {
					storage["etag"] = "new"
				} else {
					object(object(storage["properties"])["primaryEndpoints"])["blob"] = "https://other.blob.core.windows.net/"
				}
			case "missing_detail":
				s.arm.status[id] = 404 // Keep the stale listing, unlike a clean removal.
			case "denied_detail", "denied_list", "partial_page", "foreign_page":
				s.handle = func(req *http.Request) (*http.Response, bool) {
					if strings.EqualFold(req.URL.Path, path) && mode != "denied_detail" {
						switch mode {
						case "denied_list":
							return jsonResponse(403, map[string]any{"error": map[string]any{"code": "AuthorizationFailure"}}, nil), true
						case "partial_page":
							return jsonResponse(200, map[string]any{"nextLink": ""}, nil), true
						default:
							return jsonResponse(200, map[string]any{"value": []any{storage}, "nextLink": "https://foreign.example.test/list?sig=PRIVATE_SAS"}, nil), true
						}
					}
					if strings.EqualFold(req.URL.Path, id) && mode == "denied_detail" {
						return jsonResponse(403, map[string]any{"error": map[string]any{"code": "AuthorizationFailure"}}, nil), true
					}
					return nil, false
				}
			case "wrong_service":
				object(array(raw["resourceFiles"])[0])["storageContainerUrl"] = "https://inputstore.file.core.windows.net/share"
			case "wrong_account_name":
				raw = map[string]any{"mountConfiguration": []any{map[string]any{"azureFileShareConfiguration": map[string]any{"accountName": "otherstore", "azureFileUrl": "https://inputstore.file.core.windows.net/share"}}}}
			case "encoded_container":
				object(array(raw["resourceFiles"])[0])["storageContainerUrl"] = "https://inputstore.z17.blob.storage.azure.net/input%2Fother"
			case "credentials":
				object(array(raw["resourceFiles"])[0])["storageContainerUrl"] = "https://user:PRIVATE_SAS@inputstore.z17.blob.storage.azure.net/input"
			case "missing_auto_storage":
				raw = map[string]any{"resourceFiles": []any{map[string]any{"autoStorageContainerName": "input"}}}
				delete(object(s.arm.records[s.account]["properties"]), "autoStorage")
			case "unknown_external":
				s.arm.lists[path] = []any{}
			case "opaque_values":
				raw = map[string]any{"protectedSettings": raw, "settings": map[string]any{"keyUrl": "https://vault.example.test/keys/private", "applicationPackages": []any{map[string]any{"id": "invalid"}}}}
				s.handle = func(req *http.Request) (*http.Response, bool) {
					if strings.Contains(strings.ToLower(req.URL.Path), "/providers/microsoft.storage/") || strings.Contains(strings.ToLower(req.URL.Path), "/providers/microsoft.keyvault/") {
						t.Fatal("opaque extension settings were interpreted as Batch API fields")
					}
					return nil, false
				}
			}
			c, _ := r.resolve(t.Context(), "connection")
			account, _ := c.batchAccount(t.Context(), s.account)
			refs, err := c.batchReferences(t.Context(), account, s.account+"/pools/poolid", batchPoolType, raw)
			if mode == "unknown_external" {
				if err != nil || !slices.Equal(refs[storageType], []string{"https://inputstore.z17.blob.storage.azure.net"}) || len(refs[containerType]) != 0 {
					t.Fatal("external URL was dropped or assigned an invented ARM ID", refs, err)
				}
			} else if mode == "opaque_values" {
				if err != nil || len(refs) != 1 || len(refs[batchAccountType]) != 1 {
					t.Fatal("opaque values created resource references", refs, err)
				}
			} else if err == nil {
				t.Fatal("invalid external reference authority was accepted", mode)
			}
		})
	}
}
