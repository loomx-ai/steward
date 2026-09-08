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
