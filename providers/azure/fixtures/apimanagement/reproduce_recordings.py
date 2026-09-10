"""Extract response-only evidence from the pinned public Azure CLI recording.

Pass the directory containing test_apim_core_service.yaml. Original response
bodies, ETags, operation URLs and API versions are preserved. Runtime version
bridges and synthetic final GETs are confined to the Go replay tests.
"""
import hashlib
import json
from pathlib import Path
import sys

import yaml

FILE = "test_apim_core_service.yaml"
SHA = "ffdbb745bd5fcd9e5736578e4f5ba1571d1bd75d13bc844b5b7b8ae5710c7774"
BASE = "https://raw.githubusercontent.com/Azure/azure-cli/8bead7f93f086629efb160d56c25f508156925bf/src/azure-cli/azure/cli/command_modules/apim/tests/latest/recordings/"
INDICES = [43, 111, 112, 273, 275, 276, 278, 279, 282, 284, 285, 288, 289,
           290, 291, 293, 294, 296, 297, 300, 301, 302, 304, 306, 307, 308,
           309, 310, 311, 312, 313, 314, 316, 317, 319, 324, 325, 327, 328,
           329, 330, 331, 333, 334, 336, 341, 342, 346, 348, 349, 350, 351,
           352, 354, 355, 357, 358, 359, 360, 361, 362, 363, 364, 365, 366,
           367, 368]
raw = (Path(sys.argv[1]) / FILE).read_bytes()
assert hashlib.sha256(raw).hexdigest() == SHA, "upstream recording changed"
interactions = yaml.safe_load(raw)["interactions"]
records = []
for index in INDICES:
    row = interactions[index]
    response = row["response"]
    body = response.get("body", {}).get("string", "")
    headers = {k: v for k, v in response.get("headers", {}).items()
               if k.lower() in ("x-ms-request-id", "etag", "location", "azure-asyncoperation", "retry-after")}
    records.append({"interaction_index": index, "method": row["request"]["method"],
                    "uri": row["request"]["uri"], "status": response["status"]["code"],
                    "headers": headers, "body": json.loads(body) if body else None})
output = [{"file": FILE, "source_uri": BASE + FILE, "source_sha256": SHA, "recordings": records}]
Path(__file__).with_name("cli-recordings.json").write_text(json.dumps(output, indent=2) + "\n")
print("Extracted", len(records), "native APIM responses")
