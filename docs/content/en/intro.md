---
title: "What is Steward?"
description: "How Steward turns cloud connections and scans into an inventory, a relationship map, and reviewed cleanup tasks — and what each step can and cannot tell you."
navTitle: "What is Steward?"
---

Steward helps you answer three questions about your cloud accounts: what is running, what depends on what, and what a cleanup would affect. It works with AWS, Alibaba Cloud, Google Cloud (GCP), and Microsoft Azure. You can self-host it or sign up for Steward Cloud.

Teams typically use it to:

- **Inventory existing resources** — scan the accounts and regions you choose, then find resources by name, ID, type, or property.
- **Check shared dependencies** — start from a network overview and follow the relationships Steward found between resources.
- **Clean up with a review step** — put resources into a cleanup task, resolve blockers, and only then confirm deletion.

New to Steward? Read this page, then follow [First resource inventory](./tutorials/first-inventory.md) with a resource you already have.

## How Steward works

Steward reads your resources through a **cloud connection**, records what each **scan** finds, and builds the inventory and relationship views from those records.

<figure class="docs-figure"><a href="../../assets/scan-data-flow-en.svg" target="_blank" rel="noreferrer" aria-label="How scanning produces inventory and relationships (open full size)"><img src="../../assets/scan-data-flow-en.svg" alt="A scan reads cloud provider APIs within a selected scope and records resources. Inventory and relationship views use these scan results. Scan again after cloud-side changes." width="640" height="650"></a><figcaption>A scan records resources with the time it saw them. Inventory and relationship views show those records.</figcaption></figure>

Three operations touch your cloud, and only the last one changes anything:

| Operation | What it does |
| --- | --- |
| Validate a connection | Confirms which cloud identity the credentials belong to |
| Scan | Calls read-only APIs within the scope you choose |
| Confirm a cleanup task | Sends deletion requests for the reviewed resources |

## Connections and resource identity

A **cloud connection** holds the credentials for one cloud identity — an AWS account, an Alibaba Cloud account, a Google Cloud project, or an Azure subscription. Resources, scans, and cleanup tasks all belong to a connection, so check which connection is selected before you read results.

Names can change or repeat, so Steward identifies a resource by four things:

| Field | Question it answers | Example |
| --- | --- | --- |
| Cloud connection | Which account, and with which identity? | An AWS connection named "Production" |
| Region | Where is the resource? | `ap-southeast-1` |
| Resource type | What kind of resource is it? | EC2 instance, VPC, security group |
| Native resource ID | Which exact resource? | `i-demo-api01` |

In the screenshots, `api-01` is a name and `i-demo-api01` is the native resource ID.

## Reading scan results

Inventory shows what the last scan saw, not a live view of the cloud. Each resource has a **last seen** time: when a scan last observed it. If you change something in the cloud console after a scan, Steward shows the earlier state until you scan that scope again. Reloading the browser does not rescan.

To know how complete an inventory is, check two things:

- **Scope** — which regions, global resources, and resource types the scan included. Scanning a region does not include global resources (such as IAM identities) or other regions; select **Global** as well when you need them. See [scan scopes](./scans.md#scope).
- **Outcome** — whether every scan item finished, or some failed because of permissions or API errors.

An empty or short result can mean the scope was wrong, a permission was missing, or a resource type isn't covered — not necessarily that nothing exists. Open the scan's details before drawing conclusions.

## Resource panorama and relationship lines

**Resource panorama** lays out resources by account, region, and network (VPC and subnet), so you can find where something lives. **Relationship lines** add the connections Steward recognizes, such as a load balancer routing to instances or an instance using a security group.

<figure class="docs-figure"><a href="../../assets/topology-en.png" target="_blank" rel="noreferrer" aria-label="Steward organizes resources by their network placement (open full size)"><img src="../../assets/topology-en.png" alt="Steward's VPC panorama groups application instances and databases into different subnets" width="1440" height="960"></a><figcaption>Sample data: application instances and databases in separate subnets. Network grouping shows where resources are; relationship lines show how they connect.</figcaption></figure>

Sharing a VPC or subnet tells you where resources sit, not that one depends on another. Relationship lines come from scanned resources and the rules Steward supports, so a missing line does not prove that no dependency exists. [Resource relationships](./topology.md) explains how to navigate the panorama and focus on one resource.

## From selection to deletion

Cleanup narrows down in stages, and nothing is deleted until the third one:

| Stage | What you check | Deletes anything? |
| --- | --- | --- |
| Add to the resource list | Which resources you want to look at more closely | No |
| Create a cleanup task | The exact resources it resolves to, plus blockers, warnings, and retained resources | No |
| Confirm execution | That the reviewed scope matches your intent | Yes |
| Check results | Which resources were deleted, failed, or kept | Requests may still be finishing |

A task checks dependencies and whether each deletion is supported. Resolve blockers and read warnings — especially about incomplete scans. Once execution starts, pausing stops further requests but cannot recall requests already sent or restore deleted resources.

[Resource cleanup](./cleanup.md) walks through a blocked cleanup and the execution steps.
