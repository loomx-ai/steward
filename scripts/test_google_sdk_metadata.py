"""Offline conversion checks against pinned official Cloud SDK declarations."""
import json
import hashlib
from pathlib import Path
import unittest
import importlib.util
import tempfile
from unittest.mock import patch
from google_sdk_metadata import convert

ROOT = Path(__file__).resolve().parents[1] / "providers/gcp/catalog/source"


class SDKMetadataTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.selection = json.loads((ROOT / "selection.json").read_text())["sdk_documents"][0]
        cls.client = (ROOT / "sdk/networkservices_v1_client.py").read_text()
        cls.messages = (ROOT / "sdk/networkservices_v1_messages.py").read_text()
        cls.document = convert(cls.client, cls.messages, cls.selection["methods"])

    def test_native_transport_and_message_shapes(self):
        document = self.document
        method = document["methods"]["networkservices.projects.locations.multicastDomains.delete"]
        self.assertEqual(method["httpMethod"], "DELETE")
        self.assertEqual(method["path"], "v1/{+name}")
        self.assertEqual(method["parameters"]["name"]["pattern"], "^projects/[^/]+/locations/[^/]+/multicastDomains/[^/]+$")
        self.assertEqual(method["parameters"]["requestId"]["location"], "query")
        self.assertEqual(method["response"], {"$ref": "Operation"})
        props = document["schemas"]["MulticastDomain"]["properties"]
        self.assertEqual(props["labels"]["$ref"], "MulticastDomain.LabelsValue")
        labels = document["schemas"]["MulticastDomain.LabelsValue"]
        self.assertEqual(labels["additionalProperties"], {"type": "string"})
        self.assertEqual(labels["properties"], {})
        state = document["schemas"]["MulticastResourceState"]["properties"]["state"]
        self.assertIn("DELETE_FAILED", state["enum"])
        items = document["schemas"]["ListMulticastDomainsResponse"]["properties"]["multicastDomains"]
        self.assertEqual(items, {"type": "array", "items": {"$ref": "MulticastDomain"}})
        listing = document["methods"]["networkservices.projects.locations.multicastDomains.list"]
        self.assertEqual(listing["parameters"]["pageSize"]["type"], "integer")

    def test_snapshot_reproduces_sdk_conversion_with_provenance(self):
        stored = next(source for source in json.loads((ROOT / "discovery.json").read_text())["documents"] if source.get("source_format") == "google-cloud-sdk")
        self.assertEqual(stored["document"], self.document)
        self.assertEqual(stored["source_sha256"], self.selection["source_sha256"])
        self.assertEqual(stored["source_uri"], self.selection["source_uri"])

    def test_archive_hash_and_origin_are_checked_before_extracting(self):
        module_spec = importlib.util.spec_from_file_location("refresh_sdk", Path(__file__).with_name("sync-google-catalog.py"))
        refresh = importlib.util.module_from_spec(module_spec)
        module_spec.loader.exec_module(refresh)
        with tempfile.TemporaryDirectory() as directory, patch.object(refresh, "fetch_raw", return_value=b"changed archive") as fetch:
            with self.assertRaisesRegex(ValueError, "checksum mismatch"):
                refresh.fetch_sdk(self.selection, Path(directory))
            fetch.assert_called_once()
            foreign = dict(self.selection, source_uri="https://example.com/sdk.tar.gz")
            with self.assertRaisesRegex(ValueError, "Only pinned official"):
                refresh.fetch_sdk(foreign, Path(directory))
            fetch.assert_called_once()

    def test_unknown_methods_and_malformed_contracts_fail(self):
        with self.assertRaisesRegex(ValueError, "Missing SDK methods"):
            convert(self.client, self.messages, ["networkservices.projects.locations.fake.delete"])
        malformed = self.client.replace("relative_path='v1/{+name}'", "relative_path='v99/{+name}'")
        with self.assertRaisesRegex(ValueError, "flat and relative paths disagree"):
            convert(malformed, self.messages, ["networkservices.projects.locations.multicastDomains.delete"])
        # Parsing source declarations must never import or execute SDK code.
        actual = convert("raise RuntimeError('must never execute')\n" + self.client, self.messages, self.selection["methods"])
        self.assertEqual(actual, self.document)


class SecurityServicesSDKMetadataTests(unittest.TestCase):
    def test_pinned_source_conversion_and_native_contract(self):
        entries = json.loads((ROOT / "selection.json").read_text())["sdk_documents"]
        selected = next(e for e in entries if "/securitycentermanagement/" in e["client_path"])
        client = (ROOT / "sdk" / Path(selected["client_path"]).name).read_text()
        messages = (ROOT / "sdk" / Path(selected["messages_path"]).name).read_text()
        provenance = json.loads((ROOT.parent.parent / "fixtures/security-services/provenance.json").read_text())
        for filename, digest in provenance["files"].items():
            self.assertEqual(hashlib.sha256((ROOT / "sdk" / filename).read_bytes()).hexdigest(), digest)
        document = convert(client, messages, selected["methods"])
        stored = next(e for e in json.loads((ROOT / "discovery.json").read_text())["documents"] if e["document"]["name"] == "securitycentermanagement")
        self.assertEqual(stored["document"], document)
        self.assertEqual(stored["source_sha256"], selected["source_sha256"])
        self.assertEqual(stored["source_format"], "google-cloud-sdk")
        methods = document["methods"]
        listing = methods["securitycentermanagement.projects.locations.securityCenterServices.list"]
        self.assertEqual(listing["httpMethod"], "GET")
        self.assertEqual(listing["path"], "v1/{+parent}/securityCenterServices")
        self.assertEqual(listing["parameters"]["parent"]["pattern"], "^projects/[^/]+/locations/[^/]+$")
        self.assertEqual(listing["parameters"]["showEligibleModulesOnly"]["type"], "boolean")
        for parent in ("folders", "organizations"):
            for method in ("get", "list"):
                operation = methods[f"securitycentermanagement.{parent}.locations.securityCenterServices.{method}"]
                self.assertEqual(operation["httpMethod"], "GET")
                key = "name" if method == "get" else "parent"
                suffix = "/securityCenterServices/[^/]+" if method == "get" else ""
                self.assertEqual(operation["parameters"][key]["pattern"], f"^{parent}/[^/]+/locations/[^/]+{suffix}$")
                self.assertEqual(operation["path"], "v1/{+name}" if method == "get" else "v1/{+parent}/securityCenterServices")
        # The native SDK has no ancestor locations LIST; don't fabricate one.
        for parent in ("folders", "organizations"):
            self.assertNotIn(f"securitycentermanagement.{parent}.locations.list", methods)
        fields = document["schemas"]["SecurityCenterService"]["properties"]
        self.assertIn("INGEST_ONLY", fields["effectiveEnablementState"]["enum"])
        self.assertEqual(document["schemas"]["SecurityCenterService.ModulesValue"]["additionalProperties"], {"$ref": "ModuleSettings"})
        billing = methods["securitycentermanagement.projects.locations.getBillingMetadata"]
        self.assertEqual(billing["httpMethod"], "GET")
        self.assertEqual(billing["path"], "v1/{+name}")
        self.assertEqual(billing["parameters"]["name"]["pattern"], "^projects/[^/]+/locations/[^/]+/billingMetadata$")
        self.assertEqual(billing["response"], {"$ref": "BillingMetadata"})
        self.assertEqual(document["schemas"]["BillingMetadata"]["properties"]["billingTier"]["enum"], ["BILLING_TIER_UNSPECIFIED", "STANDARD", "PREMIUM", "ENTERPRISE"])
        organization_billing = methods["securitycentermanagement.organizations.locations.getBillingMetadata"]
        self.assertEqual(organization_billing["path"], "v1/{+name}")
        self.assertEqual(organization_billing["parameters"]["name"]["pattern"], "^organizations/[^/]+/locations/[^/]+/billingMetadata$")
        self.assertEqual(organization_billing["response"], {"$ref": "BillingMetadata"})
        self.assertNotIn("securitycentermanagement.folders.locations.getBillingMetadata", methods)
        self.assertTrue(all(m["httpMethod"] == "GET" for m in methods.values()))


if __name__ == "__main__":
    unittest.main()
