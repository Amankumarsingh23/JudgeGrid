"""Pinned offline evidence scorer/tokenizer. Input text never selects code/tools."""
import os
os.environ['OMP_NUM_THREADS']='1';os.environ['OPENBLAS_NUM_THREADS']='1';os.environ['TOKENIZERS_PARALLELISM']='false'
import hashlib,importlib.metadata,json,math,pathlib,sys,time
sys.stdout.reconfigure(encoding='utf-8')

def canonical(v):return json.dumps(v,ensure_ascii=False,sort_keys=True,separators=(',',':'))
def offsets(text):
    result=[0]
    for char in text:result.append(result[-1]+len(char.encode('utf-8')))
    return result
def setup(root):
    if sys.version_info[:3]!=(3,13,15):raise ValueError('python')
    lock=json.loads(pathlib.Path(__file__).with_name('model.lock.json').read_bytes());root=pathlib.Path(root)
    if not root.is_absolute():raise ValueError('root')
    root=root.resolve(strict=True)
    for name,record in lock['artifacts'].items():
        p=root/name
        if not p.is_file() or not p.resolve().is_relative_to(root) or p.stat().st_size!=record['bytes']:raise ValueError('artifact')
        with p.open('rb') as stream:
            if hashlib.file_digest(stream,'sha256').hexdigest()!=record['sha256']:raise ValueError('digest')
    for name,version in lock['runtime'].items():
        if importlib.metadata.version(name)!=version:raise ValueError('runtime')
    from tokenizers import Tokenizer
    return root,Tokenizer.from_file(str(root/'tokenizer.json'))
def score(root,tok,request):
    started=time.perf_counter();query=request['query'];docs=request['documents'];policy=request['policy']
    if not 1<=len(docs)<=48 or not query or len(query.encode())>1024:raise ValueError('inputs')
    qenc=tok.encode(query,add_special_tokens=False);qtrunc=len(qenc.ids)>96
    if qtrunc:query=query[:qenc.offsets[95][1]]
    count=policy['window_tokens'];overlap=policy['window_overlap'];quota=min(policy['windows_per_document'],policy['max_pairs']//len(docs))
    if not 32<=count<=320 or not 0<=overlap<=count//3 or not 1<=quota<=6:raise ValueError('policy')
    output=[];pairs=[];owners=[]
    for d in docs:
        text=d['text']
        if not text or len(text.encode())>16384 or '\x00' in text:raise ValueError('source')
        head='language: '+d['language']+'\npath: '+d['path']+'\nsymbol: '+d['symbol']+'\ncode:\n'
        he=tok.encode(head,add_special_tokens=False);meta_truncated=len(he.ids)>92
        if meta_truncated:head=head[:he.offsets[91][1]]+'\n'
        enc=tok.encode(text,add_special_tokens=False);steps=list(range(0,max(1,len(enc.ids)-count+1),count-overlap))
        tail=max(0,len(enc.ids)-count)
        if steps[-1]!=tail:steps.append(tail)
        selected=steps if len(steps)<=quota else [steps[round(i*(len(steps)-1)/(quota-1))] for i in range(quota)] if quota>1 else [tail]
        selected=sorted(set(selected));byte=offsets(text);windows=[]
        for start in selected:
            end=min(start+count,len(enc.ids));left=enc.offsets[start][0] if enc.ids else 0;right=enc.offsets[end-1][1] if enc.ids else len(text)
            # Full coverage includes whitespace at the outside of first/last windows.
            if start==0:left=0
            if end==len(enc.ids):right=len(text)
            pair=tok.encode(query,head+text[left:right],add_special_tokens=True)
            if len(pair.ids)>512:raise ValueError('sequence budget')
            windows.append({'start':byte[left],'end':byte[right],'tokens':len(pair.ids),'score':0.0});pairs.append(pair);owners.append((len(output),len(windows)-1))
        covered=0;pos=0
        for w in windows:
            covered+=max(0,w['end']-max(pos,w['start']));pos=max(pos,w['end'])
        output.append({'id':d['id'],'windows':windows,'total_windows':len(steps),'covered_bytes':covered,'truncated':covered<len(text.encode()),'metadata_truncated':meta_truncated})
    tokenize=time.perf_counter()-started
    import numpy as np
    import onnxruntime as ort
    load=time.perf_counter();options=ort.SessionOptions();options.intra_op_num_threads=1;options.inter_op_num_threads=1;options.execution_mode=ort.ExecutionMode.ORT_SEQUENTIAL;options.log_severity_level=4
    session=ort.InferenceSession(str(root/'onnx/model.onnx'),sess_options=options,providers=['CPUExecutionProvider']);load=time.perf_counter()-load
    begin=time.perf_counter()
    for start in range(0,len(pairs),8):
        batch=pairs[start:start+8];width=max(len(e.ids) for e in batch)
        ids=np.zeros((len(batch),width),dtype=np.int64);mask=np.zeros_like(ids);types=np.zeros_like(ids)
        for i,e in enumerate(batch):ids[i,:len(e.ids)]=e.ids;mask[i,:len(e.ids)]=1;types[i,:len(e.ids)]=e.type_ids
        available={'input_ids':ids,'attention_mask':mask,'token_type_ids':types}
        scores=session.run(None,{v.name:available[v.name] for v in session.get_inputs()})[0].reshape(-1)
        if len(scores)!=len(batch) or not np.isfinite(scores).all():raise ValueError('scores')
        for i,value in enumerate(scores):doc,w=owners[start+i];output[doc]['windows'][w]['score']=float(value)
    return {'documents':output,'pairs':len(pairs),'load_seconds':load,'inference_seconds':time.perf_counter()-begin,'tokenize_seconds':tokenize,'query_truncated':qtrunc}
def assemble(tok,request):
    scope=request['scope'];budget=request['budget'];blocks=request['blocks']
    if not 128<=budget<=8192 or len(blocks)>10:raise ValueError('pack budget')
    evidence=[];omitted=[]
    def render(items):return canonical({'schema':'judgegrid-evidence-v1','scope':scope,'evidence':items})
    def count(items):return len(tok.encode(render(items),add_special_tokens=False).ids)
    if count([])>budget:return {'text':render([]),'evidence':[],'omitted':[{'chunk':b['chunk_id'],'reason':'headers_exceed_budget'} for b in blocks],'tokens':count([])}
    for block in blocks:
        original=dict(block);original['evidence_id']='E'+str(len(evidence)+1)
        if count(evidence+[original])<=budget:evidence.append(original);continue
        text=original['source'];enc=tok.encode(text,add_special_tokens=False)
        # Try exact suffix subspans. Hash, byte/line coordinates and all citation
        # metadata are recomputed before each actual full-wire token count.
        def cropped(char):
            out=dict(original);prefix=text[:char];suffix=text[char:];span=dict(original['span']);span['start']+=len(prefix.encode());span['start_line']+=prefix.count('\n');span['start_column']=len(prefix.rsplit('\n',1)[-1].encode()) if '\n' in prefix else span['start_column']+len(prefix.encode());out['span']=span;out['source']=suffix;out['sha256']=hashlib.sha256(suffix.encode()).hexdigest();out['partial_span']=True;out['selection']+=';budget-cropped-suffix';return out
        lo,hi=0,len(enc.ids);best=None
        while lo<hi:
            mid=(lo+hi)//2;char=enc.offsets[mid][0];candidate=cropped(char)
            if count(evidence+[candidate])<=budget:best=candidate;hi=mid
            else:lo=mid+1
        if best is None and lo<len(enc.ids):
            candidate=cropped(enc.offsets[lo][0])
            if count(evidence+[candidate])<=budget:best=candidate
        if best is not None and best['source'].strip():evidence.append(best)
        else:omitted.append({'chunk':original['chunk_id'],'reason':'token_budget'})
    return {'text':render(evidence),'evidence':evidence,'omitted':omitted,'tokens':count(evidence)}
def main():
    data=sys.stdin.buffer.read((2<<20)+1)
    if len(data)>2<<20:raise ValueError('request size')
    request=json.loads(data);root,tok=setup(request['root'])
    action=request['action']
    if action=='score':result=score(root,tok,request)
    elif action=='pack':result=assemble(tok,request)
    elif action=='count':
        if len(request['texts'])>12 or sum(len(s.encode()) for s in request['texts'])>1<<20:raise ValueError('count budget')
        result=[len(tok.encode(s,add_special_tokens=False).ids) for s in request['texts']]
    else:raise ValueError('action')
    json.dump(result,sys.stdout,ensure_ascii=False,allow_nan=False,separators=(',',':'))
if __name__=='__main__':
    try:main()
    except Exception:sys.exit(1)
