package aws

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	awscredentials "github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

// Protocol evidence for APIs Moto cannot serve faithfully. Responses follow the
// documented awsQuery (DocumentDB), ec2Query (EC2) and awsJson1_1 (DMS) shapes
// and are decoded by the official SDK clients.
func protocolNativeRuntime(t *testing.T, handler http.HandlerFunc) *Runtime {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	config := awssdk.Config{
		Region: "us-east-1", BaseEndpoint: awssdk.String(server.URL), RetryMaxAttempts: 1,
		Credentials: awscredentials.NewStaticCredentialsProvider("AKID", "SECRET", ""),
	}
	source := &runtimeCredentialSource{want: "connection-protocol", value: contracts.Credential{Values: map[string]string{"access_key_id": "AKID", "secret_access_key": "SECRET"}}}
	runtime, err := newRuntime(source, &runtimeFactory{native: newNativeClients(config)})
	if err != nil {
		t.Fatal(err)
	}
	return runtime
}

func formAction(t *testing.T, r *http.Request) url.Values {
	t.Helper()
	payload, _ := io.ReadAll(r.Body)
	values, err := url.ParseQuery(string(payload))
	if err != nil {
		t.Fatal(err)
	}
	return values
}

func nativeRequest(nativeType, id string) contracts.ActionRequest {
	return contracts.ActionRequest{
		Asset:  asset.Asset{Location: "us-east-1", Identity: asset.Identity{Provider: asset.ProviderAWS, ConnectionID: "connection-protocol", NativeType: nativeType, NativeID: id}},
		Action: "delete", IdempotencyKey: "step-" + id,
	}
}

func TestDocumentDBProtocolDisablesProtectionAndRespectsMembers(t *testing.T) {
	var mu sync.Mutex
	protected, members, clusterExists, instanceExists := true, true, true, true
	var actions []string
	runtime := protocolNativeRuntime(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		form := formAction(t, r)
		action := form.Get("Action")
		actions = append(actions, action)
		if form.Get("Version") != "2014-10-31" {
			t.Errorf("DocumentDB API version = %s", form.Get("Version"))
		}
		w.Header().Set("Content-Type", "text/xml")
		w.Header().Set("X-Amzn-RequestId", "rid-"+strings.ToLower(action))
		notFound := func(code string) {
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `<ErrorResponse><Error><Type>Sender</Type><Code>`+code+`</Code><Message>not found</Message></Error><RequestId>rid-missing</RequestId></ErrorResponse>`)
		}
		switch action {
		case "DescribeDBClusters":
			if form.Get("Filters.Filter.1.Name") != "" && form.Get("Filters.Filter.1.Values.Value.1") != "docdb" {
				t.Errorf("cluster engine filter = %v", form)
			}
			if !clusterExists {
				notFound("DBClusterNotFoundFault")
				return
			}
			memberXML := ""
			if members {
				memberXML = `<DBClusterMember><DBInstanceIdentifier>docs-1</DBInstanceIdentifier><IsClusterWriter>true</IsClusterWriter></DBClusterMember>`
			}
			_, _ = io.WriteString(w, `<DescribeDBClustersResponse xmlns="http://rds.amazonaws.com/doc/2014-10-31/"><DescribeDBClustersResult><DBClusters><DBCluster>
<DBClusterIdentifier>docs</DBClusterIdentifier><Engine>docdb</Engine><Status>available</Status>
<DeletionProtection>`+map[bool]string{true: "true", false: "false"}[protected]+`</DeletionProtection>
<DBClusterMembers>`+memberXML+`</DBClusterMembers>
<VpcSecurityGroups><VpcSecurityGroupMembership><VpcSecurityGroupId>sg-docs</VpcSecurityGroupId><Status>active</Status></VpcSecurityGroupMembership></VpcSecurityGroups>
</DBCluster></DBClusters></DescribeDBClustersResult><ResponseMetadata><RequestId>rid-describe</RequestId></ResponseMetadata></DescribeDBClustersResponse>`)
		case "DescribeDBInstances":
			if !instanceExists {
				notFound("DBInstanceNotFoundFault")
				return
			}
			_, _ = io.WriteString(w, `<DescribeDBInstancesResponse xmlns="http://rds.amazonaws.com/doc/2014-10-31/"><DescribeDBInstancesResult><DBInstances><DBInstance>
<DBInstanceIdentifier>docs-1</DBInstanceIdentifier><DBClusterIdentifier>docs</DBClusterIdentifier><DBInstanceStatus>available</DBInstanceStatus>
<DBSubnetGroup><VpcId>vpc-docs</VpcId></DBSubnetGroup></DBInstance></DBInstances></DescribeDBInstancesResult><ResponseMetadata><RequestId>rid-instances</RequestId></ResponseMetadata></DescribeDBInstancesResponse>`)
		case "ModifyDBCluster":
			if form.Get("DeletionProtection") != "false" || form.Get("ApplyImmediately") != "true" {
				t.Errorf("ModifyDBCluster form = %v", form)
			}
			protected = false
			_, _ = io.WriteString(w, `<ModifyDBClusterResponse xmlns="http://rds.amazonaws.com/doc/2014-10-31/"><ModifyDBClusterResult><DBCluster><DBClusterIdentifier>docs</DBClusterIdentifier></DBCluster></ModifyDBClusterResult><ResponseMetadata><RequestId>rid-modify</RequestId></ResponseMetadata></ModifyDBClusterResponse>`)
		case "DeleteDBInstance":
			instanceExists, members = false, false
			_, _ = io.WriteString(w, `<DeleteDBInstanceResponse xmlns="http://rds.amazonaws.com/doc/2014-10-31/"><DeleteDBInstanceResult><DBInstance><DBInstanceIdentifier>docs-1</DBInstanceIdentifier><DBInstanceStatus>deleting</DBInstanceStatus></DBInstance></DeleteDBInstanceResult><ResponseMetadata><RequestId>rid-delete-instance</RequestId></ResponseMetadata></DeleteDBInstanceResponse>`)
		case "DeleteDBCluster":
			if protected || form.Get("SkipFinalSnapshot") != "true" {
				t.Errorf("DeleteDBCluster protected=%v form=%v", protected, form)
			}
			clusterExists = false
			_, _ = io.WriteString(w, `<DeleteDBClusterResponse xmlns="http://rds.amazonaws.com/doc/2014-10-31/"><DeleteDBClusterResult><DBCluster><DBClusterIdentifier>docs</DBClusterIdentifier><Status>deleting</Status></DBCluster></DeleteDBClusterResult><ResponseMetadata><RequestId>rid-delete-cluster</RequestId></ResponseMetadata></DeleteDBClusterResponse>`)
		default:
			t.Errorf("unexpected action %s", action)
		}
	})
	ctx := context.Background()
	scope := asset.Scope{Kind: asset.ScopeRegion, NativeID: "us-east-1", Location: "us-east-1"}
	batch, err := runtime.List(ctx, contracts.InventoryRequest{ConnectionID: "connection-protocol", Source: productAPISource, ResourceKind: kindFor(t, runtime, "AWS::DocDB::DBCluster"), Scope: scope})
	if err != nil || len(batch.Items) != 1 || batch.RequestID != "rid-describedbclusters" || !batch.Complete {
		t.Fatalf("cluster batch=%+v err=%v", batch, err)
	}
	if refs, _ := batch.Items[0].Normalized["security_group_ids"].([]string); len(refs) != 1 || refs[0] != "sg-docs" {
		t.Fatalf("cluster references = %+v", batch.Items[0].Normalized)
	}
	instances, err := runtime.List(ctx, contracts.InventoryRequest{ConnectionID: "connection-protocol", Source: productAPISource, ResourceKind: kindFor(t, runtime, "AWS::DocDB::DBInstance"), Scope: scope})
	if err != nil || len(instances.Items) != 1 || instances.Items[0].Normalized["vpc_id"] != "vpc-docs" {
		t.Fatalf("instance batch=%+v err=%v", instances, err)
	}

	clusterRequest := nativeRequest("AWS::DocDB::DBCluster", "docs")
	clusterDriver, err := runtime.ResolveAction(ctx, "connection-protocol", clusterRequest.Asset)
	if err != nil {
		t.Fatal(err)
	}
	preflight, err := clusterDriver.Preflight(ctx, clusterRequest)
	if err != nil || preflight.Allowed || !strings.Contains(preflight.Reason, "member instances") {
		t.Fatalf("members must block cluster deletion: %+v err=%v", preflight, err)
	}
	if _, err := clusterDriver.Execute(ctx, clusterRequest); err == nil {
		t.Fatal("execute must re-check members instead of trusting the earlier preflight")
	}

	instanceRequest := nativeRequest("AWS::DocDB::DBInstance", "docs-1")
	instanceDriver, _ := runtime.ResolveAction(ctx, "connection-protocol", instanceRequest.Asset)
	if _, err := instanceDriver.Execute(ctx, instanceRequest); err != nil {
		t.Fatal(err)
	}
	if wait, err := instanceDriver.Wait(ctx, instanceRequest, contracts.ActionResult{}); err != nil || !wait.Done {
		t.Fatalf("instance wait=%+v err=%v", wait, err)
	}

	preflight, err = clusterDriver.Preflight(ctx, clusterRequest)
	if err != nil || !preflight.Allowed || preflight.Evidence["deletion_protection"] != true {
		t.Fatalf("cluster preflight = %+v err=%v", preflight, err)
	}
	result, err := clusterDriver.Execute(ctx, clusterRequest)
	if err != nil || result.ProviderRequestID != "rid-deletedbcluster" || result.Data["protection_request_id"] != "rid-modifydbcluster" {
		t.Fatalf("cluster execute = %+v err=%v", result, err)
	}
	readback, err := clusterDriver.Readback(ctx, clusterRequest)
	if err != nil || readback.Exists || readback.Data["provider_request_id"] != "rid-describedbclusters" {
		t.Fatalf("cluster readback = %+v err=%v", readback, err)
	}
	joined := strings.Join(actions, ",")
	if !strings.Contains(joined, "ModifyDBCluster,DescribeDBClusters,DeleteDBCluster") {
		t.Fatalf("protection must be read back before deletion: %s", joined)
	}
}

func TestDMSProtocolDeletesReplicationInstanceUntilNotFound(t *testing.T) {
	const arn = "arn:aws:dms:us-east-1:123456789012:rep:ABCDEF"
	var mu sync.Mutex
	status := "available"
	runtime := protocolNativeRuntime(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		payload, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/x-amz-json-1.1")
		w.Header().Set("X-Amzn-RequestId", "rid-"+status)
		switch r.Header.Get("X-Amz-Target") {
		case "AmazonDMSv20160101.DescribeReplicationInstances":
			if strings.Contains(string(payload), "replication-instance-arn") && status == "gone" {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(w, `{"__type":"ResourceNotFoundFault","message":"No replication instance found"}`)
				return
			}
			_, _ = io.WriteString(w, `{"ReplicationInstances":[{"ReplicationInstanceArn":"`+arn+`","ReplicationInstanceIdentifier":"etl","ReplicationInstanceStatus":"`+status+`","InstanceCreateTime":1.7E9,"ReplicationSubnetGroup":{"VpcId":"vpc-dms"},"VpcSecurityGroups":[{"VpcSecurityGroupId":"sg-dms","Status":"active"}]}]}`)
		case "AmazonDMSv20160101.DeleteReplicationInstance":
			if !strings.Contains(string(payload), arn) {
				t.Errorf("delete body = %s", payload)
			}
			status = "deleting"
			_, _ = io.WriteString(w, `{"ReplicationInstance":{"ReplicationInstanceArn":"`+arn+`","ReplicationInstanceStatus":"deleting"}}`)
		default:
			t.Errorf("unexpected target %s", r.Header.Get("X-Amz-Target"))
		}
	})
	ctx := context.Background()
	batch, err := runtime.List(ctx, contracts.InventoryRequest{ConnectionID: "connection-protocol", Source: productAPISource, ResourceKind: kindFor(t, runtime, "AWS::DMS::ReplicationInstance"), Scope: asset.Scope{Kind: asset.ScopeRegion, NativeID: "us-east-1", Location: "us-east-1"}})
	if err != nil || len(batch.Items) != 1 || batch.Items[0].NativeID != arn || batch.Items[0].Name != "etl" || batch.Items[0].Normalized["vpc_id"] != "vpc-dms" {
		t.Fatalf("batch=%+v err=%v", batch, err)
	}
	request := nativeRequest("AWS::DMS::ReplicationInstance", arn)
	driver, err := runtime.ResolveAction(ctx, "connection-protocol", request.Asset)
	if err != nil {
		t.Fatal(err)
	}
	result, err := driver.Execute(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	wait, err := driver.Wait(ctx, request, result)
	if err != nil || wait.Done || wait.State != "deleting" {
		t.Fatalf("deleting wait=%+v err=%v", wait, err)
	}
	// A resubmitted Execute during deletion must not call delete again.
	if again, err := driver.Execute(ctx, request); err != nil || again.Data["phase"] != "deleting" {
		t.Fatalf("repeat execute=%+v err=%v", again, err)
	}
	mu.Lock()
	status = "gone"
	mu.Unlock()
	wait, err = driver.Wait(ctx, request, result)
	if err != nil || !wait.Done {
		t.Fatalf("final wait=%+v err=%v", wait, err)
	}
}

func TestEC2ProtocolDisablesImageDeregistrationProtection(t *testing.T) {
	var mu sync.Mutex
	protection, registered := "enabled", true
	var actions []string
	runtime := protocolNativeRuntime(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		form := formAction(t, r)
		actions = append(actions, form.Get("Action"))
		w.Header().Set("Content-Type", "text/xml")
		w.Header().Set("X-Amzn-RequestId", "rid-"+strings.ToLower(form.Get("Action")))
		switch form.Get("Action") {
		case "DescribeImages":
			state := "available"
			if !registered {
				state = "deregistered"
			}
			_, _ = io.WriteString(w, `<DescribeImagesResponse xmlns="http://ec2.amazonaws.com/doc/2016-11-15/"><requestId>rid-images</requestId><imagesSet><item><imageId>ami-1</imageId><imageState>`+state+`</imageState><deregistrationProtection>`+protection+`</deregistrationProtection><name>golden</name></item></imagesSet></DescribeImagesResponse>`)
		case "DisableImageDeregistrationProtection":
			protection = "disabled"
			_, _ = io.WriteString(w, `<DisableImageDeregistrationProtectionResponse xmlns="http://ec2.amazonaws.com/doc/2016-11-15/"><requestId>rid-disable</requestId><return>disabled</return></DisableImageDeregistrationProtectionResponse>`)
		case "DeregisterImage":
			if protection != "disabled" {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(w, `<Response><Errors><Error><Code>InvalidParameterValue</Code><Message>protected</Message></Error></Errors><RequestID>rid-denied</RequestID></Response>`)
				return
			}
			registered = false
			_, _ = io.WriteString(w, `<DeregisterImageResponse xmlns="http://ec2.amazonaws.com/doc/2016-11-15/"><requestId>rid-deregister</requestId><return>true</return></DeregisterImageResponse>`)
		default:
			t.Errorf("unexpected action %s", form.Get("Action"))
		}
	})
	ctx := context.Background()
	request := nativeRequest("AWS::EC2::Image", "ami-1")
	driver, err := runtime.ResolveAction(ctx, "connection-protocol", request.Asset)
	if err != nil {
		t.Fatal(err)
	}
	preflight, err := driver.Preflight(ctx, request)
	if err != nil || !preflight.Allowed || preflight.Evidence["deletion_protection"] != true {
		t.Fatalf("preflight=%+v err=%v", preflight, err)
	}
	result, err := driver.Execute(ctx, request)
	if err != nil || result.ProviderRequestID != "rid-deregisterimage" {
		t.Fatalf("execute=%+v err=%v", result, err)
	}
	wait, err := driver.Wait(ctx, request, result)
	if err != nil || !wait.Done {
		t.Fatalf("wait=%+v err=%v", wait, err)
	}
	if got := strings.Join(actions, ","); got != "DescribeImages,DescribeImages,DisableImageDeregistrationProtection,DescribeImages,DeregisterImage,DescribeImages" {
		t.Fatalf("actions = %s", got)
	}
}
