"""Reproduce selected native CLI responses from the checksum-pinned YAML files.

Usage: python3 reproduce_recordings.py /path/to/downloaded/recordings
Requires PyYAML; immutable source URLs are in cli-delete-recordings.json.
"""
import hashlib
import json
from pathlib import Path
import sys
import urllib.parse

import yaml

output = Path(__file__).with_name("cli-delete-recordings.json")
manifest = json.loads(output.read_text())
result = []
for source in manifest:
    raw = (Path(sys.argv[1]) / source["source_uri"].rsplit("/", 1)[1]).read_bytes()
    assert hashlib.sha256(raw).hexdigest() == source["source_sha256"]
    interactions = yaml.safe_load(raw)["interactions"]
    deletion = interactions[source["delete_index"]]
    read = interactions[source["read_index"]]
    assert read["request"]["method"] == source["read_method"]
    assert deletion["request"]["method"] == "DELETE"
    assert urllib.parse.urlsplit(deletion["request"]["uri"]).path == urllib.parse.urlsplit(read["request"]["uri"]).path

    def response(interaction):
        native = interaction["response"]
        body = native.get("body", {}).get("string", "")
        body = json.loads(body) if body else None
        if isinstance(body, dict):
            for field in ["createdBy", "lastModifiedBy"]:
                if field in body.get("systemData", {}):
                    body["systemData"][field] = "recording-user"
        headers = {key: values for key, values in native.get("headers", {}).items()
                   if key.lower() in ["x-ms-request-id", "content-type", "location", "azure-asyncoperation", "operation-location"]}
        return dict(status=native["status"]["code"], headers=headers, body=body)

    source["resource_uri"] = deletion["request"]["uri"]
    source["read"], source["delete"] = response(read), response(deletion)
    result.append(source)
output.write_text(json.dumps(result, indent=2) + "\n")
print("Reproduced", len(result), "native deletion responses")
