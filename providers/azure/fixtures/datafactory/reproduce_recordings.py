#!/usr/bin/env python3
"""Reproduce selected native CLI responses; requires PyYAML and network access."""
import argparse
import hashlib
import json
from pathlib import Path
from urllib.request import urlopen

import yaml

BASE = 'https://raw.githubusercontent.com/Azure/azure-cli-extensions/d2f60986756c939c3d6d7f85e89798cca158d935/src/datafactory/azext_datafactory/tests/latest/recordings/'
SOURCES = {
    'test_datafactory_main.yaml': (
        'ead0da2ea33784c79650ea494b2fc722bc07ae14017b39d0953db0cfa45dc4c5',
        [4, 7, 10, 13, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30,
         35, 36, 37, 38, 41, 46, 47, 48, 49, 50, 51, 55, 57, 58, 59, 63,
         65, 66, 67, 68, 69, 70, 71, 72, 73, 74, 75, 76, 77, 78, 79, 80,
         81, 82, 83, 88, 91, 94, 99, 100, 101, 102]),
    'test_datafactory_managedPrivateEndpoint.yaml': (
        'c9361d97a2c0b240dcbfd874edf15d7fd07bfb7cd1ea03dab30dd7d04246dc11',
        [3, 4, 6, 7, 8]),
}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--check', action='store_true')
    args = parser.parse_args()
    rows = []
    for name, (expected, indexes) in SOURCES.items():
        uri = BASE + name
        with urlopen(uri, timeout=60) as response:
            raw = response.read()
        assert hashlib.sha256(raw).hexdigest() == expected, name
        interactions = yaml.safe_load(raw)['interactions']
        for index in indexes:
            request, response = (interactions[index][key] for key in ('request', 'response'))
            row = {
                'source_uri': uri, 'source_sha256': expected, 'interaction_index': index,
                'method': request['method'], 'url': request['uri'],
                'status': response['status']['code'],
                'headers': {key: value for key, value in response['headers'].items()
                            if key.lower() in ('content-type', 'azure-asyncoperation', 'location')},
                'body': response['body']['string'],
            }
            if request['method'] == 'POST':
                row['request_body'] = request['body']
            rows.append(row)
    target = Path(__file__).with_name('cli-recordings.json')
    payload = json.dumps(rows, indent=2) + '\n'
    if args.check:
        assert target.read_text() == payload, 'Native CLI extraction changed'
    else:
        target.write_text(payload)
    print(f'Data Factory: {len(rows)} original CLI responses')


if __name__ == '__main__':
    main()
