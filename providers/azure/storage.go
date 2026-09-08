package azure

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/loomx-ai/steward/internal/core/execution"
)

func (c *client) storageAccountEmpty(ctx context.Context, id string, raw map[string]any) (bool, error) {
	services := []string{}
	switch text(raw["kind"]) {
	case "Storage", "StorageV2":
		services = []string{"blobServices/default/containers", "fileServices/default/shares", "queueServices/default/queues", "tableServices/default/tables"}
	case "BlobStorage", "BlockBlobStorage":
		services = []string{"blobServices/default/containers"}
	case "FileStorage":
		services = []string{"fileServices/default/shares"}
	default:
		return false, fmt.Errorf("unsupported Azure storage account kind")
	}
	for _, service := range services {
		path := id + "/" + service
		endpoint := apiURL(path, "2023-05-01")
		if strings.HasSuffix(service, "/containers") || strings.HasSuffix(service, "/shares") {
			endpoint += "&%24include=deleted"
		}
		children, next, err := c.listPage(ctx, endpoint, path)
		if isNotFound(err) {
			return false, apiError(http.StatusConflict, "storage_service_not_verified", nil)
		}
		if err != nil {
			return false, err
		}
		if len(children) != 0 || next != "" {
			return false, nil
		}
	}
	return true, nil
}
func (c *client) blobContainerEmpty(ctx context.Context, id string) (empty bool, failure error) {
	parts := strings.Split(id, "/")
	if len(parts) != 13 || !storageNamePattern.MatchString(parts[8]) || parts[9] != "blobservices" || parts[10] != "default" || parts[11] != "containers" {
		return false, fmt.Errorf("invalid Azure blob container identity")
	}
	accountID := strings.Join(parts[:9], "/")
	account, err := c.request(ctx, "GET", apiURL(accountID, "2023-05-01"))
	if err != nil {
		return false, err
	}
	base := "https://" + parts[8] + ".blob.core.windows.net/"
	if text(object(object(account.data["properties"])["primaryEndpoints"])["blob"]) != base {
		return false, fmt.Errorf("Azure storage account does not use the supported public Blob endpoint")
	}
	req, err := http.NewRequestWithContext(ctx, "GET", base+parts[12]+"?restype=container&comp=list&maxresults=1&include=versions,snapshots,deleted,deletedwithversions,uncommittedblobs", nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("x-ms-version", "2023-11-03")
	execution.LogCloudAPIRequest(ctx, "azure-storage", "ListBlobs", map[string]any{"account": parts[8], "container": parts[12], "include": "versions,snapshots,deleted,deletedwithversions,uncommittedblobs", "max_results": 1})
	defer func() {
		if failure != nil {
			execution.LogCloudAPIFailure(ctx, "azure-storage", "ListBlobs", failure)
		}
	}()
	res, err := c.storageHTTP.Do(req)
	if err != nil {
		return false, transportError(ctx, err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		status := res.StatusCode
		// ARM confirmed that the container exists. A data-plane 404 is not
		// evidence that the ARM resource was deleted.
		if status == http.StatusNotFound {
			status = http.StatusConflict
		}
		return false, apiError(status, "storage_list_failed", res.Header)
	}
	payload, err := io.ReadAll(io.LimitReader(res.Body, (1<<20)+1))
	if err != nil || len(payload) > 1<<20 {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		return false, apiError(res.StatusCode, "storage_response_unreadable", res.Header)
	}
	var listing struct {
		XMLName xml.Name `xml:"EnumerationResults"`
		Blobs   *struct {
			Blob   []struct{} `xml:"Blob"`
			Prefix []struct{} `xml:"BlobPrefix"`
		} `xml:"Blobs"`
		NextMarker *string `xml:"NextMarker"`
	}
	if xml.Unmarshal(payload, &listing) != nil || listing.Blobs == nil || listing.NextMarker == nil {
		return false, apiError(res.StatusCode, "storage_invalid_response", res.Header)
	}
	empty = len(listing.Blobs.Blob) == 0 && len(listing.Blobs.Prefix) == 0 && strings.TrimSpace(*listing.NextMarker) == ""
	execution.LogCloudAPIResponse(ctx, "azure-storage", "ListBlobs", map[string]any{"request_id": requestID(res.Header), "empty": empty, "status_code": res.StatusCode})
	return empty, nil
}
