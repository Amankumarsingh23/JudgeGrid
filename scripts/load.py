"""Bounded loopback-only HTTP load. No optional Python dependencies or private output."""
import datetime
import argparse,concurrent.futures,http.cookiejar,http.client,json,math,pathlib,socket,time,urllib.request,urllib.error,urllib.parse,uuid

class LocalIPv4Connection(http.client.HTTPConnection):
    def connect(self):
        if self.host not in ('localhost','127.0.0.1'):raise ValueError('loopback only')
        self.sock=socket.create_connection(('127.0.0.1',self.port),self.timeout,self.source_address)
class LocalIPv4Handler(urllib.request.HTTPHandler):
    def http_open(self,req):return self.do_open(LocalIPv4Connection,req)
def percentile(rows,p):
    return sorted(rows)[max(0,math.ceil(len(rows)*p)-1)] if rows else None
def main():
    p=argparse.ArgumentParser();p.add_argument('--base',default='http://localhost:8080');p.add_argument('--profile',choices=['read','create','replay','burst','real','cache','sse'],required=True);p.add_argument('--username',required=True);p.add_argument('--password-file',required=True);p.add_argument('--total',type=int,default=10);p.add_argument('--concurrency',type=int,default=2);p.add_argument('--duration',type=int,default=60);p.add_argument('--seed',default='11');p.add_argument('--output',required=True);a=p.parse_args()
    u=urllib.parse.urlsplit(a.base)
    if u.hostname not in ('localhost','127.0.0.1','::1') or u.scheme not in ('http','https') or u.username or u.query or u.fragment: p.error('explicit loopback origin required')
    if not 1<=a.total<=200 or not 1<=a.concurrency<=16 or not 1<=a.duration<=180 or (a.profile in ('real','cache') and a.total>6):p.error('bounded work/concurrency/time required; real/cache maximum six')
    # Keep Host/Origin/cookies unchanged while avoiding Windows localhost IPv6
    # fallback delay. No environment proxy may receive local account credentials.
    handlers=[urllib.request.ProxyHandler({}),urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar())]
    if u.scheme=='http' and u.hostname in ('localhost','127.0.0.1'):handlers.append(LocalIPv4Handler())
    opener=urllib.request.build_opener(*handlers)
    def request(method,path,body=None,key=None,cursor=None):
        headers={'Origin':a.base,'Content-Type':'application/json'}
        if session:headers['X-CSRF-Token']=session['csrf_token']
        if key:headers['Idempotency-Key']=key
        if cursor:headers['Last-Event-ID']=cursor
        q=urllib.request.Request(a.base+'/api/v1'+path,data=json.dumps(body).encode() if body is not None else None,headers=headers,method=method)
        return opener.open(q,timeout=min(10,max(.1,deadline-time.monotonic() if deadline else a.duration)))
    session=None;deadline=None
    with request('POST','/session',{'username':a.username,'password':pathlib.Path(a.password_file).read_text().strip()}) as r:session=json.load(r)
    with request('GET','/problems') as r:problem=next(x for x in json.load(r) if x['slug']=='two-sum')
    run=str(uuid.uuid4());source='int main(){}' if a.profile=='cache' else 'a,b=map(int,input().split());print(a+b)'
    if a.profile=='cache':source='#include <iostream>\n// seed '+a.seed+' '+run+'\nint main(){long long a,b;std::cin>>a>>b;std::cout<<a+b;}'
    body={'problem_id':problem['problem_id'],'language':'cpp17' if a.profile=='cache' else ('python3' if a.profile=='real' else 'go'),'source_code':source}
    ids=[];samples=[];codes={};started=time.monotonic();deadline=started+a.duration
    if a.profile in ('replay','sse'):
        with request('POST','/submissions',body,run) as r:ids.append(json.load(r)['submission_id'])
    def work(i):
        if time.monotonic()>=deadline:return None
        start=time.monotonic();code=0;sid=None
        try:
            if a.profile=='read':r=request('GET','/problems')
            elif a.profile=='sse':
                r=request('GET','/submissions/'+ids[0]+'/events');cursor=None
                for raw in r:
                    if raw.startswith(b'id: '):cursor=raw.decode().strip()[4:]
                    if raw.startswith(b'event: snapshot'):break
                r.close();r=request('GET','/submissions/'+ids[0]+'/events',cursor=cursor)
                for raw in r:
                    if raw.startswith(b'event: snapshot'):break
            else:r=request('POST','/submissions',body,run if a.profile=='replay' else run+'-'+str(i))
            code=r.status
            if a.profile!='sse':data=json.load(r);sid=data.get('submission_id') if isinstance(data,dict) else None
            r.close()
        except urllib.error.HTTPError as e:code=e.code;e.close()
        except (OSError,TimeoutError):code=0
        return code,time.monotonic()-start,sid
    # Cache trials are sequential: first cold, subsequent identical-source warm.
    concurrency=1 if a.profile=='cache' else a.concurrency
    terminal=[]
    def drain(sid):
        begin=time.monotonic()
        while time.monotonic()<deadline:
            try:
                with request('GET','/submissions/'+sid) as r:s=json.load(r)
                if s['status'] in ('COMPLETED','FAILED'):return {'status':s['status'],'verdict':s.get('verdict'),'terminal_duration_seconds':(datetime.datetime.fromisoformat(s['updated_at'])-datetime.datetime.fromisoformat(s['created_at'])).total_seconds(),'poll_wait_seconds':time.monotonic()-begin}
            except (urllib.error.URLError,OSError):pass
            time.sleep(.2)
        return {'status':'deadline','verdict':None,'poll_wait_seconds':time.monotonic()-begin}
    if a.profile=='cache':
        results=[]
        for i in range(a.total):
            row=work(i)
            if row:results.append(row)
            if row and row[2]:terminal.append(drain(row[2]))
    else:
        with concurrent.futures.ThreadPoolExecutor(max_workers=concurrency) as pool:results=list(pool.map(work,range(a.total)))
    http_elapsed=sum(r[1] for r in results if r) if a.profile=='cache' else time.monotonic()-started
    for row in results:
        if row:
            code,latency,sid=row;codes[str(code)]=codes.get(str(code),0)+1;samples.append(latency)
            if sid and sid not in ids:ids.append(sid)
    if a.profile=='real':terminal=[drain(sid) for sid in ids]
    elapsed=time.monotonic()-started
    result={'profile':a.profile,'seed':a.seed,'requested':a.total,'samples':len(samples),'concurrency':concurrency,'duration_cap_seconds':a.duration,'http_status_counts':codes,'http_seconds':http_elapsed,'total_seconds':elapsed,'requests_per_second':len(samples)/http_elapsed,'accepted_per_second':codes.get('201',0)/http_elapsed,'rejected_per_second':sum(codes.get(str(n),0) for n in (429,503))/http_elapsed,'completed':sum(x['status']=='COMPLETED' for x in terminal),'terminal_results':terminal,'completed_per_second':sum(x['status']=='COMPLETED' for x in terminal)/elapsed,'http_latency_seconds':{k:percentile(samples,p) for k,p in [('p50',.5),('p95',.95),('p99',.99)]},'sample_unit':'connect+reconnect pair' if a.profile=='sse' else 'HTTP request','precondition_accepted':int(a.profile in ('replay','sse')),'tool_python':__import__('sys').version.split()[0],'limits':'small loopback development sample; cache HTTP throughput uses summed serial request time; no production capacity inference'}
    pathlib.Path(a.output).write_text(json.dumps(result,indent=2)+'\n',encoding='utf-8');print(json.dumps(result))
if __name__=='__main__':main()
