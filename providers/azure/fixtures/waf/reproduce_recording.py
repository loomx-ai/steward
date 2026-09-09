"""Extract unchanged WAF responses from Microsoft's checksum-pinned CLI YAML."""
import hashlib
import json
from pathlib import Path
import sys
import yaml

SOURCE = "https://raw.githubusercontent.com/Azure/azure-cli-extensions/5813689875f128709e8db10903e893138a064236/src/front-door/azext_front_door/tests/latest/recordings/test_waf_policy_basic.yaml"
SHA256 = "74fea5e660d039b73b17087ba1f939d4a136be8a666f35017a68fbd131f519e6"
raw = Path(sys.argv[1]).read_bytes()
assert hashlib.sha256(raw).hexdigest() == SHA256, "upstream recording fingerprint changed"
rows = yaml.safe_load(raw)["interactions"]
result = []
for index in (26, 27, 28, 29):
    row = rows[index]
    response = row["response"]
    body = response.get("body", {}).get("string", "")
    headers = {key: values for key, values in response.get("headers", {}).items()
               if key.lower() in ("x-ms-request-id", "x-ms-original-request-ids", "location", "azure-asyncoperation", "retry-after")}
    result.append({"interaction_index": index, "method": row["request"]["method"],
                   "uri": row["request"]["uri"], "status": response["status"]["code"],
                   "headers": headers, "body": json.loads(body) if body else None})
Path(__file__).with_name("cli-recording.json").write_text(json.dumps({"source_uri": SOURCE, "source_sha256": SHA256, "recordings": result}, indent=2) + "\n")
print("Extracted four native WAF responses")
