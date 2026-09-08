#!/usr/bin/env python3
"""Refresh selected native Google Discovery fragments (Python standard library).

Run from the repository root, review the metadata diff, then run go generate
./providers/gcp. Normal builds and catalog verification never use the network.
"""

import concurrent.futures
import hashlib
import json
from pathlib import Path
import urllib.request
import urllib.parse


def collect_methods(resource):
    for method in resource.get("methods", {}).values():
        yield method
    for child in resource.get("resources", {}).values():
        yield from collect_methods(child)


def references(value):
    if isinstance(value, dict):
        if "$ref" in value:
            yield value["$ref"]
        for child in value.values():
            yield from references(child)
    elif isinstance(value, list):
        for child in value:
            yield from references(child)


def fetch(selection):
    uri = selection["source_uri"]
    origin = urllib.parse.urlsplit(uri)
    if origin.scheme != "https" or not origin.hostname.endswith(".googleapis.com") or origin.username or origin.port:
        raise ValueError("Only official Google Discovery sources are accepted")
    with urllib.request.urlopen(uri, timeout=60) as response:
        raw = response.read(32 * 1024 * 1024 + 1)
    if len(raw) > 32 * 1024 * 1024:
        raise ValueError("Discovery response exceeds 32 MiB")
    source = json.loads(raw)
    available = {method["id"]: method for method in collect_methods(source)}
    missing = set(selection["methods"]) - available.keys()
    if missing:
        raise ValueError(f"{uri}: unknown methods {sorted(missing)}")
    methods = {key: available[key] for key in selection["methods"]}
    schemas = {}
    pending = set(references(methods))
    while pending:
        name = pending.pop()
        if name in schemas:
            continue
        schemas[name] = source["schemas"][name]
        pending.update(references(schemas[name]))
    # Method nesting is immaterial to Discovery; IDs and all method/schema
    # objects are retained verbatim. The source hash identifies the full file.
    document = {key: source[key] for key in ("name", "version", "rootUrl", "servicePath")}
    document.update(methods=methods, schemas=schemas)
    if "revision" in source:
        document["revision"] = source["revision"]
    return {"source_uri": uri, "source_sha256": hashlib.sha256(raw).hexdigest(), "document": document}


def main():
    directory = Path(__file__).resolve().parents[1] / "providers/gcp/catalog/source"
    selection = json.loads((directory / "selection.json").read_text())
    with concurrent.futures.ThreadPoolExecutor(max_workers=6) as pool:
        documents = list(pool.map(fetch, selection["documents"]))
    keys = {"native_type": "nativeType", "class": "class", "display_name": "displayName", "scope_kinds": "scopeKinds"}
    types = [{target: item[key] for key, target in keys.items()} for item in selection["resource_types"]]
    for target, source in zip(types, selection["resource_types"]):
        target["rest"] = {key: source[key] for key in ("collection", "read_operations", "delete_operations")}
    result = {"documents": documents, "x-resource-types": types}
    (directory / "discovery.json").write_text(json.dumps(result, indent=2, sort_keys=True) + "\n")
    print(f"Saved {len(documents)} official documents with {sum(len(d['document']['methods']) for d in documents)} methods")


if __name__ == "__main__":
    main()
