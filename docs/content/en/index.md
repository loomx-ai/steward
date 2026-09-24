---
title: "Steward documentation"
description: "Find every resource in your AWS, Azure, Google Cloud, and Alibaba Cloud accounts, see what depends on what, and review cleanup before anything is deleted."
navTitle: "Home"
---

<div class="docs-start-links docs-entry-links"><a href="./quick-start.md"><strong>Get started</strong><span aria-hidden="true">→</span><span>Use Steward Cloud or run Steward locally, then scan your first account</span></a><a href="./tutorials.md"><strong>Tutorials</strong><span aria-hidden="true">→</span><span>Inventory an AWS account and review a cleanup step by step</span></a><a href="./guides.md"><strong>Documentation</strong><span aria-hidden="true">→</span><span>Connections, scans, relationships, and cleanup</span></a></div>

## What is Steward?

Steward is an open-source tool by LoomX for taking stock of cloud accounts. It scans the accounts you connect, shows every resource it finds in one searchable inventory, maps how those resources relate, and checks a cleanup for dependencies before anything is deleted. Scanning only reads; nothing in your cloud changes until you confirm a cleanup task.

Use [Steward Cloud](https://steward.console.loomx.ai) for a workspace hosted by LoomX, or [install Steward](./installation.md) and run it on your own computer or server.

[How Steward works →](./intro.md)

## Connect a cloud platform

<div class="docs-cloud-links"><a href="./aws.md"><strong>AWS</strong><span>Access keys, IAM Identity Center, and permissions</span></a><a href="./alicloud.md"><strong>Alibaba Cloud</strong><span>RAM, STS, and browser sign-in</span></a><a href="./gcp.md"><strong>Google Cloud</strong><span>Projects and service accounts</span></a><a href="./azure.md"><strong>Microsoft Azure</strong><span>Subscriptions and service principals</span></a></div>

## Common tasks

<div class="docs-start-links"><a href="./scans.md"><strong>Scan resources</strong><span aria-hidden="true">→</span><span>Choose what to scan and read the results</span></a><a href="./topology.md"><strong>Explore relationships</strong><span aria-hidden="true">→</span><span>See where a resource sits and what it connects to</span></a><a href="./cleanup.md"><strong>Clean up resources</strong><span aria-hidden="true">→</span><span>Review targets and blockers, then confirm</span></a><a href="./deployment.md"><strong>Run Steward on a server</strong><span aria-hidden="true">→</span><span>Network access, sign-in, and backups</span></a></div>
