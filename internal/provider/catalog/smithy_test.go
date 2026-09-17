package catalog

import (
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
)

// The fixture is a trimmed fragment of the official aws/api-models-aws Smithy
// AST: service traits choose protocol and endpoint prefix, member traits choose
// the idempotency token, and operation traits choose pagination.
const officialSmithyFragment = `{
  "smithy": "2.0",
  "shapes": {
    "com.amazonaws.cloudcontrol#CloudApiService": {"type": "service", "version": "2021-09-30",
      "operations": [{"target": "com.amazonaws.cloudcontrol#DeleteResource"}, {"target": "com.amazonaws.cloudcontrol#ListResources"}],
      "traits": {"aws.api#service": {"sdkId": "CloudControl", "endpointPrefix": "cloudcontrolapi"}, "aws.protocols#awsJson1_0": {}}},
    "com.amazonaws.cloudcontrol#DeleteResource": {"type": "operation", "input": {"target": "com.amazonaws.cloudcontrol#DeleteResourceInput"}},
    "com.amazonaws.cloudcontrol#DeleteResourceInput": {"type": "structure", "members": {
      "ClientToken": {"target": "com.amazonaws.cloudcontrol#ClientToken", "traits": {"smithy.api#idempotencyToken": {}}},
      "Identifier": {"target": "com.amazonaws.cloudcontrol#Identifier", "traits": {"smithy.api#required": {}}}}},
    "com.amazonaws.cloudcontrol#ListResources": {"type": "operation", "input": {"target": "com.amazonaws.cloudcontrol#ListResourcesInput"},
      "traits": {"smithy.api#paginated": {"inputToken": "NextToken", "outputToken": "NextToken", "items": "ResourceDescriptions", "pageSize": "MaxResults"}}},
    "com.amazonaws.drs#ElasticDisasterRecoveryService": {"type": "service", "version": "2020-02-26",
      "operations": [{"target": "com.amazonaws.drs#DeleteSourceServer"}],
      "traits": {"aws.api#service": {"sdkId": "drs"}, "aws.auth#sigv4": {"name": "drs"}, "aws.protocols#restJson1": {}}},
    "com.amazonaws.drs#DeleteSourceServer": {"type": "operation", "traits": {"smithy.api#http": {"method": "POST", "uri": "/DeleteSourceServer"}}},
    "com.amazonaws.ec2#AmazonEC2": {"type": "service", "version": "2016-11-15",
      "operations": [{"target": "com.amazonaws.ec2#DeregisterImage"}],
      "traits": {"aws.api#service": {"sdkId": "EC2", "endpointPrefix": "ec2"}, "aws.protocols#ec2Query": {}}},
    "com.amazonaws.ec2#DeregisterImage": {"type": "operation"}
  },
  "x-resource-types": [{"nativeType": "AWS::EC2::Image", "class": "compute.image", "displayName": "Amazon Machine Image", "scopeKinds": ["region"]}]
}`

func TestSmithyImporterDerivesAWSCallMetadataFromServiceTraits(t *testing.T) {
	c, err := SmithyImporter{}.Import(asset.ProviderAWS, "smithy.json", []byte(officialSmithyFragment))
	if err != nil {
		t.Fatal(err)
	}
	deleteResource, ok := c.Operation("com.amazonaws.cloudcontrol#DeleteResource")
	if !ok || deleteResource.Call == nil || !deleteResource.Destructive {
		t.Fatalf("DeleteResource = %#v", deleteResource)
	}
	call := deleteResource.Call
	if call.Product != "CloudControl" || call.Version != "2021-09-30" || call.Style != "aws-smithy" || call.Protocol != "awsJson1_0" ||
		call.Method != "POST" || call.Path != "/" || call.Endpoint != "https://cloudcontrolapi.{region}.amazonaws.com" ||
		call.IdempotencyParameter != "ClientToken" || len(call.EndpointParameters) != 1 || call.EndpointParameters[0] != "region" {
		t.Fatalf("DeleteResource call = %#v", call)
	}
	list, _ := c.Operation("com.amazonaws.cloudcontrol#ListResources")
	if list.Pagination == nil || list.Pagination.InputTokenPath != "NextToken" || list.Pagination.ItemsPath != "ResourceDescriptions" || list.Destructive {
		t.Fatalf("ListResources = %#v", list)
	}
	rest, _ := c.Operation("com.amazonaws.drs#DeleteSourceServer")
	if rest.Call == nil || rest.Call.Path != "/DeleteSourceServer" || rest.Call.Endpoint != "https://drs.{region}.amazonaws.com" || rest.Call.Protocol != "restJson1" {
		t.Fatalf("DeleteSourceServer = %#v", rest.Call)
	}
	deregister, _ := c.Operation("com.amazonaws.ec2#DeregisterImage")
	if !deregister.Destructive || deregister.Call == nil || deregister.Call.BodyType != "form" {
		t.Fatalf("DeregisterImage = %#v", deregister)
	}
}

func TestSmithyImporterRejectsAmbiguousServiceProtocols(t *testing.T) {
	source := `{"shapes": {
	  "a#S": {"type": "service", "version": "1", "operations": [{"target": "a#Op"}], "traits": {"aws.api#service": {"sdkId": "A", "endpointPrefix": "a"}, "aws.protocols#awsJson1_0": {}, "aws.protocols#awsQuery": {}}},
	  "a#Op": {"type": "operation"}}}`
	if _, err := (SmithyImporter{}).Import(asset.ProviderAWS, "s", []byte(source)); err == nil {
		t.Fatal("expected ambiguous protocol error")
	}
	restWithoutBinding := `{"shapes": {
	  "a#S": {"type": "service", "version": "1", "operations": [{"target": "a#Op"}], "traits": {"aws.api#service": {"sdkId": "A", "endpointPrefix": "a"}, "aws.protocols#restJson1": {}}},
	  "a#Op": {"type": "operation"}}}`
	if _, err := (SmithyImporter{}).Import(asset.ProviderAWS, "s", []byte(restWithoutBinding)); err == nil {
		t.Fatal("expected missing HTTP binding error")
	}
}
