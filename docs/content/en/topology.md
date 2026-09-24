---
title: "Resource relationships"
description: "Locate a resource in its region, VPC, and subnet in Resource Panorama, then show relationship lines to find shared dependencies."
navTitle: "Resource relationships"
---

**Resource Panorama** shows where a resource sits (region, VPC, subnet) and what it is connected to. Use it to find shared networks, security groups, and load balancers before you decide what to clean up.

<span id="navigate"></span>

## Navigate into a network

1. Open **Resource Panorama**. The top level shows the account's regions. **Global resources** and region-public resources have their own entries.
2. Choose a region, then a VPC. The VPC view arranges resources by subnet.
3. Use the breadcrumbs to move back up.

<figure class="docs-figure"><a href="../../assets/topology-en.png" target="_blank" rel="noreferrer" aria-label="The production VPC: application and database resources occupy separate subnets (open full size)"><img src="../../assets/topology-en.png" alt="The production VPC: application and database resources occupy separate subnets" width="1440" height="960"></a><figcaption>The production VPC: application and database resources occupy separate subnets <span>· Sample data · click to enlarge</span></figcaption></figure>

Open grouped nodes to see their members. To find a resource, search the canvas by region, resource ID, or name. To read a resource's properties, right-click it and choose **View details**.

<span id="relationships"></span>

## Show relationship lines

Click **Show relationship lines** in the canvas toolbar. In the sample, `public-gateway` routes to two EC2 instances, which share the `application-policy` security group.

<figure class="docs-figure"><a href="../../assets/relationships-en.png" target="_blank" rel="noreferrer" aria-label="Two EC2 instances share a load balancer and security group (open full size)"><img src="../../assets/relationships-en.png" alt="Two EC2 instances share a load balancer and security group" width="1440" height="960"></a><figcaption>Two EC2 instances share a load balancer and security group <span>· Sample data · click to enlarge</span></figcaption></figure>

To focus on one resource, open it in **Resources** and switch to the **Relationships** tab.

<span id="interpret"></span>

## Use the relationships

Before you delete a shared network or security group, open each connected resource and check whether it is still needed. Treat the graph as a guide: the cleanup task's dependency checks and the cloud-side state decide what can be deleted.

Relationships come from scanned resources and the relationship rules Steward supports, so a relationship to an unscanned resource, or one the rules don't cover, has no line. If a line you expect is missing, [scan the full scope](./scans.md#freshness) before concluding there is no dependency.

When you have decided on targets, right-click them and choose **Add to resource list**, or use **Box select** → **Add selected**. Then create the task from **Resource list**; see [Resource cleanup](./cleanup.md#select).

## Google Cloud networks

GCP VPCs are global and their subnets are regional, so the same VPC can appear in several regional views. Cleaning up a VPC group in one region keeps the shared global network. See [Google Cloud](./gcp.md#global-vpcs-and-regional-subnets).

## Next steps

To practice reviewing shared resources and record a scope decision before deletion, follow [Review cleanup dependencies](./tutorials/review-cleanup-dependencies.md).
