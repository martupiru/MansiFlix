"""Offline contract tests: real sh/awk/sort/cmp/SHA-256, fake mc and readiness.

Run: python3 -m unittest discover -s deploy/minio/tests -v
Only copies of scripts are rewritten, to redirect fixed container paths into a
TemporaryDirectory. No real mc, kubeconfig, credentials or network are used.
"""
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import tempfile
import unittest

SCRIPTS = Path(os.environ.get('HLS_TEST_SCRIPTS', Path(__file__).resolve().parents[1] / 'scripts'))
MOCK = '''import json, os, pathlib, sys
args = sys.argv[1:]
op, name = args[0], args[-1].rsplit('/', 1)[-1]
with open(os.environ['CALLS'], 'a') as f:
    f.write(json.dumps(args) + '\\n')
if name == os.environ.get('FAIL_OBJECT'):
    sys.exit(7)
store = pathlib.Path(os.environ['STORE'])
if op == 'pipe':
    (store / name).write_bytes(sys.stdin.buffer.read())
elif op == 'cat':
    try:
        sys.stdout.buffer.write((store / name).read_bytes())
    except FileNotFoundError:
        sys.exit(8)
else:
    sys.exit(9)
'''


def playlist(names):
    return '#EXTM3U\n' + ''.join('#EXTINF:6,\n' + n + '\n' for n in names) + '#EXT-X-ENDLIST\n'


class HLS(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(prefix='mansiflix-hls-')
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        for name in ['work/hls', 'tmp', 'bin', 'opt', 'store']:
            (self.root / name).mkdir(parents=True)
        self.hls = self.root / 'work/hls'
        self.store = self.root / 'store'
        for name in ['worker-transfer.sh', 'verify-demo.sh', 'common.sh']:
            source = (SCRIPTS / name).read_text()
            source = re.sub(r'/opt/minio|/work|/tmp',
                            lambda m: str(self.root / {'/opt/minio': 'opt', '/work': 'work', '/tmp': 'tmp'}[m[0]]), source)
            (self.root / 'opt' / name).write_text(source)
        (self.root / 'bin/mc').write_text('#!' + sys.executable + '\n' + MOCK)
        (self.root / 'bin/mc').chmod(0o700)
        (self.root / 'bin/wget').write_text('#!/bin/sh\nexit 0\n')
        (self.root / 'bin/wget').chmod(0o700)
        self.env = {'PATH': str(self.root / 'bin') + ':' + os.environ.get('PATH', os.defpath),
                    'MINIO_ROOT_USER': 'mansiflix-admin', 'MINIO_ROOT_PASSWORD': 'a' * 32,
                    'WORKER_ACCESS_KEY': 'mansiflix-worker', 'WORKER_SECRET_KEY': 'b' * 32,
                    'POD_UID': 'synthetic-test', 'CHECK_UID': 'synthetic-test', 'TEST_MODE': 'hls',
                    'CALLS': str(self.root / 'calls'), 'STORE': str(self.store)}

    def run_script(self, upload=False):
        cmd = ['sh', str(self.root / 'opt' / ('worker-transfer.sh' if upload else 'verify-demo.sh'))]
        if upload:
            cmd.append('upload')
        self.result = subprocess.run(cmd, env=self.env, capture_output=True, timeout=10)
        return self.result.returncode

    def calls(self):
        path = self.root / 'calls'
        return [json.loads(line) for line in path.read_text().splitlines()] if path.exists() else []

    def fixture(self, names=('segment_010.ts', 'segment_002.ts'), upload=False):
        folder = self.hls if upload else self.store
        (folder / 'index.m3u8').write_text(playlist(names))
        for name in names:
            (folder / name).write_bytes(('synthetic:' + name).encode())
        return folder

    def manifest(self):
        files = sorted(p for p in self.store.iterdir() if p.name != 'checksums.sha256')
        text = ''.join(hashlib.sha256(p.read_bytes()).hexdigest() + '  ' + p.name + '\n' for p in files)
        (self.store / 'checksums.sha256').write_text(text)
        return text

    def upload_case(self, case):
        names = ['segment_010.ts'] if case == 'one' else ['segment_010.ts', 'segment_002.ts']
        f = self.fixture(names, upload=True)
        p = f / 'index.m3u8'
        if case == 'missing': (f / names[-1]).unlink()
        elif case == 'empty': (f / names[-1]).write_bytes(b'')
        elif case == 'orphan': (f / 'segment_999.ts').write_bytes(b'orphan')
        elif case == 'malformed_local': (f / 'segment_bad.ts').write_bytes(b'orphan')
        elif case == 'duplicate': p.write_text(playlist(names + [names[0]]))
        elif case in ['traversal', 'absolute', 'subdir', 'unexpected']:
            bad = {'traversal': '../segment_000.ts', 'absolute': '/segment_000.ts', 'subdir': 'dir/segment_000.ts', 'unexpected': 'video.ts'}[case]
            p.write_text(playlist([bad]))
        elif case == 'no_header': p.write_text(p.read_text().replace('#EXTM3U\n', ''))
        elif case == 'bad_header': p.write_text(p.read_text().replace('#EXTM3U', '#EXTM3Uinvalid'))
        elif case == 'no_end': p.write_text(p.read_text().replace('#EXT-X-ENDLIST\n', ''))
        elif case == 'no_segments': p.write_text(playlist([]))
        elif case == 'after_end': p.write_text(p.read_text() + 'segment_123.ts\n')
        elif case == 'crlf': p.write_bytes(p.read_bytes().replace(b'\n', b'\r\n'))
        elif case == 'symlink':
            (f / names[-1]).unlink()
            (f / names[-1]).symlink_to(f / names[0])
        elif case == 'checksum_failure':
            tool = self.root / 'bin/sha256sum'
            tool.write_text('#!/bin/sh\nexit 6\n'); tool.chmod(0o700)
        failures = {'fail_segment': names[-1], 'fail_manifest': 'checksums.sha256', 'fail_playlist': 'index.m3u8'}
        if case in failures: self.env['FAIL_OBJECT'] = failures[case]
        rc = self.run_script(upload=True)
        success = case in ['one', 'many', 'crlf']
        self.assertEqual(rc == 0, success, self.result.stderr)
        calls = self.calls()
        if success:
            self.assertEqual([c[-1].rsplit('/', 1)[-1] for c in calls], names + ['checksums.sha256', 'index.m3u8'])
            self.assertTrue(all(c[:5] == ['pipe', '--part-size', '5MiB', '--concurrent', '1'] for c in calls))
            # Real checksum utility checks what the simulated remote received.
            check = subprocess.run(['sha256sum', '-c', 'checksums.sha256'], cwd=self.store, capture_output=True)
            self.assertEqual(check.returncode, 0, check.stderr)
        elif case in failures:
            self.assertNotIn('index.m3u8', [x.name for x in self.store.iterdir()])
            if case != 'fail_playlist': self.assertFalse(any(c[-1].endswith('/index.m3u8') for c in calls))
        else:
            self.assertEqual(calls, [], 'validation must precede every remote write')
            self.assertEqual(list(self.store.iterdir()), [])

    def verify_case(self, case):
        f = self.fixture()
        if case == 'incomplete': (f / 'index.m3u8').write_text(playlist(['segment_010.ts']).replace('#EXT-X-ENDLIST\n', ''))
        if case == 'duplicate_reference': (f / 'index.m3u8').write_text(playlist(['segment_010.ts', 'segment_010.ts']))
        text = self.manifest()
        manifest = f / 'checksums.sha256'
        if case == 'segment_hash': (f / 'segment_010.ts').write_bytes(b'changed')
        elif case == 'playlist_hash': (f / 'index.m3u8').write_text(playlist(['segment_002.ts', 'segment_010.ts']))
        elif case == 'missing': (f / 'segment_010.ts').unlink()
        elif case == 'playlist_only': manifest.write_text(text.splitlines()[0] + '\n')
        elif case == 'duplicate': manifest.write_text(text + text.splitlines()[1] + '\n')
        elif case == 'extra': manifest.write_text(text + '0' * 64 + '  segment_999.ts\n')
        elif case == 'malformed': manifest.write_text(text.replace('segment_010.ts', 'bad.ts'))
        elif case == 'short_hash': manifest.write_text(text[1:])
        elif case == 'bad_hex': manifest.write_text('g' + text[1:])
        elif case == 'path': manifest.write_text(text.replace('segment_010.ts', '../segment_010.ts'))
        elif case == 'empty_manifest': manifest.write_text('')
        elif case == 'extra_field': manifest.write_text(text + '0' * 64 + '  segment_010.ts extra\n')
        elif case == 'whitespace': manifest.write_bytes(text.upper().replace('INDEX.M3U8', 'index.m3u8').replace('SEGMENT_', 'segment_').replace('.TS', '.ts').replace('  ', '\t').replace('\n', '\r\n').encode())
        elif case == 'download_failure': self.env['FAIL_OBJECT'] = 'checksums.sha256'
        rc = self.run_script()
        self.assertEqual(rc == 0, case in ['valid', 'whitespace'], self.result.stderr)
        calls = self.calls()
        self.assertTrue(all(c[0] == 'cat' and c[-1].startswith('admin/') for c in calls))
        structural = ['playlist_only', 'duplicate', 'extra', 'malformed', 'short_hash', 'bad_hex', 'path', 'empty_manifest', 'extra_field', 'incomplete', 'duplicate_reference']
        if case in structural:
            self.assertFalse(any(c[-1].endswith('.ts') for c in calls), 'reject structure before segment downloads')


for scenario in ['one', 'many', 'missing', 'empty', 'orphan', 'malformed_local', 'duplicate', 'traversal', 'absolute', 'subdir', 'unexpected', 'no_header', 'bad_header', 'no_end', 'no_segments', 'after_end', 'crlf', 'symlink', 'checksum_failure', 'fail_segment', 'fail_manifest', 'fail_playlist']:
    setattr(HLS, 'test_upload_' + scenario, lambda self, case=scenario: self.upload_case(case))
for scenario in ['valid', 'segment_hash', 'playlist_hash', 'missing', 'playlist_only', 'duplicate', 'extra', 'malformed', 'short_hash', 'bad_hex', 'path', 'empty_manifest', 'extra_field', 'whitespace', 'incomplete', 'duplicate_reference', 'download_failure']:
    setattr(HLS, 'test_verify_' + scenario, lambda self, case=scenario: self.verify_case(case))

if __name__ == '__main__':
    unittest.main()
