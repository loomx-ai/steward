"""Reproduce selected native responses from pinned CLI YAMLs (requires PyYAML).

Supply the directory holding the original recordings. Original response bodies
are unchanged; signed URL values are replaced with explicit replay placeholders.
"""
import hashlib
import json
from pathlib import Path
import sys
import urllib.parse
import yaml

BASE = "https://raw.githubusercontent.com/Azure/azure-cli-extensions/5813689875f128709e8db10903e893138a064236/src/cdn/azext_cdn/tests/latest/recordings/"
SELECTION = {
    "test_afd_profile_crud": (28, 7, 35, 36, 69, 70),
    "test_rule_set_crud": (6, 12, 11, 13, 14, 16, 17),
    "test_afd_secret_latest_version_crud": (5, 8, 9, 10),
}

def replay_url(value):
    uri = urllib.parse.urlsplit(value)
    query = urllib.parse.parse_qsl(uri.query, keep_blank_values=True)
    if any(key in ("t", "c", "s", "h") for key, _ in query):
        query = [(key, "replay-" + key if key in ("t", "c", "s", "h") else val) for key, val in query]
        return urllib.parse.urlunsplit(uri._replace(query=urllib.parse.urlencode(query)))
    return value

expected = {source["source_uri"]: source["source_sha256"] for source in json.loads(Path(__file__).with_name("cli-recordings.json").read_text())}
result = []
for name, indexes in SELECTION.items():
    raw = (Path(sys.argv[1]) / (name + ".yaml")).read_bytes()
    assert hashlib.sha256(raw).hexdigest() == expected[BASE + name + ".yaml"], "upstream recording fingerprint changed"
    interactions = yaml.safe_load(raw)["interactions"]
    responses = []
    for index in indexes:
        row = interactions[index]
        response = row["response"]
        body = response.get("body", {}).get("string", "")
        headers = {key: [replay_url(value) for value in values]
                   for key, values in response.get("headers", {}).items()
                   if key.lower() in ("azure-asyncoperation", "location", "retry-after", "x-ms-request-id")}
        responses.append({"interaction_index": index, "method": row["request"]["method"],
                          "uri": replay_url(row["request"]["uri"]), "status": response["status"]["code"],
                          "headers": headers, "body": json.loads(body) if body else None})
    result.append({"source_uri": BASE + name + ".yaml", "source_sha256": hashlib.sha256(raw).hexdigest(), "recordings": responses})
Path(__file__).with_name("cli-recordings.json").write_text(json.dumps(result, indent=2) + "\n")
print(f"Reproduced {sum(len(source['recordings']) for source in result)} native responses")
