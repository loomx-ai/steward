"""Reproduce the sanitized CLI read fixture from its pinned YAML (requires PyYAML)."""
import hashlib
import json
from pathlib import Path
import sys
import yaml

output = Path(__file__).with_name("cli-read-recordings.json")
source = json.loads(output.read_text())
raw = Path(sys.argv[1]).read_bytes()
assert hashlib.sha256(raw).hexdigest() == source["source_sha256"], "upstream recording fingerprint changed"
interactions = yaml.safe_load(raw)["interactions"]
records = []
for index in (4, 5):
    interaction = interactions[index]
    body = json.loads(interaction["response"]["body"]["string"])
    for resource in body.get("value", [body]):
        for field in ("createdBy", "lastModifiedBy"):
            if field in resource.get("systemData", {}):
                resource["systemData"][field] = "recording-user"
    records.append({"interaction_index": index, "method": interaction["request"]["method"],
                    "uri": interaction["request"]["uri"], "status": interaction["response"]["status"]["code"], "body": body})
source["recordings"] = records
output.write_text(json.dumps(source, indent=2) + "\n")
print(f"Reproduced {len(records)} native read responses")
