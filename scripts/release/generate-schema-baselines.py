#!/usr/bin/env python3
"""Developer-only regeneration from exact historical store implementations.
Changes reviewed DDL fixtures/catalog; NEVER run on production or in a deploy.
"""
import pathlib,subprocess,tempfile,shutil,json,sqlite3
repo=pathlib.Path(__file__).resolve().parents[2]; root=pathlib.Path(tempfile.mkdtemp(prefix='harness-baselines-'))
helper='''package main
import("context";"fmt";"os";"path/filepath";"harness-platform/orchestrator/internal/store";"harness-platform/orchestrator/internal/memorystore";"harness-platform/orchestrator/internal/schemastate")
func main(){c:=context.Background();s,e:=store.Open(c,filepath.Join(os.Args[1],"orchestrator.db"));if e!=nil{panic(e)};h,e:=schemastate.Fingerprint(c,s.DBForTest());if e!=nil{panic(e)};fmt.Println(h);s.Close();m,e:=memorystore.Open(c,filepath.Join(os.Args[1],"memory.db"));if e!=nil{panic(e)};h,e=schemastate.Fingerprint(c,m.DBForTest());if e!=nil{panic(e)};fmt.Println(h);m.Close()}
'''
cat={'store':{},'memorystore':{}}
def build(rev,name):
 p=root/name;p.mkdir();archive=subprocess.check_output(['git','archive',rev,'orchestrator'],cwd=repo);subprocess.run(['tar','-x','-C',str(p)],input=archive,check=True)
 shutil.copytree(repo/'orchestrator/internal/schemastate',p/'orchestrator/internal/schemastate');d=p/'orchestrator/cmd/baseline';d.mkdir();(d/'main.go').write_text(helper)
 exe=p/'open';subprocess.run(['go','build','-buildvcs=false','-o',str(exe),'./cmd/baseline'],cwd=p/'orchestrator',check=True);return exe
old=build('669542d','old');new=build('237f8b3','new')
for label,exe,dirname in [('669542d',old,'old-db'),('target-fresh',new,'new-db'),('target-upgraded',new,'old-db')]:
 state=root/dirname;state.mkdir(exist_ok=True)
 hashes=subprocess.check_output([str(exe),str(state)],text=True).strip().splitlines()
 for kind,h in zip(cat,hashes):cat[kind][h]=label
 # Save DDL fixtures for integration tests, with virtual-table shadow DDL omitted.
 dest=repo/'orchestrator/internal/dbupgrade/testdata';dest.mkdir(exist_ok=True)
 for kind,filename in [('store','orchestrator.db'),('memorystore','memory.db')]:
  with sqlite3.connect(state/filename) as db:
   rows=db.execute("select type,name,sql from sqlite_master where sql is not null and name not like 'sqlite_%' order by case type when 'table' then 0 when 'index' then 1 else 2 end,name").fetchall()
  ddl='\n'.join(sql+';' for typ,name,sql in rows if not (typ == 'table' and name.startswith('messages_fts_')))
  (dest/('memorystore.sql' if kind=='memorystore' else label+'-'+kind+'.sql')).write_text(ddl+'\n')
(repo/'orchestrator/internal/schemastate/baselines.json').write_text(json.dumps(cat,indent=2)+'\n')
print(json.dumps(cat));print(root)
