#!/usr/bin/env python3
"""Run the built standalone twice against distinct synthetic upstreams."""
import http.server,threading,socket,subprocess,os,urllib.request,time,json,pathlib,tempfile,shutil
repo=pathlib.Path(__file__).resolve().parents[2]
class Handler(http.server.BaseHTTPRequestHandler):
 def do_GET(self):
  self.send_response(200);self.send_header('Content-Type','application/json');self.end_headers();self.wfile.write(json.dumps({'instance':self.server.server_port}).encode())
 def log_message(self,*args):pass
servers=[];processes=[]
try:
 with tempfile.TemporaryDirectory(prefix='standalone-smoke-') as d:
  app=pathlib.Path(d)/'app';shutil.copytree(repo/'frontend/.next/standalone',app,symlinks=True)
  shutil.copytree(repo/'frontend/.next/static',app/'.next/static',dirs_exist_ok=True)
  for idx in range(2):
   srv=http.server.ThreadingHTTPServer(('127.0.0.1',0),Handler);servers.append(srv);threading.Thread(target=srv.serve_forever,daemon=True).start()
   with socket.socket() as sock:sock.bind(('127.0.0.1',0));port=sock.getsockname()[1]
   env={**os.environ,'HOSTNAME':'127.0.0.1','PORT':str(port),'HARNESS_API_BASE_URL':f'http://127.0.0.1:{srv.server_port}'}
   log=open(pathlib.Path(d)/f'node-{idx}.log','w');proc=subprocess.Popen(['node',str(app/'server.js')],cwd='/',env=env,stdout=log,stderr=log);processes.append(proc)
   for _ in range(50):
    try:
     with urllib.request.urlopen(f'http://127.0.0.1:{port}/api/healthz',timeout=2) as r:result=json.load(r)
     assert result['instance']==srv.server_port,result;break
    except (OSError,urllib.error.URLError):time.sleep(.2)
   else:raise RuntimeError('standalone did not start')
  print('PASS: same standalone artifact, two runtime upstreams, cwd=/, no npm process')
finally:
 for p in processes:p.terminate();p.wait(timeout=10)
 for s in servers:s.shutdown()
