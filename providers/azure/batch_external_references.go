package azure

import (
	"context"
	"net/url"
	"strings"
)

// These objects contain opaque user-authored values, not Batch reference fields.
func batchOpaqueReferenceField(key string) bool {
	return key == "settings" || key == "protectedSettings" || key == "environmentSettings" || key == "metadata"
}

func batchReferenceEndpoint(kind string, raw map[string]any, host, name string) string {
	if name != "" {
		if strings.EqualFold(text(raw["name"]), name) {
			return "blob"
		}
		return ""
	}
	props := object(raw["properties"])
	endpoints := map[string]any{"vault": props["vaultUri"]}
	matched := ""
	if kind == storageType {
		endpoints = map[string]any{}
		for _, group := range []string{"primaryEndpoints", "secondaryEndpoints"} {
			for _, service := range []string{"blob", "dfs", "file", "web"} {
				endpoints[group+"/"+service] = object(props[group])[service]
			}
		}
		if strings.EqualFold(text(object(props["customDomain"])["name"]), host) {
			matched = "blob"
		}
	}
	for service, endpoint := range endpoints {
		u, err := url.Parse(text(endpoint))
		if err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.User == nil && u.Port() == "" && strings.EqualFold(u.Host, host) {
			if matched != "" && matched != last(service) {
				return "ambiguous"
			}
			matched = last(service)
		}
	}
	return matched
}

// Resolve names and URLs only through current ARM lists and matching GETs.
// Never fetch resource-file URLs, SAS URLs, storage keys, or Key Vault contents.
func (c *client) batchExternalReferences(ctx context.Context, account batchAccountContext, raw map[string]any, refs map[string][]string) error {
	indexes := map[string][]map[string]any{}
	resolve := func(kind, host, name string) (string, string, error) {
		values, loaded := indexes[kind]
		if !loaded {
			metadata, err := providerData()
			if err != nil {
				return "", "", err
			}
			operation := "Azure.Microsoft.Storage.StorageAccounts_List"
			if kind != storageType {
				operation = "Azure.Microsoft.KeyVault.Vaults_ListBySubscription"
			}
			op, ok := metadata.catalog.Operation(operation)
			if !ok {
				return "", "", serviceDenied("batch_reference_index_missing")
			}
			bound, err := bindAzureREST(op, map[string]any{"subscriptionId": c.subscription})
			if err != nil {
				return "", "", err
			}
			u, _ := url.Parse(bound.URL)
			rows, err := c.listAllURL(ctx, bound.URL, u.Path)
			if err != nil {
				return "", "", err
			}
			seen := map[string]bool{}
			for _, row := range rows {
				value := object(row)
				id, typ, err := parseID(text(value["id"]))
				if err != nil || !strings.EqualFold(typ, kind) || !validResponseType(kind, text(value["type"])) || !strings.HasPrefix(id, c.root()+"/") || seen[id] || !strings.EqualFold(text(value["name"]), last(id)) {
					return "", "", serviceDenied("invalid_batch_reference_index")
				}
				seen[id] = true
				values = append(values, value)
			}
			indexes[kind] = values
		}
		found, service := "", ""
		for _, value := range values {
			matched := batchReferenceEndpoint(kind, value, host, name)
			if matched == "" {
				continue
			}
			if matched == "ambiguous" {
				return "", "", serviceDenied("ambiguous_batch_reference_endpoint")
			}
			id := strings.ToLower(text(value["id"]))
			current, err := c.linkedResource(ctx, id)
			if err != nil {
				return "", "", err
			}
			if found != "" || batchReferenceEndpoint(kind, current, host, name) != matched || serviceListedIncarnation(value, current) != nil {
				return "", "", serviceDenied("batch_reference_target_changed_or_ambiguous")
			}
			found, service = id, matched
		}
		return found, service, nil
	}
	addContainer := func(parent, service, name string) error {
		if name == "" || (service != "blob" && service != "dfs" && service != "file") {
			return nil
		}
		collection, kind := "blobServices/default/containers", containerType
		if service == "file" {
			collection, kind = "fileServices/default/shares", "Microsoft.Storage/storageAccounts/fileServices/shares"
		}
		id, err := cognitiveNameID(parent, collection, name)
		if err != nil {
			return err
		}
		addReference(refs, kind, id)
		return nil
	}
	addURL := func(value any, kind, expectedService, expectedName string, generic bool) error {
		if value == nil {
			return nil
		}
		u, err := url.Parse(text(value))
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Hostname() == "" {
			return serviceDenied("invalid_batch_external_reference_url")
		}
		if u.User != nil || u.Fragment != "" || u.Port() != "" {
			if generic {
				return nil // An arbitrary download endpoint cannot establish an ARM reference.
			}
			return serviceDenied("invalid_batch_external_reference_url")
		}
		id, service, err := resolve(kind, u.Host, "")
		if err != nil {
			return err
		}
		if id == "" {
			// Generic download URLs need not identify an Azure resource.
			// Typed storage/vault URLs remain unresolved, without their SAS.
			if !generic || strings.HasSuffix(strings.ToLower(u.Host), ".blob.core.windows.net") || strings.HasSuffix(strings.ToLower(u.Host), ".blob.storage.azure.net") {
				addReference(refs, kind, strings.ToLower(u.Scheme+"://"+u.Host))
			}
			return nil
		}
		if expectedService != "" && service != expectedService && !(expectedService == "blob" && service == "dfs") {
			return serviceDenied("batch_storage_reference_service_disagrees")
		}
		if expectedName != "" && !strings.EqualFold(last(id), expectedName) {
			return serviceDenied("batch_storage_reference_name_disagrees")
		}
		addReference(refs, kind, id)
		if kind == storageType {
			parts := strings.Split(strings.TrimPrefix(u.EscapedPath(), "/"), "/")
			name, err := url.PathUnescape(parts[0])
			if err != nil {
				return serviceDenied("invalid_batch_container_reference")
			}
			if generic && service == "blob" && len(parts) == 1 && name != "" {
				name = "$root"
			}
			return addContainer(id, service, name)
		}
		return nil
	}
	var walk func(any) error
	walk = func(value any) error {
		switch value := value.(type) {
		case map[string]any:
			for key, entry := range value {
				if batchOpaqueReferenceField(key) {
					continue
				}
				var err error
				switch key {
				case "httpUrl":
					err = addURL(entry, storageType, "", "", true)
				case "storageContainerUrl", "containerUrl":
					err = addURL(entry, storageType, "blob", "", false)
				case "azureFileUrl":
					err = addURL(entry, storageType, "file", text(value["accountName"]), false)
				case "keyUrl", "keyIdentifier":
					err = addURL(entry, "Microsoft.KeyVault/vaults", "vault", "", false)
				case "autoStorageContainerName":
					parent, typ, parseErr := parseID(text(object(object(account.raw["properties"])["autoStorage"])["storageAccountId"]))
					if parseErr != nil || !strings.EqualFold(typ, storageType) || text(entry) == "" {
						return serviceDenied("invalid_batch_auto_storage_reference")
					}
					addReference(refs, storageType, parent)
					err = addContainer(parent, "blob", text(entry))
				case "azureBlobFileSystemConfiguration":
					config := object(entry)
					name := text(config["accountName"])
					if !storageNamePattern.MatchString(name) || text(config["containerName"]) == "" {
						return serviceDenied("invalid_batch_blob_mount_reference")
					}
					var id string
					id, _, err = resolve(storageType, "", name)
					if err == nil {
						if id == "" {
							addReference(refs, storageType, name)
						} else {
							addReference(refs, storageType, id)
							err = addContainer(id, "blob", text(config["containerName"]))
						}
					}
				}
				if err != nil {
					return err
				}
				if err := walk(entry); err != nil {
					return err
				}
			}
		case []any:
			for _, entry := range value {
				if err := walk(entry); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return walk(raw)
}
