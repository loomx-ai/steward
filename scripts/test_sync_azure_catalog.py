"""Offline regression checks for Azure native source selection and refresh."""
import hashlib
import importlib.util
import io
import json
from pathlib import Path
import unittest
from unittest.mock import patch
import urllib.error

spec = importlib.util.spec_from_file_location("azure_catalog", Path(__file__).with_name("sync-azure-catalog.py"))
catalog = importlib.util.module_from_spec(spec)
spec.loader.exec_module(catalog)
root = Path(__file__).resolve().parents[1] / "providers/azure/catalog/source"


class AzureRefreshTests(unittest.TestCase):
    def test_polymorphic_subtypes_and_their_transitive_references_are_preserved(self):
        base_uri = "https://raw.githubusercontent.com/Azure/azure-rest-api-specs/main/specification/base.json"
        child_uri = base_uri.replace("base.json", "child.json")
        dependency_uri = base_uri.replace("base.json", "properties.json")
        originals = {
            base_uri: {"swagger": "2.0", "info": {"title": "Base", "version": "1"},
                       "paths": {"/base": {"get": {"operationId": "GetBase", "responses": {"200": {"schema": {"$ref": "#/definitions/Base"}}}}}},
                       "definitions": {"Base": {"type": "object", "discriminator": "type", "properties": {"type": {"type": "string"}}},
                                       "Unused": {"type": "object"}}},
            child_uri: {"swagger": "2.0", "info": {"title": "Child", "version": "1"},
                        "paths": {"/other": {"get": {"operationId": "GetOther", "responses": {"200": {"schema": {"type": "object"}}}}}},
                        "definitions": {
                            "Entra": {"allOf": [{"$ref": "base.json#/definitions/Base"}], "x-ms-discriminator-value": "Entra"},
                            "Nested": {"allOf": [{"$ref": "#/definitions/Entra"}], "properties": {"config": {"$ref": "properties.json#/definitions/Config"}}}}},
            # Indexing a newly loaded dependency must also preserve subtypes of
            # a discriminator that was already visited, regardless of order.
            dependency_uri: {"definitions": {
                "Config": {"type": "object", "required": ["principalType"], "properties": {"principalType": {"type": "string", "enum": ["user"]}}},
                "Late": {"allOf": [{"$ref": "base.json#/definitions/Base"}], "x-ms-discriminator-value": "Late"}}},
        }
        selection = {"documents": [{"source_uri": base_uri, "operations": ["GetBase"]},
                                   {"source_uri": child_uri, "operations": ["GetOther"]}], "resource_types": []}
        def fetch(uri):
            return json.dumps(originals[uri]).encode()
        with patch.object(catalog, "fetch_source", side_effect=fetch):
            result = catalog.snapshot(selection)
        by_uri = {entry["source_uri"]: entry for entry in result["documents"]}
        self.assertNotIn("Unused", by_uri[base_uri]["document"]["definitions"])
        for uri, names in [(base_uri, ["Base"]), (child_uri, ["Entra", "Nested"]), (dependency_uri, ["Config", "Late"])]:
            for name in names:
                self.assertEqual(by_uri[uri]["document"]["definitions"][name], originals[uri]["definitions"][name])
            self.assertEqual(by_uri[uri]["source_sha256"], hashlib.sha256(fetch(uri)).hexdigest())

    def test_native_fragments_and_recursive_references_are_unchanged(self):
        selected = json.loads((root / "selection.json").read_text())
        checked_in = json.loads((root / "swagger.json").read_text())
        originals = {item["source_uri"]: item["document"] for item in checked_in["documents"]}
        def fetch(uri):
            self.assertIn(uri, originals)
            return json.dumps(originals[uri]).encode()
        with patch.object(catalog, "fetch_source", side_effect=fetch):
            result = catalog.snapshot(selected)
        self.assertEqual(result["x-resource-types"], checked_in["x-resource-types"])
        self.assertEqual(len(result["documents"]), len(checked_in["documents"]))
        for actual, expected in zip(result["documents"], checked_in["documents"]):
            self.assertEqual(actual["source_uri"], expected["source_uri"])
            self.assertEqual(actual["document"], expected["document"])
            self.assertEqual(actual["dependency"], expected["dependency"])
            self.assertEqual(actual["source_sha256"], hashlib.sha256(fetch(actual["source_uri"])).hexdigest())

    def test_retry_is_bounded_and_only_transient(self):
        uri = "https://raw.githubusercontent.com/Azure/azure-rest-api-specs/main/specification/example.json"
        for failure, attempts in [(ConnectionResetError("reset"), 4), (urllib.error.HTTPError(uri, 429, "throttle", {}, None), 4), (urllib.error.HTTPError(uri, 404, "missing", {}, None), 1)]:
            with self.subTest(failure=failure), patch.object(catalog.urllib.request, "urlopen", side_effect=failure) as fetch, patch.object(catalog.time, "sleep") as sleep:
                with self.assertRaisesRegex(RuntimeError, "Cannot refresh"):
                    catalog.fetch_source(uri)
                self.assertEqual(fetch.call_count, attempts)
                self.assertEqual(sleep.call_count, attempts - 1)
        with patch.object(catalog.urllib.request, "urlopen", side_effect=[TimeoutError("timeout"), io.BytesIO(b'{"native":true}')]), patch.object(catalog.time, "sleep"):
            self.assertEqual(catalog.fetch_source(uri), b'{"native":true}')

    def test_untrusted_source_is_rejected_before_network(self):
        for uri in ["https://evil.invalid/spec.json", "http://raw.githubusercontent.com/Azure/azure-rest-api-specs/main/spec.json", "https://raw.githubusercontent.com/fork/azure-rest-api-specs/main/spec.json"]:
            with patch.object(catalog.urllib.request, "urlopen") as fetch:
                with self.assertRaisesRegex(ValueError, "official Azure"):
                    catalog.fetch_source(uri)
                fetch.assert_not_called()


if __name__ == "__main__":
    unittest.main()
