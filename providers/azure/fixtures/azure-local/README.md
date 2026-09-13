# Azure Local native contracts and CLI ordering

These are contracts for the missing ENS-equivalent VM controller lifecycle.
Ordinary Arc machine deletion unregisters an external host. Azure Local has a
separate VM controller operation, and [Microsoft's management guide](https://learn.microsoft.com/en-us/azure/azure-local/manage/manage-arc-virtual-machines?view=azloc-2607)
says NICs and data disks remain after deleting the VM. They need independent
reference checks and cleanup. These fixtures do not implement that inventory,
ownership, planning or execution workflow.

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
metadata are excluded. No Azure Local cleanup action is enabled.

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
Inventory reconciliation, controller ownership, asynchronous recovery and
independent final readback remain required for lifecycle parity.
