#!/usr/bin/env python3
"""Snapshot selected Azure Swagger operations and their transitive references.

Uses only Python's standard library. Run from any directory, review the source
diff, then run `go generate ./providers/azure` from the repository root. The
ordinary build and catalog tests use the checked-in files without networking.
"""

import copy
import hashlib
import json
from pathlib import Path
import urllib.parse
import urllib.request


def references(value):
    if isinstance(value, dict):
        if "$ref" in value:
            yield value["$ref"]
        for key, child in value.items():
            if key != "x-ms-examples":
                yield from references(child)
    elif isinstance(value, list):
        for child in value:
            yield from references(child)


def main():
    directory = Path(__file__).resolve().parents[1] / "providers/azure/catalog/source"
    selection = json.loads((directory / "selection.json").read_text())
    originals, snapshots, fingerprints = {}, {}, {}

    def fetch(uri):
        parsed = urllib.parse.urlsplit(uri)
        if (parsed.scheme != "https" or parsed.netloc != "raw.githubusercontent.com"
                or not parsed.path.startswith("/Azure/azure-rest-api-specs/")
                or parsed.query or parsed.fragment):
            raise ValueError("Only official Azure REST API specification sources are accepted")
        if uri not in originals:
            with urllib.request.urlopen(uri, timeout=60) as response:
                raw = response.read(32 * 1024 * 1024 + 1)
            if len(raw) > 32 * 1024 * 1024:
                raise ValueError("Swagger source exceeds 32 MiB")
            originals[uri] = json.loads(raw)
            fingerprints[uri] = hashlib.sha256(raw).hexdigest()
        return originals[uri]

    pending = []
    for entry in selection["documents"]:
        uri = entry["source_uri"]
        document = fetch(uri)
        selected = set(entry["operations"])
        found = set()
        snapshot = {key: copy.deepcopy(document[key]) for key in ("swagger", "info", "host", "basePath", "schemes") if key in document}
        for paths_key in ("paths", "x-ms-paths"):
            paths = {}
            for path, item in document.get(paths_key, {}).items():
                operations = {method: copy.deepcopy(operation) for method, operation in item.items()
                              if isinstance(operation, dict) and operation.get("operationId") in selected}
                if not operations:
                    continue
                found.update(op["operationId"] for op in operations.values())
                if "parameters" in item:
                    operations["parameters"] = copy.deepcopy(item["parameters"])
                paths[path] = operations
            if paths:
                snapshot[paths_key] = paths
        if selected != found:
            raise ValueError(f"{uri}: unknown operations {sorted(selected - found)}")
        snapshots[uri] = snapshot
        pending.extend((uri, ref) for ref in references(snapshot))

    visited = set()
    while pending:
        base, reference = pending.pop()
        absolute = urllib.parse.urljoin(base, reference)
        if absolute in visited:
            continue
        visited.add(absolute)
        uri, fragment = urllib.parse.urldefrag(absolute)
        # Keep the original $ref and its referenced object unchanged. Each
        # dependency carries the fingerprint of the full upstream source.
        tokens = [urllib.parse.unquote(part).replace("~1", "/").replace("~0", "~") for part in fragment.lstrip("/").split("/")]
        if len(tokens) != 2 or tokens[0] not in ("parameters", "definitions"):
            raise ValueError(f"Unsupported Swagger reference {absolute}")
        original = fetch(uri)
        value = original[tokens[0]][tokens[1]]
        snapshots.setdefault(uri, {}).setdefault(tokens[0], {})[tokens[1]] = value
        pending.extend((uri, ref) for ref in references(value))

    roots = {entry["source_uri"] for entry in selection["documents"]}
    documents = [{"source_uri": uri, "source_sha256": fingerprints[uri], "dependency": uri not in roots, "document": snapshot}
                 for uri, snapshot in sorted(snapshots.items())]
    keys = {"native_type": "nativeType", "class": "class", "display_name": "displayName", "scope_kinds": "scopeKinds"}
    types = [{target: item[key] for key, target in keys.items()} for item in selection["resource_types"]]
    for target, source in zip(types, selection["resource_types"]):
        target["rest"] = {key: source[key] for key in ("collection", "read_operations", "delete_operations")}
    result = {"documents": documents, "x-resource-types": types}
    (directory / "swagger.json").write_text(json.dumps(result, indent=2, sort_keys=True) + "\n")
    print(f"Saved {len(roots)} API documents and {len(documents) - len(roots)} reference documents")


if __name__ == "__main__":
    main()
