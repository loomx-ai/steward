#!/usr/bin/env python3
"""Refresh pinned AWS API metadata (Python standard library).

Run from the repository root, review the metadata diff, then run
go generate ./providers/aws. Normal builds and catalog verification never use
the network.

Inputs:  providers/aws/catalog/source/selection.json
Outputs: providers/aws/catalog/source/smithy.json
         providers/aws/catalog/source/cloudformation.json
"""

import hashlib
import io
import json
from pathlib import Path
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
import zipfile

ROOT = Path("providers/aws/catalog/source")
MODEL_HOST = "raw.githubusercontent.com"
MODEL_REPOSITORY = "aws/api-models-aws"
SCHEMA_HOST = "schema.cloudformation.us-east-1.amazonaws.com"
LIMIT = 64 * 1024 * 1024


def fetch(uri, host):
    origin = urllib.parse.urlsplit(uri)
    if origin.scheme != "https" or origin.hostname != host or origin.username or origin.port:
        raise ValueError(f"Only official AWS metadata sources are accepted: {uri}")
    for attempt in range(4):
        try:
            with urllib.request.urlopen(uri, timeout=120) as response:
                raw = response.read(LIMIT + 1)
            break
        except (urllib.error.URLError, TimeoutError, ConnectionError) as error:
            transient = not isinstance(error, urllib.error.HTTPError) or error.code == 429 or error.code >= 500
            if not transient or attempt == 3:
                raise RuntimeError(f"Cannot refresh {uri}: {error}") from error
            time.sleep(2 ** attempt)
    if len(raw) > LIMIT:
        raise ValueError(f"AWS metadata response exceeds size limit: {uri}")
    return raw


def model_uri(commit, path):
    if len(commit) != 40 or any(c not in "0123456789abcdef" for c in commit):
        raise ValueError("api-models-aws commit must be a full lowercase SHA")
    if not path.startswith("models/") or ".." in path.split("/"):
        raise ValueError(f"invalid model path {path}")
    return f"https://{MODEL_HOST}/{MODEL_REPOSITORY}/{commit}/{path}"


def target(member):
    return member.get("target", "") if isinstance(member, dict) else ""


def select_model(model, operations, uri, digest):
    shapes = model["shapes"]
    services = [shape_id for shape_id, shape in shapes.items() if shape.get("type") == "service"]
    if len(services) != 1:
        raise ValueError(f"{uri} must define exactly one service")
    service_id = services[0]
    service = shapes[service_id]
    namespace = service_id.split("#", 1)[0]
    listed = {target(item) for item in service.get("operations", [])}
    for resource in service.get("resources", []):
        stack = [target(resource)]
        while stack:
            current = shapes.get(stack.pop(), {})
            for key in ("create", "put", "read", "update", "delete", "list"):
                if current.get(key):
                    listed.add(target(current[key]))
            listed.update(target(item) for item in current.get("operations", []))
            listed.update(target(item) for item in current.get("collectionOperations", []))
            stack.extend(target(item) for item in current.get("resources", []))
    selected = {}
    for name in sorted(set(operations)):
        operation_id = f"{namespace}#{name}"
        operation = shapes.get(operation_id)
        if operation is None or operation.get("type") != "operation":
            raise ValueError(f"{uri} has no operation {name}")
        if operation_id not in listed:
            raise ValueError(f"{uri} service does not bind operation {name}")
        selected[operation_id] = {
            "type": "operation",
            "input": operation.get("input", {}),
            "output": operation.get("output", {}),
            "traits": {
                trait: value
                for trait, value in operation.get("traits", {}).items()
                if trait in ("smithy.api#http", "smithy.api#paginated", "smithy.api#idempotent", "smithy.api#readonly")
            },
        }
        for key in ("input", "output"):
            structure_id = target(operation.get(key))
            if structure_id and structure_id in shapes:
                structure = shapes[structure_id]
                selected[structure_id] = {
                    "type": structure.get("type"),
                    "members": {
                        member_name: {
                            "target": member.get("target"),
                            "traits": {
                                trait: value
                                for trait, value in member.get("traits", {}).items()
                                if trait in ("smithy.api#required", "smithy.api#idempotencyToken", "smithy.api#httpQuery", "smithy.api#httpLabel", "smithy.api#httpHeader", "smithy.api#jsonName", "aws.protocols#ec2QueryName")
                            },
                        }
                        for member_name, member in sorted(structure.get("members", {}).items())
                    },
                }
    traits = {
        trait: value
        for trait, value in service.get("traits", {}).items()
        if trait.startswith("aws.protocols#") or trait in ("aws.api#service", "aws.auth#sigv4", "smithy.api#paginated")
    }
    selected[service_id] = {
        "type": "service",
        "version": service.get("version", ""),
        "operations": [{"target": operation_id} for operation_id in sorted(k for k, v in selected.items() if v.get("type") == "operation")],
        "traits": traits,
    }
    return selected


def schema_digest(member, document):
    handlers = {}
    for name, handler in sorted(document.get("handlers", {}).items()):
        entry = {"permissions": sorted(handler.get("permissions", []))}
        if "handlerSchema" in handler:
            entry["handlerSchema"] = handler["handlerSchema"]
        handlers[name] = entry
    properties = {}
    for name, definition in sorted(document.get("properties", {}).items()):
        kind = definition.get("type")
        if isinstance(kind, list):
            kind = "|".join(sorted(kind))
        properties[name] = kind or ("$ref" if "$ref" in definition else "unknown")
    return {
        "member": member,
        "sha256": hashlib.sha256(json.dumps(document, sort_keys=True).encode()).hexdigest(),
        "primaryIdentifier": document.get("primaryIdentifier", []),
        "handlers": handlers,
        "taggable": bool(document.get("tagging", {}).get("taggable", False)),
        "properties": properties,
        "readOnlyProperties": sorted(document.get("readOnlyProperties", [])),
    }


def main():
    selection = json.loads((ROOT / "selection.json").read_text())
    commit = selection["api_models"]["commit"]
    shapes = {}
    sources = []
    for path, operations in sorted(selection["api_models"]["models"].items()):
        uri = model_uri(commit, path)
        raw = fetch(uri, MODEL_HOST)
        digest = hashlib.sha256(raw).hexdigest()
        for shape_id, shape in select_model(json.loads(raw), operations, uri, digest).items():
            if shape_id in shapes and shapes[shape_id] != shape:
                raise ValueError(f"conflicting shape {shape_id}")
            shapes[shape_id] = shape
        sources.append({"uri": uri, "sha256": digest})

    archive_uri = selection["cloudformation"]["archive"]
    archive = fetch(archive_uri, SCHEMA_HOST)
    documents = {}
    with zipfile.ZipFile(io.BytesIO(archive)) as bundle:
        for member in sorted(bundle.namelist()):
            if member.endswith(".json"):
                document = json.loads(bundle.read(member))
                documents[document["typeName"]] = (member, document)
    types = {}
    for resource in selection["resource_types"]:
        name = resource["nativeType"]
        if not resource.get("cloudformation", True):
            continue
        if name not in documents:
            raise ValueError(f"CloudFormation schema archive has no {name}")
        types[name] = schema_digest(*documents[name])

    smithy = {
        "smithy": "2.0",
        "metadata": {"steward.sources": sources},
        "shapes": dict(sorted(shapes.items())),
        "x-resource-types": [
            {key: resource[key] for key in ("nativeType", "class", "displayName", "scopeKinds")}
            for resource in sorted(selection["resource_types"], key=lambda item: item["nativeType"])
        ],
    }
    cloudformation = {
        "source": {"uri": archive_uri, "sha256": hashlib.sha256(archive).hexdigest()},
        "types": dict(sorted(types.items())),
    }
    (ROOT / "smithy.json").write_text(json.dumps(smithy, indent=1, sort_keys=False, ensure_ascii=False) + "\n")
    (ROOT / "cloudformation.json").write_text(json.dumps(cloudformation, indent=1, sort_keys=False, ensure_ascii=False) + "\n")
    print(f"selected {sum(1 for s in shapes.values() if s.get('type') == 'operation')} operations and {len(types)} CloudFormation schemas")


if __name__ == "__main__":
    sys.exit(main())
