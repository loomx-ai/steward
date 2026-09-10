"""Extract only responses from pinned official CLI recordings.

Pass the directory containing the original YAML files. Parent creation responses
provide earlier terminal states, not a complete lifecycle timeline. Function
DELETE in its recording targets the JOB. No credentials from request bodies,
listKeys responses or unrelated data-plane operations are extracted.
"""
import hashlib
import json
from pathlib import Path
import sys

import yaml

BASE = "https://raw.githubusercontent.com/Azure/azure-cli-extensions/0349eb646d3225db5fd677114e200efdfd11e3f8/src/stream-analytics/azext_stream_analytics/tests/latest/recordings/"
SOURCES = {
    "test_job_crud": ("75f6f22751df743907272c2547611c57257900c6c4aeba17eda84ade089c35f5", [5, 6]),
    "test_input_crud": ("dbd9c53fc52b4c4f791fa424b3f03262f65365307eecb92a1507440e17415fce", [1, 11, 12]),
    "test_output_crud": ("e87ed3223bda95b63c523ebbb2eb5f98cb7f354918e4a8af8a14bcdd03f343a5", [1, 10, 11]),
    "test_function_crud": ("133e6260f7b5f0853ab0972aee53ea603b5b19f5a27eb6bfad323db52b8caf0c", [1, 8, 9]),
    "test_private_endpoint_crud": ("530b3f5901ea23c4b73dd7713b3b7b4260af794a26d386392d1884896eb1d4a0", [62, 67] + list(range(68, 86))),
    "test_cluster_crud": ("7ab948f62ecc3684e1de9f214b14a556c16e0a2a0091810bdc2a88f134303783", list(range(337, 372))),
    "test_job_scale": ("9386c877f5ecf6d95c9722abc548d0b354efb957a227ba183c9c7ad676fb2271", list(range(14, 18))),
    "test_transformation_crud": ("bf8d9a6c06b70a428c3baf7a971b8bdb212e06b60d0228841f6940c5465d7a2c", [1, 4]),
}
results = []
for name, (sha, indices) in SOURCES.items():
    file = name + ".yaml"
    raw = (Path(sys.argv[1]) / file).read_bytes()
    assert hashlib.sha256(raw).hexdigest() == sha, "upstream recording changed"
    rows = yaml.safe_load(raw)["interactions"]
    records = []
    for index in indices:
        row = rows[index]
        response = row["response"]
        body = response.get("body", {}).get("string", "")
        headers = {k: v for k, v in response.get("headers", {}).items() if k.lower() in ("x-ms-request-id", "request-id", "x-ms-correlation-request-id", "location", "azure-asyncoperation", "retry-after")}
        records.append({"interaction_index": index, "method": row["request"]["method"], "uri": row["request"]["uri"], "status": response["status"]["code"], "headers": headers, "body": json.loads(body) if body else None})
    results.append({"file": file, "source_uri": BASE + file, "source_sha256": sha, "recordings": records})
Path(__file__).with_name("cli-recordings.json").write_text(json.dumps(results, indent=2) + "\n")
print("Extracted", sum(len(r["recordings"]) for r in results), "native Stream Analytics responses")
