package aws

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	awscredentials "github.com/aws/aws-sdk-go-v2/credentials"
	awscloudcontrol "github.com/aws/aws-sdk-go-v2/service/cloudcontrol"
)

// Protocol evidence: the official SDK client serializes the Cloud Control
// awsJson1_0 requests produced by list plans and protection updates, and the
// adapter reads request IDs and progress events from documented responses.
func TestCloudControlSDKWireProtocol(t *testing.T) {
	type call struct {
		target string
		body   map[string]any
	}
	var calls []call
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		payload, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(payload, &body)
		calls = append(calls, call{target: r.Header.Get("X-Amz-Target"), body: body})
		if r.Header.Get("Content-Type") != "application/x-amz-json-1.0" || r.Header.Get("Authorization") == "" {
			t.Errorf("request headers = %v", r.Header)
		}
		w.Header().Set("Content-Type", "application/x-amz-json-1.0")
		w.Header().Set("X-Amzn-RequestId", "rid-"+r.Header.Get("X-Amz-Target"))
		switch r.Header.Get("X-Amz-Target") {
		case "CloudApiService.ListResources":
			_, _ = w.Write([]byte(`{"TypeName":"AWS::EKS::Nodegroup","ResourceDescriptions":[{"Identifier":"prod|ng-a","Properties":"{\"ClusterName\":\"prod\"}"}],"NextToken":"page-2"}`))
		case "CloudApiService.UpdateResource":
			_, _ = w.Write([]byte(`{"ProgressEvent":{"TypeName":"AWS::EC2::Instance","Identifier":"i-1","RequestToken":"update-token","Operation":"UPDATE","OperationStatus":"IN_PROGRESS","EventTime":1.7E9,"RetryAfter":1.7E9}}`))
		case "CloudApiService.GetResourceRequestStatus":
			_, _ = w.Write([]byte(`{"ProgressEvent":{"RequestToken":"delete-token","Operation":"DELETE","OperationStatus":"FAILED","ErrorCode":"NotFound","StatusMessage":"gone"}}`))
		case "CloudApiService.GetResource":
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"__type":"ResourceNotFoundException","Message":"Resource of type 'AWS::EC2::Instance' with identifier 'i-2' was not found."}`))
		default:
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer server.Close()
	client := &cloudControlSDK{client: awscloudcontrol.New(awscloudcontrol.Options{
		Region: "eu-west-1", BaseEndpoint: awssdk.String(server.URL),
		Credentials:      awscredentials.NewStaticCredentialsProvider("AKID", "SECRET", ""),
		RetryMaxAttempts: 1,
	})}
	ctx := context.Background()
	page, err := client.ListResources(ctx, CloudControlListRequest{TypeName: "AWS::EKS::Nodegroup", NextToken: "page-1", Limit: 50, ResourceModel: `{"ClusterName":"prod"}`})
	if err != nil || page.NextToken != "page-2" || page.RequestID != "rid-CloudApiService.ListResources" || len(page.Resources) != 1 {
		t.Fatalf("page=%+v err=%v", page, err)
	}
	progress, requestID, err := client.UpdateResource(ctx, CloudControlUpdateRequest{TypeName: "AWS::EC2::Instance", Identifier: "i-1", PatchDocument: `[{"op":"replace","path":"/DisableApiTermination","value":false}]`, ClientToken: "step-1:disable-deletion-protection"})
	if err != nil || progress.RequestToken != "update-token" || progress.Status != "IN_PROGRESS" || requestID != "rid-CloudApiService.UpdateResource" || progress.RetryAfter.IsZero() {
		t.Fatalf("progress=%+v request=%s err=%v", progress, requestID, err)
	}
	status, _, err := client.GetResourceRequestStatus(ctx, "delete-token")
	if err != nil || status.Status != "FAILED" || status.ErrorCode != "NotFound" {
		t.Fatalf("status=%+v err=%v", status, err)
	}
	_, missingRequestID, err := client.GetResource(ctx, "AWS::EC2::Instance", "i-2")
	if !cloudControlNotFound(err) || missingRequestID != "rid-CloudApiService.GetResource" {
		t.Fatalf("missing resource err=%v request=%s", err, missingRequestID)
	}
	if len(calls) != 4 {
		t.Fatalf("calls = %+v", calls)
	}
	list := calls[0].body
	if list["TypeName"] != "AWS::EKS::Nodegroup" || list["ResourceModel"] != `{"ClusterName":"prod"}` || list["NextToken"] != "page-1" || list["MaxResults"] != float64(50) {
		t.Fatalf("ListResources body = %+v", list)
	}
	update := calls[1].body
	if update["PatchDocument"] != `[{"op":"replace","path":"/DisableApiTermination","value":false}]` || update["ClientToken"] != "step-1:disable-deletion-protection" || update["Identifier"] != "i-1" {
		t.Fatalf("UpdateResource body = %+v", update)
	}
}
