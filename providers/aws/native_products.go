package aws

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	awsacmpca "github.com/aws/aws-sdk-go-v2/service/acmpca"
	pcatypes "github.com/aws/aws-sdk-go-v2/service/acmpca/types"
	awsbedrock "github.com/aws/aws-sdk-go-v2/service/bedrock"
	awscodebuild "github.com/aws/aws-sdk-go-v2/service/codebuild"
	awscognito "github.com/aws/aws-sdk-go-v2/service/cognitoidentityprovider"
	awssecrets "github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

type PrivateCANativeAPI interface {
	ListCertificateAuthorities(context.Context, *awsacmpca.ListCertificateAuthoritiesInput, ...func(*awsacmpca.Options)) (*awsacmpca.ListCertificateAuthoritiesOutput, error)
	DescribeCertificateAuthority(context.Context, *awsacmpca.DescribeCertificateAuthorityInput, ...func(*awsacmpca.Options)) (*awsacmpca.DescribeCertificateAuthorityOutput, error)
	UpdateCertificateAuthority(context.Context, *awsacmpca.UpdateCertificateAuthorityInput, ...func(*awsacmpca.Options)) (*awsacmpca.UpdateCertificateAuthorityOutput, error)
	DeleteCertificateAuthority(context.Context, *awsacmpca.DeleteCertificateAuthorityInput, ...func(*awsacmpca.Options)) (*awsacmpca.DeleteCertificateAuthorityOutput, error)
}

type BedrockNativeAPI interface {
	ListProvisionedModelThroughputs(context.Context, *awsbedrock.ListProvisionedModelThroughputsInput, ...func(*awsbedrock.Options)) (*awsbedrock.ListProvisionedModelThroughputsOutput, error)
	GetProvisionedModelThroughput(context.Context, *awsbedrock.GetProvisionedModelThroughputInput, ...func(*awsbedrock.Options)) (*awsbedrock.GetProvisionedModelThroughputOutput, error)
	DeleteProvisionedModelThroughput(context.Context, *awsbedrock.DeleteProvisionedModelThroughputInput, ...func(*awsbedrock.Options)) (*awsbedrock.DeleteProvisionedModelThroughputOutput, error)
}

type CodeBuildNativeAPI interface {
	ListProjects(context.Context, *awscodebuild.ListProjectsInput, ...func(*awscodebuild.Options)) (*awscodebuild.ListProjectsOutput, error)
	BatchGetProjects(context.Context, *awscodebuild.BatchGetProjectsInput, ...func(*awscodebuild.Options)) (*awscodebuild.BatchGetProjectsOutput, error)
	DeleteProject(context.Context, *awscodebuild.DeleteProjectInput, ...func(*awscodebuild.Options)) (*awscodebuild.DeleteProjectOutput, error)
}

type CognitoNativeAPI interface {
	ListUserPools(context.Context, *awscognito.ListUserPoolsInput, ...func(*awscognito.Options)) (*awscognito.ListUserPoolsOutput, error)
	DescribeUserPool(context.Context, *awscognito.DescribeUserPoolInput, ...func(*awscognito.Options)) (*awscognito.DescribeUserPoolOutput, error)
	DescribeUserPoolDomain(context.Context, *awscognito.DescribeUserPoolDomainInput, ...func(*awscognito.Options)) (*awscognito.DescribeUserPoolDomainOutput, error)
	DeleteUserPoolDomain(context.Context, *awscognito.DeleteUserPoolDomainInput, ...func(*awscognito.Options)) (*awscognito.DeleteUserPoolDomainOutput, error)
}

type SecretsNativeAPI interface {
	DescribeSecret(context.Context, *awssecrets.DescribeSecretInput, ...func(*awssecrets.Options)) (*awssecrets.DescribeSecretOutput, error)
}

const (
	privateCAType          = "AWS::ACMPCA::CertificateAuthority"
	provisionedModelType   = "AWS::Bedrock::ProvisionedModelThroughput"
	codeBuildProjectType   = "AWS::CodeBuild::Project"
	cognitoUserPoolDomain  = "AWS::Cognito::UserPoolDomain"
	secretType             = "AWS::SecretsManager::Secret"
	codeBuildBatchSize     = 100
	cognitoUserPoolPage    = 60
	secretsManagedByReason = "secret_managed_by_service"
)

// productKindsWithCloudControlHandlers use their product API although the
// official schema has Cloud Control list and delete handlers, because the
// Cloud Control handler cannot make a required pre-delete change.
var productKindsWithCloudControlHandlers = map[string]string{
	// DeleteCertificateAuthority requires an ACTIVE CA to be DISABLED first;
	// the Cloud Control model has no status to change.
	privateCAType: "an active private CA must be disabled before deletion",
}

func init() {
	for _, kind := range []nativeKind{privateCAKind, provisionedModelKind, codeBuildProjectKind, cognitoDomainKind} {
		nativeKinds[kind.nativeType] = kind
	}
}

var privateCAKind = nativeKind{
	nativeType: privateCAType, service: "acm-pca", identity: "Arn", statePath: "Status",
	listOperation: "com.amazonaws.acmpca#ListCertificateAuthorities", readOperation: "com.amazonaws.acmpca#DescribeCertificateAuthority", deleteOperation: "com.amazonaws.acmpca#DeleteCertificateAuthority",
	// A deleted CA can be restored until its permanent deletion date.
	absentStates: []string{string(pcatypes.CertificateAuthorityStatusDeleted)}, failedStates: []string{string(pcatypes.CertificateAuthorityStatusFailed)},
	list: func(ctx context.Context, c *NativeClients, token string) (nativePage, error) {
		input := &awsacmpca.ListCertificateAuthoritiesInput{MaxResults: awssdk.Int32(1000), ResourceOwner: pcatypes.ResourceOwnerSelf}
		if token != "" {
			input.NextToken = awssdk.String(token)
		}
		output, err := c.PrivateCA.ListCertificateAuthorities(ctx, input)
		if err != nil {
			return nativePage{}, err
		}
		documents, err := nativeDocuments(output.CertificateAuthorities)
		items := documents[:0]
		for _, document := range documents {
			if stringValue(document["Status"]) != string(pcatypes.CertificateAuthorityStatusDeleted) {
				items = append(items, document)
			}
		}
		return nativePage{Items: items, NextToken: awssdk.ToString(output.NextToken), RequestID: requestIDOf(output.ResultMetadata)}, err
	},
	read: func(ctx context.Context, c *NativeClients, id string) (map[string]any, string, error) {
		output, err := c.PrivateCA.DescribeCertificateAuthority(ctx, &awsacmpca.DescribeCertificateAuthorityInput{CertificateAuthorityArn: awssdk.String(id)})
		if nativeNotFound(err, "ResourceNotFoundException") {
			return nil, requestIDFromNormalized(NormalizeError(err)), errNativeAbsent
		}
		if err != nil {
			return nil, requestIDFromNormalized(NormalizeError(err)), err
		}
		if output.CertificateAuthority == nil || awssdk.ToString(output.CertificateAuthority.Arn) != id {
			return nil, requestIDOf(output.ResultMetadata), errNativeAbsent
		}
		document, err := nativeDocument(output.CertificateAuthority)
		return document, requestIDOf(output.ResultMetadata), err
	},
	remove: func(ctx context.Context, c *NativeClients, id string, _ map[string]any, _ string) (string, error) {
		// The default 30-day restore period is kept.
		output, err := c.PrivateCA.DeleteCertificateAuthority(ctx, &awsacmpca.DeleteCertificateAuthorityInput{CertificateAuthorityArn: awssdk.String(id)})
		if err != nil {
			return removed("", err)
		}
		return requestIDOf(output.ResultMetadata), nil
	},
	protection: &nativeProtection{
		operation: "com.amazonaws.acmpca#UpdateCertificateAuthority",
		enabled: func(model map[string]any) bool {
			return stringValue(model["Status"]) == string(pcatypes.CertificateAuthorityStatusActive)
		},
		disable: func(ctx context.Context, c *NativeClients, id string) (string, error) {
			output, err := c.PrivateCA.UpdateCertificateAuthority(ctx, &awsacmpca.UpdateCertificateAuthorityInput{CertificateAuthorityArn: awssdk.String(id), Status: pcatypes.CertificateAuthorityStatusDisabled})
			if err != nil {
				return removed("", err)
			}
			return requestIDOf(output.ResultMetadata), nil
		},
	},
}

var provisionedModelKind = nativeKind{
	nativeType: provisionedModelType, service: "bedrock", identity: "ProvisionedModelArn", nameField: "ProvisionedModelName", statePath: "Status",
	listOperation: "com.amazonaws.bedrock#ListProvisionedModelThroughputs", readOperation: "com.amazonaws.bedrock#GetProvisionedModelThroughput", deleteOperation: "com.amazonaws.bedrock#DeleteProvisionedModelThroughput",
	failedStates: []string{"Failed"},
	list: func(ctx context.Context, c *NativeClients, token string) (nativePage, error) {
		input := &awsbedrock.ListProvisionedModelThroughputsInput{MaxResults: awssdk.Int32(1000)}
		if token != "" {
			input.NextToken = awssdk.String(token)
		}
		output, err := c.Bedrock.ListProvisionedModelThroughputs(ctx, input)
		if err != nil {
			return nativePage{}, err
		}
		items, err := nativeDocuments(output.ProvisionedModelSummaries)
		return nativePage{Items: items, NextToken: awssdk.ToString(output.NextToken), RequestID: requestIDOf(output.ResultMetadata)}, err
	},
	read: func(ctx context.Context, c *NativeClients, id string) (map[string]any, string, error) {
		output, err := c.Bedrock.GetProvisionedModelThroughput(ctx, &awsbedrock.GetProvisionedModelThroughputInput{ProvisionedModelId: awssdk.String(id)})
		if nativeNotFound(err, "ResourceNotFoundException") {
			return nil, requestIDFromNormalized(NormalizeError(err)), errNativeAbsent
		}
		if err != nil {
			return nil, requestIDFromNormalized(NormalizeError(err)), err
		}
		if awssdk.ToString(output.ProvisionedModelArn) != id {
			return nil, requestIDOf(output.ResultMetadata), errNativeAbsent
		}
		document, err := nativeDocument(output)
		delete(document, "ResultMetadata")
		return document, requestIDOf(output.ResultMetadata), err
	},
	remove: func(ctx context.Context, c *NativeClients, id string, _ map[string]any, _ string) (string, error) {
		output, err := c.Bedrock.DeleteProvisionedModelThroughput(ctx, &awsbedrock.DeleteProvisionedModelThroughputInput{ProvisionedModelId: awssdk.String(id)})
		if err != nil {
			return removed("", err)
		}
		return requestIDOf(output.ResultMetadata), nil
	},
	precondition: func(model map[string]any) (bool, string) {
		return provisionedModelDeletable(model, time.Now())
	},
}

// A Provisioned Throughput with a commitment term cannot be deleted before
// the term ends; it keeps billing until then.
func provisionedModelDeletable(model map[string]any, now time.Time) (bool, string) {
	expiration := strings.TrimSpace(stringValue(model["CommitmentExpirationTime"]))
	if expiration == "" {
		return true, ""
	}
	end, err := time.Parse(time.RFC3339Nano, expiration)
	if err != nil || now.Before(end) {
		return false, "The Provisioned Throughput's commitment term has not ended; it can be deleted after " + expiration + "."
	}
	return true, ""
}

func codeBuildProjects(ctx context.Context, c *NativeClients, names []string) ([]map[string]any, string, error) {
	output, err := c.CodeBuild.BatchGetProjects(ctx, &awscodebuild.BatchGetProjectsInput{Names: names})
	if err != nil {
		return nil, requestIDFromNormalized(NormalizeError(err)), err
	}
	documents, err := nativeDocuments(output.Projects)
	return documents, requestIDOf(output.ResultMetadata), err
}

var codeBuildProjectKind = nativeKind{
	nativeType: codeBuildProjectType, service: "codebuild", identity: "Name", nameField: "Name",
	listOperation: "com.amazonaws.codebuild#ListProjects", readOperation: "com.amazonaws.codebuild#BatchGetProjects", deleteOperation: "com.amazonaws.codebuild#DeleteProject",
	list: func(ctx context.Context, c *NativeClients, token string) (nativePage, error) {
		input := &awscodebuild.ListProjectsInput{}
		if token != "" {
			input.NextToken = awssdk.String(token)
		}
		output, err := c.CodeBuild.ListProjects(ctx, input)
		if err != nil {
			return nativePage{}, err
		}
		// ListProjects returns names only; the details carry the service role
		// and network configuration.
		var items []map[string]any
		for start := 0; start < len(output.Projects); start += codeBuildBatchSize {
			documents, _, err := codeBuildProjects(ctx, c, output.Projects[start:min(start+codeBuildBatchSize, len(output.Projects))])
			if err != nil {
				return nativePage{}, err
			}
			items = append(items, documents...)
		}
		return nativePage{Items: items, NextToken: awssdk.ToString(output.NextToken), RequestID: requestIDOf(output.ResultMetadata)}, nil
	},
	read: func(ctx context.Context, c *NativeClients, id string) (map[string]any, string, error) {
		documents, requestID, err := codeBuildProjects(ctx, c, []string{id})
		if err != nil {
			return nil, requestID, err
		}
		return singleDocument(documents, "Name", id, requestID)
	},
	remove: func(ctx context.Context, c *NativeClients, id string, _ map[string]any, _ string) (string, error) {
		output, err := c.CodeBuild.DeleteProject(ctx, &awscodebuild.DeleteProjectInput{Name: awssdk.String(id)})
		if err != nil {
			return removed("", err)
		}
		return requestIDOf(output.ResultMetadata), nil
	},
}

// A user pool has at most one prefix domain and one custom domain. Neither is
// returned by a list API, so each user pool is read for its domains.
var cognitoDomainKind = nativeKind{
	nativeType: cognitoUserPoolDomain, service: "cognito-idp", identity: "Domain", nameField: "Domain", statePath: "Status",
	listOperation: "com.amazonaws.cognitoidentityprovider#DescribeUserPool", readOperation: "com.amazonaws.cognitoidentityprovider#DescribeUserPoolDomain", deleteOperation: "com.amazonaws.cognitoidentityprovider#DeleteUserPoolDomain",
	deletingStates: []string{"DELETING"}, failedStates: []string{"FAILED"},
	list: func(ctx context.Context, c *NativeClients, _ string) (nativePage, error) {
		var pools []string
		token := ""
		for pages := 0; ; pages++ {
			if pages >= nativeMaxParentPages {
				return nativePage{}, fmt.Errorf("AWS Cognito user pool listing exceeded %d pages", nativeMaxParentPages)
			}
			input := &awscognito.ListUserPoolsInput{MaxResults: awssdk.Int32(cognitoUserPoolPage)}
			if token != "" {
				input.NextToken = awssdk.String(token)
			}
			output, err := c.Cognito.ListUserPools(ctx, input)
			if err != nil {
				return nativePage{}, err
			}
			for _, pool := range output.UserPools {
				pools = append(pools, awssdk.ToString(pool.Id))
			}
			next := awssdk.ToString(output.NextToken)
			if next == "" || next == token {
				break
			}
			token = next
		}
		sort.Strings(pools)
		var items []map[string]any
		requestID := ""
		for _, pool := range pools {
			output, err := c.Cognito.DescribeUserPool(ctx, &awscognito.DescribeUserPoolInput{UserPoolId: awssdk.String(pool)})
			if nativeNotFound(err, "ResourceNotFoundException") {
				continue
			}
			if err != nil {
				return nativePage{}, err
			}
			requestID = requestIDOf(output.ResultMetadata)
			if output.UserPool == nil {
				continue
			}
			for kind, domain := range map[string]*string{"prefix": output.UserPool.Domain, "custom": output.UserPool.CustomDomain} {
				if name := awssdk.ToString(domain); name != "" {
					items = append(items, map[string]any{"Domain": name, "UserPoolId": pool, "DomainKind": kind})
				}
			}
		}
		sort.Slice(items, func(i, j int) bool { return stringValue(items[i]["Domain"]) < stringValue(items[j]["Domain"]) })
		return nativePage{Items: items, RequestID: requestID}, nil
	},
	read: func(ctx context.Context, c *NativeClients, id string) (map[string]any, string, error) {
		output, err := c.Cognito.DescribeUserPoolDomain(ctx, &awscognito.DescribeUserPoolDomainInput{Domain: awssdk.String(id)})
		if nativeNotFound(err, "ResourceNotFoundException") {
			return nil, requestIDFromNormalized(NormalizeError(err)), errNativeAbsent
		}
		if err != nil {
			return nil, requestIDFromNormalized(NormalizeError(err)), err
		}
		// An unknown domain returns an empty description.
		if output.DomainDescription == nil || awssdk.ToString(output.DomainDescription.Domain) != id {
			return nil, requestIDOf(output.ResultMetadata), errNativeAbsent
		}
		document, err := nativeDocument(output.DomainDescription)
		return document, requestIDOf(output.ResultMetadata), err
	},
	remove: func(ctx context.Context, c *NativeClients, id string, model map[string]any, _ string) (string, error) {
		pool := stringValue(model["UserPoolId"])
		if pool == "" {
			return "", fmt.Errorf("AWS Cognito domain %s has no user pool", id)
		}
		output, err := c.Cognito.DeleteUserPoolDomain(ctx, &awscognito.DeleteUserPoolDomainInput{Domain: awssdk.String(id), UserPoolId: awssdk.String(pool)})
		if err != nil {
			return removed("", err)
		}
		return requestIDOf(output.ResultMetadata), nil
	},
}

// enrichSecrets adds the owning service and primary Region, which the Cloud
// Control model omits. Secrets that RDS, Redshift and other services manage,
// and replicas of a secret in another Region, are removed by their owner or
// primary secret.
func enrichSecrets(ctx context.Context, client SecretsNativeAPI, region string, items []contracts.InventoryItem, indexes []int) error {
	for _, index := range indexes {
		id := items[index].NativeID
		execution.LogCloudAPIRequest(ctx, "secretsmanager", "DescribeSecret", rawCloudPayload(map[string]any{"SecretId": id}))
		output, err := client.DescribeSecret(ctx, &awssecrets.DescribeSecretInput{SecretId: awssdk.String(id)})
		if err != nil {
			execution.LogCloudAPIFailure(ctx, "secretsmanager", "DescribeSecret", err)
			if nativeNotFound(err, "ResourceNotFoundException") {
				continue
			}
			return NormalizeError(err)
		}
		owner, primary := strings.TrimSpace(awssdk.ToString(output.OwningService)), strings.TrimSpace(awssdk.ToString(output.PrimaryRegion))
		items[index].Normalized["owning_service"] = owner
		items[index].Normalized["primary_region"] = primary
		reason := ""
		if owner != "" {
			reason = secretsManagedByReason
		} else if primary != "" && region != "" && primary != region {
			reason = "secret_replica"
		}
		if reason != "" {
			actionable := false
			items[index].Actionable = &actionable
			items[index].Normalized["cleanup_protection_reason"] = reason
		}
	}
	return nil
}
