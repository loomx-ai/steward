"""Retain representative native Mesh responses from Microsoft's pinned recording."""
import hashlib
import json
from pathlib import Path
import sys
from urllib.parse import urlsplit

import yaml

SOURCE = "https://raw.githubusercontent.com/Azure/azure-cli-extensions/a20385bffcbb7403846af8a65dbfcaf96c717e4d/src/fleet/azext_fleet/tests/latest/recordings/test_fleet_cluster_mesh.yaml"
SHA256 = "f67ba23b52fdddc4566e64af5478a600d883a8bd241a8b480c41147e567ceee7"


def main():
    raw = Path(sys.argv[1]).read_bytes()
    if hashlib.sha256(raw).hexdigest() != SHA256:
        raise ValueError("Microsoft's complete native recording has changed")
    records, seen = [], set()
    for index, row in enumerate(yaml.safe_load(raw)["interactions"]):
        request, response = row["request"], row["response"]
        path = urlsplit(request["uri"]).path.lower()
        if "/providers/microsoft.containerservice/fleets/" not in path:
            continue
        mesh = "/clustermeshprofiles" in path
        member = request["method"] == "GET" and "/members/" in path
        if not mesh and not member:
            continue
        body = response["body"].get("string", "")
        try:
            data = json.loads(body) if body else {}
        except json.JSONDecodeError:
            if response["status"]["code"] < 400:
                raise
            data = {}
        props = data.get("properties", {})
        status = props.get("status", {})
        attachment = props.get("meshProperties", {})
        state = attachment.get("status", {})
        signature = json.dumps([request["method"], path, response["status"]["code"],
                                status.get("state"), props.get("memberSelector"),
                                status.get("lastAppliedMemberSelector"), state.get("state"),
                                state.get("error", {}).get("code"), data.get("value")], sort_keys=True)
        if signature in seen:
            continue
        seen.add(signature)
        allowed = {"content-type", "etag", "location", "azure-asyncoperation", "retry-after", "x-ms-request-id", "x-ms-correlation-request-id"}
        headers = {key: value for key, value in response["headers"].items() if key.lower() in allowed}
        records.append({"interaction": index, "method": request["method"], "url": request["uri"],
                        "request_body": request.get("body"), "status": response["status"]["code"],
                        "headers": headers, "body": body})
    output = {"source_uri": SOURCE, "source_sha256": SHA256, "records": records}
    Path(__file__).with_name("cli-mesh-recordings.json").write_text(json.dumps(output, indent=2, sort_keys=True) + "\n")


if __name__ == "__main__":
    main()
