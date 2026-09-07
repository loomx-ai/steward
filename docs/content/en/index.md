---
title: "Steward documentation"
description: "Connect a cloud account, scan its resources, inspect dependencies, and review cleanup before execution."
navTitle: "Overview"
---

<span id="start"></span>

## Start here

Steward supports Alibaba Cloud and AWS. Run it on your own computer or server, or use the invitation-only Steward Cloud hosted by LoomX.

<div class="docs-start-links"><a href="./quick-start.md"><strong>Self-host Steward</strong><span aria-hidden="true">↗</span><span>Install and start a local server</span></a><a href="https://steward.console.loomx.ai"><strong>Steward Cloud</strong> <span aria-hidden="true">↗</span><span>Have an invitation? Open your workspace</span></a></div>

<span id="workflow"></span>

## Your first inventory

1.  [Add a cloud connection](./connections.md), validate its credentials, and check its regions.
2.  [Run a scan](./scans.md) and wait for the selected scope to finish.
3.  [Find a resource](./resources.md), open its details, and check its properties and last-seen time.
4.  [Inspect its relationships](./topology.md) to see what still depends on it.
5.  When deletion is needed, [create a cleanup task](./cleanup.md) and review its scope.

<span id="read-the-map"></span>

## See what is running

<figure class="docs-figure"><a href="../../assets/topology-en.png" target="_blank" rel="noreferrer" aria-label="vSwitches, instances, databases, and a security group in one VPC (open full size)"><img src="../../assets/topology-en.png" alt="vSwitches, instances, databases, and a security group in one VPC" width="1440" height="960"></a><figcaption>vSwitches, instances, databases, and a security group in one VPC <span>· Sample data · click to enlarge</span></figcaption></figure>

Inventory and relationship views reflect scan results. Scan again after cloud-side changes. Cleanup tasks list blockers and retained resources; creating a task does not immediately delete anything.
