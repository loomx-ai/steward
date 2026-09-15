#!/usr/bin/env python3
"""Extract pinned official PowerShell restore-point evidence, without credentials.

Pass the original TestSqlPoolRestorePoint.json path to reproduce offline.
"""
import hashlib
import json
from pathlib import Path
import sys
import urllib.request

SOURCE = "https://raw.githubusercontent.com/Azure/azure-powershell/0f16fd4a0b915406d3b0de1708942883bf0c0840/src/Synapse/Synapse.Test/SessionRecords/Microsoft.Azure.Commands.Synapse.Test.ScenarioTests.SqlPoolBackupTests/TestSqlPoolRestorePoint.json"
SHA256 = "18f6d43cffd4ec109039078f94d36da9909182536e66ceb6d5b6ef9375d67c42"
raw = Path(sys.argv[1]).read_bytes() if len(sys.argv) > 1 else urllib.request.urlopen(SOURCE, timeout=30).read()
if hashlib.sha256(raw).hexdigest() != SHA256:
    raise ValueError("Official recording changed")
entries = json.loads(raw)["Entries"]
rows = []
for index in [42, 48, 54, 55, 58, 59, 62, 63]:
    entry = entries[index]
    if entry["RequestMethod"] not in {"GET", "POST", "DELETE"} or "/restorePoints" not in entry["RequestUri"] or "api-version=2021-06-01" not in entry["RequestUri"]:
        raise ValueError("Unexpected native restore-point request")
    row = {"interaction_index": index, "method": entry["RequestMethod"], "uri": entry["RequestUri"], "status": entry["StatusCode"],
           "headers": {k: v for k, v in entry["ResponseHeaders"].items() if k.lower() in {"location", "azure-asyncoperation", "operation-location", "retry-after", "x-ms-request-id", "content-length"}},
           "body": entry["ResponseBody"]}
    if row["method"] == "POST":
        body = json.loads(entry["RequestBody"])
        if set(body) != {"restorePointLabel"}:
            raise ValueError("Unexpected create request properties")
        row["request_body"] = body
    rows.append(row)
output = {"source_uri": SOURCE, "source_sha256": SHA256, "recordings": rows}
Path(__file__).with_name("powershell-restorepoint-recordings.json").write_text(json.dumps(output, indent=2) + "\n")
