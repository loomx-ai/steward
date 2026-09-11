#!/usr/bin/env python3
"""Retain representative native Fleet DELETE polls from pinned CLI YAML files.

Requires PyYAML. Pass the unchanged hubful and hubless recording files; their
SHA-256s must match cli-recordings.json before any output is written.
"""

import argparse
import hashlib
import json
from pathlib import Path
from urllib.parse import urlsplit

import yaml


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("recordings", nargs=2, type=Path)
    args = parser.parse_args()
    directory = Path(__file__).parent
    sources = json.loads((directory / "cli-recordings.json").read_text())["sources"]
    inputs = {}
    for path in args.recordings:
        payload = path.read_bytes()
        inputs[hashlib.sha256(payload).hexdigest()] = payload
    if set(inputs) != {source["source_sha256"] for source in sources}:
        raise ValueError("recording files do not match the pinned source hashes")
    records = []
    headers_to_keep = {"content-type", "location", "azure-asyncoperation", "operation-location", "retry-after", "x-ms-request-id", "x-ms-correlation-request-id"}
    for source in sources:
        rows = yaml.safe_load(inputs[source["source_sha256"]])["interactions"]
        operations, seen = set(), set()
        for index, row in enumerate(rows):
            request, response = row["request"], row["response"]
            path = urlsplit(request["uri"]).path.lower()
            if request["method"] == "DELETE" and "/providers/microsoft.containerservice/fleets/" in path:
                for key, values in response["headers"].items():
                    if key.lower() in {"location", "azure-asyncoperation"}:
                        operations.update(urlsplit(value).path.rsplit("/", 1)[1].lower() for value in values)
            if request["method"] != "GET" or "/providers/microsoft.containerservice/locations/" not in path:
                continue
            collection, operation = path.rsplit("/", 2)[1:]
            if operation not in operations or collection not in {"operations", "operationresults"}:
                continue
            body = response["body"]["string"]
            state = json.loads(body).get("status", "") if body else ""
            key = (operation, collection, response["status"]["code"], state)
            if key in seen:
                continue  # Keep the first pending and each distinct terminal envelope.
            seen.add(key)
            records.append({**source, "interaction": index, "method": "GET", "url": request["uri"], "status": response["status"]["code"], "headers": {key: value for key, value in response["headers"].items() if key.lower() in headers_to_keep}, "body": body})
    if len(records) != 13:
        raise ValueError(f"unexpected representative poll count: {len(records)}")
    output = directory / "cli-deletion-polls.json"
    output.write_text(json.dumps({"sources": sources, "responses": records}, indent=2, sort_keys=True) + "\n")
    print(hashlib.sha256(output.read_bytes()).hexdigest())


if __name__ == "__main__":
    main()
