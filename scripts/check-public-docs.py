#!/usr/bin/env python3
"""Check public documentation conventions without reading private evidence.

This is a regression check, not a substitute for a full secret/history audit.
"""
from pathlib import Path
import re,subprocess,sys,urllib.parse
ROOT=Path(__file__).resolve().parents[1]
tracked=subprocess.check_output(['git','ls-files','-z'],cwd=ROOT).decode().split('\0')
paths=[ROOT/p for p in tracked if p and (ROOT/p).is_file()]
# Include newly created public docs before they have been staged.
paths=list(set(paths)|set((ROOT/'docs').rglob('*.md')))
errors=[]
for path in paths:
    relative=path.relative_to(ROOT).as_posix()
    if relative.startswith('private/'):
        errors.append(f'{relative}: private file tracked');continue
    if relative.startswith(('docs/proposals/','docs/announcements/','docs/upgrade-')):
        errors.append(f'{relative}: retired documentation archive');continue
    if path.suffix not in ('.md','.mdx'):continue
    text=path.read_text(encoding='utf-8')
    reference=relative.startswith('docs/') or path.name=='README.md'
    if reference and re.search(r'[\u3400-\u9fff]',text):errors.append(f'{relative}: reference documentation must be English')
    for match in re.finditer(r'(?<!!)\[[^\]]*\]\(([^)]+)\)',text):
        target=match.group(1).strip().split(' "',1)[0].strip('<>')
        if not target or target.startswith(('#','http:','https:','mailto:','app:','codex:')):continue
        target=urllib.parse.unquote(target.split('#',1)[0])
        if target.startswith('/'):
            errors.append(f'{relative}: local link must be repository-relative');continue
        resolved=(path.parent/target).resolve()
        if not resolved.is_relative_to(ROOT) or not resolved.exists():errors.append(f'{relative}: broken local link {target}')
    if reference:
        # Real account home directories do not belong in public references.
        # TEST-NET addresses and intentionally generic /var/lib/harness examples
        # are permitted. Deeper historical/secret audit remains private.
        if re.search(r"/(?:home|Users)/[A-Za-z0-9_.-]+/",text):
            errors.append(f'{relative}: account-specific home path')

if errors:
    for error in sorted(set(errors)):print(error,file=sys.stderr)
    sys.exit(1)
print('Public documentation checks passed (English references, links, private boundary).')
