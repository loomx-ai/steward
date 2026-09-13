"""Offline execution of the retained Microsoft CLI deletion sequence with stubs."""
import hashlib
import json
from pathlib import Path
from types import SimpleNamespace
import unittest


FIXTURES = Path(__file__).resolve().parents[1] / "providers/azure/fixtures/azure-local"


class AzureLocalCLITests(unittest.TestCase):
    def test_native_delete_sequence_and_failure_boundary(self):
        source = json.loads((FIXTURES / "cli-source.json").read_text())
        fragment = (FIXTURES / source["file"]).read_bytes()
        self.assertEqual(hashlib.sha256(fragment).hexdigest(), source["sha256"])
        machine_id = "/subscriptions/test-sub/resourceGroups/test-rg/providers/Microsoft.HybridCompute/machines/test-vm"
        for fail_at in (None, "instance_delete", "instance_wait", "machine_delete"):
            with self.subTest(fail_at=fail_at):
                calls = []
                machine_poller = object()

                def record(phase):
                    calls.append(phase)
                    if phase == fail_at:
                        raise RuntimeError(phase)

                def wait():
                    record("instance_wait")

                def delete_instance(**kwargs):
                    self.assertEqual(kwargs, {"resource_uri": machine_id})
                    record("instance_delete")
                    return SimpleNamespace(result=wait)

                def delete_machine(**kwargs):
                    self.assertEqual(kwargs, {"resource_group_name": "test-rg", "machine_name": "test-vm"})
                    record("machine_delete")
                    return machine_poller

                cli_context = object()

                def subscription(context):
                    self.assertIs(context, cli_context)
                    return "test-sub"

                def resource_id(**kwargs):
                    self.assertEqual(kwargs, dict(subscription_id="test-sub", resource_group_name="test-rg",
                                                 provider_namespace="Microsoft.HybridCompute",
                                                 resource_type="machines", resource_name="test-vm"))
                    return machine_id

                scope = dict(get_subscription_id=subscription, get_resource_id=resource_id,
                             HYBRID_COMPUTE_RP_NAME="Microsoft.HybridCompute", HYBRID_COMPUTE_MACHINE_RT_NAME="machines")
                # Only this checked-in function is loaded. No CLI package or SDK is imported.
                exec(compile(fragment, str(FIXTURES / source["file"]), "exec"), scope)
                config = SimpleNamespace(polling_interval=5)
                client = SimpleNamespace(vmclient=SimpleNamespace(begin_delete=delete_instance),
                                         hybridclient=SimpleNamespace(begin_delete=delete_machine), _config=config)
                args = (SimpleNamespace(cli_ctx=cli_context), client, "test-rg", "test-vm")
                if fail_at:
                    with self.assertRaisesRegex(RuntimeError, fail_at):
                        scope[source["function"]](*args)
                    self.assertEqual(config.polling_interval, 5)
                else:
                    self.assertIs(scope[source["function"]](*args, polling_interval=17), machine_poller)
                    self.assertEqual(config.polling_interval, 17)
                order = ["instance_delete", "instance_wait", "machine_delete"]
                self.assertEqual(calls, order if fail_at is None else order[:order.index(fail_at) + 1])


if __name__ == "__main__":
    unittest.main()
