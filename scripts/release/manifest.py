#!/usr/bin/env python3
"""Assemble a release contract only after an immutable rootfs is available."""
import argparse, hashlib, json, pathlib, re

def sha(path):
    h=hashlib.sha256()
    with path.open('rb') as f:
        for b in iter(lambda:f.read(1024*1024),b''):h.update(b)
    return h.hexdigest()
def main():
    p=argparse.ArgumentParser();p.add_argument('--directory',type=pathlib.Path,required=True);p.add_argument('--sandbox-reference',required=True);p.add_argument('--runsc-version',required=True);a=p.parse_args()
    if not re.fullmatch(r'ghcr\.io/[a-z0-9/_.-]+@sha256:[a-f0-9]{64}',a.sandbox_reference):p.error('immutable GHCR digest required')
    app=json.loads((a.directory/'app-build.json').read_text());rootfs=a.directory/f'sandbox-{app["version"]}-linux-amd64.tar.gz'
    manifest={'format':1,'version':app['version'],'commit':app['commit'],'os':'linux','arch':'amd64','ubuntu':['22.04','24.04'],'toolchains':app['toolchains'],'artifacts':app['artifacts'],'sandbox':{'reference':a.sandbox_reference,'archive':{'name':rootfs.name,'size':rootfs.stat().st_size,'sha256':sha(rootfs)},'pi':app['toolchains']['pi'],'bridge':'pi_rpc_events_v1.0+turn_control_v1'},'runsc_versions':[a.runsc_version],'schema':{'store':1,'memorystore':1},'supported_sources':['669542d4ab3453509a6872e996cdf38bc7331edf','237f8b3b82cb6bc03f141da3df1f98e55445670e','2be43254c555e4d7b087ea7675063e1e2df5501a','managed-schema-1'],'downtime':True,'rollback':'snapshot-before-traffic','external_dependencies':['model-proxy','configured-evolution-project','systemd','nginx','runsc','iproute2','nftables','GNU tar with ACL/xattr support']}
    target=a.directory/'release-manifest.json'
    if target.exists():raise RuntimeError('refusing to overwrite manifest')
    target.write_text(json.dumps(manifest,indent=2)+'\n')
    names=[target.name]+[v['name'] for v in app['artifacts'].values()]+[rootfs.name]
    (a.directory/'SHA256SUMS').write_text(''.join(sha(a.directory/n)+'  '+n+'\n' for n in sorted(names)))
if __name__=='__main__':main()
