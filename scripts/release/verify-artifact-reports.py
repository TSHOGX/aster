#!/usr/bin/env python3
import argparse,json,pathlib
from artifact_report import public_report
p=argparse.ArgumentParser();p.add_argument('--directory',type=pathlib.Path,required=True);a=p.parse_args()
manifest=(a.directory/'release-manifest.json').read_bytes()
for ubuntu in ('22.04','24.04'):
    raw=json.loads((a.directory/f'artifact-smoke-{ubuntu}.json').read_text())
    if raw!=public_report(manifest,raw,ubuntu):
        raise SystemExit('artifact report contains unexpected fields')
print('Hosted artifact checks passed; production acceptance remains deployment-local.')
