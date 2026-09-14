#!/usr/bin/env python3
"""Reproduce preview-version cancellation evidence (not stable wire fixtures) (requires PyYAML).

Pass a directory containing the two original YAML files to run offline; omit it
to download the pinned files. No request credentials or bodies are extracted.
"""
import hashlib
import json
from pathlib import Path
import sys
import urllib.request
import yaml

ROOT = "https://raw.githubusercontent.com/Azure/azure-cli/c683a64f397974bae397d77a204e2ae86a908fa0/src/azure-cli/azure/cli/command_modules/synapse/tests/latest/recordings/"
SOURCES = {
    "test_spark_job.yaml": ("431c4ca2e96f5e5da72d397bda257033ef8179f14e2c175f51164369c2909d94", [3, 4]),
    "test_spark_session_and_statements.yaml": ("b64405d9ea86a3a0ac1bca1d8d4abdc550d19098f1964d3fa44c680cdf02076f", [8, 9]),
}
result = []
for filename, (fingerprint, indices) in SOURCES.items():
    if len(sys.argv) > 1:
        raw = (Path(sys.argv[1]) / filename).read_bytes()
    else:
        with urllib.request.urlopen(ROOT + filename, timeout=30) as response:
            raw = response.read()
    if hashlib.sha256(raw).hexdigest() != fingerprint:
        raise ValueError("Upstream recording changed: " + filename)
    interactions = yaml.safe_load(raw)["interactions"]
    rows = []
    for index in indices:
        entry = interactions[index]
        request, response = entry["request"], entry["response"]
        if request["method"] not in {"GET", "DELETE"} or "/versions/2019-11-01-preview/" not in request["uri"]:
            raise ValueError("Unexpected recorded request")
        headers = {k: v for k, v in response.get("headers", {}).items()
                   if k.lower() in {"etag", "x-ms-request-id", "location", "azure-asyncoperation", "operation-location"}}
        body = response.get("body", {}).get("string", "")
        rows.append({"interaction_index": index, "method": request["method"], "uri": request["uri"],
                     "status": response["status"]["code"], "headers": headers,
                     "body": json.loads(body) if body else {}})
    result.append({"file": filename, "source_uri": ROOT + filename,
                   "source_sha256": fingerprint, "recordings": rows})
Path(__file__).with_name("cli-cancellation-recordings.json").write_text(json.dumps(result, indent=2) + "\n")
