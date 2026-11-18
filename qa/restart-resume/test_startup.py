"""Benign first-reachable App image and readiness controls; no native App runs."""
import hashlib
import importlib.util
import json
import os
import shutil
from pathlib import Path
import tempfile
import threading
from types import SimpleNamespace
import unittest
from unittest.mock import patch

import image_proof as image
import provenance

ROOT = Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location('startup_attachment', ROOT / 'runner-attachment-f48/runner_attachment.py')
attachment = importlib.util.module_from_spec(spec)
spec.loader.exec_module(attachment)
adapter, fixture, core, original = attachment.dependencies(ROOT)


class Startup(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name).resolve()
        self.install = self.root / 'Application install with spaces'
        self.bundle = self.install / 'yesman.app'
        self.main = self.bundle / image.MAIN
        self.main.parent.mkdir(parents=True)
        self.main.write_bytes(b'ordinary inert main image\n')
        self.main.chmod(0o755)
        (self.main.parent / 'yesman').symlink_to('Yes Man')
        self.expected = hashlib.sha256(self.main.read_bytes()).hexdigest()
        self.diag = image.Diagnostics(self.root / 'image-diagnostics.json')
        self.addCleanup(self.diag.close)

    def linked(self):
        shim = self.root / 'data/.runtime/github-app-gh/bin/gh'
        shim.parent.mkdir(parents=True)
        os.link(self.main, shim)  # same normal operation as fixed InstallShim
        self.assertEqual(self.main.stat().st_nlink, 2)
        return shim

    def inspect(self):
        return image.main_image(self.install, self.bundle, self.expected, 'inspect-initial', self.diag, alias_data=self.root / 'data')

    def test_normal_app_shim_old_refusal_new_verified_main_and_inventory(self):
        before = original.bundle_inventory(self.bundle)
        self.linked()
        with self.assertRaisesRegex(fixture.Refused, 'bounded unique'):
            fixture.hash_file(self.main)
        with self.assertRaisesRegex(ValueError, 'bounded unique'):
            original.bundle_inventory(self.bundle)
        current = self.inspect()
        self.assertEqual((current['sha256'], current['nlink']), (self.expected, 2))
        after = image.bundle_inventory(adapter, self.install, self.bundle, self.expected, 'image', self.diag)
        self.assertEqual(before, after)
        self.assertEqual(self.main.read_bytes(), b'ordinary inert main image\n')

    def test_original_unique_mutable_reader_and_obstruction_stay_strict(self):
        data = self.root / 'data'
        data.mkdir()
        mutable = data / 'intent.json'
        mutable.write_bytes(b'{"synthetic":true}\n')
        os.link(mutable, data / 'intent-copy.json')
        with self.assertRaisesRegex(ValueError, 'bounded unique'):
            adapter.read_owned(data, mutable, 1024)
        staged = self.install / '.yesman.app.update-fixture/app/yesman.app'
        main = staged / image.MAIN
        main.parent.mkdir(parents=True)
        main.write_bytes(self.main.read_bytes())
        main.chmod(0o755)
        os.link(main, main.parent / 'second')
        with self.assertRaisesRegex(fixture.Refused, 'bounded unique'):
            fixture.obstruction_plan(self.install, staged, 'a' * 32, self.expected)
        self.assertEqual(main.read_bytes(), self.main.read_bytes())

    def test_symlink_other_bundle_fifo_and_oversize_refuse_without_read(self):
        outside = self.root / 'outside'
        outside.write_bytes(self.main.read_bytes())
        self.main.unlink()
        self.main.symlink_to(outside)
        with patch.object(image.os, 'read') as reader:
            with self.assertRaisesRegex(ValueError, 'path-refused'):
                self.inspect()
            reader.assert_not_called()
        self.main.unlink()
        os.mkfifo(self.main)
        with patch.object(image.os, 'read') as reader:
            with self.assertRaisesRegex(ValueError, 'not-regular'):
                self.inspect()
            reader.assert_not_called()
        self.main.unlink()
        with self.main.open('wb') as output:
            output.truncate(image.LIMIT + 1)
        self.main.chmod(0o755)
        with patch.object(image.os, 'read') as reader:
            with self.assertRaisesRegex(ValueError, 'size-refused'):
                self.inspect()
            reader.assert_not_called()
        with self.assertRaisesRegex(ValueError, 'path-refused'):
            image.main_image(self.install, self.root / 'other.app', self.expected, 'image', self.diag)

    def test_content_identity_replacement_and_missing_observation(self):
        self.main.write_bytes(b'changed inert image\n')
        with self.assertRaisesRegex(ValueError, 'hash-mismatch'):
            self.inspect()
        self.main.write_bytes(b'ordinary inert main image\n')
        read = os.read
        changed = False
        def replace(fd, count):
            nonlocal changed
            raw = read(fd, count)
            if raw and not changed:
                changed = True
                replacement = self.main.with_name('new-image')
                replacement.write_bytes(b'ordinary inert main image\n')
                replacement.chmod(0o755)
                os.replace(replacement, self.main)
            return raw
        with patch.object(image.os, 'read', side_effect=replace):
            with self.assertRaisesRegex(ValueError, 'identity-changed'):
                self.inspect()
        self.main.unlink()
        with self.assertRaisesRegex(ValueError, 'absent'):
            self.inspect()
        self.assertIsNone(image.main_image(self.install, self.bundle, self.expected, 'capture-failed-launch', self.diag, True))

    def test_exact_Host_initial_inspect_with_real_hard_linked_file(self):
        self.linked()
        host = object.__new__(attachment.Host)
        host.install, host.bundle, host.main = self.install, self.bundle, self.main
        host.data = self.root / 'data'
        stage = {'mainSHA256': self.expected, 'version': '0.1.49', 'candidate': provenance.CANDIDATE,
                 'candidateTree': provenance.TREE, 'sourceCommit': provenance.RELEASE,
                 'official': True, 'verificationReceipt': 'synthetic-inert-receipt'}
        observation = {'pageReady': True, 'visibility': 'visible',
                       'about': {'version': '0.1.49', 'revision': provenance.RELEASE,
                                 'piManaged': True, 'piState': 'preparing', 'modified': False},
                       'state': {'home': str(host.data)}}
        host.stages = [stage]
        host.old_page = None
        host.controller = SimpleNamespace(lock=threading.RLock(), reports={'first-page': observation})
        host.timer = attachment.Deadline(lambda: None)
        host.h, host.a, host.image_diagnostics = original, adapter, self.diag
        host.helper = self.root / 'unused-native-helper'
        host.runner = SimpleNamespace(find=lambda _: [4242], naming=SimpleNamespace(native=lambda *_:
                                     {'processName': 'Yes Man', 'windows': [{'onScreen': True, 'bounds': {'Width': 1024}}]}))
        notes = []
        host.note = lambda kind, **value: notes.append({'kind': kind, **value})
        ps = SimpleNamespace(stdout=f'4242 1 {self.main}\n'.encode())
        with patch.object(image.subprocess, 'run', return_value=ps), patch.object(fixture, 'hosted_arm64'), patch.dict(os.environ, {'RUNNER_TEMP': str(self.root)}):
            with self.assertRaisesRegex(fixture.Refused, 'bounded unique'):
                original.collect_native(self.bundle)
            value = host.inspect(0, initial=True)
        self.assertEqual(value['pid'], 4242)
        self.assertEqual(value['mainSHA256'], self.expected)
        self.assertEqual(host.page, 'first-page')
        self.assertEqual(len(notes), 1)
        self.assertEqual(observation['about']['piState'], 'preparing')

    def test_actual_failure_close_records_consumers_and_io_without_profile_handover(self):
        host = object.__new__(attachment.Host)
        host.staging_attempted, host.watcher = False, None
        host.helper_ownership = None
        host.process = SimpleNamespace(drain=lambda: {'drained': True, 'exitCode': 0})
        host.runner = SimpleNamespace(find=lambda _: [])
        host.main = self.main
        host.root = self.root
        host.evidence = self.root / 'failure-evidence'
        host.evidence.mkdir()
        host.data, host.config = self.root / 'data', self.root / 'pi-config'
        host.data.mkdir()
        sentinel = host.data / 'synthetic.keep'
        sentinel.write_bytes(b'retain failed profile')
        host.process_observer = SimpleNamespace(poll=lambda **_: None, snapshot=lambda: {'unresolvedObserved': False, 'children': [], 'scope': 'ordinary control'})
        host.controller = SimpleNamespace(close=lambda: None, thread=SimpleNamespace(is_alive=lambda: False))
        host.server = None
        host.log = (host.evidence / 'app.log').open('ab')
        host.image_diagnostics = self.diag
        host.owned_profile = True
        with patch.object(Path, 'home', return_value=self.root / 'synthetic-home'):
            host.close(False)
        result = json.loads((host.evidence / 'teardown.json').read_bytes())
        self.assertFalse(result['success'] or result['unresolvedOwnership'])
        self.assertTrue(result['ioObservation']['mainOutputFileClosed'])
        self.assertTrue(result['ioObservation']['controllerThreadJoined'])
        self.assertIsNotNone(result['failureProfileConsumers'])
        self.assertEqual(sentinel.read_bytes(), b'retain failed profile')
        self.assertFalse((self.root / 'retained synthetic profile').exists())

    def test_normal_cross_volume_copy_shape_and_alias_change(self):
        data = self.root / 'data'
        shim = data / '.runtime/github-app-gh/bin/gh'
        shim.parent.mkdir(parents=True)
        shutil.copyfile(self.main, shim)
        self.assertEqual(self.inspect()['nlink'], 1)
        self.assertEqual(json.loads((self.root / 'image-diagnostics.json').read_bytes())['rows']['inspect-initial']['knownShim']['relation'], 'different-file')
        shim.unlink()
        os.link(self.main, shim)
        read = os.read
        changed = False
        def unlink_alias(fd, count):
            nonlocal changed
            raw = read(fd, count)
            if raw and not changed:
                changed = True
                shim.unlink()
            return raw
        with patch.object(image.os, 'read', side_effect=unlink_alias):
            with self.assertRaisesRegex(ValueError, 'identity-changed'):
                self.inspect()

    def test_fstat_diagnostics_fixed_role_phase_only(self):
        self.linked()
        self.inspect()
        raw = (self.root / 'image-diagnostics.json').read_bytes()
        value = json.loads(raw)['rows']['inspect-initial']
        self.assertLessEqual(len(raw), 8192)
        self.assertEqual((self.root / 'image-diagnostics.json').stat().st_mode & 0o777, 0o600)
        self.assertEqual(value['pathRole'], 'installed-bound-main')
        self.assertEqual(value['before']['nlink'], 2)
        self.assertEqual(value['before'], value['after'])
        self.assertNotIn(str(self.root).encode(), raw)
        self.assertEqual(set(value), {'phase', 'pathRole', 'code', 'before', 'after', 'knownShim'})
        self.assertEqual(value['knownShim']['relation'], 'same-file')


class RuntimeOrdering(unittest.TestCase):
    def host(self, state, error=None):
        host = object.__new__(attachment.Host)
        host.page = 'first-page'
        host.controller = SimpleNamespace(lock=threading.RLock(), reports={'first-page': {'about': {'piState': state, 'piError': error}}})
        host.a, host.data, host.bindings, host.contract = None, Path('/synthetic-data'), {}, {'runtimeSHA256': 'a' * 64}
        host.command = lambda *_: self.fail('readiness must not publish a mutation')
        host.diagnostics = SimpleNamespace(emit=lambda *_args, **_kwargs: None)
        return host

    def test_actual_readiness_entry_waits_then_captures_verified_runtime(self):
        host = self.host('preparing')
        ticks = [0.0]
        def advance(_):
            ticks[0] += 1
            host.controller.reports[host.page]['about']['piState'] = 'ready'
        host.timer = attachment.Deadline(lambda: None, lambda: ticks[0], advance)
        notes = []
        host.note = lambda kind, **value: notes.append((kind, value))
        with patch.object(attachment, 'capture_runtime', return_value={'verified': True}) as capture:
            host.wait_runtime()
            capture.assert_called_once()
        self.assertEqual(notes, [('managed-runtime-ready', {'receipt': {'verified': True}})])

    def test_failed_or_never_ready_cannot_be_bootstrap_success(self):
        for state, error in [('failed', None), ('preparing', 'synthetic error text not retained')]:
            host = self.host(state, error)
            host.timer = attachment.Deadline(lambda: None)
            host.note = lambda *_args, **_kwargs: self.fail('failed readiness must not emit success')
            with patch.object(attachment, 'capture_runtime') as capture:
                with self.assertRaisesRegex(ValueError, '^managed runtime preparation failed$'):
                    host.wait_runtime()
                capture.assert_not_called()
        host = self.host('preparing')
        clock = [0.0]
        host.timer = attachment.Deadline(lambda: None, lambda: clock[0], lambda _: clock.__setitem__(0, 61.0))
        with patch.object(attachment, 'capture_runtime') as capture:
            with self.assertRaisesRegex(TimeoutError, 'offline production managed runtime not ready'):
                host.wait_runtime()
            capture.assert_not_called()


if __name__ == '__main__':
    unittest.main()
