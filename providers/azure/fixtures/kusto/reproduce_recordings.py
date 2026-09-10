"""Extract immutable response fixtures from the pinned official CLI YAML.

Pass the directory containing the original YAML. No request bodies or request
headers are extracted. The selected interactions include an already-absent
attachment DELETE and leader detach for provenance, not cleanup replay claims.
"""
import hashlib
import json
from pathlib import Path
import sys

import yaml

FILE = "test_kusto_Scenario.yaml"
SOURCE = "https://raw.githubusercontent.com/Azure/azure-cli-extensions/0349eb646d3225db5fd677114e200efdfd11e3f8/src/kusto/azext_kusto/tests/latest/recordings/" + FILE
SHA = "653dc385bfeae90470db0f4749ca4d7c2f435b2401d300421a65a8fb4bb90ee1"
INDICES = [28, 63, 145, 148, 152, 179, 183, 190, 248] + list(range(285, 293)) + list(range(294, 298)) + list(range(299, 323))
raw = (Path(sys.argv[1]) / FILE).read_bytes()
assert hashlib.sha256(raw).hexdigest() == SHA, "upstream recording changed"
rows = yaml.safe_load(raw)["interactions"]
records = []
for index in INDICES:
    row = rows[index]
    assert row["request"]["method"] in ("GET", "DELETE", "POST")
    response = row["response"]
    body = response.get("body", {}).get("string", "")
    headers = {k: v for k, v in response.get("headers", {}).items() if k.lower() in ("x-ms-request-id", "request-id", "x-ms-correlation-request-id", "location", "azure-asyncoperation", "retry-after")}
    records.append({"interaction_index": index, "method": row["request"]["method"], "uri": row["request"]["uri"], "status": response["status"]["code"], "headers": headers, "body": json.loads(body) if body else None})
result = {"file": FILE, "source_uri": SOURCE, "source_sha256": SHA, "recordings": records}
Path(__file__).with_name("cli-recordings.json").write_text(json.dumps(result, indent=2) + "\n")
print("Extracted", len(records), "native Kusto responses")
