#!/usr/bin/env python3
"""Checks of the private QA producer's updater-key boundaries.

Producer: the owner is a fake that records each subprocess's environment and
produces synthetic outputs; the key is a random synthetic value.
HelperIdentity and Lifecycle use real Git, real processes, the real process
group (POSIX) or job object (Windows) and real files. Real compiler/signature
effects of a complete QA build are validated separately.
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
import threading
import time
import unittest
from importlib.util import module_from_spec, spec_from_file_location
from unittest.mock import patch

HERE = pathlib.Path(__file__).resolve().parent
spec = spec_from_file_location('upgrade_qa', HERE / 'prepare-upgrade-qa.py')
qa = module_from_spec(spec)
spec.loader.exec_module(qa)

KEY = 'MYGO_UPDATER_PRIVATE_KEY'
PRODUCER = HERE.parent
WINDOWS = os.name == 'nt'
SOURCE = 'a' * 40
BLOB = 'b' * 40
PUBLIC = secrets.token_bytes(32)
PRIVATE = base64.b64encode(secrets.token_bytes(32) + PUBLIC).decode()
PY = [sys.executable, '-I', '-S', '-c']


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


class FakeGroup:
    """Records what the producer asks of its owner; never starts a process."""
    test = None
    ident = 'fake owner'

    def __init__(self):
        self.closed = False
        self.retained = []
        self.test.groups.append(self)

    def run(self, args, *, cwd=None, env=None, stdout=None, stderr=None, capture=False, timeout=None):
        return self.test.fake_run(list(args), cwd, os.environ if env is None else env, capture)

    def settle(self, aborting):
        self.test.events.append(('settle', aborting, [p.exists() for p in self.test.signers]))
        if self.test.undrained and aborting:
            raise qa.Undrained('synthetic: members unknown')

    def close(self):
        self.test.events.append(('close',))
        self.closed = True

    def signal_stop(self, escalate):
        pass


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
        self.build = self.good_build
        self.sign = lambda: None
        self.checkout_blob = BLOB
        self.undrained = False

    def tearDown(self):
        shutil.rmtree(self.dir)

    def fake_run(self, args, cwd, env, capture):
        self.calls.append({'args': args, 'cwd': cwd, 'name': KEY in env, 'value': any(PRIVATE in v for v in env.values())})
        if args[:2] == ['go', 'env']:
            return {'GOOS': b'linux\n', 'GOARCH': b'amd64\n'}[args[2]]
        if args[:2] == ['git', 'rev-parse'] and args[2] == 'HEAD':
            return (SOURCE + '\n').encode()
        if args[:3] == ['git', 'rev-parse', '--verify']:
            self.assertEqual(args[3], SOURCE + ':tools/package/upgrade_assets.go.txt')
            return (BLOB + '\n').encode()
        if args[:2] == ['git', 'hash-object']:
            self.assertEqual(pathlib.Path(cwd), PRODUCER)
            self.assertEqual(args[2:], ['--path=tools/package/upgrade_assets.go.txt', '--', 'tools/package/upgrade_assets.go.txt'])
            return (self.checkout_blob + '\n').encode()
        if args[:2] == ['git', 'archive']:
            return candidate_archive()
        if args[:4] == ['go', 'tool', 'mygo', 'build']:
            self.build(args, cwd, env)
        elif args[:4] == ['go', 'run', './tools/package', '-build-upgrade-assets']:
            self.assertEqual(pathlib.Path(cwd), PRODUCER)
            pathlib.Path(args[4]).write_bytes(b'helper')
        elif len(args) == 2:
            self.assertEqual(env[KEY], PRIVATE)
            self.sign()
        elif args[2:] != ['verify']:
            raise AssertionError(args)
        return None

    def good_build(self, args, cwd, env):
        cfg = json.loads((pathlib.Path(cwd) / 'mygo.json').read_text())
        version, signer = cfg['version'], pathlib.Path(cfg['updates']['privateKey'])
        self.assertEqual(signer.read_text().strip(), PRIVATE)
        if not WINDOWS:
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

    def main(self, key=PRIVATE, extra=None):
        self.calls, self.signers, self.events, self.groups = [], [], [], []
        FakeGroup.test = self
        shutil.rmtree(self.dir / 'out', ignore_errors=True)
        env = {'GITHUB_ACTIONS': 'true', 'RUNNER_ENVIRONMENT': 'github-hosted', 'RUNNER_TEMP': str(self.temp), KEY: key, **(extra or {})}
        with patch.dict(os.environ, env), patch.object(sys, 'argv', self.args), patch.object(qa, 'OwnedGroup', FakeGroup), \
                patch('builtins.print'), patch.object(qa.sys, 'stderr', io.StringIO()):
            try:
                qa.main()
            finally:
                self.assertNotIn(KEY, os.environ)

    def kinds(self):
        return [' '.join(c['args'][:4]) if not c['args'][0].endswith(('yesman-upgrade-assets', 'yesman-upgrade-assets.exe')) else 'helper ' + ' '.join(c['args'][2:]) for c in self.calls]

    def test_only_the_signers_receive_the_key(self):
        self.main()
        signers = [c for c in self.calls if c['args'][0].endswith(('yesman-upgrade-assets', 'yesman-upgrade-assets.exe')) and len(c['args']) == 2]
        self.assertEqual(len(signers), 1)
        self.assertTrue(signers[0]['name'] and signers[0]['value'])
        others = [c for c in self.calls if c is not signers[0]]
        self.assertGreaterEqual(len(others), 11)
        self.assertEqual([c for c in others if c['name'] or c['value']], [])
        builds = [c for c in others if c['args'][:4] == ['go', 'tool', 'mygo', 'build']]
        self.assertEqual(len(builds), 2)
        self.assertEqual(len(self.signers), 2)
        self.assertTrue(all(not p.exists() and not p.parent.exists() for p in self.signers))
        order = self.kinds()
        self.assertLess(order.index('go run ./tools/package -build-upgrade-assets'), order.index('helper '))
        self.assertEqual(order[-1], 'helper verify')
        self.assertEqual(self.events[-1], ('close',))
        self.assertEqual(list(self.temp.iterdir()), [])

    def test_missing_mismatched_or_duplicated_key_starts_no_owner_or_process(self):
        other = base64.b64encode(secrets.token_bytes(64)).decode()
        for key, extra in (('', None), (' ', None), (other, None), ('not base64!', None), (PRIVATE, {'COPY': 'x' + PRIVATE})):
            with self.assertRaises(Exception):
                self.main(key, extra)
            self.assertEqual((self.calls, self.groups), ([], []))

    def test_candidate_helper_must_match_the_producer(self):
        self.checkout_blob = 'c' * 40
        with self.assertRaises(AssertionError):
            self.main()
        self.assertNotIn('go tool mygo build', self.kinds())
        self.assertEqual(self.events[-1][:2], ('settle', True))

    def test_unsigned_or_interrupted_build_settles_before_removing_the_key(self):
        def unsigned(args, cwd, env):
            self.good_build(args, cwd, env)
            manifest = next(pathlib.Path(args[args.index('-o') + 1]).rglob('update-*.json'))
            manifest.write_text(json.dumps({**json.loads(manifest.read_text()), 'signature': ''}))

        def without_manifest(args, cwd, env):
            # MyGo skips signing (and on macOS the archive) when it finds no key.
            self.good_build(args, cwd, env)
            next(pathlib.Path(args[args.index('-o') + 1]).rglob('update-*.json')).unlink()

        def interrupted(args, cwd, env):
            self.good_build(args, cwd, env)
            raise KeyboardInterrupt

        for build, error in ((unsigned, AssertionError), (without_manifest, AssertionError), (interrupted, KeyboardInterrupt)):
            self.build = build
            with self.assertRaises(error):
                self.main()
            self.assertTrue(all(not p.exists() for p in self.signers))
            self.assertFalse([k for k in self.kinds() if k.startswith(('helper', 'go run'))])
            self.assertEqual(list(self.temp.iterdir()), [])
        # The interrupted build's key file still existed when the owner was drained.
        self.assertIn(('settle', True, [True]), self.events)

    def test_undrained_consumers_keep_their_key_and_source(self):
        def interrupted(args, cwd, env):
            self.good_build(args, cwd, env)
            raise KeyboardInterrupt
        self.build, self.undrained = interrupted, True
        with self.assertRaises(qa.Undrained):
            self.main()
        signer = self.signers[0]
        self.assertEqual(signer.read_text().strip(), PRIVATE)
        retained = sorted(p.name for p in self.temp.iterdir())
        self.assertTrue(any(name.startswith('qa-updater-key-') for name in retained))
        self.assertTrue(any(name.startswith('private-qa-successor-') for name in retained))
        receipt = json.loads(next(self.temp.glob('qa-producer-retained-*.json')).read_text())
        self.assertEqual({r['path'] for r in receipt['retained']} >= {str(signer.parent)}, True)
        for p in list(self.temp.iterdir()):
            shutil.rmtree(p) if p.is_dir() else p.unlink()

    def test_failed_signer_skips_verification_and_removes_helper(self):
        def fail():
            raise subprocess.CalledProcessError(2, 'helper')
        self.sign = fail
        with self.assertRaises(subprocess.CalledProcessError):
            self.main()
        self.assertNotIn('helper verify', self.kinds())
        self.assertEqual(list(self.temp.iterdir()), [])

    def test_workflow_runs_the_trusted_producer(self):
        text = (PRODUCER / '.github/workflows/application-upgrade-draft.yml').read_text(encoding='utf-8')
        job = text.split('\n  sign-qa:\n', 1)[1].split('\n  native:\n', 1)[0]
        self.assertIn('          ref: ${{ github.sha }}\n          path: .qa-producer\n          persist-credentials: false\n', job)
        step = job.split('      - name: Package and sign private QA successors\n', 1)[1].split('\n      - ', 1)[0]
        self.assertEqual(step, '        env:\n          MYGO_UPDATER_PRIVATE_KEY: ${{ secrets.MYGO_UPDATER_PRIVATE_KEY }}\n'
                         '          RUNNER_ENVIRONMENT: ${{ runner.environment }}\n'
                         '        run: python3 .qa-producer/scripts/prepare-upgrade-qa.py "$RUNNER_TEMP/released-inputs" "$RUNNER_TEMP/gate-inputs"')
        self.assertEqual(text.count('secrets.MYGO_UPDATER_PRIVATE_KEY'), 1)


def git(cwd, *args):
    env = {k: v for k, v in os.environ.items() if not k.startswith('GIT_')}
    env.update(GIT_CONFIG_NOSYSTEM='1', GIT_CONFIG_GLOBAL=os.devnull, GIT_TERMINAL_PROMPT='0')
    return subprocess.run(['git', *args], cwd=cwd, env=env, check=True, capture_output=True).stdout


class HelperIdentity(unittest.TestCase):
    """Real Git: the producer's checkout is compared through its own conversion rules."""

    def setUp(self):
        self.dir = pathlib.Path(tempfile.mkdtemp(prefix='qa-helper-identity-'))
        self.helper = (PRODUCER / qa.HELPER).read_bytes().replace(b'\r\n', b'\n')
        self.candidate = self.repo('candidate', 'false', self.helper)
        self.source = git(self.candidate, 'rev-parse', 'HEAD').decode().strip()
        os.environ['RUNNER_TEMP'] = str(self.dir)
        self.group = qa.OwnedGroup()

    def tearDown(self):
        self.group.close()
        shutil.rmtree(self.dir, ignore_errors=True)

    def repo(self, name, autocrlf, helper):
        root = self.dir / name
        (root / 'tools/package').mkdir(parents=True)
        git(root, 'init', '-q')
        git(root, 'config', 'core.autocrlf', 'false')
        (root / qa.HELPER).write_bytes(helper)
        git(root, 'add', '.')
        git(root, '-c', 'user.name=t', '-c', 'user.email=t@example.invalid', '-c', 'commit.gpgsign=false', 'commit', '-qm', 'helper')
        git(root, 'config', 'core.autocrlf', autocrlf)
        (root / qa.HELPER).unlink()
        git(root, 'checkout', '--', qa.HELPER)
        return root

    def blobs(self, producer):
        cwd = os.getcwd()
        os.chdir(self.candidate)
        try:
            return qa.helper_blobs(self.group, self.source, producer)
        finally:
            os.chdir(cwd)

    def test_lf_and_crlf_checkouts_of_the_same_helper_match(self):
        for autocrlf, crlf in (('false', False), ('true', True)):
            producer = self.repo('producer-' + autocrlf, autocrlf, self.helper)
            worktree = (producer / qa.HELPER).read_bytes()
            self.assertEqual(worktree.count(b'\r\n') > 0, crlf)
            candidate, checkout = self.blobs(producer)
            self.assertEqual(candidate, checkout)

    def test_a_changed_helper_or_unconverted_crlf_is_refused(self):
        changed = self.repo('producer-changed', 'true', self.helper.replace(b'package main', b'package main // changed', 1))
        unconverted = self.repo('producer-unconverted', 'false', self.helper)
        (unconverted / qa.HELPER).write_bytes(self.helper.replace(b'\n', b'\r\n'))
        for producer in (changed, unconverted):
            candidate, checkout = self.blobs(producer)
            self.assertNotEqual(candidate, checkout)


CHILD = r'''
import os, subprocess, sys, time
mode, pidfile = sys.argv[1], sys.argv[2]
grand = subprocess.Popen([sys.executable, '-I', '-S', '-c', sys.argv[3]] + sys.argv[4:])
with open(pidfile + '.tmp', 'w') as f:
    f.write('%d %d' % (os.getpid(), grand.pid))
os.replace(pidfile + '.tmp', pidfile)
if mode == 'wait':
    sys.exit(grand.wait())
if mode == 'hold':
    time.sleep(60)
'''
SLEEP = 'import time\ntime.sleep(60)'
IGNORE_TERM = 'import signal, time\nsignal.signal(signal.SIGTERM, signal.SIG_IGN)\ntime.sleep(60)'
RECORD_TERM = r'''import os, signal, sys, time
key = sys.argv[1]
def term(n, f):
    with open(sys.argv[2], 'w') as o:
        o.write('key-present' if os.path.exists(key) and os.path.getsize(key) > 0 else 'key-gone')
    os._exit(143)
signal.signal(signal.SIGTERM, term)
time.sleep(60)
'''


def alive(pid):
    if WINDOWS:
        import ctypes
        k32 = ctypes.WinDLL('kernel32')
        k32.OpenProcess.restype = ctypes.c_void_p
        handle = k32.OpenProcess(0x1000, False, pid)  # PROCESS_QUERY_LIMITED_INFORMATION
        if not handle:
            return False
        code = ctypes.c_ulong()
        k32.GetExitCodeProcess(ctypes.c_void_p(handle), ctypes.byref(code))
        k32.CloseHandle(ctypes.c_void_p(handle))
        return code.value == 259  # STILL_ACTIVE
    try:
        os.kill(pid, 0)
    except ProcessLookupError:
        return False
    state = subprocess.run(['/bin/ps', '-o', 'stat=', '-p', str(pid)], capture_output=True, text=True).stdout.strip()
    return bool(state) and not state.startswith('Z')


def gone(pids, seconds=5):
    end = time.monotonic() + seconds
    while time.monotonic() < end:
        if not any(alive(p) for p in pids):
            return True
        time.sleep(0.05)
    return False


class Lifecycle(unittest.TestCase):
    """Real processes in the real owner; the drain happens before resources are removed."""

    def setUp(self):
        self.dir = pathlib.Path(tempfile.mkdtemp(prefix='qa-lifecycle-'))
        self.saved = os.environ.get('RUNNER_TEMP')
        os.environ['RUNNER_TEMP'] = str(self.dir)
        qa.STOP.signum, qa.STOP.group = None, None
        self.group = qa.OwnedGroup()

    def tearDown(self):
        qa.STOP.signum, qa.STOP.group = None, None
        if not self.group.closed and not self.group.failed:
            self.group.drain()
        if self.saved is None:
            os.environ.pop('RUNNER_TEMP', None)
        else:
            os.environ['RUNNER_TEMP'] = self.saved
        shutil.rmtree(self.dir, ignore_errors=True)

    def start(self, mode, grand, *extra):
        pidfile = str(self.dir / ('pids-%s' % secrets.token_hex(4)))
        args = PY + [CHILD, mode, pidfile, grand, *extra]
        return pidfile, args

    def pids(self, pidfile, seconds=20):
        end = time.monotonic() + seconds
        while not os.path.exists(pidfile):
            self.assertLess(time.monotonic(), end, 'child did not start')
            time.sleep(0.02)
        return [int(x) for x in pathlib.Path(pidfile).read_text().split()]

    def run_in_thread(self, args, **kw):
        box = {}

        def target():
            try:
                box['result'] = self.group.run(args, **kw)
            except BaseException as error:  # noqa: B902 - recorded for the assertion
                box['error'] = error
        thread = threading.Thread(target=target)
        thread.start()
        return thread, box

    def test_normal_tree_returns_and_the_owner_closes_empty(self):
        out = self.group.run(PY + ['import subprocess,sys\nprint(subprocess.run([sys.executable,"-I","-S","-c","print(7)"],capture_output=True,text=True).stdout.strip())'], capture=True)
        self.assertEqual(out.split(), [b'7'])
        self.group.close()
        self.assertTrue(self.group.closed)

    def test_a_straggler_is_stopped_and_fails_the_run(self):
        pidfile, args = self.start('exit', SLEEP)
        with self.assertRaisesRegex(RuntimeError, 'not quiet'):
            self.group.run(args)
        self.assertTrue(gone(self.pids(pidfile)))
        self.assertTrue(self.group.closed)

    def test_timeout_drains_the_whole_tree(self):
        pidfile, args = self.start('wait', SLEEP)
        with self.assertRaises(subprocess.TimeoutExpired):
            self.group.run(args, timeout=2)
        pids = self.pids(pidfile)
        self.group.settle(aborting=True)
        self.assertTrue(gone(pids))
        self.assertTrue(self.group.closed)

    @unittest.skipIf(WINDOWS, 'POSIX signal escalation; Windows jobs terminate unconditionally')
    def test_a_term_ignoring_member_is_killed(self):
        pidfile, args = self.start('wait', IGNORE_TERM)
        thread, box = self.run_in_thread(args, timeout=60)
        pids = self.pids(pidfile)
        time.sleep(0.3)
        self.group.drain()
        thread.join(10)
        self.assertTrue(gone(pids))
        self.assertIsInstance(box.get('error'), subprocess.CalledProcessError)

    def test_launch_failure_leaves_the_owner_usable(self):
        with self.assertRaises(OSError):
            self.group.run(['qa-definitely-missing-program-%s' % secrets.token_hex(4)])
        self.assertEqual(self.group.run(PY + ['print(1)'], capture=True).strip(), b'1')
        self.group.close()

    def test_a_stop_signal_drains_consumers_before_their_key_is_removed(self):
        previous = qa._install_handlers()
        qa.STOP.group = self.group
        record = self.dir / 'term-record'
        try:
            with self.assertRaises((subprocess.CalledProcessError, qa.Stopped)):
                with qa.key_file(PRIVATE, self.group) as path:
                    pidfile, args = self.start('wait', SLEEP if WINDOWS else RECORD_TERM, path, str(record))

                    def stop():
                        self.pids(pidfile)
                        time.sleep(0.3)
                        if WINDOWS:
                            signal.raise_signal(signal.SIGINT)
                        else:
                            os.kill(os.getpid(), signal.SIGTERM)
                    threading.Thread(target=stop).start()
                    self.group.run(args, timeout=60)
            self.assertIsNotNone(qa.STOP.signum)
            self.assertTrue(gone(self.pids(pidfile)))
            self.assertFalse(os.path.exists(path) or os.path.exists(os.path.dirname(path)))
            if not WINDOWS:
                self.assertEqual(record.read_text(), 'key-present')
            self.assertTrue(self.group.closed)
        finally:
            for number, handler in previous.items():
                signal.signal(number, handler)

    def test_unconfirmed_drain_keeps_the_key_and_reports(self):
        pidfile, args = self.start('wait', SLEEP)
        thread, box = self.run_in_thread(args, timeout=60)
        real_live = self.group.live
        try:
            with self.assertRaises(qa.Undrained):
                with qa.key_file(PRIVATE, self.group) as path:
                    pids = self.pids(pidfile)
                    with patch.object(self.group, 'live', lambda: None), patch.object(qa.OwnedGroup, 'GRACE', 0.3):
                        self.group.settle(aborting=True)
            self.assertEqual(pathlib.Path(path).read_text().strip(), PRIVATE)
            self.assertEqual(self.group.retained[0]['path'], os.path.dirname(path))
        finally:
            # The test, not the producer, now stops its own members and files.
            if WINDOWS:
                qa._k32.TerminateJobObject(self.group.job, 1)
            else:
                try:
                    os.killpg(self.group.pgid, signal.SIGKILL)
                except PermissionError:
                    pass  # macOS: only the unreaped anchor zombie is left
            thread.join(10)
            self.assertTrue(gone(pids))
            if not WINDOWS:
                self.group.anchor.wait(10)
                self.group.anchor.stdin.close()
            self.group.closed = True
            del real_live


class OwnedFiles(unittest.TestCase):
    def setUp(self):
        self.dir = pathlib.Path(tempfile.mkdtemp(prefix='qa-owned-files-'))
        self.saved = os.environ.get('RUNNER_TEMP')
        os.environ['RUNNER_TEMP'] = str(self.dir)

    def tearDown(self):
        if self.saved is None:
            os.environ.pop('RUNNER_TEMP', None)
        else:
            os.environ['RUNNER_TEMP'] = self.saved
        shutil.rmtree(self.dir, ignore_errors=True)

    def test_key_file_normal_and_error(self):
        for fail in (False, True):
            try:
                with qa.key_file(PRIVATE) as path:
                    self.assertEqual(pathlib.Path(path).read_text().strip(), PRIVATE)
                    if not WINDOWS:
                        self.assertEqual(stat.S_IMODE(os.stat(path).st_mode), 0o600)
                        self.assertEqual(stat.S_IMODE(os.stat(os.path.dirname(path)).st_mode), 0o700)
                    if fail:
                        raise ValueError('body')
            except ValueError:
                pass
            self.assertEqual(list(self.dir.iterdir()), [])

    def test_a_replaced_key_directory_is_left_untouched(self):
        moved = self.dir / 'moved-original'
        with self.assertRaises((qa.CleanupIncomplete, PermissionError)) as caught:
            with qa.key_file(PRIVATE) as path:
                directory = pathlib.Path(path).parent
                directory.rename(moved)  # Windows refuses while the key file is open
                directory.mkdir()
                pathlib.Path(path).write_bytes(b'unrelated replacement')
        if isinstance(caught.exception, PermissionError):
            self.assertTrue(WINDOWS)
            self.assertEqual(list(self.dir.iterdir()), [])
            return
        self.assertEqual(pathlib.Path(path).read_bytes(), b'unrelated replacement')
        self.assertEqual(list(moved.iterdir()), [])  # the original key file was cleared and unlinked
        self.assertIn('no longer names the created directory', str(caught.exception))

    @unittest.skipIf(WINDOWS, 'Windows refuses to rename the open key file')
    def test_a_replaced_key_file_is_cleared_through_its_handle(self):
        aside = self.dir / 'aside'
        with self.assertRaises(qa.CleanupIncomplete):
            with qa.key_file(PRIVATE) as path:
                os.rename(path, aside)
                pathlib.Path(path).write_bytes(b'unrelated replacement')
        self.assertEqual(aside.read_bytes(), b'')  # the original key bytes are gone
        self.assertEqual(pathlib.Path(path).read_bytes(), b'unrelated replacement')
        os.unlink(path)

    def test_a_replaced_owned_directory_is_left_untouched(self):
        moved = self.dir / 'moved-original'
        try:
            with self.assertRaises(qa.CleanupIncomplete):
                with qa.owned_dir('qa-owned-') as path:
                    (pathlib.Path(path) / 'tree/config').mkdir(parents=True)
                    (pathlib.Path(path) / 'tree/config/mygo.json').write_text('{}')
                    os.rename(path, moved)
                    os.mkdir(path)
                    pathlib.Path(path, 'unknown').write_text('keep')
        except PermissionError:
            self.assertTrue(WINDOWS)
            return
        self.assertEqual(pathlib.Path(path, 'unknown').read_text(), 'keep')
        if not WINDOWS:
            self.assertEqual(list(moved.iterdir()), [])  # emptied through the held handle


if __name__ == '__main__':
    unittest.main()
