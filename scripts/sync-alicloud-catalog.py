#!/usr/bin/env python3
"""Pin the official Alibaba Cloud API metadata behind Steward's catalog.

Reads every operation declared in `providers/alicloud/catalog/source/openapi.json`,
downloads the official `api-docs.json` for its product and version from
api.aliyun.com, and stores only the selected operations, the product style and
the regional endpoints in `official.json`, each document with its SHA-256.

Uses only the standard library. Run from any directory, review the diff, then
run `go test ./providers/alicloud`. Tests use the checked-in snapshot offline.
"""

import concurrent.futures
import hashlib
import json
from pathlib import Path
import time
import urllib.error
import urllib.request

ROOT = Path(__file__).resolve().parents[1]
SOURCE = ROOT / "providers/alicloud/catalog/source"
EXAMPLES = ROOT / "providers/alicloud/fixtures/official-examples.json"
# Read operations, by official operation type or name, keep their official
# JSON response example as a fixture.
READ_PREFIXES = ("List", "Describe", "Get", "Query")
READ_TYPES = ("get", "list", "read")
MAX_PATH_DEPTH = 8
METADATA = "https://api.aliyun.com/meta/v1/products/{product}/versions/{version}/api-docs.json"


def selection(document):
    """Group the catalog's operations by official product and version."""
    result = {}
    for item in document["paths"].values():
        for operation in item.values():
            call = operation.get("x-operation-call")
            if not call:
                continue
            key = (call["product"], call["version"])
            # The native name is the operation ID's last segment when the
            # catalog does not repeat it separately.
            name = operation.get("x-operation-name") or operation["operationId"].rsplit(".", 1)[-1]
            result.setdefault(key, set()).add(name)
    return {key: sorted(names) for key, names in sorted(result.items())}


def response_paths(schema, components, prefix="", depth=0, seen=()):
    """Dotted field paths of a response schema; array levels are transparent."""
    result = set()
    if not isinstance(schema, dict) or depth > MAX_PATH_DEPTH:
        return result
    reference = schema.get("$ref", "")
    if reference:
        name = reference.rsplit("/", 1)[-1]
        if name in seen:
            return result
        return response_paths(components.get(name, {}), components, prefix, depth, seen + (name,))
    if schema.get("items"):
        result |= response_paths(schema["items"], components, prefix, depth + 1, seen)
    for key, child in (schema.get("properties") or {}).items():
        path = prefix + key
        result.add(path)
        result |= response_paths(child, components, path + ".", depth + 1, seen)
    return result


def json_example(api):
    """The official JSON response example, or None when only XML is published."""
    for demo in json.loads(api.get("responseDemo") or "[]"):
        if demo.get("type") == "json":
            try:
                return json.loads(demo.get("example") or "")
            except json.JSONDecodeError:
                return None
    return None


def prune_api(api, components=None):
    """Keep the contract fields Steward verifies, dropping prose and examples."""
    parameters = []
    for parameter in api.get("parameters", []):
        schema = parameter.get("schema") or {}
        entry = {
            "name": parameter["name"],
            "in": parameter.get("in", ""),
            "required": bool(schema.get("required", parameter.get("required", False))),
            "type": schema.get("type", ""),
        }
        # ROA request bodies carry their fields inside one body parameter.
        if schema.get("properties"):
            entry["properties"] = sorted(schema["properties"])
        parameters.append(entry)
    codes = sorted({
        entry["errorCode"]
        for group in (api.get("errorCodes") or {}).values()
        for entry in group
        if entry.get("errorCode")
    })
    tags = api.get("systemTags") or {}
    return {
        "methods": sorted(method.lower() for method in api.get("methods", [])),
        "path": api.get("path", ""),
        "deprecated": bool(api.get("deprecated", False)),
        "operation_type": tags.get("operationType", api.get("operationType", "")),
        "risk_type": tags.get("riskType", ""),
        "parameters": sorted(parameters, key=lambda value: value["name"]),
        "error_codes": codes,
        "response_paths": sorted(response_paths(
            ((api.get("responses") or {}).get("200") or {}).get("schema") or {}, components or {},
        )),
    }


class MissingOperations(ValueError):
    def __init__(self, product, version, names):
        super().__init__(f"{product} {version}: operations absent from official metadata: {names}")
        self.product, self.version, self.names = product, version, names


def snapshot(product, version, names, raw):
    document = json.loads(raw)
    apis = document.get("apis", {})
    missing = [name for name in names if name not in apis]
    if missing:
        raise MissingOperations(product, version, missing)
    endpoints = sorted(
        ({"region": entry["regionId"], "endpoint": entry.get("endpoint") or entry.get("public", "")}
         for entry in document.get("endpoints", []) if entry.get("regionId")),
        key=lambda value: value["region"],
    )
    components = (document.get("components") or {}).get("schemas") or {}
    examples = {}
    for name in names:
        tags = apis[name].get("systemTags") or {}
        if name.startswith(READ_PREFIXES) or tags.get("operationType", apis[name].get("operationType")) in READ_TYPES:
            example = json_example(apis[name])
            if example is not None:
                examples[name] = example
    return {
        "product": product,
        "version": version,
        "source_uri": METADATA.format(product=product, version=version),
        "source_sha256": hashlib.sha256(raw).hexdigest(),
        "style": (document.get("info") or {}).get("style", ""),
        "endpoints": endpoints,
        "apis": {name: prune_api(apis[name], components) for name in names},
    }, examples


def fetch(uri):
    for attempt in range(4):
        try:
            with urllib.request.urlopen(uri, timeout=120) as response:
                return response.read()
        except (urllib.error.URLError, TimeoutError):
            if attempt == 3:
                raise
            time.sleep(2 ** attempt)


def main():
    document = json.loads((SOURCE / "openapi.json").read_text())
    selected = selection(document)
    # Operations the portal does not describe are pinned to Alibaba Cloud's own
    # SDK source in official-exceptions.json instead.
    exceptions = json.loads((SOURCE / "official-exceptions.json").read_text())["exceptions"]
    excluded = {(item["product"], item["version"], item["operation"]) for item in exceptions}
    selected = {
        key: [name for name in names if (key[0], key[1], name) not in excluded]
        for key, names in selected.items()
    }
    selected = {key: names for key, names in selected.items() if names}

    def build(item):
        (product, version), names = item
        return snapshot(product, version, names, fetch(METADATA.format(product=product, version=version)))

    def attempt(item):
        try:
            return build(item), None
        except MissingOperations as error:
            return None, error

    with concurrent.futures.ThreadPoolExecutor(max_workers=6) as executor:
        results = list(executor.map(attempt, selected.items()))
    errors = [error for _, error in results if error]
    if errors:
        # Report every discrepancy at once; an unofficial operation must be
        # fixed in the catalog rather than pinned.
        raise SystemExit("\n".join(str(error) for error in errors))
    products = [product for (product, _), _ in results]
    output = {"source": "https://api.aliyun.com/meta/v1", "products": products}
    (SOURCE / "official.json").write_text(json.dumps(output, indent=1, sort_keys=True, ensure_ascii=False) + "\n")
    # Unchanged official response examples, keyed like the catalog tests.
    examples = {
        "source": "https://api.aliyun.com/meta/v1 responseDemo",
        "examples": {
            f"{product['product']}@{product['version']}#{name}": example
            for (product, found), _ in results
            for name, example in found.items()
        },
    }
    EXAMPLES.write_text(json.dumps(examples, indent=1, sort_keys=True, ensure_ascii=False) + "\n")
    operations = sum(len(product["apis"]) for product in products)
    print(f"Pinned {operations} operations and {len(examples['examples'])} response examples "
          f"from {len(products)} official product documents")


if __name__ == "__main__":
    main()
