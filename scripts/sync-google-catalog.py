#!/usr/bin/env python3
"""Refresh selected native Google Discovery fragments (Python standard library).

Run from the repository root, review the metadata diff, then run go generate
./providers/gcp. Normal builds and catalog verification never use the network.
"""

import concurrent.futures
import hashlib
import io
import json
from pathlib import Path
import tarfile
import time
import urllib.error
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


def fetch_raw(uri, limit=32 * 1024 * 1024):
    for attempt in range(4):
        try:
            with urllib.request.urlopen(uri, timeout=60) as response:
                raw = response.read(limit + 1)
            break
        except (urllib.error.URLError, TimeoutError, ConnectionError) as error:
            transient = not isinstance(error, urllib.error.HTTPError) or error.code == 429 or error.code >= 500
            if not transient or attempt == 3:
                raise RuntimeError(f"Cannot refresh {uri}: {error}") from error
            time.sleep(2 ** attempt)
    if len(raw) > limit:
        raise ValueError("Google metadata response exceeds size limit")
    return raw


def fetch(selection):
    uri = selection["source_uri"]
    origin = urllib.parse.urlsplit(uri)
    if origin.scheme != "https" or not origin.hostname.endswith(".googleapis.com") or origin.username or origin.port:
        raise ValueError("Only official Google Discovery sources are accepted")
    raw = fetch_raw(uri)
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


def fetch_sdk(selection, directory):
    from google_sdk_metadata import convert
    uri = selection["source_uri"]
    origin = urllib.parse.urlsplit(uri)
    if origin.scheme != "https" or origin.netloc != "dl.google.com" or not origin.path.startswith("/dl/cloudsdk/channels/rapid/components/google-cloud-sdk-core-") or origin.query or origin.fragment:
        raise ValueError("Only pinned official Cloud SDK core archives are accepted")
    raw = fetch_raw(uri, 64 * 1024 * 1024)
    if hashlib.sha256(raw).hexdigest() != selection["source_sha256"]:
        raise ValueError("Cloud SDK archive checksum mismatch")
    sources = []
    directory.mkdir(parents=True, exist_ok=True)
    with tarfile.open(fileobj=io.BytesIO(raw), mode="r:gz") as archive:
        for key in ("client_path", "messages_path"):
            member = archive.getmember(selection[key])
            if not member.isfile() or member.size > 8 * 1024 * 1024:
                raise ValueError("Invalid Cloud SDK source member")
            source = archive.extractfile(member).read()
            (directory / Path(member.name).name).write_bytes(source)
            sources.append(source.decode("utf-8"))
        (directory / "LICENSE").write_bytes(archive.extractfile("LICENSE").read())
    document = convert(*sources, selection["methods"])
    return {"source_uri": uri, "source_sha256": selection["source_sha256"], "source_format": "google-cloud-sdk", "document": document}


def main():
    directory = Path(__file__).resolve().parents[1] / "providers/gcp/catalog/source"
    selection = json.loads((directory / "selection.json").read_text())
    with concurrent.futures.ThreadPoolExecutor(max_workers=6) as pool:
        documents = list(pool.map(fetch, selection["documents"]))
    documents.extend(fetch_sdk(entry, directory / "sdk") for entry in selection.get("sdk_documents", []))
    keys = {"native_type": "nativeType", "class": "class", "display_name": "displayName", "scope_kinds": "scopeKinds"}
    types = [{target: item[key] for key, target in keys.items()} for item in selection["resource_types"]]
    for target, source in zip(types, selection["resource_types"]):
        target["rest"] = {key: source[key] for key in ("collection", "read_operations", "delete_operations", "list_operations") if key in source}
    result = {"documents": documents, "x-resource-types": types}
    (directory / "discovery.json").write_text(json.dumps(result, indent=2, sort_keys=True) + "\n")
    print(f"Saved {len(documents)} official metadata sources with {sum(len(d['document']['methods']) for d in documents)} methods")


if __name__ == "__main__":
    main()
