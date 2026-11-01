"""Explicitly provision pinned public cross-encoder artifacts; never source data."""
import argparse,hashlib,json,pathlib,ssl,urllib.request
def main():
 p=argparse.ArgumentParser();p.add_argument('--directory',required=True);p.add_argument('--ca-file');args=p.parse_args();root=pathlib.Path(args.directory).resolve();root.mkdir(parents=True,exist_ok=True)
 lock=json.loads((pathlib.Path(__file__).resolve().parents[1]/'internal/contextpack/helper/model.lock.json').read_bytes());ctx=ssl.create_default_context()
 if args.ca_file:ctx.load_verify_locations(cafile=args.ca_file)
 for name,record in lock['artifacts'].items():
  target=root/name;target.parent.mkdir(parents=True,exist_ok=True)
  if target.exists() and target.stat().st_size==record['bytes'] and hashlib.file_digest(target.open('rb'),'sha256').hexdigest()==record['sha256']:continue
  with urllib.request.urlopen(f"https://huggingface.co/{lock['model']}/resolve/{lock['revision']}/{name}",context=ctx,timeout=120) as r:data=r.read(record['bytes']+1)
  if len(data)!=record['bytes'] or hashlib.sha256(data).hexdigest()!=record['sha256']:raise ValueError('public model digest mismatch')
  tmp=target.with_suffix(target.suffix+'.staging');tmp.write_bytes(data);tmp.replace(target)
 print('Pinned cross-encoder artifacts verified; inference stays local')
if __name__=='__main__':main()
