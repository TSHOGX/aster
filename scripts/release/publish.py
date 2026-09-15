#!/usr/bin/env python3
"""Draft -> verify remote bytes -> publish. Never replace published assets."""
import argparse, hashlib, json, os, pathlib, urllib.request, urllib.error
from qualification import public_report

def main():
    p=argparse.ArgumentParser();p.add_argument('--directory',type=pathlib.Path,required=True);p.add_argument('--repository',required=True);a=p.parse_args()
    manifest_bytes=(a.directory/'release-manifest.json').read_bytes()
    m=json.loads(manifest_bytes)
    for ubuntu in ('22.04','24.04'):
        raw=json.loads((a.directory/f'qualification-{ubuntu}.json').read_text())
        if raw != public_report(manifest_bytes,raw,ubuntu):raise RuntimeError('refusing publication of non-public qualification fields')
    token=os.environ['GH_TOKEN'];base=f'https://api.github.com/repos/{a.repository}'
    def api(path,data=None,method=None):
        req=urllib.request.Request(base+path,data=json.dumps(data).encode() if data is not None else None,method=method,headers={'Authorization':'Bearer '+token,'Accept':'application/vnd.github+json','Content-Type':'application/json'})
        with urllib.request.urlopen(req,timeout=60) as r:return json.load(r)
    try:r=api('/releases/tags/'+m['version'])
    except urllib.error.HTTPError as e:
        if e.code!=404:raise
        r=api('/releases',{'tag_name':m['version'],'target_commitish':m['commit'],'name':m['version'],'draft':True,'prerelease':'-rc.' in m['version'],'body':(a.directory/'UPGRADE.md').read_text()})
    if not r['draft']:raise RuntimeError('release already public; immutable publication refused')
    names=[v['name'] for v in m['artifacts'].values()]+['release-manifest.json','SHA256SUMS','UPGRADE.md','qualification-22.04.json','qualification-24.04.json','rootfs-apt-packages.txt','rootfs-python-packages.txt','rootfs-requirements.lock']
    # Stream uploads with http.client: binary packages can exceed process memory.
    import http.client,urllib.parse
    existing={x['name']:x for x in api('/releases/'+str(r['id'])+'/assets?per_page=100')}
    for name in names:
        path=a.directory/name
        if name not in existing:
            u=urllib.parse.urlparse(r['upload_url'].split('{')[0]+'?name='+urllib.parse.quote(name));conn=http.client.HTTPSConnection(u.hostname,timeout=1800)
            conn.putrequest('POST',u.path+'?'+u.query);conn.putheader('Authorization','Bearer '+token);conn.putheader('Content-Type','application/octet-stream');conn.putheader('Content-Length',str(path.stat().st_size));conn.endheaders()
            with path.open('rb') as f:
                for chunk in iter(lambda:f.read(1024*1024),b''):conn.send(chunk)
            response=conn.getresponse();body=response.read();conn.close()
            if response.status!=201:raise RuntimeError(f'asset upload failed: {name}: HTTP {response.status}')
            existing[name]=json.loads(body)
        req=urllib.request.Request(existing[name]['url'],headers={'Authorization':'Bearer '+token,'Accept':'application/octet-stream'})
        # urllib strips neither custom auth nor host-specific headers reliably
        # on redirects. Fetch signed location without forwarding credentials.
        class NoRedirect(urllib.request.HTTPRedirectHandler):
            def redirect_request(self,*args,**kwargs):return None
        try:response=urllib.request.build_opener(NoRedirect).open(req,timeout=60)
        except urllib.error.HTTPError as e:
            if e.code not in (301,302,303,307,308):raise
            location=e.headers['Location']
            if not location.startswith('https://'):raise RuntimeError('insecure asset redirect')
            response=urllib.request.urlopen(location,timeout=1800)
        h=hashlib.sha256()
        with response:
            for chunk in iter(lambda:response.read(1024*1024),b''):h.update(chunk)
        local=hashlib.sha256()
        with path.open('rb') as f:
            for chunk in iter(lambda:f.read(1024*1024),b''):local.update(chunk)
        if h.digest()!=local.digest():raise RuntimeError(f'immutable asset mismatch: {name}')
    api('/releases/'+str(r['id']),{'draft':False},'PATCH')
if __name__=='__main__':main()
