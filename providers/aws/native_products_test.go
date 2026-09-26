package aws

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	awssecrets "github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	secretstypes "github.com/aws/aws-sdk-go-v2/service/secretsmanager/types"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

// ACM Private CA uses awsJson1_1. An active CA is disabled and read back
// before DeleteCertificateAuthority; a DELETED CA is restorable but absent.
func TestPrivateCAProtocolDisablesAnActiveCAFirst(t *testing.T) {
	var mu sync.Mutex
	status := "ACTIVE"
	var targets []string
	runtime := protocolNativeRuntime(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		target := strings.TrimPrefix(r.Header.Get("X-Amz-Target"), "ACMPrivateCA.")
		targets = append(targets, target)
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/x-amz-json-1.1")
		switch target {
		case "DescribeCertificateAuthority":
			payload, _ := json.Marshal(map[string]any{"CertificateAuthority": map[string]any{"Arn": body["CertificateAuthorityArn"], "Status": status, "Type": "ROOT"}})
			_, _ = w.Write(payload)
		case "UpdateCertificateAuthority":
			if body["Status"] != "DISABLED" {
				t.Errorf("update body = %v", body)
			}
			status = "DISABLED"
			_, _ = io.WriteString(w, `{}`)
		case "DeleteCertificateAuthority":
			if status != "DISABLED" {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(w, `{"__type":"InvalidStateException","message":"active"}`)
				return
			}
			if _, set := body["PermanentDeletionTimeInDays"]; set {
				t.Errorf("restore period changed: %v", body)
			}
			status = "DELETED"
			_, _ = io.WriteString(w, `{}`)
		default:
			t.Errorf("unexpected target %s", target)
		}
	})
	ctx := context.Background()
	request := nativeRequest(privateCAType, "arn:aws:acm-pca:us-east-1:123456789012:certificate-authority/1")
	driver, err := runtime.ResolveAction(ctx, "connection-protocol", request.Asset)
	if err != nil {
		t.Fatal(err)
	}
	preflight, err := driver.Preflight(ctx, request)
	if err != nil || !preflight.Allowed || preflight.Evidence["pre_delete_action"] != "disable_deletion_protection" {
		t.Fatalf("preflight = %+v err=%v", preflight, err)
	}
	result, err := driver.Execute(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if wait, err := driver.Wait(ctx, request, result); err != nil || !wait.Done {
		t.Fatalf("wait = %+v err=%v", wait, err)
	}
	if got := strings.Join(targets, ","); got != "DescribeCertificateAuthority,DescribeCertificateAuthority,UpdateCertificateAuthority,DescribeCertificateAuthority,DeleteCertificateAuthority,DescribeCertificateAuthority" {
		t.Fatalf("targets = %s", got)
	}
}

func TestProvisionedThroughputWithAnOpenCommitmentIsNotDeleted(t *testing.T) {
	now := time.Date(2026, 9, 26, 0, 0, 0, 0, time.UTC)
	if ok, _ := provisionedModelDeletable(map[string]any{}, now); !ok {
		t.Fatal("no-commitment throughput was blocked")
	}
	if ok, reason := provisionedModelDeletable(map[string]any{"CommitmentExpirationTime": "2027-03-26T00:00:00Z"}, now); ok || !strings.Contains(reason, "commitment") {
		t.Fatalf("open commitment was allowed: %s", reason)
	}
	if ok, _ := provisionedModelDeletable(map[string]any{"CommitmentExpirationTime": "2026-03-26T00:00:00Z"}, now); !ok {
		t.Fatal("ended commitment was blocked")
	}
}

// Cognito uses awsJson1_1. Domains are read from each user pool and deleted
// with the pool ID they belong to.
func TestCognitoDomainProtocolListsPoolDomainsAndDeletesThem(t *testing.T) {
	var mu sync.Mutex
	deleted := false
	runtime := protocolNativeRuntime(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		target := strings.TrimPrefix(r.Header.Get("X-Amz-Target"), "AWSCognitoIdentityProviderService.")
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/x-amz-json-1.1")
		switch target {
		case "ListUserPools":
			_, _ = io.WriteString(w, `{"UserPools":[{"Id":"us-east-1_A","Name":"users"},{"Id":"us-east-1_B","Name":"staff"}]}`)
		case "DescribeUserPool":
			if body["UserPoolId"] == "us-east-1_A" {
				_, _ = io.WriteString(w, `{"UserPool":{"Id":"us-east-1_A","Domain":"login-a","CustomDomain":"auth.example.com"}}`)
				return
			}
			_, _ = io.WriteString(w, `{"UserPool":{"Id":"us-east-1_B"}}`)
		case "DescribeUserPoolDomain":
			if deleted {
				_, _ = io.WriteString(w, `{"DomainDescription":{}}`)
				return
			}
			payload, _ := json.Marshal(map[string]any{"DomainDescription": map[string]any{"Domain": body["Domain"], "UserPoolId": "us-east-1_A", "Status": "ACTIVE"}})
			_, _ = w.Write(payload)
		case "DeleteUserPoolDomain":
			if body["Domain"] != "login-a" || body["UserPoolId"] != "us-east-1_A" {
				t.Errorf("delete body = %v", body)
			}
			deleted = true
			_, _ = io.WriteString(w, `{}`)
		default:
			t.Errorf("unexpected target %s", target)
		}
	})
	ctx := context.Background()
	kind := runtime.resourceKind(cognitoUserPoolDomain, asset.ScopeRegion)
	batch, err := runtime.List(ctx, contracts.InventoryRequest{
		ConnectionID: "connection-protocol", Source: productAPISource, ResourceKind: &kind,
		Scope: asset.Scope{Kind: asset.ScopeRegion, NativeID: "us-east-1", Location: "us-east-1"},
	})
	if err != nil || len(batch.Items) != 2 || batch.Items[0].NativeID != "auth.example.com" || batch.Items[1].NativeID != "login-a" || batch.Items[1].Normalized["UserPoolId"] != "us-east-1_A" {
		t.Fatalf("batch = %+v err=%v", batch, err)
	}
	request := nativeRequest(cognitoUserPoolDomain, "login-a")
	driver, err := runtime.ResolveAction(ctx, "connection-protocol", request.Asset)
	if err != nil {
		t.Fatal(err)
	}
	result, err := driver.Execute(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if wait, err := driver.Wait(ctx, request, result); err != nil || !wait.Done || !deleted {
		t.Fatalf("wait = %+v err=%v", wait, err)
	}
}

type fakeSecretsAPI map[string]*awssecrets.DescribeSecretOutput

func (f fakeSecretsAPI) DescribeSecret(_ context.Context, input *awssecrets.DescribeSecretInput, _ ...func(*awssecrets.Options)) (*awssecrets.DescribeSecretOutput, error) {
	if output, ok := f[awssdk.ToString(input.SecretId)]; ok {
		return output, nil
	}
	return nil, &secretstypes.ResourceNotFoundException{Message: awssdk.String("missing")}
}

// Secrets that another service owns, and replicas of a secret whose primary
// is in another Region, are removed by their owner or primary.
func TestSecretsOwnedByServicesOrReplicatedAreProtected(t *testing.T) {
	api := fakeSecretsAPI{
		"own":     {},
		"rds":     {OwningService: awssdk.String("rds")},
		"replica": {PrimaryRegion: awssdk.String("eu-west-1")},
		"primary": {PrimaryRegion: awssdk.String("us-east-1")},
	}
	var items []contracts.InventoryItem
	for _, id := range []string{"own", "rds", "replica", "primary", "gone"} {
		items = append(items, contracts.InventoryItem{NativeType: secretType, NativeID: id, Location: "us-east-1", Normalized: map[string]any{}})
	}
	if err := enrichLifecycleFacts(context.Background(), &NativeClients{Secrets: api}, items); err != nil {
		t.Fatal(err)
	}
	for index, protected := range []bool{false, true, true, false, false} {
		if (items[index].Actionable != nil && !*items[index].Actionable) != protected {
			t.Errorf("%s actionable = %v", items[index].NativeID, items[index].Actionable)
		}
	}
}

func TestCognitoUserPoolDomainsPrecedeTheirPoolAndCodeBuildUsesItsRole(t *testing.T) {
	role := awsAsset("role", "AWS::IAM::Role", "codebuild-service", nil)
	role.Location = ""
	contribution, err := NewLifecycle().Contribute(context.Background(), "scope", []asset.Asset{
		awsAsset("pool", "AWS::Cognito::UserPool", "us-east-1_A", nil),
		awsAsset("domain", cognitoUserPoolDomain, "login-a", map[string]any{"UserPoolId": "us-east-1_A"}),
		awsAsset("project", codeBuildProjectType, "build", map[string]any{"service_role_name": "codebuild-service"}),
		role,
	})
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	for _, relationship := range contribution.Relationships {
		found[string(relationship.SourceAssetID)+">"+string(relationship.TargetAssetID)+":"+string(relationship.Type)] = true
	}
	if !found["pool>domain:depends_on"] || !found["project>role:uses"] {
		t.Fatalf("relationships = %v", found)
	}
	if iamName("arn:aws:iam::123456789012:role/service-role/codebuild-service") != "codebuild-service" {
		t.Fatal("service role name")
	}
}

func TestSecretGuardTreatsScheduledDeletionAsDeletedAndBlocksOwnedSecrets(t *testing.T) {
	deleted := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	api := fakeSecretsAPI{"scheduled": {DeletedDate: &deleted}, "rds": {OwningService: awssdk.String("rds")}, "replica": {PrimaryRegion: awssdk.String("eu-west-1")}, "own": {}}
	guard := secretGuard(api, "us-east-1")
	for id, want := range map[string]string{"scheduled": "pending", "gone": "pending", "rds": "blocked", "replica": "blocked", "own": "allowed"} {
		outcome, err := guard(context.Background(), id)
		got := "allowed"
		if outcome.pending {
			got = "pending"
		} else if outcome.blocked != "" {
			got = "blocked"
		}
		if err != nil || got != want {
			t.Errorf("%s = %s, %v", id, got, err)
		}
	}
}
