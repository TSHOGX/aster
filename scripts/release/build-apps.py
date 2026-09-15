#!/usr/bin/env python3
"""Build the exact checked-out revision; outputs never include host state."""
import argparse, hashlib, json, os, pathlib, shutil, subprocess, tarfile, tempfile, urllib.request
ROOT = pathlib.Path(__file__).resolve().parents[2]
def run(*args, **kw): subprocess.run(args, check=True, **kw)
def sha(p):
    h=hashlib.sha256()
    with p.open('rb') as f:
        for b in iter(lambda:f.read(1024*1024),b''): h.update(b)
    return h.hexdigest()
# Explicit allowlist: ignored or accidentally tracked local files cannot enter
# Release archives merely by residing under deploy/ or docs/delivery/.
PUBLIC_DELIVERY_FILES = {
    "deploy/target.example.json": "examples/target.example.json",
    "deploy/harness.example.yaml": "examples/harness.example.yaml",
    "deploy/nginx-server.inc.example": "examples/nginx-server.inc.example",
    "docs/delivery/operations.md": "docs/operations.md",
    "docs/delivery/privacy.md": "docs/privacy.md",
}
def copy_public_delivery_files(source,destination):
    for src,dst in PUBLIC_DELIVERY_FILES.items():
        path=source/src
        if path.is_symlink():raise RuntimeError("public release inputs must not be symlinks")
        out=destination/dst;out.parent.mkdir(parents=True,exist_ok=True);shutil.copy2(path,out)

def main():
    p=argparse.ArgumentParser();p.add_argument('--version',required=True);p.add_argument('--output',type=pathlib.Path,required=True);a=p.parse_args()
    import re
    if not re.fullmatch(r'v(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)(?:-rc\.(?:0|[1-9][0-9]*))?',a.version):p.error('exact release tag required')
    pin=json.loads((ROOT/'scripts/release/toolchains.json').read_text());commit=subprocess.check_output(['git','rev-parse','HEAD'],cwd=ROOT,text=True).strip()
    if subprocess.check_output(['go','env','GOVERSION'],text=True).strip()!='go'+pin['go']:p.error('use pinned Go '+pin['go'])
    if os.uname().machine!='x86_64':p.error('native Linux amd64 builder required')
    a.output.mkdir(parents=True,exist_ok=True);artifacts={}
    with tempfile.TemporaryDirectory(prefix='harness-apps-') as td:
        tmp=pathlib.Path(td);node_archive=tmp/'node.tar.xz'
        with urllib.request.urlopen(f'https://nodejs.org/dist/v{pin["node"]}/node-v{pin["node"]}-linux-x64.tar.xz',timeout=120) as r,node_archive.open('wb') as f:shutil.copyfileobj(r,f)
        if sha(node_archive)!=pin['node_linux_x64_sha256']:raise RuntimeError('Node checksum mismatch')
        node=tmp/'node';node.mkdir();run('tar','-xJf',str(node_archive),'-C',str(node),'--strip-components=1')
        env={**os.environ,'PATH':str(node/'bin')+':'+os.environ['PATH'],'NEXT_TELEMETRY_DISABLED':'1'}
        run('npm','ci',cwd=ROOT/'frontend',env=env);run('npm','run','build',cwd=ROOT/'frontend',env=env)
        standalone=ROOT/'frontend/.next/standalone'
        if not (standalone/'server.js').is_file():raise RuntimeError('standalone server.js missing')
        frontend=tmp/'frontend';shutil.copytree(standalone,frontend,symlinks=True)
        shutil.copytree(ROOT/'frontend/.next/static',frontend/'.next/static',symlinks=True,dirs_exist_ok=True)
        if (ROOT/'frontend/public').exists():shutil.copytree(ROOT/'frontend/public',frontend/'public',symlinks=True,dirs_exist_ok=True)
        shutil.copytree(node,frontend/'node',symlinks=True)
        for kind,cmd in [('backend','orchestrator'),('deploy','harness-deploy')]:
            dest=tmp/kind; (dest/'bin').mkdir(parents=True)
            run('go','build','-trimpath','-ldflags',f'-s -w -X main.version={a.version} -X main.commit={commit}','-o',str(dest/'bin'/cmd),'./cmd/'+cmd,cwd=ROOT/'orchestrator',env={**os.environ,'CGO_ENABLED':'0','GOOS':'linux','GOARCH':'amd64'})
            if kind=='backend':(dest/'config-reference').mkdir();shutil.copy2(ROOT/'deploy/harness.example.yaml',dest/'config-reference/harness.yaml')
            else:
                copy_public_delivery_files(ROOT,dest)
        for kind,label in [('backend','orchestrator'),('frontend','frontend'),('deploy','harness-deploy')]:
            archive=a.output/f'{label}-{a.version}-linux-amd64.tar.gz'
            if archive.exists():raise RuntimeError(f'refusing to overwrite {archive}')
            with tarfile.open(archive,'w:gz',format=tarfile.PAX_FORMAT) as tar:
                for child in sorted((tmp/kind).iterdir()):tar.add(child,arcname=child.name)
            artifacts[kind]={'name':archive.name,'size':archive.stat().st_size,'sha256':sha(archive)}
        (a.output/'app-build.json').write_text(json.dumps({'version':a.version,'commit':commit,'toolchains':pin,'artifacts':artifacts},indent=2)+'\n')
if __name__=='__main__':main()
