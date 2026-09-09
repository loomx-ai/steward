"""Reproduce the sanitized Microsoft CLI fixture from two downloaded YAML recordings.

Usage: python3 reproduce_recordings.py /path/to/recordings (requires PyYAML).
Download the immutable source URLs in cli-delete-recordings.json into that directory.
"""
import hashlib, json, pathlib, sys, urllib.parse, yaml

p=pathlib.Path(sys.argv[1])
out=pathlib.Path(__file__).with_name('cli-delete-recordings.json')
sources=json.loads(out.read_text())
result=[]
for filename, index in [('test_amg_crud.yaml',16),('test_amg_private_endpoint.yaml',35),('test_amg_private_endpoint.yaml',59)]:
    raw=(p/filename).read_bytes()
    xs=yaml.safe_load(raw)['interactions']
    deletion=xs[index]
    path=urllib.parse.urlsplit(deletion['request']['uri']).path
    previous=[(i,r) for i,r in enumerate(xs[:index]) if r['request']['method']=='GET' and urllib.parse.urlsplit(r['request']['uri']).path.lower()==path.lower()]
    read_index, read=previous[-1]
    operation_path=urllib.parse.urlsplit(deletion['response']['headers']['azure-asyncoperation'][0]).path
    polls=[(i,x) for i,x in enumerate(xs[index+1:],index+1) if x['request']['method']=='GET' and urllib.parse.urlsplit(x['request']['uri']).path==operation_path]
    # Retain all native polling responses, including their delay sequence.
    # Signed query values are credentials for an operation and deliberately
    # replaced. Keep the exact source hash and selected interaction indices.
    def sanitize_url(url):
        u=urllib.parse.urlsplit(url)
        q=urllib.parse.parse_qsl(u.query,keep_blank_values=True)
        q=[(k, 'fixture-'+k if k in ['s','c','h','t'] else v) for k,v in q]
        return urllib.parse.urlunsplit(u._replace(query=urllib.parse.urlencode(q)))
    def response(x):
        r=x['response']
        headers={k:[sanitize_url(v) if k.lower() in ['location','azure-asyncoperation','operation-location'] else v for v in vs] for k,vs in r.get('headers',{}).items() if k.lower() in ['location','azure-asyncoperation','operation-location','retry-after','x-ms-request-id','content-type']}
        body=r.get('body',{}).get('string','')
        return dict(status=r['status']['code'],headers=headers,body=json.loads(body) if body else None)
    source=next(s for s in sources if s['source_uri'].endswith(filename))
    assert hashlib.sha256(raw).hexdigest()==source['source_sha256'], 'upstream recording fingerprint changed'
    result.append(dict(source_uri=source['source_uri'],source_sha256=hashlib.sha256(raw).hexdigest(),recorded_api_version='2023-09-01',read_index=read_index,delete_index=index,poll_indices=[i for i,x in polls],
        resource_uri=read['request']['uri'],read=response(read),delete=response(deletion),poll=[response(x) for i,x in polls]))
out.write_text(json.dumps(result,indent=2)+'\n')
print([(r['source_sha256'],r['delete_index'],len(r['poll'])) for r in result])
