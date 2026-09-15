#!/usr/bin/env python3
"""Install a checksum-pinned runsc on a disposable GitHub runner."""
import argparse,hashlib,json,pathlib,urllib.request
p=argparse.ArgumentParser();p.add_argument('--output',type=pathlib.Path,required=True);a=p.parse_args()
pins=json.loads((pathlib.Path(__file__).resolve().parent/'toolchains.json').read_text())
url=f'https://storage.googleapis.com/gvisor/releases/release/{pins["runsc"]}/x86_64/runsc'
a.output.parent.mkdir(parents=True,exist_ok=True)
with urllib.request.urlopen(url,timeout=120) as r,a.output.open('wb') as f:
    h=hashlib.sha512()
    for b in iter(lambda:r.read(1024*1024),b''):h.update(b);f.write(b)
if h.hexdigest()!=pins['runsc_linux_x64_sha512']:
    a.output.unlink();raise SystemExit('runsc checksum mismatch')
a.output.chmod(0o755)
