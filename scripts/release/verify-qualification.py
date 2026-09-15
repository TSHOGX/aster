#!/usr/bin/env python3
import argparse,json,pathlib
from qualification import public_report
p=argparse.ArgumentParser();p.add_argument('--directory',type=pathlib.Path,required=True);a=p.parse_args()
manifest=(a.directory/'release-manifest.json').read_bytes()
for ubuntu in ('22.04','24.04'):
    raw=json.loads((a.directory/f'qualification-{ubuntu}.json').read_text())
    approved=public_report(manifest,raw,ubuntu)
    if raw != approved:
        raise SystemExit('qualification report contains non-public fields; sanitize on the qualification host')
print('Both public qualification reports passed for the exact release manifest.')
