#!/usr/bin/env python3
"""Check the source actually used by -mod=vendor against the pinned Xray tree."""
import argparse
from pathlib import Path
import re
import sys

parser = argparse.ArgumentParser()
parser.add_argument('--root', type=Path, default=Path(__file__).resolve().parent.parent)
parser.add_argument('--sync', action='store_true', help='Update existing vendored packages from the pinned source')
args = parser.parse_args()
source = args.root / 'third_party/xray-core'
vendor = args.root / 'vendor/github.com/xtls/xray-core'
if not source.is_dir() or not vendor.is_dir():
    sys.exit('Missing pinned or vendored Xray source')

packages = {p.parent.relative_to(vendor) for p in vendor.rglob('*.go') if not p.name.endswith('_test.go')}
expected = {p.relative_to(source) for package in packages for p in (source/package).glob('*.go') if not p.name.endswith('_test.go')}
expected.update(p.relative_to(vendor) for p in vendor.rglob('*') if p.is_file() and not p.name.endswith('_test.go'))
errors = []
updated = 0
for name in sorted(expected):
    original, copied = source/name, vendor/name
    if not original.is_file():
        errors.append('Stale vendored file: '+str(name))
        continue
    data = original.read_bytes().replace(b'\r\n', b'\n')
    present = copied.read_bytes().replace(b'\r\n', b'\n') if copied.is_file() else None
    if present != data:
        if args.sync:
            copied.parent.mkdir(parents=True, exist_ok=True)
            copied.write_bytes(data)
            updated += 1
        else:
            errors.append('Xray vendor mismatch: '+str(name))
version = '.'.join(re.findall(r'Version_[xyz]\s+byte\s*=\s*(\d+)', (source/'core/core.go').read_text()))
if len(version.split('.')) != 3 or 'v'+version not in (source/'RESPROXY-PATCHES.md').read_text():
    errors.append('Xray source version does not match its pin notice')
if errors:
    sys.exit('\n'.join(errors))
print('Xray '+version+': '+str(len(expected))+' vendored files match pinned source; updated '+str(updated))
