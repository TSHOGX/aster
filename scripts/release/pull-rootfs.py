#!/usr/bin/env python3
"""Fetch the pinned OCI rootfs into a release directory using a read-only token."""
import argparse,hashlib,json,os,pathlib,re,subprocess
p=argparse.ArgumentParser();p.add_argument('--directory',type=pathlib.Path,required=True);a=p.parse_args()
m=json.loads((a.directory/'release-manifest.json').read_text());reference=m['sandbox']['reference'];archive=m['sandbox']['archive']
if not re.fullmatch(r'ghcr\.io/[a-z0-9/_.-]+@sha256:[a-f0-9]{64}',reference):raise SystemExit('invalid rootfs reference')
subprocess.run(['oras','login','ghcr.io','--username',os.environ['GITHUB_ACTOR'],'--password-stdin'],input=os.environ['GHCR_TOKEN'],text=True,check=True)
try:
    subprocess.run(['oras','pull',reference,'--output',str(a.directory)],check=True)
    path=a.directory/archive['name'];h=hashlib.sha256()
    with path.open('rb') as f:
        for b in iter(lambda:f.read(1024*1024),b''):h.update(b)
    if path.stat().st_size!=archive['size'] or h.hexdigest()!=archive['sha256']:raise SystemExit('rootfs checksum mismatch')
finally:subprocess.run(['oras','logout','ghcr.io'],check=True)
