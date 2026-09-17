"""Offline checks for the pinned AWS metadata refresh."""
import importlib.util
import io
import json
from pathlib import Path
import unittest
from unittest.mock import patch
import urllib.error

spec = importlib.util.spec_from_file_location("aws_catalog", Path(__file__).with_name("sync-aws-catalog.py"))
catalog = importlib.util.module_from_spec(spec)
spec.loader.exec_module(catalog)

MODEL = {
    "smithy": "2.0",
    "shapes": {
        "com.amazonaws.cloudcontrol#CloudApiService": {
            "type": "service", "version": "2021-09-30",
            "operations": [{"target": "com.amazonaws.cloudcontrol#DeleteResource"}],
            "traits": {"aws.api#service": {"sdkId": "CloudControl", "endpointPrefix": "cloudcontrolapi"}, "aws.protocols#awsJson1_0": {}, "smithy.api#documentation": "long"},
        },
        "com.amazonaws.cloudcontrol#DeleteResource": {
            "type": "operation", "input": {"target": "com.amazonaws.cloudcontrol#DeleteResourceInput"},
            "errors": [{"target": "com.amazonaws.cloudcontrol#Throttling"}],
            "traits": {"smithy.api#documentation": "long", "smithy.api#idempotent": {}},
        },
        "com.amazonaws.cloudcontrol#Unbound": {"type": "operation"},
        "com.amazonaws.cloudcontrol#DeleteResourceInput": {
            "type": "structure",
            "members": {"ClientToken": {"target": "smithy.api#String", "traits": {"smithy.api#idempotencyToken": {}, "smithy.api#documentation": "x"}}},
        },
    },
}


class SelectionTests(unittest.TestCase):
    def test_select_model_keeps_call_traits_and_drops_documentation(self):
        selected = catalog.select_model(MODEL, ["DeleteResource"], "uri", "digest")
        operation = selected["com.amazonaws.cloudcontrol#DeleteResource"]
        self.assertEqual(operation["traits"], {"smithy.api#idempotent": {}})
        self.assertNotIn("errors", operation)
        member = selected["com.amazonaws.cloudcontrol#DeleteResourceInput"]["members"]["ClientToken"]
        self.assertEqual(member["traits"], {"smithy.api#idempotencyToken": {}})
        service = selected["com.amazonaws.cloudcontrol#CloudApiService"]
        self.assertEqual(service["operations"], [{"target": "com.amazonaws.cloudcontrol#DeleteResource"}])
        self.assertNotIn("smithy.api#documentation", service["traits"])

    def test_select_model_rejects_missing_or_unbound_operations(self):
        with self.assertRaisesRegex(ValueError, "no operation"):
            catalog.select_model(MODEL, ["Missing"], "uri", "digest")
        with self.assertRaisesRegex(ValueError, "does not bind"):
            catalog.select_model(MODEL, ["Unbound"], "uri", "digest")

    def test_schema_digest_records_handlers_and_properties(self):
        document = {
            "typeName": "AWS::EKS::Nodegroup", "primaryIdentifier": ["/properties/Id"],
            "properties": {"ClusterName": {"type": "string"}, "Labels": {"$ref": "#/definitions/Labels"}, "Taints": {"type": ["array", "null"]}},
            "handlers": {"list": {"permissions": ["eks:ListNodegroups"], "handlerSchema": {"required": ["ClusterName"]}}, "delete": {"permissions": ["eks:DeleteNodegroup"]}},
            "tagging": {"taggable": True},
        }
        digest = catalog.schema_digest("aws-eks-nodegroup.json", document)
        self.assertEqual(digest["handlers"]["list"]["handlerSchema"], {"required": ["ClusterName"]})
        self.assertEqual(digest["properties"], {"ClusterName": "string", "Labels": "$ref", "Taints": "array|null"})
        self.assertTrue(digest["taggable"])
        self.assertEqual(len(digest["sha256"]), 64)

    def test_sources_must_be_official_and_pinned(self):
        with self.assertRaisesRegex(ValueError, "official"):
            catalog.fetch("https://example.com/models/x.json", catalog.MODEL_HOST)
        with self.assertRaisesRegex(ValueError, "full lowercase SHA"):
            catalog.model_uri("main", "models/ec2/service/2016-11-15/ec2-2016-11-15.json")
        with self.assertRaisesRegex(ValueError, "invalid model path"):
            catalog.model_uri("a" * 40, "../secrets.json")

    def test_transient_errors_are_retried_boundedly(self):
        with patch.object(catalog.urllib.request, "urlopen", side_effect=[urllib.error.URLError("reset"), io.BytesIO(b"{}")]) as open_url, patch.object(catalog.time, "sleep") as sleep:
            self.assertEqual(catalog.fetch("https://raw.githubusercontent.com/aws/api-models-aws/x", catalog.MODEL_HOST), b"{}")
        self.assertEqual(open_url.call_count, 2)
        sleep.assert_called_once_with(1)
        with patch.object(catalog.urllib.request, "urlopen", side_effect=urllib.error.HTTPError("url", 404, "missing", {}, None)) as open_url, patch.object(catalog.time, "sleep"):
            with self.assertRaisesRegex(RuntimeError, "Cannot refresh"):
                catalog.fetch("https://raw.githubusercontent.com/aws/api-models-aws/x", catalog.MODEL_HOST)
        self.assertEqual(open_url.call_count, 1)

    def test_checked_in_selection_is_consistent(self):
        root = Path(__file__).resolve().parents[1] / "providers/aws/catalog/source"
        selection = json.loads((root / "selection.json").read_text())
        smithy = json.loads((root / "smithy.json").read_text())
        schemas = json.loads((root / "cloudformation.json").read_text())
        pinned = {source["uri"] for source in smithy["metadata"]["steward.sources"]}
        for path in selection["api_models"]["models"]:
            self.assertIn(catalog.model_uri(selection["api_models"]["commit"], path), pinned)
        expected = {item["nativeType"] for item in selection["resource_types"] if item["cloudformation"]}
        self.assertEqual(expected, set(schemas["types"]))


if __name__ == "__main__":
    unittest.main()
