#!/usr/bin/env python3
"""Offline checks of where the private QA producer gives the updater key.

Subprocesses are fakes that record their environment and produce synthetic
outputs; the key is a random synthetic value. Real compiler/signature effects
are validated separately; this pins the producer's own boundaries in CI.
"""
import base64
import io
import json
import os
import pathlib
import secrets
import shutil
import signal
import stat
import subprocess
import sys
import tarfile
import tempfile
import unittest
from importlib.util import module_from_spec, spec_from_file_location
from unittest.mock import patch

HERE = pathlib.Path(__file__).resolve().parent
spec = spec_from_file_location('upgrade_qa', HERE / 'prepare-upgrade-qa.py')
qa = module_from_spec(spec)
spec.loader.exec_module(qa)

KEY = 'MYGO_UPDATER_PRIVATE_KEY'
PRODUCER = HERE.parent
SOURCE = 'a' * 40
PUBLIC = secrets.token_bytes(32)
PRIVATE = base64.b64encode(secrets.token_bytes(32) + PUBLIC).decode()
HELPER = (PRODUCER / 'tools/package/upgrade_assets.go.txt').read_bytes()


def candidate_archive():
    data = io.BytesIO()
    with tarfile.open(fileobj=data, mode='w') as t:
        for name, body in (('mygo.json', json.dumps({'version': '0.1.39', 'updates': {'publicKey': base64.b64encode(PUBLIC).decode(), 'deltas': 3}})),
                           ('main.go', 'package main\n'), ('web/index.html', 'synthetic')):
            raw = body.encode()
            info = tarfile.TarInfo(name)
            info.size = len(raw)
            t.addfile(info, io.BytesIO(raw))
    return data.getvalue()


class Producer(unittest.TestCase):
    def setUp(self):
        self.dir = pathlib.Path(tempfile.mkdtemp(prefix='qa-producer-test-'))
        self.temp = self.dir / 'runner'
        self.temp.mkdir()
        inputs = self.dir / 'inputs'
        meta = {'refs': {'v0.1.39': SOURCE}, 'PublicKey': base64.b64encode(PUBLIC).decode(), 'stages': {}, 'releases': []}
        for tag in ('v0.1.38', 'v0.1.39'):
            (inputs / tag / 'web').mkdir(parents=True)
            (inputs / tag / 'archive.tar.gz').write_bytes(b'official')
            (inputs / tag / 'update.json').write_text('{"deltas":[]}')
            meta['stages'][tag + '/linux-amd64'] = {'Archive': tag + '/archive.tar.gz', 'Manifest': tag + '/update.json', 'Web': tag + '/web'}
        (inputs / 'release-inputs.json').write_text(json.dumps(meta))
        self.args = ['qa', str(inputs), str(self.dir / 'out')]
        self.calls = []
        self.build = self.good_build
        self.sign = lambda: None
        self.helper = HELPER

    def tearDown(self):
        shutil.rmtree(self.dir)

    def record(self, args, env):
        values = env.values()
        self.calls.append({'args': list(args), 'name': KEY in env, 'value': any(PRIVATE in v for v in values)})

    def check_output(self, args, **kw):
        self.record(args, kw.get('env', os.environ))
        if args[:2] == ['go', 'env']:
            return {'GOOS': 'linux\n', 'GOARCH': 'amd64\n'}[args[2]]
        if args[:2] == ['git', 'rev-parse']:
            return SOURCE + '\n'
        if args[:2] == ['git', 'show']:
            return self.helper
        if args[:2] == ['git', 'archive']:
            return candidate_archive()
        raise AssertionError(args)

    def good_build(self, args, kw):
        cfg = json.loads((pathlib.Path(kw['cwd']) / 'mygo.json').read_text())
        version, signer = cfg['version'], pathlib.Path(cfg['updates']['privateKey'])
        self.assertEqual(signer.read_text().strip(), PRIVATE)
        self.assertEqual(stat.S_IMODE(signer.stat().st_mode), 0o600)
        self.assertEqual(stat.S_IMODE(signer.parent.stat().st_mode), 0o700)
        self.assertEqual(signer.parent.parent, self.temp)
        self.signers.append(signer)
        package = pathlib.Path(args[args.index('-o') + 1]) / 'linux-amd64'
        package.mkdir(parents=True)
        (package / 'Yes Man').write_bytes(b'binary')
        archive = package / ('yesman-' + version + '-linux-amd64.tar.gz')
        archive.write_bytes(b'archive ' + version.encode())
        (package / 'update-linux-amd64.json').write_text(json.dumps({'version': version, 'size': archive.stat().st_size, 'signature': 'synthetic'}))

    def run_fake(self, args, **kw):
        env = kw.get('env', os.environ)
        self.record(args, env)
        if args[:4] == ['go', 'tool', 'mygo', 'build']:
            self.build(args, kw)
        elif args[:4] == ['go', 'run', './tools/package', '-build-upgrade-assets']:
            self.assertEqual(pathlib.Path(kw['cwd']), PRODUCER)
            pathlib.Path(args[4]).write_bytes(b'helper')
        elif len(args) == 2:
            self.assertEqual(env[KEY], PRIVATE)
            self.sign()
        elif args[2:] != ['verify']:
            raise AssertionError(args)
        return subprocess.CompletedProcess(args, 0)

    def main(self, key=PRIVATE, extra=None):
        self.signers, self.handlers = [], {}
        shutil.rmtree(self.dir / 'out', ignore_errors=True)
        env = {'GITHUB_ACTIONS': 'true', 'RUNNER_ENVIRONMENT': 'github-hosted', 'RUNNER_TEMP': str(self.temp), KEY: key, **(extra or {})}
        with patch.dict(os.environ, env), patch.object(sys, 'argv', self.args), \
                patch.object(qa.subprocess, 'run', self.run_fake), patch.object(qa.subprocess, 'check_output', self.check_output), \
                patch('signal.signal', lambda n, h: self.handlers.__setitem__(n, h)), patch('builtins.print'):
            try:
                qa.main()
            finally:
                self.assertNotIn(KEY, os.environ)
                self.assertEqual(list(self.temp.iterdir()), [], 'private key/helper directories remain')

    def kinds(self):
        return [' '.join(c['args'][:4]) if not c['args'][0].endswith('yesman-upgrade-assets') else 'helper ' + ' '.join(c['args'][2:]) for c in self.calls]

    def test_only_the_signers_receive_the_key(self):
        self.main()
        signers = [c for c in self.calls if c['args'][0].endswith('yesman-upgrade-assets') and len(c['args']) == 2]
        self.assertEqual(len(signers), 1)
        self.assertTrue(signers[0]['name'] and signers[0]['value'])
        others = [c for c in self.calls if c is not signers[0]]
        self.assertGreaterEqual(len(others), 10)
        self.assertEqual([c for c in others if c['name'] or c['value']], [])
        builds = [c for c in others if c['args'][:4] == ['go', 'tool', 'mygo', 'build']]
        self.assertEqual(len(builds), 2)
        self.assertEqual(len(self.signers), 2)
        self.assertTrue(all(not p.exists() and not p.parent.exists() for p in self.signers))
        order = self.kinds()
        self.assertLess(order.index('go run ./tools/package -build-upgrade-assets'), order.index('helper '))
        self.assertEqual(order[-1], 'helper verify')

    def test_missing_mismatched_or_duplicated_key_runs_nothing(self):
        other = base64.b64encode(secrets.token_bytes(64)).decode()
        for key, extra in (('', None), (' ', None), (other, None), ('not base64!', None), (PRIVATE, {'COPY': 'x' + PRIVATE})):
            self.calls = []
            with self.assertRaises(Exception):
                self.main(key, extra)
            self.assertEqual(self.calls, [])

    def test_candidate_helper_must_match_the_producer(self):
        self.helper = HELPER + b'// changed\n'
        with self.assertRaises(AssertionError):
            self.main()
        self.assertNotIn('go tool mygo build', self.kinds())

    def test_unsigned_or_interrupted_build_stops_before_signing(self):
        def unsigned(args, kw):
            self.good_build(args, kw)
            manifest = next(pathlib.Path(args[args.index('-o') + 1]).rglob('update-*.json'))
            manifest.write_text(json.dumps({**json.loads(manifest.read_text()), 'signature': ''}))

        def interrupted(args, kw):
            self.good_build(args, kw)
            raise KeyboardInterrupt

        def stopped(args, kw):
            self.good_build(args, kw)
            self.handlers[signal.SIGTERM](signal.SIGTERM, None)

        for build, error in ((unsigned, AssertionError), (interrupted, KeyboardInterrupt), (stopped, SystemExit)):
            self.calls, self.build = [], build
            with self.assertRaises(error):
                self.main()
            self.assertTrue(all(not p.exists() for p in self.signers))
            self.assertFalse([k for k in self.kinds() if k.startswith(('helper', 'go run'))])

    def test_failed_signer_skips_verification_and_removes_helper(self):
        def fail():
            raise subprocess.CalledProcessError(2, 'helper')
        self.sign = fail
        with self.assertRaises(subprocess.CalledProcessError):
            self.main()
        self.assertNotIn('helper verify', self.kinds())

    def test_workflow_runs_the_trusted_producer(self):
        text = (HERE.parent / '.github/workflows/application-upgrade-draft.yml').read_text(encoding='utf-8')
        job = text.split('\n  sign-qa:\n', 1)[1].split('\n  native:\n', 1)[0]
        self.assertIn('          ref: ${{ github.sha }}\n          path: .qa-producer\n          persist-credentials: false\n', job)
        step = job.split('      - name: Package and sign private QA successors\n', 1)[1].split('\n      - ', 1)[0]
        self.assertEqual(step, '        env:\n          MYGO_UPDATER_PRIVATE_KEY: ${{ secrets.MYGO_UPDATER_PRIVATE_KEY }}\n'
                         '          RUNNER_ENVIRONMENT: ${{ runner.environment }}\n'
                         '        run: python3 .qa-producer/scripts/prepare-upgrade-qa.py "$RUNNER_TEMP/released-inputs" "$RUNNER_TEMP/gate-inputs"')
        self.assertEqual(text.count('secrets.MYGO_UPDATER_PRIVATE_KEY'), 1)


if __name__ == '__main__':
    unittest.main()
