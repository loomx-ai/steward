"""Extract selected native Microsoft Search GET/DELETE responses; no request bodies."""
import hashlib
import json
from pathlib import Path
import sys
import yaml

SOURCES = {'test_service_create_delete_show.yaml': {'source_uri': 'https://raw.githubusercontent.com/Azure/azure-cli/db34d9752ceddcde6db94eec5d681f6742d86403/src/azure-cli/azure/cli/command_modules/search/tests/latest/recordings/test_service_create_delete_show.yaml',
                                          'source_sha256': '18de8e572841e6e5c2c40f5744b47bb5eaf9052aacc8f198ddcbd464ccc8d963',
                                          'indices': [20, 21, 22]},
 'test_private_endpoint_connection_crud.yaml': {'source_uri': 'https://raw.githubusercontent.com/Azure/azure-cli/db34d9752ceddcde6db94eec5d681f6742d86403/src/azure-cli/azure/cli/command_modules/search/tests/latest/recordings/test_private_endpoint_connection_crud.yaml',
                                                'source_sha256': 'd3990b8a7ec5768d7bf70b2f20024917f93d076a63e84f7d3272047d42bf14bd',
                                                'indices': [40,
                                                            41,
                                                            42,
                                                            46,
                                                            47,
                                                            48]},
 'test_shared_private_link_resource_crud.yaml': {'source_uri': 'https://raw.githubusercontent.com/Azure/azure-cli/db34d9752ceddcde6db94eec5d681f6742d86403/src/azure-cli/azure/cli/command_modules/search/tests/latest/recordings/test_shared_private_link_resource_crud.yaml',
                                                 'source_sha256': '3f078cfa324f81f2068d8c22a64d2f6a6f95d61280ee33fc2f5f122ffc2d9e8c',
                                                 'indices': [3,
                                                             9,
                                                             15,
                                                             16,
                                                             17,
                                                             18,
                                                             19,
                                                             20,
                                                             21,
                                                             22]}}
result=[]
for file, source in SOURCES.items():
 raw=(Path(sys.argv[1])/('stable-'+file)).read_bytes()
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
print('Extracted',sum(len(r['recordings']) for r in result),'native Search responses')
