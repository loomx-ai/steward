#!/usr/bin/env python3
"""Offline checks for the Alibaba Cloud official metadata snapshot."""
import hashlib
import importlib.util
import json
from pathlib import Path
import unittest

spec = importlib.util.spec_from_file_location("sync", Path(__file__).with_name("sync-alicloud-catalog.py"))
sync = importlib.util.module_from_spec(spec)
spec.loader.exec_module(sync)

DOCUMENT = {
    "info": {"style": "RPC", "product": "Ecs", "version": "2014-05-26"},
    "endpoints": [{"regionId": "cn-hangzhou", "endpoint": "ecs.cn-hangzhou.aliyuncs.com"}],
    "apis": {
        "DeleteDisk": {
            "methods": ["post", "get"], "deprecated": False, "operationType": "write",
            "systemTags": {"operationType": "delete", "riskType": "high"},
            "parameters": [{"name": "DiskId", "in": "query", "schema": {"type": "string", "required": True, "description": "prose"}}],
            "errorCodes": {"403": [{"errorCode": "IncorrectDiskStatus", "errorMessage": "..."}]},
            "responses": {"200": {"schema": {"description": "dropped"}}},
        },
        "DescribeDisks": {
            "methods": ["get"], "parameters": [],
            "responses": {"200": {"schema": {"type": "object", "properties": {
                "Disks": {"type": "object", "properties": {"Disk": {"type": "array", "items": {"$ref": "#/components/schemas/Disk"}}}},
            }}}},
            "responseDemo": json.dumps([
                {"type": "xml", "example": "<Disks/>"},
                {"type": "json", "example": json.dumps({"Disks": {"Disk": [{"DiskId": "d-a"}]}})},
            ]),
        },
    },
    "components": {"schemas": {"Disk": {"type": "object", "properties": {
        "DiskId": {"type": "string"}, "Parent": {"$ref": "#/components/schemas/Disk"},
    }}}},
}


class SnapshotTest(unittest.TestCase):
    def test_keeps_only_selected_contract_fields(self):
        raw = json.dumps(DOCUMENT).encode()
        result, examples = sync.snapshot("Ecs", "2014-05-26", ["DeleteDisk"], raw)
        self.assertEqual(examples, {})
        self.assertEqual(result["source_sha256"], hashlib.sha256(raw).hexdigest())
        self.assertEqual(result["style"], "RPC")
        self.assertEqual(sorted(result["apis"]), ["DeleteDisk"])
        api = result["apis"]["DeleteDisk"]
        self.assertEqual(api["methods"], ["get", "post"])
        self.assertEqual(api["operation_type"], "delete")
        self.assertEqual(api["parameters"], [{"name": "DiskId", "in": "query", "required": True, "type": "string"}])
        self.assertEqual(api["error_codes"], ["IncorrectDiskStatus"])
        self.assertNotIn("responses", api)
        self.assertEqual(api["response_paths"], [])
        self.assertEqual(result["endpoints"], [{"region": "cn-hangzhou", "endpoint": "ecs.cn-hangzhou.aliyuncs.com"}])

    def test_keeps_response_paths_and_json_examples_of_reads(self):
        result, examples = sync.snapshot("Ecs", "2014-05-26", ["DescribeDisks"], json.dumps(DOCUMENT).encode())
        # References resolve once; a recursive reference does not repeat.
        self.assertEqual(result["apis"]["DescribeDisks"]["response_paths"], ["Disks", "Disks.Disk", "Disks.Disk.DiskId", "Disks.Disk.Parent"])
        self.assertEqual(examples, {"DescribeDisks": {"Disks": {"Disk": [{"DiskId": "d-a"}]}}})

    def test_rejects_operations_missing_from_official_metadata(self):
        with self.assertRaises(sync.MissingOperations):
            sync.snapshot("Ecs", "2014-05-26", ["DeleteDisks"], json.dumps(DOCUMENT).encode())

    def test_selection_uses_operation_id_when_name_is_implicit(self):
        document = {"paths": {"/disks": {"get": {"operationId": "DescribeDisks", "x-operation-call": {"product": "Ecs", "version": "2014-05-26"}}}}}
        self.assertEqual(sync.selection(document), {("Ecs", "2014-05-26"): ["DescribeDisks"]})


if __name__ == "__main__":
    unittest.main()
