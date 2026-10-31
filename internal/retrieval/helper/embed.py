"""Trusted offline ONNX inference. Repository data is JSON text, never code."""
import os
os.environ['TOKENIZERS_PARALLELISM']='false'
os.environ['OMP_NUM_THREADS']='1'
os.environ['OPENBLAS_NUM_THREADS']='1'
import hashlib,importlib.metadata,json,pathlib,sys

def main():
    if sys.version_info[:3]!=(3,13,15):raise ValueError('python')
    request=json.loads(sys.stdin.buffer.read(1<<20))
    lock=json.loads(pathlib.Path(__file__).with_name('model.lock.json').read_bytes())
    root=pathlib.Path(request['root'])
    if not root.is_absolute():raise ValueError('root')
    root=root.resolve(strict=True)
    for name,record in lock['artifacts'].items():
        path=root/name
        if not path.is_file() or not path.resolve().is_relative_to(root):raise ValueError('artifact')
        if path.stat().st_size!=record['bytes'] or hashlib.file_digest(path.open('rb'),'sha256').hexdigest()!=record['sha256']:raise ValueError('digest')
    for name,version in lock['runtime'].items():
        if importlib.metadata.version(name)!=version:raise ValueError('runtime')
    texts=request['texts']
    if not 1<=len(texts)<=32 or any(not isinstance(t,str) or not t or len(t.encode('utf-8'))>20000 or '\x00' in t for t in texts):raise ValueError('input')
    import numpy as np
    import onnxruntime as ort
    from tokenizers import Tokenizer
    tokenizer=Tokenizer.from_file(str(root/'tokenizer.json'))
    tokenizer.enable_truncation(max_length=256,direction='right')
    tokenizer.enable_padding(pad_id=0,pad_token='[PAD]')
    enc=tokenizer.encode_batch(texts)
    ids=np.array([e.ids for e in enc],dtype=np.int64)
    mask=np.array([e.attention_mask for e in enc],dtype=np.int64)
    options=ort.SessionOptions()
    options.intra_op_num_threads=1;options.inter_op_num_threads=1
    options.execution_mode=ort.ExecutionMode.ORT_SEQUENTIAL
    options.log_severity_level=4
    session=ort.InferenceSession(str(root/'onnx/model.onnx'),sess_options=options,providers=['CPUExecutionProvider'])
    inputs={'input_ids':ids,'attention_mask':mask,'token_type_ids':np.zeros_like(ids)}
    hidden=session.run(None,{i.name:inputs[i.name] for i in session.get_inputs()})[0]
    weights=mask[:,:,None].astype(np.float32)
    pooled=(hidden*weights).sum(axis=1)/weights.sum(axis=1).clip(min=1e-9)
    vectors=(pooled/np.linalg.norm(pooled,axis=1,keepdims=True).clip(min=1e-9)).astype(np.float32)
    if vectors.shape!=(len(texts),384) or not np.isfinite(vectors).all():raise ValueError('vector')
    json.dump(vectors.tolist(),sys.stdout,allow_nan=False,separators=(',',':'))
if __name__=='__main__':
    try:main()
    except Exception:
        # No source, queries, model paths or exception payloads leave the helper.
        sys.exit(1)
