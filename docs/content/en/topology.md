---
title: "Resource relationships"
description: "Locate a resource in its network, then inspect its connections."
navTitle: "Resource relationships"
---

<span id="navigate"></span>

## Navigate into a network

Open Resource panorama, choose a region, then a VPC. Global and region-public resources have separate entries. Use the breadcrumbs to move back up.

<figure class="docs-figure"><a href="../../assets/topology-en.png" target="_blank" rel="noreferrer" aria-label="The production VPC: application and database resources occupy separate vSwitches (open full size)"><img src="../../assets/topology-en.png" alt="The production VPC: application and database resources occupy separate vSwitches" width="1440" height="960"></a><figcaption>The production VPC: application and database resources occupy separate vSwitches <span>· Sample data · click to enlarge</span></figcaption></figure>

The VPC view arranges resources by vSwitch. Open grouped nodes to inspect their members; search by name or ID to locate a resource. Select one to read its properties or add it to the cleanup list.

<span id="relationships"></span>

## Show relationship lines

Choose Show relationship lines in the canvas toolbar. Here, public-gateway routes to two ECS instances, which share the application-policy security group.

<figure class="docs-figure"><a href="../../assets/relationships-en.png" target="_blank" rel="noreferrer" aria-label="Two ECS instances share a load balancer and security group (open full size)"><img src="../../assets/relationships-en.png" alt="Two ECS instances share a load balancer and security group" width="1440" height="960"></a><figcaption>Two ECS instances share a load balancer and security group <span>· Sample data · click to enlarge</span></figcaption></figure>

To focus on one resource, open its detail page and switch to Relationships.

<span id="interpret"></span>

## Use the relationships

Before deleting a shared network or security group, check whether other instances still use it. The graph helps you find dependencies; verify them against the cleanup checks and cloud-side state.

Relationships come from scanned resources and supported relationship rules. A missing edge does not prove there is no dependency. Complete the scan coverage first.
