---
title: "Amazon Web Services (AWS)"
description: "Connect an AWS account, configure discovery permissions, and review resource and CloudFormation cleanup."
navTitle: "AWS"
---

Steward accesses one AWS account with the current connection's credentials. Discovery combines Cloud Control API with Resource Explorer; inventory can include read-only resources without cleanup support.

## Prepare credentials

Choose **AWS** in **Settings → Cloud connections → Add connection**.

| Credential type | Required fields | Usage |
| --- | --- | --- |
| Access key | Access Key ID and Secret Access Key | Access keys for a dedicated IAM identity. |
| Temporary credentials | Access Key ID, Secret Access Key, Session Token, and expiration | A complete credential set from an authorized session. |

Connections use the supplied credentials. They do not automatically load local AWS profiles, SSO sessions, or instance roles, and do not provide automatic AssumeRole refresh. Use **Replace credential** after temporary credentials expire, keeping the original cloud identity.

The current integration targets the commercial AWS partition. Identity partitions and endpoints for AWS China and GovCloud are not yet adapted.

## Configure discovery permissions

Connection validation identifies the account through STS `GetCallerIdentity`; region discovery uses EC2 `DescribeRegions`. A valid identity does not establish permission to read every resource.

| Purpose | IAM actions to check |
| --- | --- |
| Region and network selection | `ec2:DescribeRegions`, `ec2:DescribeVpcs`, `ec2:DescribeSubnets` |
| Resource Explorer discovery | `resource-explorer-2:Search`; Steward calls `ListResources`, which uses the Search permission. |
| Cloud Control inventory and details | `cloudformation:ListResources`, `cloudformation:GetResource`, and the service reads required by each resource type's handlers. |
| CloudFormation stack details and ownership | `cloudformation:DescribeStacks`, `cloudformation:ListStackResources`, `cloudformation:GetTemplate` |

Cloud Control actions use the `cloudformation:` IAM prefix. Its generic read actions do not replace the underlying EC2, S3, RDS, or other service permissions. Check the [resource operation and handler requirements](https://docs.aws.amazon.com/cloudcontrolapi/latest/userguide/resource-operations.html).

Resource Explorer must return resources in the queried region. Steward uses that region's default view, so view filters affect visibility. Have an administrator complete any required index, view, or service-linked role setup in AWS. See [Resource Explorer setup](https://docs.aws.amazon.com/resource-explorer/latest/userguide/getting-started-setting-up.html) and [ListResources permissions](https://docs.aws.amazon.com/resource-explorer/latest/apireference/API_ListResources.html).

## Run your first scan

1. Add and validate the connection, then confirm the AWS account identity. A useful example name is “AWS · Test”.
2. Refresh regions. Automatic discovery includes enabled regions; enable additional regions in AWS before refreshing again.
3. Start in a region containing a known resource, such as `us-east-1`, or select a known VPC.
4. Run the scan and inspect failed items. The total resource count alone does not establish success.
5. Search **Resources** for a known instance, bucket, or stack. Confirm its account, region, and identifier. Scan the global scope when you need global resources such as IAM identities.

**Success check:** Expected resources are present under the correct account and region, with no unresolved permission or type failures. Continue to [resource relationships](./topology.md) or expand the scan scope.

## Resource coverage and relationships

| Category | Common resource examples |
| --- | --- |
| Compute and networking | EC2 instances, EBS volumes, VPCs, subnets, security groups, interfaces, route tables, NAT and Internet Gateways |
| Storage and databases | S3 buckets, EFS, DynamoDB tables, RDS instances and clusters |
| Applications and containers | Lambda, ECS, EKS, ECR, load balancers, target groups, API Gateway |
| Messaging and workflows | SQS, SNS, EventBridge, Kinesis, Step Functions |
| Identity, monitoring, and orchestration | IAM identities and policies, CloudWatch, log groups, CloudFormation stacks |

Cloud Control reads integrated resource types and their properties; Resource Explorer discovers additional types. Being discoverable does not imply that a resource supports deletion in Steward. Check resource details and the cleanup plan for its supported actions.

EC2 subnets appear in the subnet layer of the resource panorama. Global resources such as IAM identities do not belong to a VPC. A VPC scan therefore does not establish complete account coverage.

## Review cleanup effects

Cleanup requires `cloudformation:DeleteResource`, `cloudformation:GetResourceRequestStatus`, and the target type's service deletion and read permissions. CloudFormation stacks use stack permissions such as `cloudformation:DeleteStack`. Internet Gateway preparation may also detach it from a VPC.

Review CloudFormation-managed resources together with their stack. Steward reads stack resources and templates to identify ownership and `DeletionPolicy`. Resources with `Retain` or `RetainExceptOnCreate` use retention relationships; termination-protected stacks block deletion. Resolve unknown ownership or policy findings before execution.

Provider deletion protection, nonempty buckets, dependencies, and changing resource states can still prevent an operation. Steward waits for asynchronous operations and reads back the result. An accepted request is not proof of deletion. Read [Clean up resources](./cleanup.md) before executing a plan.

## Troubleshooting

| Symptom | What to check |
| --- | --- |
| `ExpiredToken` or validation failure | Verify matching keys, the Session Token, expiration, and account partition; replace the complete credential set. |
| A region is missing | Check region enablement and `ec2:DescribeRegions`. |
| Resource Explorer is unauthorized or returns no resources | Check the regional default view, its filters, Search permission, and index state. |
| Cloud Control returns `AccessDenied` | Check both the Cloud Control action and the resource handler's service permissions. |
| A type is unavailable in a region | Check AWS support for that type and region, and select the types you actually use. |
| A stack or resource cannot be deleted | Inspect termination protection, retention policy, dependencies, and operation errors before retrying. |

Next: [Scan resources](./scans.md) · [Query resources](./resources.md) · [Clean up resources](./cleanup.md)
