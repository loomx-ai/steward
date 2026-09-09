"""Extract selected native Microsoft Redis GET/DELETE responses; no request bodies."""
import hashlib
import json
from pathlib import Path
import sys
import yaml

SOURCES = {'test_redis_cache_authentication.yaml': {'source_uri': 'https://raw.githubusercontent.com/Azure/azure-cli/dc50d475a00ded4a1a1980d4a10a9fbd9a750a81/src/azure-cli/azure/cli/command_modules/redis/tests/latest/recordings/test_redis_cache_authentication.yaml',
                                          'source_sha256': 'fec35e598e39ce9c32457f85b22ad9be4ad12a43bcc62806d63c9bc981925522',
                                          'indices': [26,
                                                      27,
                                                      32,
                                                      36,
                                                      44,
                                                      50,
                                                      52,
                                                      53,
                                                      57,
                                                      58,
                                                      59,
                                                      60,
                                                      62,
                                                      63]},
 'test_redis_cache_firewall.yaml': {'source_uri': 'https://raw.githubusercontent.com/Azure/azure-cli/dc50d475a00ded4a1a1980d4a10a9fbd9a750a81/src/azure-cli/azure/cli/command_modules/redis/tests/latest/recordings/test_redis_cache_firewall.yaml',
                                    'source_sha256': '524aa1760b1dd4761082ff9f6f2a02b0bf4db7514bb3095f00b9ffbcd3f82c70',
                                    'indices': [23, 26, 27, 28, 29, 30, 38]},
 'test_redis_cache_patch_schedule.yaml': {'source_uri': 'https://raw.githubusercontent.com/Azure/azure-cli/dc50d475a00ded4a1a1980d4a10a9fbd9a750a81/src/azure-cli/azure/cli/command_modules/redis/tests/latest/recordings/test_redis_cache_patch_schedule.yaml',
                                          'source_sha256': 'f56e9b4e3cdef0ab1f77f923cbb2d8e9090d3fd222c2b548745494dea54ff0b3',
                                          'indices': [22, 25, 26]},
 'test_redis_cache_server_link.yaml': {'source_uri': 'https://raw.githubusercontent.com/Azure/azure-cli/dc50d475a00ded4a1a1980d4a10a9fbd9a750a81/src/azure-cli/azure/cli/command_modules/redis/tests/latest/recordings/test_redis_cache_server_link.yaml',
                                       'source_sha256': 'd2437be1db7cd3e270ad5b984845f5465fd609bc401a0c13ea26a0b6ab561b02',
                                       'indices': [22, 43, 58, 59, 61, 62, 65, 66, 67, 68, 77]},
 'test_redisenterprise_scenario1.yaml': {'source_uri': 'https://raw.githubusercontent.com/Azure/azure-cli-extensions/5813689875f128709e8db10903e893138a064236/src/redisenterprise/azext_redisenterprise/tests/latest/recordings/test_redisenterprise_scenario1.yaml',
                                         'source_sha256': '30d81943c86f9b58daf0ca30b98dd4da69a43c406b98d7f377564871cc4216da',
                                         'indices': [19, 20, 43, 47, 48, 62]},
 'test_redisenterprise_scenario2.yaml': {'source_uri': 'https://raw.githubusercontent.com/Azure/azure-cli-extensions/5813689875f128709e8db10903e893138a064236/src/redisenterprise/azext_redisenterprise/tests/latest/recordings/test_redisenterprise_scenario2.yaml',
                                         'source_sha256': 'dacfa69ec6b714c1e88f6263c5d612ad78ae9cda44bb4b2872fc748adf2851da',
                                         'indices': [15, 43, 44, 47, 48, 50]},
 'test_redisenterprise_scenario3.yaml': {'source_uri': 'https://raw.githubusercontent.com/Azure/azure-cli-extensions/5813689875f128709e8db10903e893138a064236/src/redisenterprise/azext_redisenterprise/tests/latest/recordings/test_redisenterprise_scenario3.yaml',
                                         'source_sha256': 'b2688cc3dba44f470d4a578ce6816c007061c0e91bef47e5a76adb105f412dee',
                                         'indices': [15, 27, 41, 49, 53, 54]},
 'test_redisenterprise_scenario4.yaml': {'source_uri': 'https://raw.githubusercontent.com/Azure/azure-cli-extensions/5813689875f128709e8db10903e893138a064236/src/redisenterprise/azext_redisenterprise/tests/latest/recordings/test_redisenterprise_scenario4.yaml',
                                         'source_sha256': '53f66595bcfcc98fcffd76d800c87d3e2b0fff52c36876355bde93f1fdff0edf',
                                         'indices': [17, 41, 48, 49, 50, 51, 52, 53]}}
result=[]
for file, source in SOURCES.items():
 raw=(Path(sys.argv[1])/file).read_bytes()
 assert hashlib.sha256(raw).hexdigest()==source['source_sha256'], 'upstream recording changed'
 rows=yaml.safe_load(raw)['interactions']
 records=[]
 for index in source['indices']:
  row=rows[index]
  assert row['request']['method'] in ('GET','DELETE')
  response=row['response']; body=response.get('body',{}).get('string','')
  headers={k:v for k,v in response.get('headers',{}).items() if k.lower() in ('x-ms-request-id','x-ms-correlation-request-id','location','azure-asyncoperation','retry-after')}
  records.append({'interaction_index':index,'method':row['request']['method'],'uri':row['request']['uri'],'status':response['status']['code'],'headers':headers,'body':json.loads(body) if body else None})
 result.append({'file':file,'source_uri':source['source_uri'],'source_sha256':source['source_sha256'],'recordings':records})
Path(__file__).with_name('cli-recordings.json').write_text(json.dumps(result,indent=2)+'\n')
print('Extracted',sum(len(r['recordings']) for r in result),'native Redis responses')
