#!/usr/bin/env python3
"""Read private evidence locally; emit only allowlisted public evidence."""
import argparse,json,pathlib,sys
from qualification import public_report

def main():
    p=argparse.ArgumentParser();p.add_argument('--manifest',type=pathlib.Path,required=True);p.add_argument('--report',type=pathlib.Path,required=True);p.add_argument('--ubuntu',required=True);a=p.parse_args()
    try:
        result=public_report(a.manifest.read_bytes(),json.loads(a.report.read_text()),a.ubuntu)
    except Exception:
        # Do not echo private report contents or host paths to public CI logs.
        print('qualification failed validation; inspect private host evidence',file=sys.stderr)
        return 1
    print(json.dumps(result,indent=2));return 0
if __name__=='__main__':sys.exit(main())
