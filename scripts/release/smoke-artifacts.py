#!/usr/bin/env python3
"""Exercise shipped artifacts on a disposable hosted VM with synthetic data.
No production credentials, databases, services or model requests are used.
"""
import argparse,hashlib,http.server,json,os,pathlib,shutil,socket,sqlite3,subprocess,tarfile,tempfile,threading,time,urllib.request
from artifact_report import TESTS,public_report
ROOT=pathlib.Path(__file__).resolve().parents[2]

def run(args,**kw):
    return subprocess.run([str(a) for a in args],check=True,timeout=180,**kw)
def output(args,**kw):
    return subprocess.check_output([str(a) for a in args],text=True,timeout=180,**kw)
def sha(path):
    h=hashlib.sha256()
    with path.open('rb') as f:
        for b in iter(lambda:f.read(1024*1024),b''):h.update(b)
    return h.hexdigest()
def verify_archive(directory,a):
    p=directory/a['name']
    if pathlib.Path(a['name']).name!=a['name'] or p.stat().st_size!=a['size'] or sha(p)!=a['sha256']:
        raise RuntimeError('archive checksum/size mismatch')
    with tarfile.open(p,'r:gz') as tar:
        for member in tar:
            if member.name.startswith('/') or '..' in pathlib.PurePosixPath(member.name).parts:
                raise RuntimeError('unsafe archive member')
    return p

def database_smoke(binary,work,baseline=None):
    work.mkdir();control=work/'control.db';memory=work/'memory.db'
    if baseline:
        fixtures=ROOT/'orchestrator/internal/dbupgrade/testdata'
        with sqlite3.connect(control) as db:
            db.executescript((fixtures/f'{baseline}-store.sql').read_text())
            db.executescript("""INSERT INTO users(id,display_name,created_at) VALUES('test','Synthetic user','2026-01-01');
INSERT INTO sessions(id,user_id,status,driver_id,mode,workspace,restore_id,created_at,updated_at) VALUES('test','test','created','pi','agent','/synthetic','test','2026-01-01','2026-01-01');
INSERT INTO messages(session_id,role,content,created_at) VALUES('test','user','synthetic history','2026-01-01');
INSERT INTO turns(session_id,sequence,role,content,status,attempt,created_at) VALUES('test',1,'user','synthetic history','queued',0,'2026-01-01');""")
        with sqlite3.connect(memory) as db:
            db.executescript((fixtures/'memorystore.sql').read_text())
            db.execute("INSERT INTO semantic_memories(type,statement,first_seen_at,last_seen_at,updated_at) VALUES('fact','synthetic fact','2026-01-01','2026-01-01','2026-01-01')")
    config=work/'harness.yaml';config.write_text(f'harness:\n  run_dir: {work}/run\n')
    (work/'auth-users.yaml').write_text("users:\n  - username: test\n    password_hash: 'pbkdf2-sha256$600000$c2FsdA$aGFzaA'\n")
    env={k:v for k,v in os.environ.items() if not k.startswith(('HARNESS_','EVOLUTION_'))}
    env.update(HARNESS_MANAGED='1',HARNESS_DB_PATH=str(control),HARNESS_MEMORY_DB_PATH=str(memory))
    args=[binary,'db','migrate','--config',config]
    if not baseline:args.append('--initialize')
    run(args,env=env,stdout=subprocess.DEVNULL)
    run([binary,'db','migrate','--config',config],env=env,stdout=subprocess.DEVNULL)
    for path in (control,memory):
        with sqlite3.connect(path) as db:
            assert db.execute('PRAGMA integrity_check').fetchall()==[('ok',)]
            assert db.execute('PRAGMA foreign_key_check').fetchall()==[]
            assert db.execute('SELECT version FROM harness_schema_migrations').fetchall()==[(1,)]
    if baseline:
        with sqlite3.connect(control) as db:
            assert db.execute('SELECT content FROM messages').fetchall()==[('synthetic history',)]
            assert db.execute('SELECT status,materializes_message FROM turns').fetchall()==[('queued',0)]
        with sqlite3.connect(memory) as db:
            assert db.execute('SELECT statement FROM semantic_memories').fetchall()==[('synthetic fact',)]

class Upstream(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        self.send_response(200);self.send_header('Content-Type','application/json');self.end_headers()
        self.wfile.write(json.dumps({'upstream':self.server.server_port}).encode())
    def log_message(self,*args):pass

def frontend_smoke(frontend):
    # The same unpacked application must pick up each environment at runtime.
    for _ in range(2):
        upstream=http.server.ThreadingHTTPServer(('127.0.0.1',0),Upstream)
        thread=threading.Thread(target=upstream.serve_forever,daemon=True);thread.start()
        with socket.socket() as sock:sock.bind(('127.0.0.1',0));port=sock.getsockname()[1]
        env={**os.environ,'HOSTNAME':'127.0.0.1','PORT':str(port),'HARNESS_API_BASE_URL':f'http://127.0.0.1:{upstream.server_port}'}
        proc=subprocess.Popen([str(frontend/'node/bin/node'),str(frontend/'server.js')],cwd='/',env=env,stdout=subprocess.DEVNULL)
        try:
            for attempt in range(60):
                try:
                    with urllib.request.urlopen(f'http://127.0.0.1:{port}/api/healthz',timeout=2) as r:answer=json.load(r)
                    assert answer=={'upstream':upstream.server_port};break
                except OSError:
                    if proc.poll() is not None:raise RuntimeError('standalone exited')
                    time.sleep(0.5)
            else:raise RuntimeError('standalone health timeout')
        finally:
            proc.terminate()
            try:proc.wait(timeout=10)
            except subprocess.TimeoutExpired:proc.kill();proc.wait()
            upstream.shutdown();upstream.server_close()

def sandbox_smoke(runsc,rootfs,work):
    bundle=work/'oci';bundle.mkdir()
    run([runsc,'spec'],cwd=bundle,stdout=subprocess.DEVNULL)
    config=json.loads((bundle/'config.json').read_text())
    config['root']={'path':str(rootfs),'readonly':True}
    config['process']['terminal']=False
    config['process']['cwd']='/'
    config['process']['env']=['PATH=/usr/local/bin:/usr/bin:/bin','HOME=/tmp','MPLCONFIGDIR=/tmp/matplotlib']
    python="import numpy,pandas,scipy,duckdb; c=duckdb.connect(config={'extension_directory':'/usr/local/share/duckdb-extensions'}); c.execute('LOAD httpfs'); print('sandbox-python-ok')"
    import shlex
    config['process']['args']=['/bin/sh','-ec','node --version; pi --version; python3 -c '+shlex.quote(python)]
    config['mounts'].append({'destination':'/tmp','type':'tmpfs','source':'tmpfs','options':['nosuid','nodev','mode=1777','size=256m']})
    (bundle/'config.json').write_text(json.dumps(config))
    prefix=[runsc,'--root='+str(work/'runsc'),'--network=none','--platform=systrap']
    try:
        result=output(prefix+['run','artifact-smoke'],cwd=bundle)
        if 'sandbox-python-ok' not in result:raise RuntimeError('sandbox smoke incomplete')
    finally:
        subprocess.run([str(a) for a in prefix+['delete','--force','artifact-smoke']],cwd=bundle,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL,timeout=30)

def main():
    p=argparse.ArgumentParser();p.add_argument('--directory',type=pathlib.Path,required=True);p.add_argument('--ubuntu',required=True);p.add_argument('--runsc',type=pathlib.Path,required=True);p.add_argument('--report',type=pathlib.Path,required=True);a=p.parse_args()
    if os.geteuid()!=0 or os.uname().machine!='x86_64':p.error('requires a disposable native amd64 VM as root')
    release=pathlib.Path('/etc/os-release').read_text()
    if f'VERSION_ID="{a.ubuntu}"' not in release:p.error('Ubuntu runner mismatch')
    directory=a.directory.resolve();manifest_bytes=(directory/'release-manifest.json').read_bytes();m=json.loads(manifest_bytes)
    runsc=a.runsc.resolve();runsc_version=output([runsc,'--version']).splitlines()[0].strip()
    if runsc_version not in m['runsc_versions']:raise RuntimeError('test runtime does not match manifest')
    artifacts=[*m['artifacts'].values(),m['sandbox']['archive']]
    for item in artifacts:verify_archive(directory,item)
    with tempfile.TemporaryDirectory(prefix='harness-artifact-smoke-') as temp:
        work=pathlib.Path(temp);tools=work/'tools';tools.mkdir()
        run(['tar','-xzf',directory/m['artifacts']['deploy']['name'],'-C',tools])
        deploy=tools/'bin/harness-deploy'
        cache=work/'cache';version_dir=cache/m['version'];version_dir.mkdir(parents=True)
        for name in ['release-manifest.json']+[a['name'] for a in artifacts]:os.link(directory/name,version_dir/name)
        run([deploy,'stage','--cache',cache,'--release',m['version'],'--releases',work/'releases','--rootfs',work/'rootfs'],stdout=subprocess.DEVNULL)
        installed=work/'releases'/m['version'];backend=installed/'backend/bin/orchestrator';frontend=installed/'frontend'
        for exe in (backend,deploy):assert json.loads(output([exe,'version']))=={'version':m['version'],'commit':m['commit']}
        assert output([frontend/'node/bin/node','--version']).strip()=='v'+m['toolchains']['node']
        frontend_smoke(frontend)
        database_smoke(backend,work/'new-db')
        database_smoke(backend,work/'old-db','669542d')
        database_smoke(backend,work/'merged-db','target-fresh')
        sandbox_smoke(runsc,work/'rootfs'/m['sandbox']['archive']['sha256'],work)
    raw={'manifest_sha256':hashlib.sha256(manifest_bytes).hexdigest(),'version':m['version'],'commit':m['commit'],'ubuntu':a.ubuntu,'arch':'amd64','production_verified':False,'runsc_version':runsc_version,'tests':dict.fromkeys(TESTS,'passed')}
    a.report.write_text(json.dumps(public_report(manifest_bytes,raw,a.ubuntu),indent=2)+'\n')
    print('Hosted artifact smoke passed. No production environment was accessed.')
if __name__=='__main__':main()
