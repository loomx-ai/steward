---
title: "Steward documentation"
description: "Connect a cloud account, scan its resources, inspect dependencies, and review cleanup before execution."
navTitle: "Overview"
---

<span id="start"></span>

## Start here

Steward helps you inventory existing cloud resources, understand their relationships, and review the impact before executing cleanup.

- **Understand the product:** [What is Steward?](./intro.md) explains scans, resource records, and cleanup.
- **Complete a task:** [First resource inventory](./tutorials/first-inventory.md) walks through verifying scan results for a known resource.
- **Find an operation:** Use the sidebar for connection, scan, inventory, relationship, and cleanup guides.

Steward supports Alibaba Cloud, AWS, Google Cloud (GCP), and Microsoft Azure. Run it on your own computer or server, or use the invitation-only Steward Cloud hosted by LoomX.

<div class="docs-start-links"><a href="./quick-start.md"><strong>Self-host Steward</strong><span aria-hidden="true">↗</span><span>Install and start a local server</span></a><a href="https://steward.console.loomx.ai"><strong>Steward Cloud</strong> <span aria-hidden="true">↗</span><span>Have an invitation? Open your workspace</span></a><a href="./azure.md"><strong>Microsoft Azure</strong><span>Subscriptions, service principals, and resource locks</span></a></div>

## Connect your cloud

Choose your platform for credentials, permissions, inventory coverage, and cleanup considerations.

<div class="docs-cloud-links"><a href="./alicloud.md"><strong>Alibaba Cloud</strong><span>RAM, STS, and browser authorization</span></a><a href="./aws.md"><strong>AWS</strong><span>IAM, resource discovery, and stacks</span></a><a href="./gcp.md"><strong>Google Cloud</strong><span>Projects, service accounts, and global networks</span></a><a href="./azure.md"><strong>Microsoft Azure</strong><span>Subscriptions, service principals, and resource locks</span></a></div>

<span id="workflow"></span>

## Common operations

1.  [Add a cloud connection](./connections.md), validate its credentials, and check its regions.
2.  [Run a scan](./scans.md) and wait for the selected scope to finish.
3.  [Find a resource](./resources.md), open its details, and check its properties and last-seen time.
4.  [Inspect its relationships](./topology.md) to see what still depends on it.
5.  When deletion is needed, [create a cleanup task](./cleanup.md) and review its scope.

<span id="read-the-map"></span>

## Resource panorama

<figure class="docs-figure"><a href="../../assets/topology-en.png" target="_blank" rel="noreferrer" aria-label="vSwitches, instances, databases, and a security group in one VPC (open full size)"><img src="../../assets/topology-en.png" alt="vSwitches, instances, databases, and a security group in one VPC" width="1440" height="960"></a><figcaption>vSwitches, instances, databases, and a security group in one VPC <span>· Sample data · click to enlarge</span></figcaption></figure>

Inventory and relationship views reflect scan results. Scan again after cloud-side changes. Cleanup tasks list blockers and retained resources; creating a task does not immediately delete anything.
