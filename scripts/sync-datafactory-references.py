#!/usr/bin/env python3
"""Project native Data Factory reference shapes from the pinned offline catalog.

Keep typed object/array/map edges and native discriminators. Unstructured user
parameters, scripts and JSON bodies are intentionally not searched for IDs.
"""
import argparse
import json
from pathlib import Path

REFERENCE_TYPES = {
    'DatasetReference': 'datasets', 'LinkedServiceReference': 'linkedservices',
    'IntegrationRuntimeReference': 'integrationRuntimes', 'DataFlowReference': 'dataflows',
    'PipelineReference': 'pipelines', 'TriggerReference': 'triggers',
    'CredentialReference': 'credentials', 'ManagedVirtualNetworkReference': 'managedVirtualNetworks',
}


def project(document):
    definitions = document['definitions']

    def bases(name):
        result = set()
        for base in definitions[name].get('allOf', []):
            if base.get('$ref', '').startswith('#/definitions/'):
                parent = base['$ref'].rsplit('/', 1)[1]
                result.add(parent)
                result.update(bases(parent))
        return result

    def fields(name):
        result = {}
        for parent in sorted(bases(name), key=lambda n: len(bases(n))):
            result.update(definitions[parent].get('properties', {}))
        result.update(definitions[name].get('properties', {}))
        return result

    def shape(schema):
        ref = schema.get('$ref', '')
        if ref.startswith('#/definitions/'):
            return {'s': ref.rsplit('/', 1)[1]}
        result = {}
        if schema.get('properties'):
            result['f'] = {key: shape(value) for key, value in schema['properties'].items()}
        if schema.get('items'):
            result['a'] = shape(schema['items'])
        if isinstance(schema.get('additionalProperties'), dict) and schema['additionalProperties']:
            result['m'] = shape(schema['additionalProperties'])
        return result

    result = {}
    for name, definition in definitions.items():
        if name in REFERENCE_TYPES:
            result[name] = {'r': REFERENCE_TYPES[name]}
            continue
        result[name] = shape({**definition, 'properties': fields(name)})
        if definition.get('discriminator'):
            result[name]['d'] = definition['discriminator']
            result[name]['v'] = {value.get('x-ms-discriminator-value', child): {'s': child}
                                  for child, value in definitions.items() if name in bases(child)}

    def prune(value):
        if 'r' in value:
            return value
        if 's' in value:
            return value if result.get(value['s']) else {}
        clean = {}
        for key in ('f', 'v'):
            entries = {k: prune(v) for k, v in value.get(key, {}).items()}
            if key == 'f':
                entries = {k: v for k, v in entries.items() if v}
            if any(entries.values()):
                clean[key] = entries
        for key in ('a', 'm'):
            child = prune(value.get(key, {})) if value.get(key) else {}
            if child:
                clean[key] = child
        if clean.get('v'):
            clean['d'] = value['d']
        return clean

    while True:
        cleaned = {name: prune(value) for name, value in result.items()}
        cleaned = {name: value for name, value in cleaned.items() if value}
        if cleaned == result:
            return cleaned
        result = cleaned


def main():
    root = Path(__file__).resolve().parent.parent
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--check', action='store_true')
    args = parser.parse_args()
    source = json.loads((root / 'providers/azure/catalog/source/swagger.json').read_text())
    documents = [d for d in source['documents'] if '/Microsoft.DataFactory/' in d['source_uri']]
    assert len(documents) == 1
    document = documents[0]
    output = {'source_uri': document['source_uri'], 'source_sha256': document['source_sha256'], 'shapes': project(document['document'])}
    payload = json.dumps(output, indent=2, sort_keys=True) + '\n'
    target = root / 'providers/azure/catalog/generated/datafactory-references.json'
    if args.check:
        assert target.read_text() == payload, 'Data Factory reference shapes need regeneration'
    else:
        target.write_text(payload)
    print(f"Data Factory: {len(output['shapes'])} native reference shapes")


if __name__ == '__main__':
    main()
