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


class AzureLocalGuestSDKTests(unittest.TestCase):
    manifest = "sdk-guest-source.json"
    polling_options = {}
    arguments = ("native-machine",)
    native_parameters = {"resource_uri": "native-machine"}
    subscription_parameters = {}

    def native_functions(self):
        import textwrap
        source = json.loads((FIXTURES / self.manifest).read_text())
        functions = {}
        for entry in source["functions"]:
            fragment = (FIXTURES / entry["file"]).read_bytes()
            self.assertEqual(hashlib.sha256(fragment).hexdigest(), entry["sha256"])
            functions[entry["function"]] = textwrap.dedent(fragment.decode())
        return functions

    def test_native_initial_delete_responses(self):
        from types import MethodType
        class NativeError(Exception):
            def __init__(self, **kwargs):
                super().__init__("native SDK rejected response")
        class Deserialize:
            def __call__(self, kind, value):
                return value
            def failsafe_deserialize(self, *args):
                return None
        for status in (200, 201, 202, 204, 400, 403, 404, 409, 500):
            with self.subTest(status=status):
                requests = []
                def build(**kwargs):
                    requests.append(kwargs)
                    return SimpleNamespace(url="https://management.azure.com/native-guest")
                response = SimpleNamespace(status_code=status, headers={"Location": "https://management.azure.com/native-poll"})
                pipeline = SimpleNamespace(http_response=response)
                scope = dict(build_delete_request=build, ClientAuthenticationError=NativeError,
                             ResourceNotFoundError=NativeError, ResourceExistsError=NativeError,
                             ResourceNotModifiedError=NativeError, HttpResponseError=NativeError,
                             ARMErrorFormat=object(), _models=SimpleNamespace(ErrorResponse=object()),
                             map_error=lambda **kwargs: None)
                exec("from __future__ import annotations\n" + self.native_functions()["_delete_initial"], scope)
                instance = SimpleNamespace(_config=SimpleNamespace(api_version="2024-01-01", subscription_id="test-sub"),
                                           _client=SimpleNamespace(format_url=lambda url: url,
                                               _pipeline=SimpleNamespace(run=lambda *args, **kwargs: pipeline)),
                                           _deserialize=Deserialize())
                call = MethodType(scope["_delete_initial"], instance)
                if status in (202, 204):
                    result = call(*self.arguments, cls=lambda raw, data, headers: (raw, headers))
                    self.assertIs(result[0], pipeline)
                    self.assertEqual(result[1], {"Location": response.headers["Location"]} if status == 202 else {})
                else:
                    with self.assertRaises(NativeError):
                        call(*self.arguments)
                self.assertEqual(requests, [dict(**self.native_parameters, **self.subscription_parameters, api_version="2024-01-01", headers={}, params={})])

    def test_native_delete_continuation_skips_mutation(self):
        from types import MethodType
        class Poller:
            def __class_getitem__(cls, item):
                return cls
            def __init__(self, *args):
                self.args = args
            @classmethod
            def from_continuation_token(cls, **kwargs):
                return kwargs
        polls, mutations = [], []
        def polling(delay, **kwargs):
            polls.append((delay, kwargs))
            return "arm-polling"
        scope = dict(LROPoller=Poller, ARMPolling=polling, NoPolling=lambda: "no-polling",
                     cast=lambda typ, value: value, PollingMethod=object)
        exec("from __future__ import annotations\n" + self.native_functions()["begin_delete"], scope)
        native_response = object()
        def initial(**kwargs):
            mutations.append(kwargs)
            return native_response
        client = object()
        instance = SimpleNamespace(_delete_initial=initial, _client=client, _config=SimpleNamespace(polling_interval=5))
        call = MethodType(scope["begin_delete"], instance)
        first = call(*self.arguments, polling_interval=7)
        self.assertEqual(polls, [(7, self.polling_options)])
        self.assertEqual(len(mutations), 1)
        self.assertEqual({k: mutations[0][k] for k in self.native_parameters}, self.native_parameters)
        self.assertIs(first.args[1], native_response)
        restored = call(*self.arguments, continuation_token="saved-native-token")
        self.assertEqual(len(mutations), 1)
        self.assertEqual(polls, [(7, self.polling_options), (5, self.polling_options)])
        self.assertEqual(restored["continuation_token"], "saved-native-token")
        self.assertEqual(restored["polling_method"], "arm-polling")
        self.assertIs(restored["client"], client)


class AzureLocalVMSDKTests(AzureLocalGuestSDKTests):
    manifest = "sdk-vm-source.json"
    polling_options = {"lro_options": {"final-state-via": "azure-async-operation"}}

    def test_native_request_builder(self):
        calls = []

        def serialize(name, value, kind, **kwargs):
            calls.append((name, value, kind, kwargs))
            return value

        scope = dict(case_insensitive_dict=dict, HttpRequest=lambda **kwargs: kwargs,
                     _SERIALIZER=SimpleNamespace(url=serialize, query=serialize, header=serialize))
        exec("from __future__ import annotations\n" + self.native_functions()["build_delete_request"], scope)
        machine = "subscriptions/test-sub/resourceGroups/test-rg/providers/Microsoft.HybridCompute/machines/test-vm"
        request = scope["build_delete_request"](machine, headers={"x-ms-client-request-id": "reviewed-request"})
        self.assertEqual(request, dict(method="DELETE",
            url="/" + machine + "/providers/Microsoft.AzureStackHCI/virtualMachineInstances/default",
            params={"api-version": "2024-01-01"},
            headers={"Accept": "application/json", "x-ms-client-request-id": "reviewed-request"}))
        self.assertEqual(calls, [
            ("resource_uri", machine, "str", {"skip_quote": True}),
            ("api_version", "2024-01-01", "str", {}),
            ("accept", "application/json", "str", {}),
        ])


class AzureLocalDiskSDKTests(AzureLocalGuestSDKTests):
    manifest = "sdk-disk-source.json"
    polling_options = {"lro_options": {"final-state-via": "azure-async-operation"}}
    arguments = ("test-rg", "test-resource")
    native_parameters = {"resource_group_name": "test-rg", "virtual_hard_disk_name": "test-resource"}
    subscription_parameters = {"subscription_id": "test-sub"}
    collection = "virtualHardDisks"

    def test_native_request_builder(self):
        calls = []
        def serialize(name, value, kind, **kwargs):
            calls.append((name, value, kind, kwargs))
            return value
        scope = dict(case_insensitive_dict=dict, HttpRequest=lambda **kwargs: kwargs,
                     _SERIALIZER=SimpleNamespace(url=serialize, query=serialize, header=serialize))
        exec("from __future__ import annotations\n" + self.native_functions()["build_delete_request"], scope)
        request = scope["build_delete_request"](**self.native_parameters, **self.subscription_parameters,
                                              headers={"x-ms-client-request-id": "reviewed-request"})
        self.assertEqual(request, dict(method="DELETE",
            url=f"/subscriptions/test-sub/resourceGroups/test-rg/providers/Microsoft.AzureStackHCI/{self.collection}/test-resource",
            params={"api-version": "2024-01-01"},
            headers={"Accept": "application/json", "x-ms-client-request-id": "reviewed-request"}))
        self.assertEqual(len(calls), 5)
        self.assertEqual(calls[0], ("subscription_id", "test-sub", "str", {"min_length": 1}))
        self.assertEqual(calls[1], ("resource_group_name", "test-rg", "str", {"max_length": 90, "min_length": 1}))
        self.assertEqual(calls[2][3]["max_length"], 80)
        self.assertIn("pattern", calls[2][3])


class AzureLocalNICSDKTests(AzureLocalDiskSDKTests):
    manifest = "sdk-nic-source.json"
    native_parameters = {"resource_group_name": "test-rg", "network_interface_name": "test-resource"}
    collection = "networkInterfaces"


if __name__ == "__main__":
    unittest.main()
