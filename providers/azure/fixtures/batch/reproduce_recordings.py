"""Extract selected responses from pinned Azure CLI Batch recordings.

Pass the directory containing the original YAML files. Request credentials,
request bodies, key-list responses and certificate operations are not extracted.
The output preserves original response bodies, versions and polling headers;
2025 runtime compatibility bridges are made explicitly in the Go replay tests.
"""
import hashlib
import json
from pathlib import Path
import sys

import yaml

BASE = "https://raw.githubusercontent.com/Azure/azure-cli/dc50d475a00ded4a1a1980d4a10a9fbd9a750a81/src/azure-cli/azure/cli/command_modules/batch/tests/latest/recordings/"
SOURCES = {
    "test_batch_general_arm_cmd": ("3c32b0f3e49366f98b8c04f801f8d4b868347bda968ceca73e480c9a01cd31ef", [8, 13, 14, 15, 16, 17, 18, 19]),
    "test_batch_byos_account_cmd": ("f33d85b394374cc013c6c99dce902931a11e7725217c9c01abb008ccac935504", [11, 13, 14, 15, 16]),
    "test_batch_application_cmd": ("449afa4ca871fb91200aafdf0a2205707a6bef4669c5a86bf083ce5123f395cb", [7, 9, 13, 16, 20, 25, 29, 31, 33, 35]),
    "test_batch_job_list_cmd": ("c01111f360dbf5f234b9ec42f340ca6341595d8d1b51743c55e2829b922cc9e8", [4, 7, 8, 9, 10]),
    "test_batch_jobs_and_tasks": ("01970496ace4a14e0db9297ee04209ff51a67fe066abad4fff5c82d4974b1b75", [4, 7, 10, 13, 14, 16, 17, 18, 20, 21, 22, 24, 25]),
    "test_batch_pools_and_nodes": ("25b69cc54933def38f8cae0e08020e8cd55fcbc91c58ca7fdc67589203617b23", [4, 6, 9, 10, 11, 12, 14, 17, 20, 22, 24, 26, 29, 46, 47, 48, 50, 51]),
    "test_batch_pool_cmd": ("00476c7379a9f49882fbb4524ea3cb335f6ad8d779b8b97120d4b8ef9258e979", [3, 9, 10, 11, 13, 15, 16, 17, 18, 20, 22, 24, 26]),
    "test_batch_privateendpoint_cmd": ("daf63055b905f75531f97caf0b0df017754b940dd7b795fc638296ffb5e25753", [3, 24, 25]),
}
results = []
for name, (sha, indices) in SOURCES.items():
    file = name + ".yaml"
    raw = (Path(sys.argv[1]) / file).read_bytes()
    assert hashlib.sha256(raw).hexdigest() == sha, "upstream recording changed"
    interactions = yaml.safe_load(raw)["interactions"]
    records = []
    for index in indices:
        row = interactions[index]
        response = row["response"]
        body = response.get("body", {}).get("string", "")
        headers = {k: v for k, v in response.get("headers", {}).items() if k.lower() in ("x-ms-request-id", "request-id", "client-request-id", "etag", "location", "azure-asyncoperation", "retry-after")}
        records.append({"interaction_index": index, "method": row["request"]["method"], "uri": row["request"]["uri"], "status": response["status"]["code"], "headers": headers, "body": json.loads(body) if body else None})
    results.append({"file": file, "source_uri": BASE+file, "source_sha256": sha, "recordings": records})
Path(__file__).with_name("cli-recordings.json").write_text(json.dumps(results, indent=2)+"\n")
print("Extracted", sum(len(source["recordings"]) for source in results), "native Batch responses")
