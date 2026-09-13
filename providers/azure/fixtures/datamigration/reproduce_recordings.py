#!/usr/bin/env python3
"""Reproduce pinned DMS CLI evidence (PyYAML; network or --source-dir)."""
import argparse
import hashlib
import json
from pathlib import Path
from urllib.parse import parse_qsl, urlencode, urlsplit, urlunsplit
from urllib.request import urlopen

import yaml

SOURCES = [('https://raw.githubusercontent.com/Azure/azure-cli-extensions/d2f60986756c939c3d6d7f85e89798cca158d935/src/datamigration/azext_datamigration/tests/latest/recordings/test_datamigration_Scenario.yaml',
  '52afcca05398c948fe6ce59e37ae2387b3bc4b5736886998f224ca12ee09f7a1',
  [2,
   3,
   4,
   5,
   8,
   9,
   10,
   11,
   12,
   15,
   16,
   17,
   18,
   19,
   20,
   21,
   22,
   23,
   24,
   25,
   33,
   34,
   35,
   36,
   37,
   38,
   39,
   40,
   41,
   42,
   46,
   47,
   48,
   51,
   52,
   53,
   54,
   57,
   58,
   59,
   60,
   61,
   62,
   63,
   64,
   65,
   66,
   67]),
 ('https://raw.githubusercontent.com/Azure/azure-cli/8bead7f93f086629efb160d56c25f508156925bf/src/azure-cli/azure/cli/command_modules/dms/tests/latest/recordings/test_project_commands.yaml',
  '04bc89f55029610584c0dd2980edbf14a5c98924c67c2562001b6b8aa2df6b9e',
  [30, 31, 32, 34, 35, 37, 38, 40, 41, 43, 45, 47, 48, 49, 50, 51, 52, 53, 54, 55, 56]),
 ('https://raw.githubusercontent.com/Azure/azure-cli/8bead7f93f086629efb160d56c25f508156925bf/src/azure-cli/azure/cli/command_modules/dms/tests/latest/recordings/test_service_commands.yaml',
  '38dddb3d706d5b090126300aa97ed3b59fcbd7f95d9d1c1c2a8f740908ce4c4c',
  [2, 28, 29, 30, 68, 69, 70, 71, 72, 73]),
 ('https://raw.githubusercontent.com/Azure/azure-cli/8bead7f93f086629efb160d56c25f508156925bf/src/azure-cli/azure/cli/command_modules/dms/tests/latest/recordings/test_task_commands.yaml',
  '632eb25e8716c3196559ae1cbd5981795a33ddcd21dd0d1f99396c6dfeeb4428',
  [22,
   24,
   26,
   28,
   30,
   31,
   32,
   33,
   35,
   36,
   37,
   40,
   42,
   43,
   45,
   46,
   47,
   49,
   50,
   51,
   53,
   55,
   56,
   57,
   58,
   59,
   60,
   61])]


def replay_url(value):
    uri = urlsplit(value)
    query = parse_qsl(uri.query, keep_blank_values=True)
    signing = {"t", "c", "s", "h"}
    if any(key in signing for key, _ in query):
        query = [(key, "replay-" + key if key in signing else val) for key, val in query]
        return urlunsplit(uri._replace(query=urlencode(query)))
    return value


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--check", action="store_true")
    parser.add_argument("--source-dir", type=Path)
    args = parser.parse_args()
    output = []
    for uri, expected, indexes in SOURCES:
        if args.source_dir:
            raw = (args.source_dir / uri.rsplit("/", 1)[-1]).read_bytes()
        else:
            with urlopen(uri, timeout=60) as response:
                raw = response.read()
        assert hashlib.sha256(raw).hexdigest() == expected, "Upstream recording changed"
        interactions = yaml.safe_load(raw)["interactions"]
        records = []
        for index in indexes:
            request, response = (interactions[index][key] for key in ("request", "response"))
            record = {
                "index": index,
                "method": request["method"],
                "url": replay_url(request.get("uri", request.get("url"))),
                "status": response["status"]["code"],
                "headers": {key: [replay_url(value) if key.lower() != "content-type" else value
                                  for value in values]
                            for key, values in response.get("headers", {}).items()
                            if key.lower() in ("content-type", "azure-asyncoperation", "location")},
                "body": response.get("body", {}).get("string", ""),
            }
            if request["method"] == "POST":
                record["request_body"] = request.get("body")
            records.append(record)
        output.append({"source_uri": uri, "source_sha256": expected, "records": records})
    target = Path(__file__).with_name("cli-recordings.json")
    payload = json.dumps(output, indent=2) + "\n"
    if args.check:
        assert target.read_text() == payload, "Native DMS extraction changed"
    else:
        target.write_text(payload)
    print("Data Migration:", sum(len(row["records"]) for row in output), "native CLI responses")


if __name__ == "__main__":
    main()
