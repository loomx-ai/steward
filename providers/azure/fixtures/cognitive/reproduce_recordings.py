"""Extract selected official Cognitive Services responses; never request bodies."""
import hashlib
import json
from pathlib import Path
import sys
import yaml

SOURCES = {'test_cognitiveservices_crud.yaml': {'source_uri': 'https://raw.githubusercontent.com/Azure/azure-cli/dc50d475a00ded4a1a1980d4a10a9fbd9a750a81/src/azure-cli/azure/cli/command_modules/cognitiveservices/tests/latest/recordings/test_cognitiveservices_crud.yaml', 'source_sha256': '39899207ed18783ed58c3ba928671d05bf04af1c5a8bb8902f70fae2184fc829', 'indices': [5, 9, 10]}, 'test_cognitiveservices_commitment_plan.yaml': {'source_uri': 'https://raw.githubusercontent.com/Azure/azure-cli/dc50d475a00ded4a1a1980d4a10a9fbd9a750a81/src/azure-cli/azure/cli/command_modules/cognitiveservices/tests/latest/recordings/test_cognitiveservices_commitment_plan.yaml', 'source_sha256': '422b68cb6862bccb73fba3eeb4d12bb9d85567e6d57c7723617b73f839ebb0b3', 'indices': [5, 9, 10, 11, 12]}, 'test_cognitiveservices_deployment.yaml': {'source_uri': 'https://raw.githubusercontent.com/Azure/azure-cli/dc50d475a00ded4a1a1980d4a10a9fbd9a750a81/src/azure-cli/azure/cli/command_modules/cognitiveservices/tests/latest/recordings/test_cognitiveservices_deployment.yaml', 'source_sha256': '4e5f93a150656b3a890872bd75883da7a20b918a1358dbcfb4030c097dfc7c72', 'indices': [3, 6, 7, 8, 9]}, 'test_cognitiveservices_private_endpoint_connection.yaml': {'source_uri': 'https://raw.githubusercontent.com/Azure/azure-cli/dc50d475a00ded4a1a1980d4a10a9fbd9a750a81/src/azure-cli/azure/cli/command_modules/cognitiveservices/tests/latest/recordings/test_cognitiveservices_private_endpoint_connection.yaml', 'source_sha256': '90339997831d242fcefad11b68da1fef237fd99dc4fa95186bd2d46fe4640eac', 'indices': [22, 29, 30, 31, 32]}, 'test_account_connections_from_file.yaml': {'source_uri': 'https://raw.githubusercontent.com/Azure/azure-cli/dc50d475a00ded4a1a1980d4a10a9fbd9a750a81/src/azure-cli/azure/cli/command_modules/cognitiveservices/tests/latest/recordings/test_account_connections_from_file.yaml', 'source_sha256': '36f629af832b7cd5fcc125bc0d561bbfc0f650e20ae8c3f174bf921075573534', 'indices': [3, 5, 6, 7]}, 'test_project_connections_from_file.yaml': {'source_uri': 'https://raw.githubusercontent.com/Azure/azure-cli/dc50d475a00ded4a1a1980d4a10a9fbd9a750a81/src/azure-cli/azure/cli/command_modules/cognitiveservices/tests/latest/recordings/test_project_connections_from_file.yaml', 'source_sha256': '19976c422c74f5ea24140778fb344beb7ee4ac9377e7bdf20551d78626c55032', 'indices': [3, 5, 8, 9, 10, 11]}, 'test_cognitiveservices_softdelete.yaml': {'source_uri': 'https://raw.githubusercontent.com/Azure/azure-cli/dc50d475a00ded4a1a1980d4a10a9fbd9a750a81/src/azure-cli/azure/cli/command_modules/cognitiveservices/tests/latest/recordings/test_cognitiveservices_softdelete.yaml', 'source_sha256': '2c72211a30ead51e8452136d9b24ae936bc0ee00600007b201ffd7dc213dd951', 'indices': [5, 6, 7]}}
result=[]
for file,source in SOURCES.items():
 raw=(Path(sys.argv[1])/file).read_bytes()
 assert hashlib.sha256(raw).hexdigest()==source['source_sha256'],'upstream recording changed'
 rows=yaml.safe_load(raw)['interactions'];records=[]
 for index in source['indices']:
  row=rows[index];assert row['request']['method'] in ('GET','DELETE')
  response=row['response'];body=response.get('body',{}).get('string','')
  headers={k:v for k,v in response.get('headers',{}).items() if k.lower() in ('x-ms-request-id','x-ms-correlation-request-id','location','azure-asyncoperation','retry-after')}
  records.append({'interaction_index':index,'method':row['request']['method'],'uri':row['request']['uri'],'status':response['status']['code'],'headers':headers,'body':json.loads(body) if body else None})
 result.append({'file':file,'source_uri':source['source_uri'],'source_sha256':source['source_sha256'],'recordings':records})
Path(__file__).with_name('cli-recordings.json').write_text(json.dumps(result,indent=2)+'\n')
print('Extracted',sum(len(r['recordings'])for r in result),'native Cognitive Services responses')
