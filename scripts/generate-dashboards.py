"""Reproducible provisioned dashboards; no network or private input."""
import json
from pathlib import Path
root=Path(__file__).resolve().parent.parent/'infra/observability/grafana/dashboards'
groups=[('api','Service/API health',[
 ('HTTP throughput','sum by(status_class)(rate(judgegrid_http_requests_total[2m]))'),
 ('Accepted and terminal rates','sum by(operation,outcome)(rate(judgegrid_operations_total{operation=~"accepted|terminal"}[2m]))'),
 *[(f'HTTP p{int(p*100)}','histogram_quantile('+str(p)+',sum by(le,route)(rate(judgegrid_http_duration_seconds_bucket{route!="events"}[2m])))') for p in (.5,.95,.99)],
 ('HTTP sample count','sum(increase(judgegrid_http_duration_seconds_count{route!="events"}[2m]))'),
 ('Target availability','up{job=~"judgegrid-.+"}')]),
 ('dispatch','Outbox/Redis and fair scheduler',[
 ('Cached outbox states','judgegrid_state{kind="outbox"}'),('Oldest pending intent seconds','judgegrid_outbox_oldest_pending_seconds'),
 ('Pending logical jobs','judgegrid_state{kind="scheduler"}'),('Admission cap/rate rejection','sum by(operation,outcome)(rate(judgegrid_operations_total{outcome=~"capacity|throttle"}[2m]))'),
 ('Admission wait p95','histogram_quantile(0.95,sum by(le,phase)(rate(judgegrid_duration_seconds_bucket{phase=~"eligible_admission|accepted_admission|outbox_delay"}[2m])))'),
 ('Redis deliveries/duplicates','sum by(operation,outcome)(rate(judgegrid_operations_total{operation=~"redis.process|redis.duplicate|outbox.publish"}[2m]))'),
 ('Snapshot health and truncation','judgegrid_state{kind=~"snapshot_ok|snapshot_capped"}')]),
 ('judge','Judge phases and cache efficiency',[
 ('Phase p95 seconds','histogram_quantile(0.95,sum by(le,phase)(rate(judgegrid_duration_seconds_bucket{phase=~"bundle.fetch|cache.lookup|sandbox.workspace|cache.restore|sandbox.compile|sandbox.run|sandbox.cleanup|result.persist"}[5m])))'),
 ('Cache hit ratio','sum(rate(judgegrid_operations_total{operation="cache.hit"}[5m])) / clamp_min(sum(rate(judgegrid_operations_total{operation=~"cache.hit|cache.miss"}[5m])),0.001)'),
 ('Compiler/cache builds','sum by(operation,outcome)(increase(judgegrid_operations_total{operation=~"cache.build|cache.hit|cache.miss|cache.publish"}[5m]))'),
 ('Terminal verdict counts','sum by(outcome,language)(increase(judgegrid_operations_total{operation="terminal"}[5m]))'),
 ('Accepted-to-terminal p95','histogram_quantile(0.95,sum by(le)(rate(judgegrid_duration_seconds_bucket{phase="accepted_terminal"}[5m])))')]),
 ('recovery','Ownership, recovery and SSE',[
 ('Valid/expired leases','judgegrid_state{kind="leases"}'),('Lease renewals and fences','sum by(operation,outcome)(rate(judgegrid_operations_total{operation=~"lease.renew|lease.fail|fence.reject"}[2m]))'),
 ('SSE connections','judgegrid_state{kind="sse"}'),('Reconnect/replay/reset/slow','sum by(operation,outcome)(increase(judgegrid_operations_total{operation=~"sse.reconnect|sse.replay|sse.reset|sse.slow"}[5m]))'),
 ('Event timestamp to server flush p95','histogram_quantile(0.95,sum by(le)(rate(judgegrid_duration_seconds_bucket{phase="sse.delivery"}[2m])))'),
 ('Exporter errors/drops','sum by(operation,outcome)(rate(judgegrid_operations_total{operation=~"exporter.export|exporter.drop"}[2m]))'),
 ('Reconciliation outcomes','sum by(operation,outcome)(increase(judgegrid_operations_total{operation=~"reconcile.*"}[5m]))')])]
root.mkdir(parents=True,exist_ok=True)
for uid,title,queries in groups:
 panels=[{'id':1,'type':'text','title':'Reading these development measurements','gridPos':{'x':0,'y':0,'w':24,'h':3},'options':{'mode':'markdown','content':'Small samples are not capacity/SLO proof. Empty series means no observations. Histogram quantiles combine bucket rates across instances, never average instance quantiles. SSE HTTP time includes stream lifetime. Timestamp estimates include commit/skew; phase intervals may overlap. Trace lookup: Explore → Tempo → paste a trace_id from trusted operation logs; follow span links.'}}]
 for n,(label,query) in enumerate(queries,2):panels.append({'id':n,'type':'timeseries','title':label,'datasource':{'type':'prometheus','uid':'judgegrid-prometheus'},'gridPos':{'x':((n-2)%2)*12,'y':3+((n-2)//2)*8,'w':12,'h':8},'targets':[{'refId':'A','expr':query,'legendFormat':'{{phase}} {{operation}} {{outcome}} {{state}} {{route}}'}]})
 (root/(uid+'.json')).write_text(json.dumps({'uid':'judgegrid-'+uid,'title':'JudgeGrid — '+title,'schemaVersion':40,'version':1,'refresh':'5s','time':{'from':'now-15m','to':'now'},'panels':panels,'tags':['JudgeGrid','development']},indent=2)+'\n',encoding='utf-8')
print('Generated four JudgeGrid dashboard groupings')
