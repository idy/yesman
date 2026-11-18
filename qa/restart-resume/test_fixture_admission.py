"""R7 normal/negative model and observer-lifetime controls. No App execution."""
import copy
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import select
import subprocess
import sys
import tempfile
import threading
from types import SimpleNamespace
import unittest
from unittest.mock import patch

import fixture_admission as admission
import guards
import native_diagnostics as diagnostic
import process_proof
import freeze_native_diagnostics as snapshot

ROOT = Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location('admission_attachment', ROOT / 'runner-attachment-f48/runner_attachment.py')
attachment = importlib.util.module_from_spec(spec)
spec.loader.exec_module(attachment)
# Reuse only the sealed adapter object if another suite already verified/imported it.
adapter = sys.modules.get('adapter')
if adapter is None:
    adapter, _, _, _ = attachment.dependencies(ROOT)

GOOD = {'schema': 1, 'status': 'observed', 'readAt': '2026-11-18T22:56:20.123Z',
        'selected': 'expected', 'modelCount': 1, 'memberCount': 1,
        'supportedLevels': ['off'], 'memberKeyMatches': True, 'effortMatches': True,
        'memberMissing': False, 'usable': True}


class Admission(unittest.TestCase):
    def test_two_field_overlay_preserves_literal_original_and_all_pi_bytes(self):
        original = adapter.seed_plan('/synthetic-only-home', 'http://127.0.0.1:1234/v1')
        unchanged = copy.deepcopy(original)
        new, receipt = admission.fresh_seed(original)
        name = str(Path(original['data']) / 'settings.json')
        old_settings = json.loads(original['files'][name])
        settings = json.loads(new['files'][name])
        self.assertEqual(settings['crew']['options'][0]['effort'], 'off')
        self.assertEqual(settings['crew']['yesman'], admission.KEY)
        settings['crew']['options'][0]['effort'] = ''
        settings['crew']['yesman'] = 't199-fixture/fixture'
        self.assertEqual(settings, old_settings)
        self.assertEqual(original, unchanged)
        self.assertEqual({k: v for k, v in new['files'].items() if k != name},
                         {k: v for k, v in original['files'].items() if k != name})
        self.assertEqual(receipt['originalSettingsSHA256'], hashlib.sha256(original['files'][name]).hexdigest())
        self.assertEqual(receipt['settingsSHA256'], hashlib.sha256(new['files'][name]).hexdigest())
        self.assertNotIn('http://127.0.0.1', json.dumps(receipt))
        for field, value in [('effort', 'off'), ('provider', 'unknown')]:
            changed = copy.deepcopy(original)
            bad = copy.deepcopy(old_settings)
            bad['crew']['options'][0][field] = value
            changed['files'][name] = json.dumps(bad).encode()
            with self.assertRaisesRegex(ValueError, 'original fresh fixture'):
                admission.fresh_seed(changed)

    def test_real_rendered_bridge_projection_once_and_negative_models(self):
        # Execute the actual injected observer using a harmless MyGo call double.
        # It never imports production UI or calls a provider/App.
        script = admission.BRIDGE + r'''
const assert = require('node:assert/strict');
const key = "t199-fixture/fixture:off";
let calls = 0, source;
async function call(method) { assert.equal(method,"Crew"); calls++; if(source.throw) throw Error("SECRET-ERROR"); return source; }
(async () => {
const good = {readAt:"2026-11-18T22:56:20.123Z", yesman:key,
 models:[{provider:"t199-fixture",id:"fixture",levels:["off"], config:"SECRET-MODEL"},
         {provider:"SECRET-PROVIDER",id:"PRIVATE-ID",levels:["high"]}],
 members:[{provider:"t199-fixture",model:"fixture",key,effort:"off",missing:false,auth:"SECRET-AUTH"}]};
const cases = [
 ["normal", good, true],
 ["missing-model", {...good, models:[]}, false],
 ["empty-effort", {...good,members:[{...good.members[0],key:"t199-fixture/fixture",effort:"",missing:true}]},false],
 ["effort-only-correction", {...good,yesman:"t199-fixture/fixture"},false],
 ["key-only-correction", {...good,members:[{...good.members[0],key:"t199-fixture/fixture",effort:"",missing:true}]},false],
 ["unsupported-effort", {...good,models:[{...good.models[0],levels:["high"]}],members:[{...good.members[0],missing:true}]},false],
 ["missing-catalog-read", {...good,readAt:"0001-01-01T00:00:00Z"},false],
 ["catalog-error", {...good,error:"SECRET-CATALOG-ERROR"},false],
 ["bridge-error", {throw:true},false],
];
let rows=[];
for (const [name,value,want] of cases) {
 source=value; fixtureCrewAdmission=undefined; const before=calls;
 assert.equal(await fixtureAdmission({piState:"preparing"}),null);
 assert.equal(calls,before);
 const row=await fixtureAdmission({piState:"ready"});
 assert.equal(await fixtureAdmission({piState:"ready"}),row);
 assert.equal(calls,before+1);
 assert(!JSON.stringify(row).includes("SECRET")); assert(!JSON.stringify(row).includes("PRIVATE"));
 rows.push({name,want,row});
}
console.log(JSON.stringify({rows,calls}));
})().catch(e=>{console.error(e);process.exitCode=1;});
'''
        result = subprocess.run(['node', '-'], input=script.encode(), capture_output=True, timeout=10)
        self.assertEqual(result.returncode, 0, result.stderr.decode())
        rows = json.loads(result.stdout)
        self.assertEqual(rows['calls'], 9)
        for case in rows['rows']:
            self.assertEqual(admission.retain_admission(case['row'])['admitted'], case['want'], case['name'])
        print(json.dumps({'control': 'actual-rendered-model-projection', 'cases': rows['rows']}))
        base = (ROOT / 'qa-fixture-prep/driver.js').read_text()
        rendered = admission.driver(base)
        self.assertEqual(rendered.split('  window.addEventListener', 1)[1], base.split('  window.addEventListener', 1)[1])
        self.assertEqual(rendered.split(admission.BRIDGE, 1)[0],
                         base.split('  async function observe()', 1)[0])

    def test_durable_admission_for_both_scenarios_precedes_original_hires(self):
        for scenario in ('update', 'failed-launch-recovery'):
            with tempfile.TemporaryDirectory() as folder:
                host = object.__new__(attachment.Host)
                host.page = 'synthetic-page'
                host.controller = SimpleNamespace(lock=threading.RLock(), reports={host.page: {'modelAdmission': GOOD}})
                host.diagnostics = diagnostic.Journal(Path(folder) / 'diagnostics')
                try:
                    host.admit_model()
                    value = json.loads((host.diagnostics.root / 'model-admission.json').read_bytes())
                    self.assertTrue(value['admitted'], scenario)
                    self.assertEqual(host.phase, 'model-admission')
                    self.assertEqual((host.diagnostics.root / 'model-admission.json').stat().st_mode & 0o777, 0o600)
                finally:
                    host.diagnostics.close()
        import ast
        start = next(n for c in ast.parse((ROOT / 'runner-attachment-f48/runner_attachment.py').read_text()).body
                     if isinstance(c, ast.ClassDef) and c.name == 'Host' for n in c.body
                     if isinstance(n, ast.FunctionDef) and n.name == 'start')
        calls = [ast.unparse(n.func) for n in ast.walk(start) if isinstance(n, ast.Call)]
        self.assertIn('self.admit_model', calls)
        text = ast.unparse(start)
        self.assertLess(text.index('self.wait_runtime()'), text.index('self.admit_model()'))
        self.assertLess(text.index('self.admit_model()'), text.index("self.command('HireNamed'"))

    def test_actual_both_scenario_start_seeds_overlay_and_admits_before_hires(self):
        for failed in (False, True):
            for accepted in (False, True):
                with tempfile.TemporaryDirectory() as folder:
                    root = Path(folder).resolve()
                    home = root / 'fresh-home'
                    home.mkdir()
                    events = []
                    host = object.__new__(attachment.Host)
                    host.failed, host.a = failed, adapter
                    host.data, host.config = home / 'yesman', home / '.pi/agent'
                    host.contract = {k: 'a' * 64 for k in ('helperLiteralSHA256', 'runtimeSHA256', 'extensionSHA256')}
                    host.contract['publicNpmCache'] = 'unused-synthetic-cache'
                    host.stages = [{'verificationReceipt': 'synthetic-S'}, {'verificationReceipt': 'synthetic-Q'}]
                    host.f = SimpleNamespace(validate_contract=lambda _: None)
                    host.evidence = root / 'evidence'
                    host.evidence.mkdir()
                    host.diagnostics = diagnostic.Journal(host.evidence / 'diagnostics')
                    host.install = root / 'install'
                    host.install.mkdir()
                    archive = root / 'inert.tar'
                    with attachment.tarfile.open(archive, 'w'):
                        pass
                    host.plan = {'Feed': 'https://127.0.0.1:1234/feed', 'Stages': [{'Archive': str(archive)}]}
                    host.tls = None
                    host.ledger, host.events, host.submissions = guards.ProviderLedger(), [], {}
                    host.bound, host.command_results = {}, {}
                    host.note = lambda *_, **__: None
                    host.image = lambda *_: events.append('image-guard-double')
                    host.launch = lambda *_: events.append('launch-double-no-App')
                    def inspect(*_, **__):
                        host.page = 'page'
                        return {'synthetic': True}
                    host.inspect = inspect
                    host.wait_runtime = lambda: events.append('runtime-guard-double')
                    def command(method, args):
                        events.append(method)
                        receipt = json.loads((host.diagnostics.root / 'model-admission.json').read_bytes())
                        self.assertTrue(receipt['admitted'])
                        self.assertEqual(method, 'HireNamed')
                        return {'name': args[0], 'hiredAt': 'synthetic-birth'}
                    host.command = command
                    class Controller:
                        def __init__(self, *_):
                            self.lock = threading.RLock()
                            self.reports = {'page': {'modelAdmission': {**GOOD, 'effortMatches': accepted}}}
                        def start(self):
                            return {'providerBase': 'http://127.0.0.1:1234/v1'}
                        def select_page(self, _):
                            pass
                    class Feed:
                        def __init__(self, *_):
                            pass
                        def serve(self, *_):
                            return None
                    host.core, host.runner = SimpleNamespace(Controller=Controller), SimpleNamespace(Feed=Feed)
                    try:
                        with patch.object(Path, 'home', return_value=home), patch.object(
                                sys.modules['fixture'], 'hosted_arm64'), patch.object(
                                attachment.public_npm_cache, 'bootstrap', return_value={'synthetic': True}), patch.object(
                                attachment.tarfile, 'open', return_value=attachment.contextlib.nullcontext(
                                    SimpleNamespace(extractall=lambda *_, **__: None))):
                            if accepted:
                                host.start()
                            else:
                                with self.assertRaisesRegex(ValueError, 'before Hire'):
                                    host.start()
                        self.assertEqual(events.count('HireNamed'), 2 if accepted else 0)
                        written = json.loads((host.data / 'settings.json').read_bytes())
                        self.assertEqual(written['crew']['yesman'], admission.KEY)
                        self.assertEqual(written['crew']['options'][0]['effort'], 'off')
                        seed = json.loads((host.diagnostics.root / 'seed-overlay.json').read_bytes())
                        self.assertEqual(seed['writtenSettingsSHA256'], seed['settingsSHA256'])
                        self.assertEqual(host.runtime['scenario'], 'failed-launch-recovery' if failed else 'update')
                    finally:
                        host.diagnostics.close()

    def test_refused_projection_is_retained_without_raw_error_before_mutation(self):
        for changed in ({'status': 'bridge-error', 'secret': 'PRIVATE'}, {'effortMatches': False},
                        {'memberMissing': True}, {'selected': 'mismatched-or-missing'}, {'modelCount': 0}):
            with tempfile.TemporaryDirectory() as folder:
                host = object.__new__(attachment.Host)
                host.page = 'page'
                host.controller = SimpleNamespace(lock=threading.RLock(), reports={'page': {'modelAdmission': {**GOOD, **changed}}})
                host.command = lambda *_: self.fail('no mutation before admitted Crew')
                host.diagnostics = diagnostic.Journal(Path(folder) / 'diagnostics')
                try:
                    with self.assertRaisesRegex(ValueError, 'before Hire'):
                        host.admit_model()
                    raw = (host.diagnostics.root / 'model-admission.json').read_bytes()
                    self.assertFalse(json.loads(raw)['admitted'])
                    self.assertNotIn(b'PRIVATE', raw)
                finally:
                    host.diagnostics.close()
        self.assertFalse(admission.retain_admission(None)['admitted'])


@unittest.skipUnless(sys.platform == 'darwin', 'Darwin real profile-descriptor controls')
class ObserverRelease(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name).resolve()
        self.main = self.root / 'install/main'
        self.main.parent.mkdir()
        self.main.write_bytes(b'inert observer bytes, never executed\n')
        self.main.chmod(0o700)
        self.data = self.root / 'profile'
        self.shim = self.data / '.runtime/github-app-gh/bin/gh'
        self.shim.parent.mkdir(parents=True)
        self.journal = diagnostic.Journal(self.root / 'diagnostics')
        self.addCleanup(self.journal.close)
        self.observed = {'pid': 4242, 'uid': os.getuid(), 'startSeconds': 1,
                         'startMicroseconds': 2, 'executable': str(self.main)}
        self.owned = diagnostic.DiagnosticOwned(4242, self.main, observe=lambda _: self.observed,
            journal=self.journal, phase=lambda: 'teardown', shim=self.shim,
            expected_sha256=hashlib.sha256(self.main.read_bytes()).hexdigest())
        self.addCleanup(self.owned.close_observer)
        os.link(self.main, self.shim.with_name('tmp'))
        os.rename(self.shim.with_name('tmp'), self.shim)

    def consumers(self):
        return process_proof.profiles((('fixture-data', self.data),))['paths'][0]

    def host(self, unresolved=False, failed=False):
        h = object.__new__(attachment.Host)
        h.failed = failed
        h.staging_attempted, h.watcher, h.helper_ownership, h.process = False, None, None, self.owned
        h.runner = SimpleNamespace(find=lambda _: [])
        h.main, h.root, h.data, h.config = self.main, self.root, self.data, self.root / 'pi-config'
        h.evidence = self.root / 'evidence'
        h.evidence.mkdir()
        h.process_observer = SimpleNamespace(poll=lambda **_: None, snapshot=lambda: {'unresolvedObserved': unresolved, 'children': []})
        h.controller, h.server = None, None
        h.log = (h.evidence / 'app.log').open('ab')
        h.image_diagnostics = SimpleNamespace(close=lambda: None)
        h.diagnostics, h.diagnostic_owners, h.owned_profile = self.journal, [self.owned], True
        return h

    def successful_handover(self, failed):
        self.assertEqual(self.consumers()['PIDs'], [os.getpid()])
        self.observed = None
        h = self.host(failed=failed)
        with patch.object(Path, 'home', return_value=self.root / 'fresh-home'):
            h.close(True)
        row = json.loads((h.evidence / 'teardown.json').read_bytes())
        self.assertTrue(row['success'])
        release = row['observerRelease']['observers'][0]
        self.assertEqual(release['observer']['pid'], os.getpid())
        self.assertEqual(release['observer']['uid'], os.getuid())
        self.assertTrue(release['closed'] and release['ownedMainDeparted'])
        self.assertEqual(release['postClose'], 'EBADF')
        self.assertEqual(release['kernelPathRole'], 'fixed-private-gh-shim')
        self.assertEqual(self.owned.signals, [])
        self.assertIsNone(self.owned.bound_main.fd)
        self.assertTrue((self.root / 'retained synthetic profile/0/.runtime/github-app-gh/bin/gh').is_file())
        print(json.dumps({'control': 'actual-Host-success-observer-release', 'receipt': release}))

    def test_update_original_scan_fails_then_corrected_Host_handover_passes(self):
        self.successful_handover(False)

    def test_failed_launch_scenario_uses_same_full_scan_and_observer_handover(self):
        self.successful_handover(True)

    def lingering_consumer(self, success):
        sentinel = self.data / 'held.txt'
        sentinel.write_text('synthetic child descriptor')
        child = subprocess.Popen([sys.executable, '-c', 'import sys; f=open(sys.argv[1]); print("ready",flush=True); sys.stdin.read(); f.close()', str(sentinel)],
                                 stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        try:
            self.assertTrue(select.select([child.stdout], [], [], 5)[0])
            self.assertEqual(child.stdout.readline(), b'ready\n')
            owner = guards.identity(child.pid)
            self.observed = None
            h = self.host()
            with patch.object(Path, 'home', return_value=self.root / 'fresh-home'):
                with self.assertRaisesRegex(ValueError, 'fixture profile' if success else 'profile consumer remains'):
                    h.close(success)
            row = json.loads((h.evidence / 'teardown.json').read_bytes())
            self.assertFalse(row['success'])
            self.assertTrue(row['unresolvedOwnership'])
            self.assertIsNone(self.owned.bound_main.fd)
            consumer = self.consumers() if success else row['failureProfileConsumers']['paths'][0]
            self.assertIn(child.pid, consumer['PIDs'])
            self.assertEqual(guards.identity(child.pid), owner)
            self.assertFalse((self.root / 'retained synthetic profile').exists())
            child.stdin.close()
            self.assertEqual(child.wait(timeout=5), 0)
            self.assertEqual(self.consumers()['status'], 'no-observed-consumer')
        finally:
            if child.poll() is None:
                child.stdin.close()
                child.wait(timeout=5)
            for stream in (child.stdin, child.stdout, child.stderr):
                stream.close()

    def test_failure_scan_lingering_child_still_refuses_after_own_observer_release(self):
        self.lingering_consumer(False)

    def test_success_scan_lingering_child_still_refuses_after_own_observer_release(self):
        self.lingering_consumer(True)

    def test_live_changed_unknown_owners_and_unresolved_drain_keep_refusal(self):
        for value in (self.observed, {**self.observed, 'startMicroseconds': 3}):
            self.observed = value
            with patch.object(guards.os, 'kill') as killed:
                with self.assertRaises(ValueError):
                    self.owned.release_observer()
                killed.assert_not_called()
            self.assertIsNotNone(self.owned.bound_main.fd)
        with patch.object(self.owned, 'observe', side_effect=ValueError('unknown identity')), patch.object(guards.os, 'kill') as killed:
            with self.assertRaisesRegex(ValueError, 'unknown identity'):
                self.owned.release_observer()
            killed.assert_not_called()
        self.observed = None
        h = self.host(unresolved=True)
        with patch.object(Path, 'home', return_value=self.root / 'fresh-home'):
            with self.assertRaisesRegex(ValueError, 'descendant remains'):
                h.close(False)
        row = json.loads((h.evidence / 'teardown.json').read_bytes())
        self.assertEqual(row['observerRelease']['status'], 'refused-unresolved-drain')
        self.assertIsNotNone(self.owned.bound_main.fd)
        self.assertIn(os.getpid(), row['failureProfileConsumers']['paths'][0]['PIDs'])

    def test_old_replaced_or_unlinked_main_releases_hash_bound_original_only(self):
        self.main.unlink()
        self.shim.unlink()
        self.main.write_bytes(b'new inert Q name; old observer still holds S')
        self.main.chmod(0o700)
        self.observed = None
        result = self.owned.release_observer()
        self.assertEqual(result['nlink'], 0)
        self.assertTrue(result['closed'])
        self.assertNotEqual(result['ino'], self.main.stat().st_ino)
        self.owned.close_observer()  # fallback is idempotent

    def test_release_and_admission_receipts_survive_small_snapshot(self):
        self.observed = None
        released = self.owned.release_observer()
        self.journal.receipt('observer-release.json', {'observers': [released]})
        self.journal.receipt('model-admission.json', admission.retain_admission(GOOD))
        self.journal.receipt('seed-overlay.json', {'synthetic': True})
        source = self.root / 'snapshot-source'
        episode = source / 'stateful/failed-launch-recovery'
        episode.mkdir(parents=True)
        os.rename(self.journal.root, episode / 'diagnostics')
        value = snapshot.freeze(source, self.root / 'frozen')
        for name in ('observer-release.json', 'model-admission.json', 'seed-overlay.json'):
            self.assertEqual(value['entries']['stateful/failed-launch-recovery/diagnostics/' + name]['status'], 'frozen-original-bytes')


if __name__ == '__main__':
    unittest.main()
