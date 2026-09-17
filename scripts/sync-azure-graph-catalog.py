#!/usr/bin/env python3
"""Snapshot selected Microsoft Graph v1.0 operations into the Azure catalog.

Entra ID users and groups are Microsoft Graph objects, not ARM resources. The
official OpenAPI description is pinned by commit and SHA-256; only the selected
operations and the parameters they reference are stored, unchanged. Requires
PyYAML. Run from any directory, review the diff, then run
`go generate ./providers/azure` from the repository root.
"""

import hashlib
import json
from pathlib import Path
import urllib.request

SOURCE_URI = "https://raw.githubusercontent.com/microsoftgraph/msgraph-metadata/28e4d24f3547898328eb52c700f2beb869116cfe/openapi/v1.0/openapi.yaml"
SOURCE_SHA256 = "77c1a39c94ab0a72c0a2e07ff74f221903141e8913e748fddcc189b98da42feb"
SOURCE_FORMAT = "msgraph-openapi3"
OPERATIONS = [
    "groups.ListMembers",
    "groups.group.GetGroup",
    "groups.group.ListGroup",
    "users.user.GetUser",
    "users.user.ListUser",
]
METHODS = ("get", "put", "post", "patch", "delete", "head", "options")


def extract(document, operations):
    selected = set(operations)
    found = set()
    snapshot = {key: document[key] for key in ("openapi", "info", "servers") if key in document}
    paths = {}
    parameters = {}
    for path, item in document.get("paths", {}).items():
        chosen = {method: operation for method, operation in item.items()
                  if method in METHODS and isinstance(operation, dict) and operation.get("operationId") in selected}
        if not chosen:
            continue
        if "parameters" in item:
            chosen["parameters"] = item["parameters"]
        for method, operation in chosen.items():
            values = operation if method == "parameters" else operation.get("parameters", [])
            if method != "parameters":
                found.add(operation["operationId"])
            for parameter in values:
                reference = parameter.get("$ref", "")
                if reference.startswith("#/components/parameters/"):
                    name = reference.rsplit("/", 1)[1]
                    parameters[name] = document["components"]["parameters"][name]
                elif reference:
                    raise ValueError(f"unsupported parameter reference {reference}")
        paths[path] = chosen
    if found != selected:
        raise ValueError(f"unknown Microsoft Graph operations {sorted(selected - found)}")
    snapshot["paths"] = paths
    snapshot["components"] = {"parameters": dict(sorted(parameters.items()))}
    return snapshot


def main():
    import yaml

    with urllib.request.urlopen(SOURCE_URI, timeout=300) as response:
        raw = response.read()
    if hashlib.sha256(raw).hexdigest() != SOURCE_SHA256:
        raise SystemExit("Microsoft Graph OpenAPI description changed; review and update the pin")
    loader = getattr(yaml, "CSafeLoader", yaml.SafeLoader)
    document = yaml.load(raw, Loader=loader)
    path = Path(__file__).resolve().parents[1] / "providers/azure/catalog/source/swagger.json"
    catalog = json.loads(path.read_text())
    catalog["documents"] = [value for value in catalog["documents"] if value["source_uri"] != SOURCE_URI]
    catalog["documents"].append({"source_uri": SOURCE_URI, "source_sha256": SOURCE_SHA256, "source_format": SOURCE_FORMAT, "document": extract(document, OPERATIONS)})
    catalog["documents"].sort(key=lambda value: value["source_uri"])
    path.write_text(json.dumps(catalog, indent=2, sort_keys=True) + "\n")
    print(f"Saved {len(OPERATIONS)} Microsoft Graph operations")


if __name__ == "__main__":
    main()
