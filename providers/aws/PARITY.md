# AWS provider parity with Alibaba Cloud

Objective: bring AWS to the engineering and functional completeness of the
Alibaba Cloud integration with the same acceptance bar used for GCP and Azure in
`PROVIDER_PARITY.md`: official versioned API metadata, explicit resource rules,
inventory, relationships, lifecycle-aware cleanup, and retained tests that
separate protocol fixtures, independent emulator evidence, and real-cloud
evidence.

## Baseline

At `c579d80`, AWS had 45 specifications, 7 source files and 6 test files. It was
not part of `providers/parity.yaml`. Its catalog was a hand-written Smithy
fragment and every Cloud Control specification was assumed to support list,
read and delete. The official CloudFormation schema shows that
`AWS::OpenSearchService::Domain` has no list handler, so its registered
authoritative Cloud Control inventory could never succeed.

## Plan

1. Add an `aws` target to every parity row. Unmapped rows record the concrete
   platform difference instead of a substitute resource.
2. Pin official metadata: the CloudFormation resource provider schema archive
   (handler presence, permissions, primary identifiers, list handler inputs,
   tagging) and the AWS Smithy API models for native operations. Refreshes are
   scripted; builds and tests are offline.
3. Add explicit specifications for all mapped resource types. Cloud Control
   types must have list/read/delete handlers in the pinned schema; types
   without them use typed native product APIs.
4. Runtime: parent-scoped Cloud Control listing, compound identifiers, global
   service endpoints, preconditions, deletion-protection disablement through
   Cloud Control `UpdateResource`, native product inventory and actions.
5. Lifecycle contributors: EBS `DeleteOnTermination`, ENI attachments,
   Auto Scaling and EKS managed members, NAT gateway addresses, requester-managed
   network interfaces, Transit Gateway attachments, backup vault contents.
6. Tests: unit and SDK-level protocol tests for every transport, contract tests
   for every specification, and emulator tests against Moto where Moto
   implements the API.
7. Bilingual capability and permission documentation.

## Evidence classes

- `protocol`: official SDK clients against `httptest` servers returning
  documented response shapes.
- `emulator`: independent Moto server (`STEWARD_AWS_MOTO_URL`), pinned version.
- `real-cloud`: none recorded in this repository.
