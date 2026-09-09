"""Reproduce CLI response extracts from the pinned YAML (requires PyYAML)."""
import hashlib
import json
from pathlib import Path
import sys
import yaml

output = Path(__file__).with_name("cli-recordings.json")
source = json.loads(output.read_text())
raw = Path(sys.argv[1]).read_bytes()
assert hashlib.sha256(raw).hexdigest() == source["source_sha256"], "upstream recording fingerprint changed"
interactions = yaml.safe_load(raw)["interactions"]
records = []
for index in (6, 7, 9):
    interaction = interactions[index]
    records.append({"interaction_index": index, "method": interaction["request"]["method"],
                    "uri": interaction["request"]["uri"], "status": interaction["response"]["status"]["code"],
                    "body": json.loads(interaction["response"]["body"]["string"])})
source["recordings"] = records
output.write_text(json.dumps(source, indent=2) + "\n")
print(f"Reproduced {len(records)} native responses")
