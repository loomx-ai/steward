"""Extract selected unchanged Microsoft CLI responses; no request bodies."""
import hashlib
import json
from pathlib import Path
import sys
import yaml

COMMIT = "dc50d475a00ded4a1a1980d4a10a9fbd9a750a81"
BASE = f"https://raw.githubusercontent.com/Azure/azure-cli/{COMMIT}/src/azure-cli/azure/cli/command_modules/appservice/tests/latest/recordings/"
SOURCES = {
    "test_webapp_ssl.yaml": ("c83e0ce75900cbcfc7e00d0946e6e2ae9604d859e6523ec816f10092350871f6", [16, 17, 18, 20, 31, 42, 50, 60, 62, 73, 83, 84, 89]),
    "test_functionapp_retain_plan.yaml": ("e56af56b7e1069704f1115762efe8daaf70cb975f9cb677bec0d55f0332cde58", [16, 18]),
    "test_functionapp_update_slot.yaml": ("74dc65233b9b000d7377035188a8edb015e9034f58a38e057fd01db3cf689092", [20, 23, 27]),
}
result = []
for file, (fingerprint, indices) in SOURCES.items():
    raw = (Path(sys.argv[1]) / file).read_bytes()
    assert hashlib.sha256(raw).hexdigest() == fingerprint, "upstream recording changed"
    rows = yaml.safe_load(raw)["interactions"]
    records = []
    for index in indices:
        row = rows[index]
        response = row["response"]
        raw_body = response.get("body", {}).get("string", "")
        headers = {key: values for key, values in response.get("headers", {}).items()
                   if key.lower() in ("x-ms-request-id", "x-ms-correlation-request-id", "location", "azure-asyncoperation", "retry-after")}
        records.append({"interaction_index": index, "method": row["request"]["method"], "uri": row["request"]["uri"],
                        "status": response["status"]["code"], "headers": headers, "body": json.loads(raw_body) if raw_body else None})
    result.append({"file": file, "source_uri": BASE + file, "source_sha256": fingerprint, "recordings": records})
Path(__file__).with_name("cli-recordings.json").write_text(json.dumps(result, indent=2) + "\n")
print("Extracted 18 native App Service responses")
