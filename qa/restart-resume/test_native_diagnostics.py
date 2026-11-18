"""QA049-6/7 local controls: harmless processes/files only, never an App."""
import copy
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch

import guards
import native_diagnostics as diagnostic
import freeze_native_diagnostics as snapshot


class Diagnostics(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name).resolve()
        self.main = self.root / 'Application install with spaces/yesman.app/Contents/MacOS/Yes Man'
        self.main.parent.mkdir(parents=True)
        self.main.write_bytes(b'harmless local fixture image')
        self.main.chmod(0o755)
        self.shim = self.root / 'synthetic-data/.runtime/github-app-gh/bin/gh'
        self.shim.parent.mkdir(parents=True)
        self.journal = diagnostic.Journal(self.root / 'diagnostics')
        self.addCleanup(self.journal.close)
        self.owner = {'pid': 4242, 'executable': str(self.main), 'startSeconds': 10,
                      'startMicroseconds': 20, 'uid': os.getuid()}

    def held(self, observe):
        owned = diagnostic.DiagnosticOwned(4242, self.main, observe=observe,
            journal=self.journal, phase=lambda: 'runtime-ready', role='initial-S-main', shim=self.shim,
            expected_sha256=hashlib.sha256(self.main.read_bytes()).hexdigest())
        self.addCleanup(owned.close_observer)
        return owned

    def rows(self):
        return [json.loads(raw) for raw in (self.journal.root / 'process.jsonl').read_text().splitlines()]

    def test_each_identity_operand_refuses_before_signal_and_retains_evidence(self):
        for field, changed in [('startSeconds', 11), ('startMicroseconds', 21), ('uid', os.getuid() + 1),
                               ('executable', '/unknown/sanitized-path-sentinel')]:
            current = dict(self.owner)
            held = self.held(lambda _: current)
            current = {**current, field: changed}
            with patch.object(guards.os, 'kill') as killed:
                with self.assertRaisesRegex(ValueError, 'incarnation changed'):
                    held.terminate()
                killed.assert_not_called()
            row = self.rows()[-1]
            self.assertEqual(row['state'], 'mismatch')
            self.assertEqual(row['differentFields'], [field])
            self.assertEqual(row['expected']['startSeconds'], 10)
            self.assertEqual(row['signals'], [])
        raw = (self.journal.root / 'process.jsonl').read_bytes()
        self.assertNotIn(b'sanitized-path-sentinel', raw)
        self.assertNotIn(str(self.root).encode(), raw)
        self.assertLessEqual(len(raw), 128 << 10)
        self.assertEqual((self.journal.root / 'process.jsonl').stat().st_mode & 0o777, 0o600)

    def test_unknown_observation_propagates_and_does_not_signal(self):
        held = self.held(lambda _: self.owner)
        def unknown(_):
            raise ValueError('RAW-ERROR-SENTINEL')
        # Replace the inner observation, retaining the diagnostic wrapper.
        value = [self.owner]
        held = self.held(lambda _: value[0] if value[0] is not None else unknown(4242))
        value[0] = None
        with patch.object(guards.os, 'kill') as killed:
            with self.assertRaisesRegex(ValueError, 'RAW-ERROR-SENTINEL'):
                held.kill()
            killed.assert_not_called()
        self.assertEqual(self.rows()[-1]['state'], 'unknown')
        self.assertNotIn(b'RAW-ERROR-SENTINEL', (self.journal.root / 'process.jsonl').read_bytes())

    def test_legitimate_fixed_hardlink_causal_old_refusal_and_narrow_bound_acceptance(self):
        current = self.owner
        old = guards.Owned(4242, self.main, observe=lambda _: current)
        held = self.held(lambda _: current)
        os.link(self.main, self.shim.with_name('tmp-gh'))
        os.rename(self.shim.with_name('tmp-gh'), self.shim)
        current = {**current, 'executable': str(self.shim)}
        with patch.object(guards.os, 'kill') as killed:
            with self.assertRaisesRegex(ValueError, 'incarnation changed'):
                old.poll()
            self.assertIsNone(held.poll())
            killed.assert_not_called()
        row = self.rows()[-1]
        self.assertTrue(row['mainAndShimSameFile'] and row['fixedAliasQualified'])
        self.assertEqual(row['current']['executableRole'], 'fixed-private-gh-shim')
        self.assertEqual(row['differentFields'], ['executable'])

    def test_alias_replacement_matching_bytes_bad_birth_unknown_path_all_refuse(self):
        current = self.owner
        held = self.held(lambda _: current)
        shutil.copyfile(self.main, self.shim)
        current = {**self.owner, 'executable': str(self.shim)}
        with self.assertRaisesRegex(ValueError, 'held bound main'):
            held.poll()
        self.shim.unlink()
        os.link(self.main, self.shim)
        current = {**current, 'startMicroseconds': 12345}
        with patch.object(guards.os, 'kill') as killed:
            with self.assertRaisesRegex(ValueError, 'incarnation changed'):
                held.terminate()
            current = {**self.owner, 'executable': str(self.shim.parent / 'other')}
            with self.assertRaisesRegex(ValueError, 'incarnation changed'):
                held.kill()
            killed.assert_not_called()
        current = {**self.owner, 'executable': str(self.shim)}
        self.main.write_bytes(b'tampered original inode')
        with self.assertRaisesRegex(ValueError, 'hash/identity changed'):
            held.poll()

    def test_bound_alias_observer_keeps_descendant_discovery_conservative(self):
        import process_proof
        current = self.owner
        held = self.held(lambda _: current)
        os.link(self.main, self.shim)
        current = {**current, 'executable': str(self.shim)}
        child = {**self.owner, 'pid': 4243, 'executable': '/synthetic-child'}
        observer = process_proof.Processes(self.root,
            observe=lambda pid: held.observe(pid) if pid == held.pid else child)
        observer.register(held)
        result = SimpleNamespace(returncode=0, stdout=b'4242 1 4242 harmless-main\n4243 4242 4242 harmless-child\n')
        with patch.object(process_proof.subprocess, 'run', return_value=result), patch.object(guards.os, 'kill') as killed:
            observer.poll(force=True)
            value = observer.snapshot()
            killed.assert_not_called()
        self.assertEqual(value['children'][0]['owner']['pid'], 4243)
        self.assertEqual(value['children'][0]['state'], 'same-incarnation-live')
        self.assertTrue(value['unresolvedObserved'])

    def test_normal_and_both_original_and_teardown_failure_are_independent(self):
        for failed in (False, True):
            journal = diagnostic.Journal(self.root / str(failed))
            calls = []
            class Host:
                diagnostics = journal
                phase = 'runtime-ready'
                def snapshot_failure(self, error):
                    calls.append('snapshot')
                    raise OSError('PRIVATE-RAW-PATH')
                def close(self, success):
                    calls.append(('close', success))
                    if failed:
                        raise ValueError('secondary teardown refusal')
            def sequence(host, failure_mode):
                calls.append('sequence')
                if failed:
                    raise RuntimeError('original controlled failure')
            if failed:
                with self.assertRaisesRegex(RuntimeError, 'original controlled failure'):
                    diagnostic.execute_episode(Host(), False, sequence)
                self.assertEqual(calls, ['sequence', 'snapshot', ('close', False)])
                for name in ('original-failure.json', 'snapshot-failure.json', 'teardown-failure.json'):
                    self.assertTrue((journal.root / name).is_file())
                raw = b''.join(p.read_bytes() for p in journal.root.iterdir())
                self.assertNotIn(b'PRIVATE-RAW-PATH', raw)
                self.assertNotIn(b'original controlled failure', raw)
            else:
                diagnostic.execute_episode(Host(), False, sequence)
                self.assertEqual(calls, ['sequence', ('close', True)])
                self.assertTrue(json.loads((journal.root / 'episode-result.json').read_bytes())['completed'])
            self.assertIsNone(journal.fd)

    def test_late_deleted_staging_never_affects_frozen_original_failure_and_teardown(self):
        source = self.root / 'restart-evidence'
        episode = source / 'stateful/update'
        episode.mkdir(parents=True)
        failure = b'{"original":"controlled failure"}\n'
        teardown = b'{"unresolvedOwnership":true,"success":false}\n'
        (episode / 'failure.json').write_bytes(failure)
        (episode / 'teardown.json').write_bytes(teardown)
        staging = self.root / 'synthetic-profile/.runtime/pi-managed/.prepare-local'
        staging.mkdir(parents=True)
        (staging / 'LICENSE-pi').write_text('late deleted inert data')
        (source / 'forbidden-profile-link').symlink_to(staging.parent, target_is_directory=True)
        shutil.rmtree(staging)
        result = snapshot.freeze(source, self.root / 'stable')
        self.assertFalse(result['profileOrRuntimeBulkWalked'])
        self.assertEqual((self.root / 'stable/stateful/update/failure.json').read_bytes(), failure)
        self.assertEqual((self.root / 'stable/stateful/update/teardown.json').read_bytes(), teardown)
        self.assertEqual(result['entries']['stateful/update/failure.json']['sha256'], hashlib.sha256(failure).hexdigest())
        (episode / 'failure.json').write_text('changed after snapshot')
        self.assertEqual((self.root / 'stable/stateful/update/failure.json').read_bytes(), failure)
        self.assertFalse((self.root / 'stable/forbidden-profile-link').exists())

    def test_unstable_link_fifo_oversize_are_recorded_without_losing_other_receipts(self):
        source = self.root / 'receipt-source'
        source.mkdir()
        (source / 'entry-status.json').symlink_to(self.main)
        os.mkfifo(source / 'run-inputs.json')
        with (source / 'compatibility-reused.json').open('wb') as out:
            out.truncate(snapshot.PER_FILE + 1)
        (source / 'attachment-result.json').write_bytes(b'{"completed":false}')
        value = snapshot.freeze(source, self.root / 'negative-snapshot')
        for name in ('entry-status.json', 'run-inputs.json', 'compatibility-reused.json'):
            self.assertEqual(value['entries'][name]['status'], 'unavailable-or-unstable')
        self.assertEqual(value['entries']['attachment-result.json']['status'], 'frozen-original-bytes')

    @unittest.skipUnless(sys.platform == 'darwin', 'Darwin executable alias observation')
    def test_real_darwin_owned_executable_normal_alias_lifecycle(self):
        source = self.root / 'normal.c'
        source.write_text('#include <stdio.h>\nint main(void){puts("ready");fflush(stdout);return getchar()==EOF?3:0;}\n')
        env = {'PATH': '/usr/bin:/bin:/usr/sbin:/sbin', 'HOME': str(self.root), 'TMPDIR': str(self.root)}
        subprocess.run(['/usr/bin/clang', str(source), '-o', str(self.main)], env=env,
                       check=True, capture_output=True, timeout=20)
        child = subprocess.Popen([str(self.main)], env=env, stdin=subprocess.PIPE,
                                 stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        observations = []
        try:
            self.assertEqual(child.stdout.readline(), b'ready\n')
            owner = guards.identity(child.pid)
            held = diagnostic.DiagnosticOwned(child.pid, self.main, child=child,
                journal=self.journal, phase=lambda: 'runtime-ready', role='initial-S-main', shim=self.shim,
                expected_sha256=hashlib.sha256(self.main.read_bytes()).hexdigest())
            self.addCleanup(held.close_observer)
            old = guards.Owned(child.pid, self.main, child=child)
            for mode in ('before-link', 'link-rename', 'copy-fallback'):
                if mode == 'link-rename':
                    os.link(self.main, self.shim.with_name('tmp-gh'))
                    os.replace(self.shim.with_name('tmp-gh'), self.shim)
                elif mode == 'copy-fallback':
                    self.shim.unlink()
                    shutil.copyfile(self.main, self.shim)
                current = guards.identity(child.pid)
                changed = current != owner
                with patch.object(guards.os, 'kill') as killed:
                    if changed:
                        with self.assertRaisesRegex(ValueError, 'incarnation changed'):
                            old.poll()
                    self.assertIsNone(held.poll())
                    killed.assert_not_called()
                comm = subprocess.check_output(['/bin/ps', '-p', str(child.pid), '-o', 'comm='], env=env, timeout=3).decode().strip()
                self.assertEqual(comm, str(self.main))
                observations.append({'phase': mode, 'comparisonChanged': changed, 'psCommRole': 'installed-main',
                    'expected': diagnostic.safe_identity(owner, self.main, self.shim),
                    'current': diagnostic.safe_identity(current, self.main, self.shim),
                    'main': diagnostic.file_identity(self.main), 'shim': diagnostic.file_identity(self.shim)})
            child.stdin.write(b'x');child.stdin.flush()
            self.assertEqual(child.wait(timeout=3), 0)
            self.assertEqual(held.poll(), 0)
            print(json.dumps({'control': 'real-Darwin-alias', 'observations': observations,
                              'exitCode': child.returncode, 'signals': held.signals}))
        finally:
            if child.poll() is None:
                child.stdin.close()
                child.wait(timeout=3)
            for stream in (child.stdin, child.stdout, child.stderr):
                stream.close()


if __name__ == '__main__':
    unittest.main()
