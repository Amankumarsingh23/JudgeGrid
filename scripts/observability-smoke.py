"""Read-only backend/provisioning checks. Requires the explicitly started local overlay."""
import json,time,urllib.request,urllib.parse
def read(url):
    with urllib.request.urlopen(url,timeout=5) as r:return json.load(r)
def wait(url):
    deadline=time.monotonic()+60
    while time.monotonic()<deadline:
        try:
            with urllib.request.urlopen(url,timeout=3) as r:
                if r.status==200:return
        except OSError:pass
        time.sleep(1)
    raise RuntimeError('local backend readiness deadline')
for url in ['http://127.0.0.1:19090/-/ready','http://127.0.0.1:13200/ready','http://127.0.0.1:13000/api/health']:wait(url)
health=read('http://127.0.0.1:13000/api/health');assert health['database']=='ok'
dashboards=read('http://127.0.0.1:13000/api/search?type=dash-db');assert len([d for d in dashboards if d['uid'].startswith('judgegrid-')])==4
datasource_health={}
for uid in ['judgegrid-prometheus','judgegrid-tempo']:
    datasource_health[uid]=read('http://127.0.0.1:13000/api/datasources/uid/'+uid+'/health');assert datasource_health[uid]['status']=='OK'
targets=read('http://127.0.0.1:19090/api/v1/targets')['data']['activeTargets']
deadline=time.monotonic()+30
while time.monotonic()<deadline and not all(any(t['labels']['job']==job and t['health']=='up' for t in targets) for job in ('judgegrid-api','judgegrid-relay')):
    time.sleep(1);targets=read('http://127.0.0.1:19090/api/v1/targets')['data']['activeTargets']
states={t['labels']['job']+':'+t['labels']['instance']:t['health'] for t in targets}
for job in ('judgegrid-api','judgegrid-relay'):
    assert any(t['labels']['job']==job and t['health']=='up' for t in targets),(job,states)
queries={}
for q in ['up','sum(judgegrid_http_requests_total)','sum(judgegrid_operations_total{operation="accepted"})','judgegrid_state{kind="snapshot_ok"}','sum by(phase)(judgegrid_duration_seconds_count)','histogram_quantile(0.95,sum by(le)(rate(judgegrid_http_duration_seconds_bucket{route!="events"}[2m])))']:
    result=read('http://127.0.0.1:19090/api/v1/query?'+urllib.parse.urlencode({'query':q}));assert result['status']=='success';queries[q]=result['data']['result']
traces=[];deadline=time.monotonic()+30
while time.monotonic()<deadline and not traces:
    traces=read('http://127.0.0.1:13200/api/search?limit=5').get('traces',[])
    if not traces:time.sleep(1)
assert traces,'no actual exported trace found in Tempo'
result={'stored_trace_examples':traces,'datasource_health':datasource_health,'grafana_database':health['database'],'dashboard_uids':[d['uid'] for d in dashboards],'targets':states,'queries':queries,'visual_inspection':'not performed; verifies provisioning/connectivity/query results only'}
print(json.dumps(result,indent=2))
