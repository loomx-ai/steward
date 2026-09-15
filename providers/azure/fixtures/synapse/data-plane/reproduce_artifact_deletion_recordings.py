#!/usr/bin/env python3
"""Reproduce official CLI artifact deletion and polling responses (requires PyYAML).

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
    "test_notebook.yaml": ("c712c41bae3470b6e5dfc5cfd9cc16b57ffab90bdb19337391ceda3fcfae35bd", [27, 28, 29, 30]),
    "test_spark_job_definition.yaml": ("033079355277b797e330400f307080b8a0664cba0f2d38dfe011038cc082650f", [25, 26, 27, 28]),
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
        if request["method"] not in {"GET", "DELETE"} or "api-version=2020-12-01" not in request["uri"]:
            raise ValueError("Unexpected recorded request")
        headers = {k: v for k, v in response.get("headers", {}).items()
                   if k.lower() in {"etag", "x-ms-request-id", "location", "azure-asyncoperation", "operation-location", "retry-after"}}
        body = response.get("body", {}).get("string", "")
        rows.append({"interaction_index": index, "method": request["method"], "uri": request["uri"],
                     "status": response["status"]["code"], "headers": headers,
                     "body": body})
    result.append({"file": filename, "source_uri": ROOT + filename,
                   "source_sha256": fingerprint, "recordings": rows})
Path(__file__).with_name("cli-artifact-deletion-recordings.json").write_text(json.dumps(result, indent=2) + "\n")
