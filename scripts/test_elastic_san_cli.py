"""Execute retained Microsoft CLI request/response code with offline AAZ stubs."""
import hashlib
import json
from pathlib import Path
from types import SimpleNamespace
import textwrap
import unittest

FIXTURES = Path(__file__).resolve().parents[1] / 'providers/azure/fixtures/elastic-san'


class NativeOperation:
    def __init__(self, **arguments):
        self.ctx = SimpleNamespace(subscription_id='subscription', args=SimpleNamespace(
            elastic_san_name='san', resource_group='group', volume_group_name='volumegroup',
            volume_name='volume', no_wait=False, delete_type=None, x_ms_delete_snapshots=None,
            x_ms_force_delete=None, x_ms_access_soft_deleted_resources=None))
        for name, value in arguments.items():
            setattr(self.ctx.args, name, value)
        self.client = SimpleNamespace(format_url=lambda pattern, **params: pattern.format(**params))

    @staticmethod
    def serialize(name, value, **kwargs):
        if value is None:
            if kwargs.get('required'):
                raise ValueError(name)
            return {}
        return {name: value}

    serialize_url_param = serialize
    serialize_query_param = serialize
    serialize_header_param = serialize

    def make_request(self):
        return dict(method=self.method, url=self.url, query=self.query_parameters,
                    headers=self.header_parameters)

    def on_error(self, response):
        raise RuntimeError(f'native error {response.status_code}')


class ElasticSanCLITests(unittest.TestCase):
    def classes(self):
        manifest = json.loads((FIXTURES/'cli-source.json').read_text())
        self.assertEqual(manifest['native_api_version'], '2024-07-01-preview')
        self.assertEqual(manifest['runtime_catalog_api_version'], '2026-04-01-preview')
        result = {}
        for entry in manifest['classes']:
            raw = (FIXTURES/entry['file']).read_bytes()
            self.assertEqual(hashlib.sha256(raw).hexdigest(), entry['sha256'])
            scope = {'AAZHttpOperation': NativeOperation}
            # Request properties and DELETE response dispatch are unchanged.
            # Generated GET response schema builders are retained but not run.
            exec(compile(textwrap.dedent(raw.decode()), entry['file'], 'exec'), scope)
            result[entry['class']] = scope[entry['class']]
        return result

    def test_native_delete_options_are_explicit(self):
        cls = self.classes()['VolumesDelete']
        for arguments, headers, query in (
            ({}, {}, {}),
            ({'x_ms_force_delete': 'false', 'x_ms_delete_snapshots': 'false'},
             {'x-ms-force-delete': 'false', 'x-ms-delete-snapshots': 'false'}, {}),
            ({'x_ms_force_delete': 'true', 'x_ms_delete_snapshots': 'true', 'delete_type': 'permanent'},
             {'x-ms-force-delete': 'true', 'x-ms-delete-snapshots': 'true'}, {'deleteType': 'permanent'}),
        ):
            with self.subTest(arguments=arguments):
                request = cls(**arguments).make_request()
                self.assertEqual(request['method'], 'DELETE')
                self.assertEqual(request['url'], '/subscriptions/subscription/resourceGroups/group/providers/Microsoft.ElasticSan/elasticSans/san/volumegroups/volumegroup/volumes/volume')
                self.assertEqual(request['headers'], headers)
                self.assertEqual(request['query'], {'api-version': '2024-07-01-preview', **query})

    def test_native_lists_separate_active_and_soft_deleted(self):
        for name in ('VolumesListByVolumeGroup', 'VolumeGroupsListByElasticSan'):
            cls = self.classes()[name]
            for value in (None, 'false', 'true'):
                with self.subTest(name=name, value=value):
                    request = cls(x_ms_access_soft_deleted_resources=value).make_request()
                    self.assertEqual(request['method'], 'GET')
                    self.assertEqual(request['query'], {'api-version': '2024-07-01-preview'})
                    expected = {'Accept': 'application/json'}
                    if value is not None:
                        expected['x-ms-access-soft-deleted-resources'] = value
                    self.assertEqual(request['headers'], expected)

    def test_native_get_does_not_declare_soft_deleted_header(self):
        cls = self.classes()['VolumesGet']
        request = cls(x_ms_access_soft_deleted_resources='true').make_request()
        self.assertEqual(request['headers'], {'Accept': 'application/json'})
        self.assertEqual(request['method'], 'GET')

    def test_native_delete_status_and_location_poller_setup(self):
        cls = self.classes()['VolumesDelete']
        for status in (200, 202, 204, 201, 400, 403, 404, 409, 500):
            with self.subTest(status=status):
                operation = cls()
                sent, polls = [], []
                session = SimpleNamespace(http_response=SimpleNamespace(status_code=status))
                def send(**kwargs):
                    sent.append(kwargs)
                    return session
                def poll(*args, **kwargs):
                    polls.append((args, kwargs))
                    return 'native-poller'
                operation.client.send_request = send
                operation.client.build_lro_polling = poll
                if status in (200, 202, 204):
                    self.assertEqual(operation(), 'native-poller')
                    self.assertEqual(len(polls), 1)
                    args, kwargs = polls[0]
                    self.assertFalse(args[0])
                    self.assertIs(args[1], session)
                    self.assertEqual(args[2].__name__, 'on_204' if status == 204 else 'on_200')
                    self.assertEqual(kwargs['lro_options'], {'final-state-via': 'location'})
                    self.assertEqual(kwargs['path_format_arguments'], operation.url_parameters)
                else:
                    with self.assertRaisesRegex(RuntimeError, f'native error {status}'):
                        operation()
                    self.assertEqual(polls, [])
                self.assertEqual(len(sent), 1)
                self.assertFalse(sent[0]['stream'])


if __name__ == '__main__':
    unittest.main()
