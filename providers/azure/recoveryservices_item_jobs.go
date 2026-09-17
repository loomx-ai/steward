package azure

import (
	"context"
	"net/http"
	"slices"
	"strings"

	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func recoveryItemOperationToken(value string) bool {
	return value != "" && value != "." && value != ".." && len(value) <= 1024 && !strings.ContainsAny(value, "%?#/\\\x00\r\n\t ")
}

// A successful operation can merely have created a native backup job. Keep its
// IDs so restart recovery can verify the jobs before reconciling the item itself.
func recoveryItemOperationJobs(data map[string]any) ([]string, error) {
	raw, exists := data["properties"]
	if !exists || raw == nil {
		return nil, nil
	}
	properties := object(raw)
	if properties == nil {
		return nil, serviceDenied("invalid_recovery_item_job_properties")
	}
	var values []any
	switch properties["objectType"] {
	case "OperationStatusJobExtendedInfo":
		values = []any{properties["jobId"]}
	case "OperationStatusJobsExtendedInfo":
		var ok bool
		values, ok = properties["jobIds"].([]any)
		if !ok {
			return nil, serviceDenied("invalid_recovery_item_job_ids")
		}
		if raw, exists := properties["failedJobsError"]; exists {
			failures := object(raw)
			if failures == nil || len(failures) != 0 {
				return nil, serviceDenied("recovery_item_jobs_failed")
			}
		}
	default:
		return nil, serviceDenied("unknown_recovery_item_job_properties")
	}
	if len(values) == 0 || len(values) > 1000 {
		return nil, serviceDenied("invalid_recovery_item_job_count")
	}
	ids := make([]string, 0, len(values))
	for _, value := range values {
		id, ok := value.(string)
		if !ok || !recoveryItemOperationToken(id) || slices.Contains(ids, id) {
			return nil, serviceDenied("invalid_recovery_item_job_identity")
		}
		ids = append(ids, id)
	}
	return ids, nil
}

func (c *client) recoveryItemJobs(ctx context.Context, id string, jobs []string) (bool, error) {
	owner, err := c.recoveryServicesIdentity(id, recoveryServicesItem)
	if err != nil || owner != id || len(jobs) > 1000 {
		return false, serviceDenied("invalid_recovery_item_job_owner")
	}
	seen := map[string]bool{}
	for _, job := range jobs {
		if !recoveryItemOperationToken(job) || seen[job] {
			return false, serviceDenied("invalid_recovery_item_job_identity")
		}
		seen[job] = true
	}
	metadata, err := providerData()
	if err != nil {
		return false, err
	}
	operation, ok := metadata.catalog.Operation("Azure.Microsoft.RecoveryServices.JobDetails_Get")
	if !ok {
		return false, serviceDenied("missing_recovery_item_job_operation")
	}
	vault := recoveryServicesVaultID(id)
	parts := strings.Split(vault, "/")
	done := true
	for _, job := range jobs {
		nativeID := recoveryServicesVaultID(id) + "/backupJobs/" + job
		bound, err := bindAzureREST(operation, map[string]any{"subscriptionId": c.subscription, "resourceGroupName": parts[4], "vaultName": last(vault), "jobName": job})
		if err != nil {
			return false, err
		}
		res, err := c.request(ctx, bound.Method, bound.URL)
		if err != nil {
			return false, contracts.DependencyReadError(err)
		}
		if err = operationError(res); err != nil {
			return false, err
		}
		if res.status != http.StatusOK || len(res.header.Values("Location"))+len(res.header.Values("Azure-AsyncOperation"))+len(res.header.Values("Operation-Location")) != 0 || !strings.EqualFold(text(res.data["id"]), nativeID) || text(res.data["name"]) != job || last(text(res.data["id"])) != job || !strings.EqualFold(text(res.data["type"]), "Microsoft.RecoveryServices/vaults/backupJobs") {
			return false, serviceDenied("invalid_recovery_item_job_resource")
		}
		p := object(res.data["properties"])
		if p["operation"] != "DeleteBackupData" || text(p["jobType"]) == "" {
			return false, serviceDenied("recovery_item_job_operation_changed")
		}
		if details, exists := p["errorDetails"]; exists {
			values, ok := details.([]any)
			if !ok || len(values) != 0 && p["status"] != "CompletedWithWarnings" {
				return false, serviceDenied("recovery_item_job_error")
			}
		}
		switch p["status"] {
		case "Completed", "CompletedWithWarnings":
			// This is only job completion. Own-item readback must still prove absence
			// or the explicitly reviewed same-ID deferred-delete transition.
		case "InProgress", "Cancelling":
			done = false
		case "Failed", "Cancelled":
			return false, serviceDenied("recovery_item_job_failed")
		default:
			return false, serviceDenied("unknown_recovery_item_job_status")
		}
	}
	return done, nil
}
