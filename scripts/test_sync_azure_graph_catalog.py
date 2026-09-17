#!/usr/bin/env python3
import importlib.util
from pathlib import Path
import unittest

spec = importlib.util.spec_from_file_location("sync", Path(__file__).with_name("sync-azure-graph-catalog.py"))
sync = importlib.util.module_from_spec(spec)
spec.loader.exec_module(sync)


class ExtractTest(unittest.TestCase):
    document = {
        "openapi": "3.0.4", "info": {"version": "v1.0"}, "servers": [{"url": "https://graph.microsoft.com/v1.0"}],
        "paths": {
            "/users": {"get": {"operationId": "users.user.ListUser", "parameters": [{"$ref": "#/components/parameters/top"}]}, "post": {"operationId": "users.user.CreateUser"}},
            "/users/{user-id}": {"parameters": [{"name": "user-id", "in": "path", "required": True, "schema": {"type": "string"}}], "get": {"operationId": "users.user.GetUser"}, "delete": {"operationId": "users.user.DeleteUser"}},
            "/admin": {"get": {"operationId": "admin.admin.GetAdmin"}},
        },
        "components": {"parameters": {"top": {"name": "$top", "in": "query"}, "skip": {"name": "$skip", "in": "query"}}, "schemas": {"microsoft.graph.user": {}}},
    }

    def test_keeps_only_selected_operations_and_references(self):
        result = sync.extract(self.document, ["users.user.ListUser", "users.user.GetUser"])
        self.assertEqual(sorted(result["paths"]), ["/users", "/users/{user-id}"])
        self.assertEqual(sorted(result["paths"]["/users"]), ["get"])
        self.assertEqual(sorted(result["paths"]["/users/{user-id}"]), ["get", "parameters"])
        self.assertEqual(result["components"], {"parameters": {"top": {"name": "$top", "in": "query"}}})
        self.assertNotIn("schemas", result["components"])

    def test_rejects_unknown_operations(self):
        with self.assertRaises(ValueError):
            sync.extract(self.document, ["users.user.ListUsers"])


if __name__ == "__main__":
    unittest.main()
