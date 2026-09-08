package gcp

import (
	"context"
	"fmt"
	"strings"

	"github.com/loomx-ai/steward/internal/provider/catalog"
)

// Deletion removes resource records; it does not schedule destruction of key
// material. Imported versions and still-active material have distinct provider
// restrictions and must not be mistaken for immediately deletable resources.
func (a *action) kmsPreflight(ctx context.Context, data map[string]any) (string, error) {
	switch a.kind.NativeType {
	case "cloudkms.googleapis.com/CryptoKeyVersion":
		if text(data["importTime"]) != "" && text(data["state"]) != "IMPORT_FAILED" {
			return "imported_key_version_not_deletable", nil
		}
	case "cloudkms.googleapis.com/CryptoKey":
		if text(data["nextRotationTime"]) != "" || text(data["rotationPeriod"]) != "" {
			return "key_rotation_enabled", nil
		}
		found, err := a.kmsChildren(ctx, "cloudkms.projects.locations.keyRings.cryptoKeys.cryptoKeyVersions.list", "cryptoKeyVersions", nil)
		if err != nil {
			return "", err
		}
		if found {
			return "key_versions_not_deleted", nil
		}
	case "cloudkms.googleapis.com/KeyRing":
		found, err := a.kmsChildren(ctx, "cloudkms.projects.locations.keyRings.cryptoKeys.list", "cryptoKeys", nil)
		if err != nil {
			return "", err
		}
		if found {
			return "key_ring_contains_keys", nil
		}
		found, err = a.kmsChildren(ctx, "cloudkms.projects.locations.keyRings.importJobs.list", "importJobs", func(item map[string]any) bool { return text(item["state"]) != "EXPIRED" })
		if err != nil {
			return "", err
		}
		if found {
			return "key_ring_contains_unexpired_import_jobs", nil
		}
	}
	return "", nil
}
func (a *action) kmsChildren(ctx context.Context, operationID, itemsPath string, matches func(map[string]any) bool) (bool, error) {
	metadata, err := providerData()
	if err != nil {
		return false, err
	}
	operation, ok := metadata.catalog.Operation(operationID)
	if !ok {
		return false, fmt.Errorf("missing KMS child list operation")
	}
	parent := strings.TrimPrefix(a.endpoint, "https://cloudkms.googleapis.com/v1/")
	parameters := map[string]any{"parent": parent, "pageSize": 100}
	seen := map[string]bool{}
	for {
		bound, err := catalog.BindREST(operation, parameters)
		if err != nil {
			return false, err
		}
		response, err := a.client.requestResult(ctx, bound.Method, bound.URL, nil, bound.Body)
		if err != nil {
			return false, err
		}
		if err = checkListCompleteness(response.Data); err != nil {
			return false, err
		}
		items, err := productRecords(response.Data, itemsPath)
		if err != nil {
			return false, err
		}
		for _, item := range items {
			if matches == nil || matches(item.Data) {
				return true, nil
			}
		}
		next := text(response.Data["nextPageToken"])
		if next == "" {
			return false, nil
		}
		if seen[next] {
			return false, fmt.Errorf("KMS child pagination did not advance")
		}
		seen[next] = true
		parameters["pageToken"] = next
	}
}
