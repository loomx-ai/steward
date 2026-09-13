# Azure Local native contracts and CLI ordering

These contracts support the native Local VM controller lifecycle.
Ordinary Arc machine deletion unregisters an external host. Azure Local has a
separate VM controller operation, and [Microsoft's management guide](https://learn.microsoft.com/en-us/azure/azure-local/manage/manage-arc-virtual-machines?view=azloc-2607)
says NICs and data disks remain after deleting the VM. They need independent
reference checks and cleanup. Native inventory, reference graphs, VM/guest cleanup and managed-resource readback are implemented; reviewed Arc-registration cleanup follows VM removal. Disks, NICs, both image families and storage paths have independent cleanup; logical-network cleanup remains unfinished.

## Original Swagger examples

`sources.json` records 33 unchanged examples, SHA-256 values, operations, methods,
paths and upstream URLs. Seven StackHCIVM root documents at API `2024-01-01` are
pinned to Azure/azure-rest-api-specs commit
`c20bf553ad64f20c6d5e3f56080380c086cb1fde`: virtualMachineInstances,
networkInterfaces, virtualHardDisks, logicalNetworks, storageContainers,
galleryImages and marketplaceGalleryImages. Selected native fragments and
transitive schemas are retained in `../../catalog/source/swagger.json`.

The selection contains 24 GETs, eight DELETEs and VM Stop, covering VM instances,
guest agents, read-only guest hybrid identity metadata and six independent
resource families. No destructive operation is invented for identity metadata.
Tests validate all 25 response bodies against native schemas and preserve all
42 example responses. They explicitly account for these source discrepancies:

| Original discrepancy | Test treatment |
| --- | --- |
| Nine extension examples omit `providers` from the HybridCompute parent | Original binding fails; only the in-memory request gets the missing segment |
| Stop also passes the VM-instance suffix as part of `resourceUri` | Remove that suffix only in memory; nested instance parents remain rejected |
| Two NIC list examples include undeclared name/group parameters | Original binding fails; remove exactly those extra parameters in memory |
| Eighteen DELETE/Stop response headers use `http://azure.async.operation/status` | Preserve the placeholder and assert transport validation rejects it |

Schema validation does not establish valid ARM identities or ownership. Some
response IDs omit `providers`, use another subscription/group, or contain short
references such as `test-nic` and `external`. These are schema examples, not usable
inventory or deletion evidence. The [VM DELETE contract](https://learn.microsoft.com/en-us/rest/api/stackhci/virtual-machine-instances/delete?view=rest-stackhci-2024-01-01)
uses a HybridCompute machine as parent. Runtime binding enforces that direct
parent and the connection's subscription. Generic invocation/logging projects
only existing Arc public fields; OS/SSH/proxy configuration and unknown nested
metadata are excluded. VM, guest-agent, disk, NIC, image and storage-path cleanup use their native DELETE operations. Other Local direct cleanup actions remain unavailable.

## Original CLI function

`cli_vm_delete.py.txt` is an exact extraction of Microsoft's
`stackhcivm_virtualmachine_delete`, lines 1385–1403 of
`azext_stack_hci_vm/generated/custom.py` in `stack-hci-vm` 1.15.1.
`cli-source.json` retains the pinned official extension index, wheel URL,
archive/member/fragment hashes and extraction coordinates. The member declares
Copyright (c) Microsoft Corporation and the MIT License; `LICENSE.microsoft`
retains the notice and license from the pinned extension repository.

The independently authored CLI function begins VM-instance deletion, waits on
its poller, then begins Arc machine deletion and returns that second poller.
`scripts/test_azure_local_cli.py` executes only this function with stubbed clients;
no CLI package or SDK is installed or imported. It checks arguments, ordering,
polling interval and failure at each phase. Instance deletion or polling failure
prevents machine deletion. This establishes CLI ordering, not actual controller
completion, final GET 404, agent removal, disk/NIC absence or billing termination.

To reproduce the extraction from this directory (network required):

```python
import ast, hashlib, io, json, urllib.request, zipfile
from pathlib import Path

source = json.loads(Path("cli-source.json").read_text())
wheel = urllib.request.urlopen(source["wheel_uri"]).read()
assert hashlib.sha256(wheel).hexdigest() == source["wheel_sha256"]
member = zipfile.ZipFile(io.BytesIO(wheel)).read(source["member"])
assert hashlib.sha256(member).hexdigest() == source["member_sha256"]
function = next(n for n in ast.parse(member).body
                if isinstance(n, ast.FunctionDef) and n.name == source["function"])
assert (function.lineno, function.end_lineno) == (source["start_line"], source["end_line"])
fragment = b"".join(member.splitlines(keepends=True)[function.lineno - 1:function.end_lineno])
assert hashlib.sha256(fragment).hexdigest() == source["sha256"]
assert fragment == Path(source["file"]).read_bytes()
```

Offline checks from the repository root:

```sh
go test ./providers/azure -run 'TestAzureLocal|TestCatalogReproducible' -count=1
python3 -m unittest scripts/test_azure_local_cli.py scripts/test_sync_azure_catalog.py
```

These are native contract, CLI-sequence and composed transport checks. No
independent Azure Local emulator or live Hyper-V/controller backend was run.
Arc-registration cleanup is implemented below; live backend verification
remains required for lifecycle parity.

## Native inventory and application evidence

Nine explicit specifications use the `azure-local` source. Six independent
resource families use their subscription list and individual GET operations.
VM instances are enumerated under native HybridCompute machines; guest agents
and identity metadata are enumerated under each VM instance. All three are
fixed `default` singletons, so their own GET also recovers unrecorded omissions
from empty or missing indexes. Known resources recover omitted roots and parents.
Parent disappearance alone cannot close a known surviving guest resource.

Tests execute native pagination, changing/forged continuation tokens, parent
identity drift, partial/denied responses and malformed identities/references.
Two observations bind authored configuration, parent registrations and reference
sets without exposing private values. Guest regions come from verified machine
reads; explicitly conflicting locations fail. VM/disk/storage/image state and
network addresses use a typed public projection; credentials, keys, proxies,
local paths, error messages and unknown nested configuration are omitted.

SQLite scan/graph/reconciliation tests cover all nine families, exact reference
edges, known omissions, failed reads and individual absence. Network closure
finds the logical network, its NIC, VM and guest records. Custom locations and
cross-subscription targets remain unresolved; no relationship grants ownership.
Final Arc-registration cleanup and real backend outcomes remain pending.

## Network selection and attached disks

`azure_local_network_test.go` composes VNet and Local native transports to check
signed pagination across both families, filtered empty pages, exact ARM-ID
lookup, known index omissions, scope/connection/query isolation, denied reads
and terminal cursors. Local subnet configuration never becomes a fabricated ARM
subnet target. Disk network references use VM attachment reads and signed saved
VM identities; known parent omissions, altered signatures, detach and persisted
empty membership are covered without adding a reverse graph dependency.

A SQLite integration test enables the nine Local rules and the broad ARM source,
then runs the real scan creator, worker and graph handler. It rereads the selected
network, executes ten shards and retains six matching assets (network, NIC, VM,
two guest records and attached disk). Unrelated storage/image resources stay out.
Deleting the network prevents a second task from being saved. This scoped fixture
does not establish every service family's complete network-scan behavior, scope
membership pruning, controller deletion or live backend outcomes.

Frontend tests cover VNet-to-Local paging, Local selection, submission and retry
without losing an existing selection. Browser QA uses the actual scan dialog and
styles with a temporary HTTP-response fixture in light and dark themes; it is
UI evidence, not an independent cloud implementation.

## Guest-agent cleanup and SDK evidence

The native guest-agent action binds the guest configuration, VM configuration,
HCI registration UUID/location, protection state and ETag from inventory. It
checks current protection and inherited locks twice before DELETE. Private
credentials and unknown fields remain hashed, not exposed. Parent disappearance
prevents a new mutation; only the guest's own absence closes its asset. The
VM, Arc machine and read-only identity metadata are not removed by this action.

The ARM receipt is signed to the resource and immutable action request. It
supports native 202 and 204 responses, Azure-AsyncOperation precedence, Location
polling, signed-query rotation, persisted completion and restart without DELETE
replay. Tests reject altered scope/owner/version/phase, malformed callbacks,
failed/unknown states and operation 404 as evidence of successful deletion.
The bounded callback policy accepts subscription-scoped StackHCI regional
operation collections at the pinned API version. These callback paths are
composed protocol fixtures based on ARM conventions, not observed Local backend
recordings; live callback compatibility remains unverified.

`sdk-guest-source.json` pins the exact `_delete_initial` and `begin_delete`
methods from the official 1.15.1 CLI wheel's vendored 2024-01-01 SDK. The retained
fragments preserve original indentation, member/fragment hashes and line ranges;
`LICENSE.microsoft` applies. The offline Python tests execute these original
methods with transport/poller stubs: only 202/204 are accepted, native Location
handling is preserved, ARM polling is selected, and continuation skips the
initial mutation. No installed SDK, emulator or cloud account is used. Reproduce
the extraction using the same wheel/hash verification above, selecting class
`GuestAgentOperations` and the named method line ranges in the new manifest.

A registered SQLite scan/graph/plan/execution test selects the guest, includes
the cleanup warning, recreates repository/runtime across worker retries and
checks a stable original operation and deletion deadline. A completed operation
with a still-live guest remains pending; eventual own absence closes only the
guest. VM and identity assets stay active. This does not validate guest-side
uninstallation, physical VM deletion, controller cascades or billing outcomes.

## VM deletion transport contract

`sdk-vm-source.json` retains the VM request builder, `_delete_initial` and
`begin_delete` from `VirtualMachineInstancesOperations` in that same pinned
1.15.1 wheel. Archive, full member and exact AST fragment hashes are recorded;
`LICENSE.microsoft` applies. Offline tests execute the original code with
transport/poller stubs and check the direct HybridCompute-parent DELETE path,
API version, empty body, request headers, 202/204 response contract and restart
without another initial mutation. The VM SDK explicitly passes
`final-state-via: azure-async-operation` to `ARMPolling`; the guest SDK passes
no final-state option. Both settings are asserted independently.

The shared Local receipt transport now validates canonical VM-instance and
existing guest-agent owners. The same malformed-response, callback scope,
status failure, signed-query rotation and persisted-completion tests run for
all seven supported kinds. Tests reject receipts transferred between resources,
machines or across subscriptions before any HTTP call. Identity metadata,
logical-network roots and malformed/noncanonical IDs cannot own receipts,
including synchronous receipts without a callback. The raw native SDK does not
supply these application-level ownership guarantees.

This transport preparation now underlies the VM lifecycle described below.
The subsequent Arc-registration controller is connected below. The tests do not execute the real ARMPolling implementation or an
independent cloud backend. Composed callback shapes are still not recordings,
and actual VM/NIC/disk/identity or billing outcomes remain unverified.


## VM lifecycle and system-disk correction

VM inventory now signs the native guest/Arc child manifest, OS-disk identity,
known VM scopes, HCI registration, authored configuration and ETags. Native Arc
indexes discover extensions/commands/profiles and named reads recover omissions;
Local guest/identity singletons and the OS disk use their own GETs. VM graph
contributions require inventory for every affected resource. Arc resources are
ordered prerequisites without transferring ownership from their registration.
Guest agents use direct cleanup; identity metadata and the OS disk are reviewed
managed impacts. Retention/protection prevents a VM cascade.

`lifecycle-sources.json` records the important OS/data-disk distinction. The
Microsoft support thread first gave a retention answer, then corrected it after
reporting confirmation with the Local engineering team: OS disks are deleted
with the VM; data disks remain. The corrected support response is not an
independent backend execution or an explicit Swagger cascade guarantee. Tests
were updated to remove the registered OS disk and require its own absence,
while keeping a distinct data disk. No disk DELETE is fabricated by the VM
action. An omitted OS-disk ID has no separate ARM asset to verify, and the VM
warning still describes the OS-disk effect.

Before VM deletion, native machine/VM reads reject another consumer of its OS
disk, recovering known VMs even when a parent index omits them. The action binds
resource/registration identity and private authored fields, checks changed
ETags and locks/protection in all affected resource groups, and permits changing
native instance-view/agent-installation observations while preserving SMBIOS
identity. Neither a missing parent nor a successful poll proves managed absence.
The signed action receipt survives restart; VM, identity and registered OS-disk
GETs must all establish absence before those assets close. Arc registration,
data disks, NICs, images and storage paths are left for their separate lifecycles.

The registered SQLite test scans both Local and Arc sources, reviews five native
deletes and two managed impacts, restarts repository/runtime between worker
retries, and keeps operation identity and deletion deadlines stable. VM absence,
then identity absence, each leave the task pending while the OS disk survives.
Only the final disk own absence completes the controller action. The test
verifies retained data disk/registration assets and private-log sanitization.
Real backend behavior, guest/physical removal and billing outcomes are not proved
by these composed transports.

## Arc registration after Local VM removal

The retained original CLI function calls the Local VM delete, waits for it, then
calls HybridCompute machine delete. The implementation now expresses that sequence
as a Local VM direct lifecycle under its Arc registration. A registration scan
binds the VM configuration, guest/identity singletons and registered OS disk.
Signed history is preserved only for the same registration when the VM's own GET
is 404; bare HCI hosts without this evidence stay protected. Ordinary Arc machine
records and pending receipts keep their existing representation.

The registration driver requires each Local resource's own absence before native
Arc DELETE and before completion, including after registration disappearance.
It reuses the native Arc receipt, independent readback, protection and lock checks.
VM-only selection still leaves the registration active. Retention/protection of
the VM or OS disk blocks the complete registration plan. The expanded SQLite test
reviews six direct steps and two managed impacts, restarts between worker polls,
and checks that only the reviewed registration/VM family is closed. Tests also
exercise history recovery, substituted registration, denied reads, changed VM/
disk/identity configuration, synchronous responses and DELETE 404.

This implements the reviewed native API sequence; the composed transport does
not prove physical removal, production callback shapes or billing termination.

## Independent disk and NIC cleanup

`sdk-disk-source.json` and `sdk-nic-source.json` retain the original request
builder, initial delete and `begin_delete` from the same pinned 1.15.1 CLI wheel.
Their native methods accept 202/204 and use `final-state-via: azure-async-operation`.
The offline Python tests execute the retained functions with stubs and verify
resource-group/resource names, subscription, API version, header serialization,
response status handling and continuation without replay. These are original
SDK function checks, not a live service or independent emulator.

The driver reuses the signed Local receipt and own readback. An ARM 204 is
terminal even with diagnostic operation headers (the original examples include
a placeholder header); those headers are discarded and never visited or saved.
For 202, only validated same-subscription StackHCI ARM callback endpoints are
accepted. Neither status proves the resource itself is absent.

Root inventory binds all observed VM scopes, preserving known identities through
index omissions and VM 404. Configuration prefixes and signed context prevent
removing recovery metadata to bypass consumer checks. Legacy disk snapshots
remain usable for pending VM impacts, while direct root deletion requires rescan.
Native VM references block direct deletion; graph prerequisites require explicit
VM selection and never silently expand a disk/NIC selection into VM deletion.
OS disks retain their existing VM-managed lifecycle. Protection, ETags, inherited
locks, new consumers and denied reads are checked before mutation.

Registered SQLite tests now cover VM cleanup followed by a separately selected
data disk or NIC, with two VM-managed impacts, six direct steps, repeated runtime/
repository restart and own absence checks. Standalone tests cover detach,
retention/protection, omitted parents, 202/204/404, malformed context and legacy
rescans. A shared network-filter regression prevents private recovery hints from
being interpreted as network attachments; explicit verified references remain.
Physical disk/NIC removal, real callback compatibility and billing are unverified.

## Independent gallery and marketplace image cleanup

`sdk-image-source.json` and `sdk-marketplace-source.json` retain the original
request builder, initial response handler and resumable delete methods from the
same verified Microsoft CLI wheel. The six extracted functions retain exact
line ranges and hashes. Offline checks execute them with stubs, covering native
resource names, API version, 202/204 response acceptance, error statuses and
continuation without another DELETE. All original Swagger examples are unchanged.

The [Azure Local FAQ](https://learn.microsoft.com/en-us/azure/azure-local/manage/azure-arc-vms-faq)
explains that image deletion does not affect deployed VMs: VM creation copied the
source image. `lifecycle-sources.json` records this distinction. Images reuse the
signed root action but have no VM consumers, prerequisite deletions or cascade
impacts. Image-only inventory/actions do not read Arc/VM endpoints. If both image
and VM are selected, the ordinary reference orders VM removal before the image.

Tests retain a live VM referencing each image while deleting that image, reject
injected VM prerequisites/impacts, and check native permissions, protection,
locks, ETags and changed configuration. Own GET absence, not 202/204/DELETE 404 or
operation success, completes cleanup. Poll ownership/failure tests include both
image kinds. SQLite tests scan the full Local graph, select only the image,
restart runtimes/repositories between polls and close only the selected image.
Separate complete-VM tests also select each image and verify ordered cleanup.
These are composed protocol and original SDK-function checks; no independent
Azure Local emulator or live backend was used. Physical removal and real
controller callback compatibility remain unverified.

## Storage paths and verified managed prerequisites

`sdk-storage-source.json` retains the three native storage-container SDK methods
from the same pinned Microsoft CLI wheel, including exact line ranges and hashes.
Offline tests execute the original builder, 202/204 response handler and resumable
poller setup. The original storage-name pattern permits a trailing underscore;
the catalog preserves this native contract rather than copying the disk pattern.

Native disk/image lists and individual reads, plus Arc machine/Local VM reads,
establish storage consumers. Signed root context retains all observed disk/image
IDs and VM scopes; known resources recover index omissions and their IDs survive
404 for future reads. Placement fields are optional, so missing placement is
explicitly recorded as uncertain dependency evidence and never inferred unused.
The graph requires explicit workload selection; it does not claim ownership.

OS disks need special prerequisite handling: their native VM controller removes
and verifies them. A provider-declared cascade can satisfy a required deletion
through an already planned controller, only for an exclusive reviewed delegated
impact whose controller verifies own absence. The shared solver records that
controller alongside the required disk asset; the worker restores the frozen
impact after closure/restart. Missing/changed authority, retained/protected
impacts, ambiguous records and foreign snapshots are rejected. A requirement
never selects an unselected controller or adds a fabricated disk DELETE.

Tests cover standalone 202/204/404 readback, surviving consumers after path 404,
new/omitted/unknown/foreign placement, permission and index failures, malformed
history, configuration/ETags/locks, legacy rescan and explicit selection. The
SQLite execution test reviews nine direct steps and two VM impacts, restarts
runtimes/repositories between observations, and retains the registration, NIC,
network and license. The storage DELETE occurs only after VM, both disks and
both images disappear. These are composed transports and native SDK-function
checks; physical path/volume behavior and production callbacks remain unverified.
