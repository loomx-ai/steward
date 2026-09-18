---
title: "Amazon Web Services (AWS)"
description: "Connect an AWS account, configure discovery permissions, and review resource, lifecycle, and CloudFormation cleanup effects."
navTitle: "AWS"
---

Steward accesses one AWS account with the current connection's credentials. Each supported resource type has one authoritative discovery source: Cloud Control API for types with list, read, and delete handlers, or the service's own API for types Cloud Control does not fully support. Resource Explorer adds a broad index of other resources; it never removes resources observed by the authoritative sources.

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
| Cloud Control inventory and details | `cloudformation:ListResources`, `cloudformation:GetResource`, and the service reads required by each resource type's handlers |
| Attachment and membership facts | `ec2:DescribeInstances`, `ec2:DescribeNetworkInterfaces`, `ec2:DescribeVolumes`, `autoscaling:DescribeAutoScalingGroups`, `eks:DescribeNodegroup` |
| Service API inventory | `ec2:DescribeImages`, `ec2:DescribeSnapshots`, `es:ListDomainNames`, `es:DescribeDomains`, `rds:DescribeDBClusters`, `rds:DescribeDBInstances` (DocumentDB), `dms:DescribeReplicationInstances`, `fsx:DescribeFileSystems`, `storagegateway:ListGateways`, `storagegateway:DescribeGatewayInformation`, `route53domains:ListDomains`, `drs:DescribeSourceServers`, `mobiletargeting:ListTemplates`, `mobiletargeting:GetSmsTemplate` |
| Organization tree | `organizations:ListRoots`, `organizations:ListOrganizationalUnitsForParent` |
| Resource Explorer index | `resource-explorer-2:Search`; Steward calls `ListResources`, which uses the Search permission |
| CloudFormation stack details and ownership | `cloudformation:DescribeStacks`, `cloudformation:ListStackResources`, `cloudformation:GetTemplate` |

Cloud Control actions use the `cloudformation:` IAM prefix. Its generic read actions do not replace the underlying EC2, S3, RDS, or other service permissions. Check the [resource operation and handler requirements](https://docs.aws.amazon.com/cloudcontrolapi/latest/userguide/resource-operations.html). Only grant the rows for services you use; a denied type fails its own scan item without affecting other types.

Resource Explorer must return resources in the queried region. Steward uses that region's default view, so view filters affect visibility. See [Resource Explorer setup](https://docs.aws.amazon.com/resource-explorer/latest/userguide/getting-started-setting-up.html) and [ListResources permissions](https://docs.aws.amazon.com/resource-explorer/latest/apireference/API_ListResources.html).

## Run your first scan

1. Add and validate the connection, then confirm the AWS account identity. A useful example name is “AWS · Test”.
2. Refresh regions. Automatic discovery includes enabled regions; enable additional regions in AWS before refreshing again.
3. Start in a region containing a known resource, such as `us-east-1`, or select a known VPC.
4. Run the scan and inspect failed items. The total resource count alone does not establish success.
5. Search **Resources** for a known instance, bucket, or stack. Confirm its account, region, and identifier. Include the global scope for global resources such as IAM identities.

**Success check:** Expected resources are present under the correct account and region, with no unresolved permission or type failures. Continue to [resource relationships](./topology.md) or expand the scan scope.

## Resource coverage

| Category | Resource types |
| --- | --- |
| Compute | EC2 instances, AMIs, launch templates, Dedicated Hosts, capacity reservations and fleets, Auto Scaling groups, Lightsail instances, key pairs, Instance Connect endpoints |
| Containers and serverless | EKS clusters, managed node groups, Fargate profiles and add-ons; ECS clusters, services and task definitions; ECR repositories and pull-through cache rules; Lambda; App Runner |
| Networking | VPCs, subnets, CIDR blocks, route tables, security groups, network ACLs, interfaces, Elastic IPs, Internet, egress-only, NAT and virtual private gateways, customer gateways, VPN connections, VPC peering, endpoints and endpoint services, flow logs, DHCP option sets |
| Transit and hybrid networking | Transit gateways with route tables, VPC, peering and Connect attachments, multicast domains, associations, members and sources; Direct Connect connections, LAGs, gateways, associations and virtual interfaces; Cloud WAN global and core networks; Global Accelerator accelerators, listeners and endpoint groups |
| Load balancing, edge, and DNS | Application, Network and Gateway Load Balancers with listeners and target groups, Classic Load Balancers, CloudFront distributions, WAF web ACLs, Shield Advanced protections, Route 53 hosted zones, health checks, Resolver rules and registered domains, API Gateway APIs and custom domains, VPC Lattice |
| Storage and backup | S3 buckets, EBS volumes and snapshots, Data Lifecycle Manager policies, EFS file systems, mount targets and access points, FSx file systems, Storage Gateway, AWS Backup vaults, plans and selections, Elastic Disaster Recovery source servers |
| Databases and analytics | RDS and Aurora, RDS Proxy, Aurora DSQL, DynamoDB, DocumentDB and DocumentDB Elastic, Neptune and Neptune Analytics, Keyspaces, ElastiCache, MemoryDB, Timestream, OpenSearch Service and Serverless, Redshift and Redshift Serverless, Glue databases, Athena workgroups, EMR Serverless, Kinesis, Managed Service for Apache Flink, DMS, MSK, Amazon MQ, DataZone, QuickSight dashboards and datasets |
| Messaging and applications | SQS, SNS, EventBridge buses and rules, Step Functions, CodePipeline, Cloud Map namespaces and services, AppRegistry applications, Pinpoint SMS templates, IVS channels and stages, Kendra indexes, SageMaker endpoints, configurations, models and HyperPod clusters, AWS PCS and Batch compute environments |
| Identity, security, and governance | IAM users, groups, roles, instance profiles and managed policies, IAM Identity Center instances and groups, Organizations, organizational units and member accounts, KMS keys and aliases, ACM certificates, GuardDuty, Security Hub, Macie, Network Firewall, IAM Access Analyzer, CloudTrail trails and event data stores |
| Monitoring and orchestration | CloudWatch alarms, dashboards and log groups, Synthetics canaries, X-Ray groups, Observability Access Manager, Managed Grafana and Prometheus, CloudFormation stacks and StackSets |

Child types are listed through their parent: EKS node groups, add-ons, and Fargate profiles through each cluster; load balancer listeners through each load balancer; EFS mount targets through each file system; multicast associations, members, and sources through each domain; Identity Center groups through each instance; and organizational units through the complete organization tree. A failure to read the parent fails the child's scan item instead of reporting an empty list.

Global services are read from their home region: IAM, CloudFront, Route 53, Organizations, and Shield Advanced from `us-east-1`; Global Accelerator and Cloud WAN from `us-west-2`. Include the global scope to scan them. CloudFront-scoped WAF web ACLs are listed in the `us-east-1` region scan.

## Relationships and ownership

Relationships come from the resource model, such as a resource's VPC, subnets, and security groups, and from attachment facts read from the service APIs:

- **Instance storage and interfaces:** an EBS volume or network interface with `DeleteOnTermination` is managed by its instance. Terminating the instance deletes it. Other attached volumes are deleted after the instance.
- **Auto Scaling and EKS:** instances of an Auto Scaling group and the groups of an EKS managed node group are managed by their controller and can only be cleaned up through it.
- **Service-managed interfaces:** interfaces created by NAT gateways, VPC endpoints, load balancers, EFS mount targets, or Lambda belong to that service. They cannot be deleted directly.
- **Elastic IPs:** an address is released only after the NAT gateway or instance using it is gone.
- **CloudFormation:** Steward reads stack resources and the processed template to identify ownership and `DeletionPolicy`. Resources with `Retain` or `RetainExceptOnCreate` use retention relationships; termination-protected stacks block deletion.

## Review cleanup effects

Cleanup requires `cloudformation:DeleteResource`, `cloudformation:GetResourceRequestStatus`, and the target type's service deletion and read permissions. Service API types use their own delete actions, such as `ec2:DeregisterImage`, `ec2:DeleteSnapshot`, `es:DeleteDomain`, `rds:DeleteDBCluster`, `dms:DeleteReplicationInstance`, `fsx:DeleteFileSystem`, `storagegateway:DeleteGateway`, `route53domains:DeleteDomain`, `drs:DeleteSourceServer`, and `mobiletargeting:DeleteSmsTemplate`.

The plan shows what each deletion removes and what it keeps:

- **Retaining attachments:** to keep a volume or interface that its instance would delete, retain it in the plan. Before termination, Steward sets `DeleteOnTermination` to false, reads the change back, and after termination confirms that retained resources still exist and deleted ones are gone. This needs `ec2:ModifyInstanceAttribute` and `ec2:ModifyNetworkInterfaceAttribute`. If attachments changed after the plan was reviewed, execution stops.
- **Deletion protection:** protection on EC2 instances, RDS and Aurora, DynamoDB, EKS clusters, load balancers, log groups, Neptune, Network Firewall, Aurora DSQL, CloudTrail event data stores, Auto Scaling groups, DocumentDB clusters, and AMIs is turned off before deletion, then read back. This needs `cloudformation:UpdateResource` and the service's modify permission, such as `rds:ModifyDBCluster` or `ec2:DisableImageDeregistrationProtection`.
- **Preconditions:** Internet and virtual private gateways are detached from their VPC first. DocumentDB clusters must have no member instances, and Elastic Disaster Recovery source servers must be disconnected. AWS managed KMS keys cannot be deleted. A customer managed key that a scanned resource still references by key ID, key ARN, alias or alias ARN is deleted only after that resource; if the resource is not in the task, the plan is blocked. When a key policy grants unconditional key administration only to scanned IAM users or roles, without delegating to the account root, the task cannot delete all of them while the key remains. Backup vaults must hold no recovery points and must not be locked in compliance mode; S3 buckets must hold no object versions or delete markers, even when versioning is suspended; Steward does not empty buckets. A scan marks a non-empty bucket as protected so the plan shows it, and the bucket is checked again right before deletion; after emptying a bucket, scan again. These checks need `kms:DescribeKey`, `backup:DescribeBackupVault` and `s3:ListBucketVersions`.
- **Service defaults:** DocumentDB clusters are deleted without a final snapshot. FSx file systems follow each type's default final-backup behavior. KMS keys enter their scheduled deletion window; a key already pending deletion is treated as deleted. Deleting a Route 53 domain registration cannot be undone and is only supported for some top-level domains.

Nonempty buckets and repositories, other dependencies, and changing resource states can still prevent an operation. Steward waits for asynchronous operations and reads back the result. An accepted request is not proof of deletion. Read [Clean up resources](./cleanup.md) before executing a plan.

## Differences from other clouds

AWS has no equivalent for some resources available on other clouds. Steward does not substitute an unrelated resource for them:

| Resource | AWS behavior |
| --- | --- |
| Bandwidth packages | Internet, inter-region, and Global Accelerator traffic is charged per GB; there is no bandwidth package resource. |
| Inter-region QoS and traffic marking policies | Transit Gateway and Cloud WAN have no QoS queue or DSCP marking policy resource. |
| Route maps | Transit Gateway has no route-map object; Cloud WAN routing policy is part of the core network policy. |
| Accelerator attachments | Amazon Elastic Inference was discontinued on April 15, 2024. |
| Dedicated block storage clusters and cloud phones | No corresponding AWS service is available. |
| Multi-cluster fleet control planes | EKS has no managed fleet controller resource. |

## Troubleshooting

| Symptom | What to check |
| --- | --- |
| `ExpiredToken` or validation failure | Verify matching keys, the Session Token, expiration, and account partition; replace the complete credential set. |
| A region is missing | Check region enablement and `ec2:DescribeRegions`. |
| Resource Explorer is unauthorized or returns no resources | Check the regional default view, its filters, Search permission, and index state. |
| Cloud Control returns `AccessDenied` | Check both the Cloud Control action and the resource handler's service permissions. |
| A child type fails while its parent succeeds | Check the child's list permission; for organizational units, check that the account is the organization's management account. |
| A type is unavailable in a region | Check AWS support for that type and region, and select the types you actually use. |
| An instance cleanup stops with an attachment mismatch | Rescan and review the plan again; volumes or interfaces changed after review. |
| A stack or resource cannot be deleted | Inspect termination protection, retention policy, dependencies, and operation errors before retrying. |

Next: [Scan resources](./scans.md) · [Query resources](./resources.md) · [Clean up resources](./cleanup.md)

For a coverage record you can verify across regions and connections, follow [Inventory AWS resources across regions](./tutorials/aws-resource-inventory.md).
