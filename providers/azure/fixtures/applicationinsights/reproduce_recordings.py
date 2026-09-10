"""Extract unchanged Application Insights response evidence from pinned CLI YAML.

Run with the directory containing the ten files named below. PyYAML is needed
only to reproduce this evidence; normal provider tests read the checked-in JSON.
No request credential or request body is retained.
"""
import hashlib
import json
from pathlib import Path
import sys
from urllib.parse import urlsplit

import yaml

BASE = "https://raw.githubusercontent.com/Azure/azure-cli-extensions/a40e7adf5e136e663273bf68a725e8f645554131/src/application-insights/azext_applicationinsights/tests/latest/recordings/"
FILES = {
    "test_api_key.yaml": "52c4555f80b1c4d5adfea0b780b55ecf6431a89ba8efd4e0638e98b6c3bd1b91",
    "test_appinsights_component_favorite.yaml": "2c029bd74000bc64db29ae5e3b148c9de78ad12e5b08ddd4ae808e9dc2a4d968",
    "test_appinsights_my_workbook.yaml": "a6505f1ba00142a2150fa475b22c23e26928bfb2817fc058ee0408bf39acdf56",
    "test_appinsights_webtest_crud.yaml": "58cc3c9be20ba43170d3bf2ccf63a74697aad435ae04e8281dab83bb398dfd6d",
    "test_appinsights_workbook.yaml": "6bb8221f9cf947766dc887581514bea81beec6afc7d54c85bb2b39e3b542ec22",
    "test_appinsights_workbook_identity.yaml": "c4e9685d8153e39669b1fdef72665897b3d9ba751883ace44d6e2b31af6a2a83",
    "test_component.yaml": "9fb6080762faffcd3e0ba22832ab7135ab3c1a4367d9850ef61460aa100b3be5",
    "test_component_continues_export.yaml": "556df72092a3fe24f0edad9c81e8e83d3eba108d718e142246f53bd713a59ecd",
    "test_component_with_linked_storage.yaml": "d76361f421474b6b2c2324a55566eab55755069243a62d8b1ce66999e755f13e",
    "test_component_with_linked_workspace.yaml": "722d68916bf6c5c2070a1bb68d951480c38ea6d8b1e54c71ddf0fae5ea6a4374"
}

output = []
for name, digest in FILES.items():
    payload = (Path(sys.argv[1]) / name).read_bytes()
    assert hashlib.sha256(payload).hexdigest() == digest, "upstream recording changed"
    records = []
    for index, row in enumerate(yaml.safe_load(payload)["interactions"]):
        request, response = row["request"], row["response"]
        url = urlsplit(request["uri"])
        if request["method"] not in ["GET", "DELETE"] or url.netloc != "management.azure.com" or "/providers/microsoft.insights/" not in url.path.lower():
            continue
        body = response.get("body", {}).get("string", "")
        headers = {key: value for key, value in response.get("headers", {}).items()
                   if key.lower() in ["etag", "location", "azure-asyncoperation", "retry-after", "x-ms-request-id"]}
        records.append({"interaction_index": index, "method": request["method"], "uri": request["uri"],
                        "status": response["status"]["code"], "headers": headers,
                        "body": json.loads(body) if body else None})
    output.append({"file": name, "source_uri": BASE + name, "source_sha256": digest, "recordings": records})
Path(__file__).with_name("cli-recordings.json").write_text(json.dumps(output, indent=2) + "\n")
print("Extracted", sum(len(row["recordings"]) for row in output), "original Application Insights responses")
