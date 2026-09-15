#!/usr/bin/env python3
"""Only immutable SemVer tags whose commit is on main enter release jobs."""
import os,re,subprocess
version=os.environ['GITHUB_REF_NAME']
number=r'(?:0|[1-9][0-9]*)'
if not re.fullmatch('v'+number+r'\.'+number+r'\.'+number+r'(?:-rc\.'+number+r')?',version):raise SystemExit('exact SemVer release tag required')
head=subprocess.check_output(['git','rev-parse','HEAD'],text=True).strip()
tag=subprocess.check_output(['git','rev-parse',f'refs/tags/{version}^{{}}'],text=True).strip()
if head!=tag:raise SystemExit('checkout does not match release tag')
subprocess.run(['git','merge-base','--is-ancestor',head,'origin/main'],check=True)
print('Release source verified:',version,head)
