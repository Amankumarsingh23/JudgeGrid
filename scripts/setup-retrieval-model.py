"""Explicit public-artifact provisioning. Inference itself is strictly offline."""
import argparse,hashlib,json,pathlib,ssl,urllib.request

def main():
    parser=argparse.ArgumentParser()
    parser.add_argument('--directory',required=True)
    parser.add_argument('--ca-file',help='optional operator CA; TLS verification remains enabled')
    args=parser.parse_args()
    root=pathlib.Path(args.directory).resolve();root.mkdir(parents=True,exist_ok=True)
    lock=json.loads((pathlib.Path(__file__).resolve().parents[1]/'internal/retrieval/helper/model.lock.json').read_bytes())
    ctx=ssl.create_default_context()
    if args.ca_file:ctx.load_verify_locations(cafile=args.ca_file)
    for name,record in lock['artifacts'].items():
        path=root/name;path.parent.mkdir(parents=True,exist_ok=True)
        if path.exists() and path.stat().st_size==record['bytes'] and hashlib.file_digest(path.open('rb'),'sha256').hexdigest()==record['sha256']:continue
        url=f"https://huggingface.co/{lock['model']}/resolve/{lock['revision']}/{name}"
        with urllib.request.urlopen(url,timeout=120,context=ctx) as response:data=response.read(record['bytes']+1)
        if len(data)!=record['bytes'] or hashlib.sha256(data).hexdigest()!=record['sha256']:raise ValueError('public model artifact digest mismatch')
        tmp=path.with_suffix(path.suffix+'.staging');tmp.write_bytes(data);tmp.replace(path)
    print('Verified pinned public model artifacts; no private source was transmitted')
if __name__=='__main__':main()
