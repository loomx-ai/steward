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

## Current state

| Measure | AWS |
| --- | --- |
| Explicit specifications | 194 (182 Cloud Control, 11 product API, 1 CloudFormation stack) |
| Parity rows with an implemented mapping | 149/159 |
| Rows with a documented platform difference instead of a mapping | 10 |
| Candidate types without a specification | 0 |
| Pinned official operations | 61 from 19 Smithy models |
| Pinned CloudFormation resource schemas | 189 |

These are registration and verification measures, not a claim that every row
has closed behavioral acceptance. Rows remain `pending_verification` in the
matrix, as for GCP and Azure.

## Work and evidence

1. [x] **Matrix.** Every row has an `aws` target. `providers/parity_test.go`
   requires each AWS reference to have a specification and to be part of the
   pinned catalog selection, and each empty mapping to state the platform
   difference.
2. [x] **Official metadata.** `scripts/sync-aws-catalog.py` pins
   `aws/api-models-aws` at a commit and the CloudFormation schema archive by
   SHA-256. The Smithy importer derives call metadata from service and
   operation traits. `catalog_test.go` regenerates the catalog byte for byte;
   `scripts/test_sync_aws_catalog.py` checks the refresh offline.
3. [x] **Specifications.** `spec_contract_test.go` checks every Cloud Control
   specification against its official schema: list/read/delete handlers,
   list handler inputs, relationship and deletion-protection properties, scope
   and home region, bilingual names. Product API specifications must match their
   typed handlers, and every parameter must be a member of the official
   operation input.
4. [x] **Runtime.** Spec-declared Cloud Control list requests with parent
   discovery (bound cursors, parent-set change detection, recursive parents,
   organization tree, WAF scopes, account ID), global home regions, deletion
   protection disabled through `UpdateResource` with live readback before
   deletion, native product API inventory and actions with preconditions,
   protection, oversized-page cursors and absence readback.
5. [x] **Lifecycle.** EBS and ENI `DeleteOnTermination` bindings with reviewed
   retention (policy changed and read back before termination, outcomes verified
   after), Auto Scaling and EKS managed members, requester-managed interfaces,
   Elastic IP ordering, Internet and virtual private gateway detachment,
   CloudFormation stacks.
6. [x] **Tests.** Protocol tests use the official SDK clients against documented
   response shapes (`cloudcontrol_protocol_test.go`,
   `native_protocol_test.go`). Unit tests cover plans, cursors, protection,
   lifecycle contributors and retention drift.
7. [x] **Emulator and application.** Moto 5.2.3 (`STEWARD_AWS_MOTO_URL`, CI job
   `aws-emulator`) verifies EC2 images and snapshots, OpenSearch, FSx,
   Route 53 Domains, the organization tree, instance volume retention and drift,
   VPN gateway detachment, and the full application pipeline: SQLite scan, graph,
   plan with explicit retention, restartable execution, reconciliation and
   rescan.
8. [x] **Documentation.** `docs/content/{en,zh}/aws.md` describe sources,
   permissions, coverage, ownership, cleanup effects and platform differences.

## Evidence classes and limits

- `protocol`: Cloud Control, DocumentDB, DMS, Storage Gateway, DRS and Pinpoint.
  Moto has no Cloud Control API, rejects the `docdb` engine and encodes DMS
  timestamps as strings, so these cannot be emulator evidence.
- `emulator`: the Moto tests above. The pipeline test translates Cloud Control
  instance and volume calls onto Moto's EC2 API; that adapter is scaffolding and
  is labelled as such.
- `real-cloud`: none recorded in this repository.

## Remaining work

- Behavioral acceptance per matrix row, including real-cloud evidence.
- Cloud Control handler behavior for other individual types (for example ECR
  contents) is taken from the official handler contract and is not
  independently verified. KMS keys (AWS managed keys, scheduled deletion),
  Backup vaults (recovery points, compliance lock) and S3 buckets (object
  versions and delete markers) have native guards verified on Moto and fakes.
  Customer managed KMS keys are ordered after, and blocked by, scanned
  resources that reference them. Key policies are not parsed for their last
  administrator.
- Resource Explorer remains a non-authoritative index for types without rules.
- Auto Scaling and EKS members cannot be retained individually; the plan blocks
  such requests.
