"""Offline checks for the official catalog refresh transport and schema closure."""
import importlib.util
import io
import json
from pathlib import Path
import unittest
from unittest.mock import patch
import urllib.error

spec = importlib.util.spec_from_file_location("google_catalog", Path(__file__).with_name("sync-google-catalog.py"))
catalog = importlib.util.module_from_spec(spec)
spec.loader.exec_module(catalog)


class RefreshTests(unittest.TestCase):
    def test_transient_retry_preserves_native_schema_and_source(self):
        fixture = Path(__file__).resolve().parents[1] / "providers/gcp/catalog/source/discovery.json"
        source = next(item for item in json.loads(fixture.read_text())["documents"] if item["document"]["name"] == "compute")
        document = source["document"]
        methods = [method for method in catalog.collect_methods(document) if method["id"] == "compute.regions.list"]
        selected = {"source_uri": source["source_uri"], "methods": [methods[0]["id"]]}
        with patch.object(catalog.urllib.request, "urlopen", side_effect=[urllib.error.URLError("temporary reset"), io.BytesIO(json.dumps(document).encode())]) as open_url, patch.object(catalog.time, "sleep") as sleep:
            result = catalog.fetch(selected)
        self.assertEqual(open_url.call_count, 2)
        sleep.assert_called_once_with(1)
        self.assertEqual(result["source_uri"], selected["source_uri"])
        self.assertEqual(result["document"]["methods"][methods[0]["id"]], methods[0])
        self.assertEqual(len(result["source_sha256"]), 64)

    def test_permanent_error_does_not_retry(self):
        with patch.object(catalog.urllib.request, "urlopen", side_effect=urllib.error.HTTPError("url", 404, "missing", {}, None)) as open_url, patch.object(catalog.time, "sleep") as sleep:
            with self.assertRaisesRegex(RuntimeError, "Cannot refresh"):
                catalog.fetch({"source_uri": "https://compute.googleapis.com/$discovery/rest?version=v1"})
        self.assertEqual(open_url.call_count, 1)
        sleep.assert_not_called()

    def test_retry_is_bounded(self):
        with patch.object(catalog.urllib.request, "urlopen", side_effect=ConnectionResetError("reset")) as open_url, patch.object(catalog.time, "sleep") as sleep:
            with self.assertRaisesRegex(RuntimeError, "Cannot refresh"):
                catalog.fetch({"source_uri": "https://compute.googleapis.com/$discovery/rest?version=v1"})
        self.assertEqual(open_url.call_count, 4)
        self.assertEqual([call.args[0] for call in sleep.call_args_list], [1, 2, 4])


if __name__ == "__main__":
    unittest.main()
