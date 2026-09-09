# Firewall policy verification

Steward inventories hierarchical FirewallPolicy, global/regional
NetworkFirewallPolicy, and their native associations. Selecting a policy adds
each association as a separate deletion prerequisite. Associations support
independent removal; deleting a policy never deletes its target organization,
folder or VPC network. Rules are embedded policy configuration.

## Native contracts and sources

- Hierarchical policies use Google's numeric name under
  `locations/global/firewallPolicies/ID`, with an explicit `parent` organization
  or folder. Their operations use `locations/global/operations/ID`.
- Network policies use `projects/PROJECT/global/firewallPolicies/NAME` or
  `projects/PROJECT/regions/REGION/firewallPolicies/NAME`, and the corresponding
  global or regional operation endpoint.
- Associations have native names and `getAssociation`/`removeAssociation`
  methods. They have no REST collection or independent LIST method. Their
  inventory identity appends an escaped name to the policy identity; requests
  send the decoded name in the native query, including default names with spaces.
- Native removal is POST with an empty body and query `name` and nonzero UUID
  `requestId`. Policy deletion is DELETE with an empty body and UUID request ID.
  All associations must be removed before policy deletion. See the native
  [hierarchical removal](https://docs.cloud.google.com/compute/docs/reference/rest/v1/firewallPolicies/removeAssociation),
  [global removal](https://docs.cloud.google.com/compute/docs/reference/rest/v1/networkFirewallPolicies/removeAssociation),
  [regional removal](https://docs.cloud.google.com/compute/docs/reference/rest/v1/regionNetworkFirewallPolicies/removeAssociation)
  and [policy management guide](https://docs.cloud.google.com/firewall/docs/manage-hierarchical-firewall-policies).
- Hierarchical [listAssociations](https://docs.cloud.google.com/compute/docs/reference/rest/v1/firewallPolicies/listAssociations)
  reads the target side with `includeInheritedPolicies=false`; this response
  has no pagination. The network-policy API has no equivalent reverse list.

The retained schema files contain 35 unmodified official schemas and their
transitive references. Tests use an independent JSON Schema validator.

| Source | Revision | SHA-256 of complete upstream response |
| --- | --- | --- |
| [Compute v1](https://www.googleapis.com/discovery/v1/apis/compute/v1/rest) | `20260828` | `5cee2d2fedf69f756fbc23aaef9153db01c2a2caffdbd6f63681912b65ca8139` |
| [Resource Manager v3](https://cloudresourcemanager.googleapis.com/$discovery/rest?version=v3) | `20260820` | `e46acd311b9d23a7ddac98ab85e2d9edaec3df9105f66679821c055c8e1c6131` |

Fourteen selected methods were added: eleven Compute methods and three Resource
Manager reads. Both source fragments reproduce exactly through the refresher.
Earlier selected methods/schemas and the other 53 source documents are unchanged.
The resulting catalog has 190 rules, 173 kinds with native deletion and 761
methods from 54 Discovery documents and one pinned Cloud SDK archive. Two catalog
generations produced SHA-256
`5d7ae94b3a97c3a126c31fa7c85ae7f7e12c7e569e008853a0fd15cc20b7618e`.

## Scope, inventory and cleanup

The optional connection credential `firewall_policy_parent` explicitly configures
`organizations/ID` or `folders/ID` and its descendant folders. A project connection
alone does not authorize hierarchical policy access. Resource Manager GETs prove
the live ancestor chain, including folder creation and parent identity. The policy
owner and each association target must be within that boundary. Policy ownership
is not inferred from the connected project's ancestors.

Global scans include hierarchical policies only when this scope is configured.
Their `firewall-visible` source preserves prior observations after scope removal,
scope changes or permission loss. It does not claim authoritative absence for an
organization. Network policies retain the authoritative project product source.

Two complete policy sets, folder-tree reads, native detail reads and association
GETs reconcile inventory. Cursors bind the connection, scope, rule revision,
complete set and configuration. Native policy configuration, incarnation,
fingerprint, target identity and membership are frozen in the reviewed manifest.
Configuration survives the common SQLite projection unchanged; display metadata
remains separately stored on the asset.

The actual solver creates association prerequisites before policy deletion.
Execution accepts only the reviewed remaining subset after earlier associations
have been removed; additions, changed rules, moved folders and replaced targets
block the plan. Policy DELETE requires every reviewed association absent in both
policy and native association reads. Hierarchical absence additionally requires
the target's native association list to exclude the original policy. Unrelated
associations and all target containers/networks remain intact.

Persisted waits bind the operation URL, policy numeric ID, target link, request
UUID, reviewed manifest and native operation type. The operation type is an open
native string, not an invented enumeration. Malformed, failed, foreign, changed
or expired operations cannot substitute for final resource readback. Serialized
restart does not replay an acknowledged mutation.

Run the protocol, schema, scope, scan-worker and projection checks locally:

```sh
go test ./providers/gcp -run 'TestFirewall|TestNativeCleanupAcceptsValidatedGCPPartition' -count=1
go test -race ./providers/gcp ./internal/app/inventory -run 'TestFirewall|TestNativeCleanupAcceptsValidatedGCPPartition|TestProjectionPreservesNativeFieldsAndDisplayMetadata' -count=1
```

The three-policy scenario has nine assets and two association prerequisites per
policy. Failure cases cover native paging and partial results, foreign scopes,
changed trees and targets, malformed associations, altered plans, retention,
permission loss, corrupt phases, native errors, delayed removal and expired LROs.
The real registry, inventory Creator, scan worker and SQLite tests cover six
shards, source authority, preserved observations and executable persisted plans.
A regression also exercises validated `gcp` connection partitions and persisted
MetricsScope, Deployment, Preview and DeploymentGroup actions.

## Independent Google mock

Google's unmodified [Config Connector mockcompute](https://github.com/GoogleCloudPlatform/k8s-config-connector/tree/673a61419de1b8e4f7d26070ce20dde2daa61da8/mockgcp/mockcompute)
at commit `673a61419de1b8e4f7d26070ce20dde2daa61da8` passed the opt-in
`TestFirewallIndependentMockGCP` over loopback HTTP. The retained harness uses
upstream Compute routing, storage and operation handling in its own Go module;
it adds no Steward dependency.

The run independently verified policy creation, native GET/DELETE, global
organization LRO GET, serialized restart and final GET absence. After creation,
it forwarded 14 native calls and counted two GET-wrapped list substitutions plus
16 fixture Resource Manager reads. These are explicit read-only substitutions;
no detail, mutation, operation or absence response was replaced.

Upstream has no hierarchical policy LIST, association methods, or global/regional
network-policy implementation. This verifies an unassociated hierarchical policy
only. Association cleanup, network policies, IAM and delayed operations use the
local protocol fixtures. It is not a full firewall emulator or real-cloud test.

To reproduce, check out the exact commit in a temporary directory. Copy
`testdata/mockgcp/main.go` into a new command directory beneath its `mockgcp`
module, then build and run it using that module. The harness prints its loopback
origin. In the Steward checkout, run:

```sh
STEWARD_FIREWALL_MOCKGCP_URL=http://127.0.0.1:PORT \
  go test ./providers/gcp -run '^TestFirewallIndependentMockGCP$' -count=1 -v
```

Replace `PORT` with the printed port, then stop the harness and remove the
temporary checkout and binary. The recorded run verified unchanged upstream
service source before testing, then stopped the server and removed its temporary
checkout and binary.

## Permissions and limits

Grant native policy list/get, association get/removal, delete and operation-read
permissions for the relevant scope. Hierarchical inventory also needs Resource
Manager organization/folder GET and recursive folders LIST. The linked REST
contracts list these IAM permissions for association operations:

| Method family | Documented IAM permissions |
| --- | --- |
| Hierarchical removal | `compute.firewallPolicies.use`, `compute.organizations.setFirewallPolicy` |
| Global network removal | `compute.firewallPolicies.use`, `compute.networks.setFirewallPolicy` |
| Regional network removal | `compute.regionFirewallPolicies.use`, `compute.networks.setFirewallPolicy` |
| Hierarchical target-side list | `compute.organizations.listAssociations`, `compute.organizations.setFirewallPolicy` |

Consult the native contracts for how those permissions apply to each resource.
Connection validation does not prove all these operations are permitted.

Removing an association changes firewall enforcement. Successful management-plane
readback does not prove that every packet-processing cache has converged. These
DELETE/removal APIs do not accept the policy fingerprint as an atomic condition.
Concurrent changes after the final read remain possible, and an identically
recreated same-name association has no native creation token to distinguish it.
Other rules, security profiles, tag/address groups and target resources are not
deleted as association effects. Individual policy-rule editing and complete
network-security parity remain separate work.
