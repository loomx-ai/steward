---
title: "Amazon Web Services (AWS)"
description: "Connect an AWS account, grant IAM permissions for inventory and cleanup, run a first scan, and check resource coverage, cleanup protections, and limits."
navTitle: "AWS"
---

Use this page to connect one AWS account to Steward, grant the IAM permissions it needs, and understand what it can find and clean up.

Steward reads one AWS account with the connection's credentials. Each supported resource type has one authoritative source: Cloud Control API for types with list, read, and delete handlers, or the service's own API for types Cloud Control does not fully support. Resource Explorer adds a broad index of other resources; it never removes resources found by the authoritative sources.

## Prepare credentials

Open the user menu → **Settings** → **Cloud connections** → **Add connection**, choose **AWS**, and pick a credential type:

| Credential type | Required fields | Use it for |
| --- | --- | --- |
| **AWS access key** | Access Key ID and Secret Access Key | A dedicated IAM identity. |
| **AWS session credential** | Access Key ID, Secret Access Key, Session Token, and expiration | A complete temporary credential set from an authorized session. |
| **IAM Identity Center** | Start URL and region; after sign-in, an account and role | [Browser sign-in](./connections.md#browser), renewed automatically. |
| **OIDC workload identity** | Role ARN; optional write role ARN | Temporary credentials without stored keys, when the server is configured for [OIDC](./oidc.md). |

- Steward uses only the credentials you enter. It does not load local AWS profiles, SSO sessions, or instance roles, and does not refresh credentials through AssumeRole.
- When session credentials expire, use **Replace credential**. The new credentials must belong to the same cloud identity.
- Steward supports the commercial AWS partition only. AWS China and GovCloud identities and endpoints are not supported yet.

**IAM Identity Center sign-in** runs Steward's own authorization instead of reading the AWS CLI's cached session. Steward registers a public client with your directory, signs you in through the browser, and exchanges the result for the selected role's temporary credentials, refreshing them as they expire. The connection can read whatever that role allows.

- The client registration expires after about 90 days; the connection then asks you to authorize again.
- The browser and the Steward server must run on the same machine.

<span id="configure-discovery-permissions"></span>

## Configure permissions

Validating a connection calls STS `GetCallerIdentity` to identify the account. A valid identity does not mean Steward can read every resource: grant the read permissions below, then run a scan to confirm.

### Read permissions for inventory

| Purpose | IAM actions |
| --- | --- |
| Regions and network selection | `ec2:DescribeRegions`, `ec2:DescribeVpcs`, `ec2:DescribeSubnets` |
| Cloud Control inventory and details | `cloudformation:ListResources`, `cloudformation:GetResource`, plus the service reads each resource type's handlers require |
| Attachments and membership | `ec2:DescribeInstances`, `ec2:DescribeNetworkInterfaces`, `ec2:DescribeVolumes`, `autoscaling:DescribeAutoScalingGroups`, `eks:DescribeNodegroup`, `config:DescribeConfigRules`, `config:DescribeConformancePackCompliance` |
| Service API inventory | `ec2:DescribeImages`, `ec2:DescribeSnapshots`, `es:ListDomainNames`, `es:DescribeDomains`, `rds:DescribeDBClusters`, `rds:DescribeDBInstances` (DocumentDB), `dms:DescribeReplicationInstances`, `fsx:DescribeFileSystems`, `storagegateway:ListGateways`, `storagegateway:DescribeGatewayInformation`, `route53domains:ListDomains`, `drs:DescribeSourceServers`, `mobiletargeting:ListTemplates`, `mobiletargeting:GetSmsTemplate`, `ec2:DescribeClientVpnEndpoints`, `ec2:DescribeClientVpnTargetNetworks`, `ec2:DescribeClientVpnAuthorizationRules`, `ec2:DescribeClientVpnRoutes`, `elasticmapreduce:ListClusters`, `elasticmapreduce:DescribeCluster`, `workspaces:DescribeWorkspaceDirectories`, `wafv2:ListWebACLs`, `wafv2:GetWebACL`, `wafv2:ListResourcesForWebACL`, `wafv2:GetWebACLForResource` |
| Organization tree | `organizations:ListRoots`, `organizations:ListOrganizationalUnitsForParent` |
| Resource Explorer index | `resource-explorer-2:Search` (Steward calls `ListResources`, which uses this permission) |
| CloudFormation stacks and ownership | `cloudformation:DescribeStacks`, `cloudformation:ListStackResources`, `cloudformation:GetTemplate` |

- **Cloud Control needs two layers.** Its actions use the `cloudformation:` prefix, but those generic actions do not replace the underlying EC2, S3, RDS, or other service permissions. Check each type's [resource handler requirements](https://docs.aws.amazon.com/cloudcontrolapi/latest/userguide/resource-operations.html).
- **MSK topics use Kafka data-plane permissions.** Listing and deleting topics calls the cluster with `kafka-cluster:Connect`, `kafka-cluster:DescribeTopic` and `kafka-cluster:DeleteTopic`, so the cluster must have IAM access control enabled; otherwise its topic scan item fails.
- **Grant only what you use.** A denied type fails its own scan item without affecting other types.
- **Resource Explorer** must return resources in the queried region. Steward uses that region's default view, so the view's filters limit what it sees. See [Resource Explorer setup](https://docs.aws.amazon.com/resource-explorer/latest/userguide/getting-started-setting-up.html) and [ListResources permissions](https://docs.aws.amazon.com/resource-explorer/latest/apireference/API_ListResources.html).

### Permissions for cleanup

Read permissions do not allow cleanup. Add these only for the types you plan to delete:

| Purpose | IAM actions |
| --- | --- |
| Delete through Cloud Control | `cloudformation:DeleteResource`, `cloudformation:GetResourceRequestStatus`, plus the target type's service delete and read permissions |
| Delete through service APIs | The type's own delete action, such as `ec2:DeregisterImage`, `ec2:DeleteSnapshot`, `es:DeleteDomain`, `rds:DeleteDBCluster`, `dms:DeleteReplicationInstance`, `fsx:DeleteFileSystem`, `storagegateway:DeleteGateway`, `route53domains:DeleteDomain`, `drs:DeleteSourceServer`, `mobiletargeting:DeleteSmsTemplate`, `ec2:DeleteClientVpnEndpoint`, `ec2:DisassociateClientVpnTargetNetwork`, `ec2:RevokeClientVpnIngress`, `ec2:DeleteClientVpnRoute`, `elasticmapreduce:TerminateJobFlows`, `workspaces:DeregisterWorkspaceDirectory`, `wafv2:DisassociateWebACL` (plus the protected service's permission, such as `elasticloadbalancing:SetWebACL`) |
| Keep volumes or interfaces when an instance is terminated | `ec2:ModifyInstanceAttribute`, `ec2:ModifyNetworkInterfaceAttribute` |
| Turn off deletion protection | `cloudformation:UpdateResource` plus the service's modify permission, such as `rds:ModifyDBCluster`, `ec2:DisableImageDeregistrationProtection` or `elasticmapreduce:SetTerminationProtection` |
| Pre-deletion checks for KMS keys, Backup vaults, and S3 buckets | `kms:DescribeKey`, `backup:DescribeBackupVault`, `s3:ListBucketVersions` |

## Run your first scan

Start small: one region with a resource you know, then expand.

1. Add the connection (for example, name it “AWS · Test”). Steward validates it before saving; confirm that the account ID it shows is the one you expect.
2. On the connection, open **Manage regions** → **Refresh from cloud API**. Steward lists the regions enabled in the account (through `ec2:DescribeRegions`). To scan another region, enable it in AWS first, then refresh again.
3. Go to **Scans** → **Start scan**. Choose **Selected regions** and pick a region with known resources, such as `us-east-1`, or choose **Selected VPCs / vSwitches** and pick a known VPC or subnet. Add **Global** if you want IAM and other [global resources](#global-services).
4. Open the scan and check for failed items. Fix permission errors, then select **Retry**.
5. In **Resources**, search for a known instance, bucket, or stack, and check its account, region, and ID.

**Result check:** the resources you expect appear under the right account and region, and the scan has no unresolved permission or type failures. A resource count alone does not prove the scan is complete. Next, [view relationships](./topology.md) or widen the scan scope.

For a coverage record you can verify across regions and connections, follow [Inventory AWS resources across regions](./tutorials/aws-resource-inventory.md).

## Resource coverage

| Category | Resource types |
| --- | --- |
| Compute | EC2 instances, AMIs, launch templates, Dedicated Hosts, capacity reservations and fleets, Auto Scaling groups, Lightsail instances, key pairs, Instance Connect endpoints, WorkSpaces and WorkSpaces directories |
| Containers and serverless | EKS clusters, managed node groups, Fargate profiles and add-ons; ECS clusters, services and task definitions; ECR repositories and pull-through cache rules; Lambda; App Runner |
| Networking | VPCs, subnets, CIDR blocks, route tables, security groups, network ACLs, network interfaces, Elastic IPs, internet, egress-only, NAT and virtual private gateways, customer gateways, VPN connections, Client VPN endpoints with their target networks, authorization rules and routes, VPC peering, endpoints and endpoint services, flow logs, DHCP option sets |
| Transit and hybrid networking | Transit gateways with route tables, VPC, peering and Connect attachments, multicast domains, associations, members and sources; Direct Connect connections, LAGs, gateways, associations and virtual interfaces; Cloud WAN global and core networks; Global Accelerator accelerators, listeners and endpoint groups |
| Load balancing, edge, and DNS | Application, Network and Gateway Load Balancers with listeners, target groups and trust stores, Classic Load Balancers, CloudFront distributions, WAF web ACLs and their associations, Shield Advanced protections, Route 53 hosted zones, health checks, Resolver rules and registered domains, API Gateway APIs, custom domains, usage plans, usage plan keys and API keys, VPC Lattice |
| Storage and backup | S3 buckets, EBS volumes and snapshots, Data Lifecycle Manager policies, EFS file systems, mount targets and access points, FSx file systems, Storage Gateway, AWS Backup vaults, plans and selections, Elastic Disaster Recovery source servers |
| Databases and analytics | RDS and Aurora, RDS Proxy, Aurora DSQL, DynamoDB, DocumentDB and DocumentDB Elastic, Neptune and Neptune Analytics, Keyspaces, ElastiCache, MemoryDB, Timestream, OpenSearch Service and Serverless, Redshift and Redshift Serverless, Glue databases, Athena workgroups, EMR clusters, EMR Serverless, Kinesis, Managed Service for Apache Flink, DMS, MSK clusters and topics, Amazon MQ, DataZone, QuickSight dashboards and datasets |
| Messaging and applications | SQS, SNS topics and subscriptions, EventBridge buses and rules, Step Functions, CodePipeline, Cloud Map namespaces and services, AppRegistry applications, Pinpoint SMS templates, IVS channels and stages, Kendra indexes, SageMaker endpoints, endpoint configurations, models and HyperPod clusters, AWS PCS and Batch compute environments |
| Identity, security, and governance | IAM users, groups, roles, instance profiles and managed policies, IAM Identity Center instances and groups, Organizations, organizational units and member accounts, KMS keys and aliases, ACM certificates, GuardDuty, Security Hub, Macie, Network Firewall, IAM Access Analyzer, CloudTrail trails and event data stores, AWS Config rules, remediation configurations, conformance packs and aggregators |
| Monitoring and orchestration | CloudWatch alarms, dashboards and log groups, Resource Groups, Synthetics canaries, X-Ray groups, Observability Access Manager, Managed Grafana and Prometheus, CloudFormation stacks and StackSets |

### Child resources

Some types are listed through their parent:

| Child type | Listed through |
| --- | --- |
| EKS node groups, add-ons, Fargate profiles | Each cluster |
| Load balancer listeners | Each load balancer |
| EFS mount targets | Each file system |
| Multicast associations, members, sources | Each multicast domain |
| IAM Identity Center groups | Each instance |
| API Gateway usage plan keys | Each usage plan |
| MSK topics | Each MSK provisioned cluster |
| Client VPN target networks, authorization rules and routes | Each Client VPN endpoint |
| WAF web ACL associations | Each regional web ACL, for every protected resource type |
| Organizational units | The complete organization tree |

If Steward cannot read the parent, the child's scan item fails instead of reporting an empty list.

<span id="global-services"></span>

### Global services

Global services are read from their home region. Include **Global** in the scan scope to scan them.

| Home region | Services |
| --- | --- |
| `us-east-1` | IAM, CloudFront, Route 53, Organizations, Shield Advanced |
| `us-west-2` | Global Accelerator, Cloud WAN |

CloudFront-scoped WAF web ACLs appear in the `us-east-1` region scan.

## Relationships and ownership

Steward builds relationships from the resource model (such as a resource's VPC, subnets, and security groups) and from attachment details read through the service APIs. These relationships decide what a cleanup task deletes, keeps, or blocks:

| Relationship | What it means for cleanup |
| --- | --- |
| Instance storage and interfaces | An EBS volume or network interface with `DeleteOnTermination` is managed by its instance: terminating the instance deletes it. Other attached volumes are deleted after the instance. |
| Auto Scaling and EKS | Instances in an Auto Scaling group, and the Auto Scaling groups of an EKS managed node group, are managed by their controller and can only be cleaned up through it. |
| Service-managed interfaces | Interfaces created by NAT gateways, VPC endpoints, load balancers, EFS mount targets, or Lambda belong to that service and cannot be deleted directly. |
| Elastic IPs | An address is released only after the NAT gateway or instance using it is gone. |
| Required children | A Client VPN endpoint is deleted only after its target networks are disassociated, and a Config rule only after its remediation configuration; both are added to the task. A WorkSpaces directory is deregistered only after its WorkSpaces are terminated, and a trust store is deleted only after the listeners that use it; Steward never selects those for you, so the task is blocked until you select them. |
| Deleted with the parent | Deleting an MSK cluster deletes its topics, an SNS topic its subscriptions, a Client VPN endpoint its authorization rules and manually added routes, and a usage plan its keys. The task lists them as deleted with the parent. |
| Parent-only members | Rules deployed by a conformance pack, routes added by a Client VPN subnet association and instances of an EMR cluster are removed only through that pack, association or cluster. |
| CloudFormation | Steward reads each stack's resources and processed template to find ownership and `DeletionPolicy`. Resources with `Retain` or `RetainExceptOnCreate` are treated as retained; a stack with termination protection blocks deletion. |

## Cleanup behavior and protections

The cleanup task shows what each deletion removes and what it keeps. Steward waits for asynchronous operations and reads the result back; an accepted request does not count as a deletion. Read [Clean up resources](./cleanup.md) before you execute a task.

### Keep attached volumes and interfaces

To keep a volume or interface that its instance would delete, retain it when you review the cleanup task. Before terminating the instance, Steward sets `DeleteOnTermination` to false and reads the change back. After termination, it confirms that retained resources still exist and deleted ones are gone. If attachments changed after you reviewed the task, execution stops.

### Deletion protection

For these types, Steward turns deletion protection off before deletion, then reads it back:

EC2 instances, AMIs, Auto Scaling groups, EKS clusters, load balancers, RDS and Aurora, DocumentDB clusters, Neptune, Aurora DSQL, DynamoDB, CloudWatch log groups, Network Firewall, CloudTrail event data stores, EMR clusters (termination protection).

### Preconditions and blockers

| Resource | Condition |
| --- | --- |
| Internet and virtual private gateways | Detached from their VPC first. |
| DocumentDB clusters | Must have no member instances. |
| AWS Config rules | Rules created by another service (a conformance pack, an organization rule, Security Hub) and member packs of an organization conformance pack cannot be deleted directly. |
| MSK topics | Internal topics whose names begin with `__`, such as `__consumer_offsets`, belong to Kafka and MSK and cannot be deleted. |
| Resource groups | Groups whose names begin with `AWS`, which AWS services create, cannot be deleted. |
| WAF web ACL associations | Associations of a web ACL that Firewall Manager manages cannot be removed. CloudFront distributions are not listed: their web ACL is a distribution setting. |
| Elastic Disaster Recovery source servers | Must be disconnected. |
| KMS keys | AWS managed keys cannot be deleted. A customer managed key that a scanned resource still references (by key ID, key ARN, alias, or alias ARN) is deleted only after that resource; if the resource is not in the task, the task is blocked. If a key policy grants unconditional key administration only to scanned IAM users or roles, without delegating to the account root, the task cannot delete all of them while the key remains. |
| Backup vaults | Must hold no recovery points and must not be locked in compliance mode. |
| S3 buckets | Must hold no object versions or delete markers, even when versioning is suspended. Steward does not empty buckets. A scan marks a non-empty bucket as protected so it shows in the task, and the bucket is checked again right before deletion. After you empty a bucket, scan again. |

Non-empty repositories, other dependencies, and resources changing state can still make an operation fail.

### Service defaults

| Resource | What deletion does |
| --- | --- |
| DocumentDB clusters | Deleted without a final snapshot. |
| EMR clusters | Terminated; unfinished steps are canceled and instance storage is lost. Logs already delivered to S3 remain. Only clusters that are not yet terminated are listed. |
| WorkSpaces directories | Deregistered from WorkSpaces; the Directory Service directory itself remains and is billed by Directory Service once no WorkSpaces use it. |
| AWS Config rules | Deleted with their evaluation results. |
| FSx file systems | Follow each file system type's default final-backup behavior. |
| KMS keys | Enter their scheduled deletion window. A key already pending deletion counts as deleted. |
| Route 53 registered domains | Deletion cannot be undone and is supported only for some top-level domains. |

## Differences from other clouds

Some resources that exist on other clouds have no AWS equivalent. Steward does not substitute an unrelated resource for them:

| Resource | AWS behavior |
| --- | --- |
| Bandwidth packages | Internet, inter-region, and Global Accelerator traffic is charged per GB; there is no bandwidth package resource. |
| Inter-region QoS and traffic marking policies | Transit Gateway and Cloud WAN have no QoS queue or DSCP marking policy resource. |
| Route maps | Transit Gateway has no route-map object; Cloud WAN routing policy is part of the core network policy. |
| Accelerator attachments | Amazon Elastic Inference was discontinued on April 15, 2024. |
| Dedicated block storage clusters and cloud phones | No corresponding AWS service. |
| Multi-cluster fleet control planes | EKS has no managed fleet controller resource. |

## Troubleshooting

| Symptom | What to check |
| --- | --- |
| `ExpiredToken` or validation fails | Check that the keys match, the Session Token and expiration, and the account partition; replace the complete credential set. |
| A region is missing | Check that the region is enabled in the account, and `ec2:DescribeRegions`. |
| Resource Explorer is unauthorized or returns nothing | Check the region's default view and its filters, the Search permission, and the index state. |
| Cloud Control returns `AccessDenied` | Check both the Cloud Control action and the service permissions the resource handler needs. |
| A child type fails while its parent succeeds | Check the child's list permission. For organizational units, connect the organization's management account. |
| A type is unavailable in a region | Check that AWS supports the type in that region, and scan only the types you use. |
| A bucket shows as protected | It still holds object versions or delete markers. Empty it outside Steward, then scan again. |
| Instance cleanup stops with an attachment mismatch | Volumes or interfaces changed after review. Rescan and review the task again. |
| A stack or resource cannot be deleted | Check termination protection, retention policy, dependencies, and the operation error before retrying. |

## Next steps

- [Scan resources](./scans.md)
- [Query resources](./resources.md)
- [Clean up resources](./cleanup.md)
