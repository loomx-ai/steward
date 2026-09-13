"""Offline checks of unchanged Microsoft SDK network model declarations.

The fragments are parsed as AST, not imported as an installed SDK. They
establish wire-field and enum provenance, not live service behavior.
"""
import ast
import hashlib
import json
from pathlib import Path
import unittest

FIXTURES = Path(__file__).resolve().parents[1] / "providers/azure/fixtures/azure-local"


class AzureLocalNetworkModelTests(unittest.TestCase):
    def test_native_read_only_discriminator(self):
        source = json.loads((FIXTURES / "network-models-source.json").read_text())
        declarations = {}
        for fragment in source["fragments"]:
            with self.subTest(file=fragment["file"]):
                body = (FIXTURES / fragment["file"]).read_bytes()
                self.assertEqual(hashlib.sha256(body).hexdigest(), fragment["sha256"])
                self.assertEqual(len(body.splitlines()), fragment["end_line"] - fragment["start_line"] + 1)
                node, = ast.parse(body).body
                self.assertIsInstance(node, ast.ClassDef)
                self.assertEqual(node.name, fragment["class_name"])
                declarations[fragment["file"]] = {
                    target.id: ast.literal_eval(stmt.value)
                    for stmt in node.body if isinstance(stmt, ast.Assign)
                    for target in stmt.targets if isinstance(target, ast.Name)
                }
        self.assertEqual(len(declarations), 3)
        old = declarations["sdk_network_2024_properties.py.txt"]
        current = declarations["sdk_network_preview_properties.py.txt"]
        self.assertNotIn("network_type", old["_attribute_map"])
        self.assertEqual(current["_attribute_map"]["network_type"], {"key": "networkType", "type": "str"})
        self.assertEqual(current["_validation"]["network_type"], {"readonly": True})
        self.assertEqual(declarations["sdk_network_preview_enum.py.txt"], {
            "WORKLOAD": "Workload", "INFRASTRUCTURE": "Infrastructure"})

    def test_historical_native_examples_remain_unchanged(self):
        originals = json.loads((FIXTURES / "network-2024-sources.json").read_text())
        self.assertEqual(len(originals), 4)
        for original in originals:
            body = (FIXTURES / original["file"]).read_bytes()
            self.assertEqual(hashlib.sha256(body).hexdigest(), original["source_sha256"])
            self.assertEqual(json.loads(body)["parameters"]["api-version"], "2024-01-01")


if __name__ == "__main__":
    unittest.main()
