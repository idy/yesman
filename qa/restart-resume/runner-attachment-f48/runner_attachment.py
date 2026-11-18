"""t199 attachment to the fixed production-upgrade runner; import is inert.

Live execution needs actual hashed package/gate/handoff receipts and a fresh
hosted ARM Mac. Pure tests use ordinary dictionaries and scripted observations.
"""
from __future__ import annotations
import collections
import contextlib
import datetime
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import re
import select
import shutil
import signal
import subprocess
import sys
import tarfile
import time
import uuid

QA = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(QA))
from guards import Owned, ProviderLedger, identity
import public_npm_cache
import provenance
import image_proof
import process_proof
import native_diagnostics
import fixture_admission

HEAD = 'cfaaeec242a7d372669b5e39739b71bbd15ad646'
TREE = 'c7b0c06488b613c56dd0e52ceba98e343a4e0369'
PRIOR = {
    'candidate-adapter-f48-repair': '3395b1998e1808ecd7c9d9934064f999d4045de1b46f79a4a4363ade6673561a',
    'candidate-adapter-main048': 'b3ad4d7d677f1cbe9c8fed391d92735ba43b0ff2874154ea0a47fc171575c3da',
    'qa-fixture-prep': '4f36fd03d62ca6b48e1fd198e238d6b1866f33737cebc415c51201f38d3deb24',
}
NOTE_EXIT, NOTE_EXEC, NOTE_EXITSTATUS = 0x80000000, 0x20000000, 0x04000000


def need(ok, why):
    if not ok:
        raise ValueError(why)


def sha(data):
    return hashlib.sha256(data).hexdigest()


def read(path, limit=4 << 20):
    path = Path(path)
    need(path.is_file() and not path.is_symlink() and path.stat().st_size <= limit, 'missing/oversized input: ' + str(path))
    value = path.read_bytes()
    need(len(value) <= limit, 'input grew beyond budget')
    return value


def obj(path):
    return json.loads(read(path))


def save(path, value):
    Path(path).write_text(json.dumps(value, indent=2, default=str) + '\n')


def receipt(binding):
    need(isinstance(binding, dict) and binding.get('path') and binding.get('sha256'), 'missing verification receipt binding')
    raw = read(binding['path'])
    need(sha(raw) == binding['sha256'], 'verification receipt digest mismatch')
    return json.loads(raw)


def module(name, path):
    spec = importlib.util.spec_from_file_location(name, path)
    value = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(value)
    return value


def dependencies(evidence_root):
    """Load preserved implementations by their immutable packet identities."""
    root = Path(evidence_root).resolve()
    manifests = {}
    for folder, expected in PRIOR.items():
        raw = read(root / folder / 'manifest.json')
        need(sha(raw) == expected, 'prior packet seal changed: ' + folder)
        rows = json.loads(raw); manifests[folder] = rows
        for name, r in rows.items():
            b = read(root / folder / name)
            need(len(b) == r['bytes'] and sha(b) == r['sha256'], 'prior payload changed: ' + name)
    entry = root / 'candidate-adapter-f48-repair/adapter.py'
    need('adapter' not in sys.modules, 'start in a fresh Python interpreter for t199 imports')
    adapter = module('adapter', entry); sys.modules['adapter'] = adapter
    fixture, core = adapter.load_prototype()
    host = module('t199_host_proof', root / 'candidate-adapter-main048/host_proof.py')
    return adapter, fixture, core, host


def tree_hashes(root):
    root = Path(root); rows = {}
    for p in sorted(root.rglob('*')):
        if p.is_dir():
            continue
        need(not p.is_symlink() and len(rows) < 1000, 'unexpected web input')
        rows[p.relative_to(root).as_posix()] = sha(read(p, 8 << 20))
    need(rows.get('index.html') and rows.get('app.js'), 'production web input missing')
    return rows


def preflight(contract, plan, source_root, bindings, plan_path, run_id, execution=None):
    """Before CA, socket, profile, compiler or app effects. Receipts are supplied
    through the Lead handoff; hashes bind them, they are not attestations."""
    need(contract.get('schema') == 1 and contract.get('candidate') == HEAD and contract.get('candidateTree') == TREE, 'wrong candidate contract')
    need(contract.get('target') == plan.get('Target') == 'darwin-arm64', 'one ARM64 target only')
    need(contract.get('scenarios') == ['update', 'failed-launch-recovery'], 'agreed two episode scope only')
    need(plan.get('kind') == 'released-candidate-plus-private-QA' and len(plan.get('Stages', [])) == 2, 'exact S/private-Q package pair required')
    from urllib.parse import urlsplit
    feed = urlsplit(plan.get('Feed', ''))
    need(feed.scheme == 'http' and feed.hostname == '127.0.0.1' and feed.port and not feed.username and not feed.password and not feed.query and not feed.fragment and feed.path in ('', '/'), 'owned loopback transport input required')
    need(sha(read(plan_path)) == contract.get('packagePlanSHA256'), 'package plan binding missing/changed')
    gates = receipt(contract.get('sourceGates'))
    need(gates.get('candidate') == HEAD and gates.get('tree') == TREE and gates.get('sourceAccepted') is True and gates.get('currentGatesAccepted') is True, 'independent exact-head source/current gates not accepted')
    authority = receipt(contract.get('hostedHandoff'))
    authority_run = str(execution['producer']['runID']) if execution else run_id
    need(authority.get('task') == 't199' and authority.get('candidate') == HEAD and authority.get('tree') == TREE and authority.get('authorized') is True and authority.get('runID') == authority_run and authority.get('scenarios') == contract['scenarios'], 'specific hosted handoff missing/wrong run')
    need(plan.get('sourceSHA') == contract.get('releaseSourceCommit'), 'actual S release source missing/mismatched')
    if execution is None:
        provenance.verify_contract(contract, plan, plan_path, run_id)
    else:
        import consume_r5
        consume_r5.verify_original_contract(contract, plan, plan_path, run_id, execution)
    public_npm_cache.verify(contract['publicNpmCache'], read(QA / '../../internal/piruntime/assets/package-lock.json'))
    need(plan.get('PublicKey') == bindings['mygoConfig']['updates']['publicKey'], 'compiled updater public key mismatch')
    need(contract.get('helperLiteralSHA256') == bindings['mygoUpdater']['helperLiteralSHA256'], 'helper changed')
    need(contract.get('runtimeContentKey') == '498b685a8d6ccde6', 'runtime content key changed')
    need(contract.get('extensionSHA256') == bindings['files']['pi/yesman.ts']['sha256'], 'production extension changed')
    need(contract.get('runtimeSHA256') == bindings['runtimeSourceSHA256'], 'runtime artifact receipt missing/mismatched')
    root = Path(source_root)
    for name, r in bindings['files'].items():
        if name == 'scripts/check-production-upgrade.py':
            continue  # exact patched image is checked in run() before preflight
        need(sha(read(root / name)) == r['sha256'], 'fixed runner dependency changed: ' + name)
    need({p.name for p in (root / 'tools/package').glob('*.go') if not p.name.endswith('_test.go')} == set(bindings['goRunFiles']), 'package verifier Go source boundary changed')
    stages = []
    for key, s in zip(('S', 'Q'), plan['Stages']):
        bound = contract.get('artifacts', {}).get(key)
        need(isinstance(bound, dict), 'actual ' + key + ' artifact binding unset')
        verified = receipt(bound.get('verificationReceipt'))
        for field in ('version', 'sourceCommit', 'archiveSHA256', 'mainSHA256', 'bundleInventorySHA256', 'webSHA256'):
            need(bound.get(field) and bound[field] == verified.get(field), 'unverified artifact field: ' + key + '/' + field)
        for field in ('archiveSHA256', 'mainSHA256', 'bundleInventorySHA256', 'webSHA256'):
            need(isinstance(bound[field], str) and re.fullmatch('[a-f0-9]{64}', bound[field]), 'invalid artifact digest: ' + field)
        id_fields = ('releaseID', 'archiveAssetID') if key == 'S' else ('producerRunID', 'transferArtifactID')
        for field in id_fields:
            need(isinstance(bound.get(field), int) and not isinstance(bound[field], bool) and bound[field] > 0 and bound[field] == verified.get(field), 'actual artifact IDs missing/mismatched')
        need(verified.get('passed') is True and verified.get('candidate') == HEAD and verified.get('candidateTree') == TREE and verified.get('runtimeSHA256') == contract['runtimeSHA256'] and verified.get('extensionSHA256') == contract['extensionSHA256'] and verified.get('updaterPublicKey') == plan['PublicKey'], 'package provenance/runtime/key not verified')
        need(s.get('mainName') == 'Yes Man' and s.get('Version') == bound['version'] and s.get('Source') == bound['sourceCommit'] and s.get('binarySHA256') == bound['mainSHA256'], 'package plan/stage identity mismatch')
        need(sha(read(s['Archive'], 512 << 20)) == bound['archiveSHA256'], 'archive bytes changed')
        need(re.fullmatch('[a-f0-9]{40}', bound['sourceCommit']), 'full immutable artifact source commit required')
        web_rows = tree_hashes(s['Web'])
        need(web_rows == bindings['webFiles'] and sha(json.dumps(web_rows, sort_keys=True).encode()) == bound['webSHA256'], 'fixed production web source bytes changed')
        need(verified.get('sourceFilesVerified') is True, 'both package sources must be verified')
        need(bound.get('manifestSHA256') == verified.get('manifestSHA256') == sha(read(s['Manifest'])), 'original manifest bytes changed')
        manifest = obj(s['Manifest'])
        need(manifest.get('version') == bound['version'] and manifest.get('size') == Path(s['Archive']).stat().st_size and manifest.get('signature'), 'manifest version/size/signature missing')
        stages.append(dict(bound, candidate=HEAD, candidateTree=TREE, verificationReceipt=bound['verificationReceipt']['sha256'], official=key == 'S', privateQA=key == 'Q', sourceFilesVerified=verified.get('sourceFilesVerified') is True))
    need(stages[0]['sourceCommit'] == stages[1]['sourceCommit'] == contract['releaseSourceCommit'] and plan['Stages'][0].get('official') is True and plan['Stages'][1].get('official') is False, 'official/private stage source distinction missing')
    need(stages[1]['sourceFilesVerified'] and verified.get('privateQA') is True, 'private Q source verification missing')
    need(stages[0]['archiveSHA256'] != stages[1]['archiveSHA256'] and stages[0]['mainSHA256'] != stages[1]['mainSHA256'], 'S and Q images are aliased')
    need(all(re.fullmatch(r'0\.1\.\d+', s['version']) for s in stages), 'invalid package version')
    versions = [tuple(map(int, s['version'].split('.'))) for s in stages]
    need(versions[1] > versions[0], 'private Q must be a successor')
    return stages


def capture_runtime(adapter, data, bindings, expected):
    """Read only this synthetic profile's actual managed installation. The
    aggregate binds six shipped assets; ready/after hashes bind installed code.
    No import/execute/probe of pi, npm, config or credentials is performed."""
    root = Path(data) / '.runtime/pi-managed/1.5.2-yesman.5-498b685a8d6ccde6'
    raw, hashes = {}, {}
    for name in bindings['runtimeAssetNames']:
        path = 'internal/piruntime/assets/' + name
        raw[name] = adapter.read_owned(data, root / name, 4 << 20)
        hashes[path] = sha(raw[name])
        need(hashes[path] == bindings['files'][path]['sha256'], 'installed runtime asset differs: ' + name)
    need(sha(json.dumps(hashes, sort_keys=True).encode()) == expected, 'installed runtime aggregate differs')
    ready_raw = adapter.read_owned(data, root / 'ready.json', 64 << 10)
    ready = adapter.decode(ready_raw)
    need(ready == {'version': '1.5.2-yesman.5', 'lock': sha(raw['package-lock.json']), 'patch': sha(raw['patch.json']), 'queue': sha(raw['pending-queue.js'])}, 'installed ready receipt mismatch')
    patched = {}
    for row in adapter.decode(raw['patch.json'])['patches']:
        path = Path(row['path'])
        need(not path.is_absolute() and '..' not in path.parts and row['path'] not in patched, 'invalid patched runtime path')
        patched[row['path']] = sha(adapter.read_owned(data, root / 'node_modules' / path, 16 << 20))
        need(patched[row['path']] == row['after'], 'installed patched runtime differs: ' + row['path'])
    queue_path = root / 'node_modules/@earendil-works/pi-agent-core/dist/pending-queue.js'
    need(sha(adapter.read_owned(data, queue_path, 4 << 20)) == sha(raw['pending-queue.js']), 'installed runtime queue differs')
    return {'root': str(root), 'assetHashes': hashes, 'runtimeSHA256': expected, 'ready': ready, 'readySHA256': sha(ready_raw), 'patchedFiles': patched, 'nativeAcceptance': False}


def inject_page(original, driver):
    need(original.count('<head>') == 1 and '</script>' not in driver.lower(), 'unexpected production page/driver')
    injected = '<script>' + driver + '</script>'
    page = original.replace('<head>', '<head>' + injected, 1)
    need(page.replace(injected, '', 1) == original, 'normal production page changed')
    return page


def helper_rows(raw, old_pid, install, staged):
    matches = list(re.finditer(r'^\s*(\d+)\s+(\d+)\s+([^\n]*)', raw, re.M)); result = []
    work = str(Path(staged).parent.parent)
    for i, m in enumerate(matches):
        command = raw[m.start(3):matches[i + 1].start() if i + 1 < len(matches) else len(raw)].strip()
        if int(m[2]) == old_pid and 'mygo-update ' + str(old_pid) + ' ' in command and all(str(x) in command for x in (Path(install) / 'yesman.app', staged, work)):
            result.append({'pid': int(m[1]), 'ppid': int(m[2]), 'command': command, 'work': work})
    need(len(result) == 1, 'cannot identify exactly one owned staged helper')
    return result[0]


def helper_failure(events, pid):
    own = [e for e in events if e['pid'] == pid]
    exits = [e for e in own if e['fflags'] & NOTE_EXIT]
    execs = [i for i, e in enumerate(own) if e['fflags'] & NOTE_EXEC]
    need(execs and len(exits) == 1 and own[-1] == exits[0] and execs[0] <= len(own) - 1, 'helper exec-before-exit not observed')
    # kqueue may coalesce NOTE_EXEC and NOTE_EXIT in one delivery. The fixed
    # helper's exec is then evidenced without claiming separate timestamps.
    event = exits[0]
    need(event['fflags'] & NOTE_EXITSTATUS and os.WIFEXITED(event['data']), 'helper normal exit status unavailable')
    code = os.WEXITSTATUS(event['data'])
    need(code != 0, 'helper launch did not report a failure')
    return {'pid': pid, 'waitStatus': event['data'], 'exitCode': code, 'kernelExecThenFailedExit': True,
            'stderrCaptured': False, 'mechanism': 'fixed helper exec /usr/bin/open, then kernel-observed nonzero normal exit'}


class HelperExit:
    """Arm while the helper still waits for S. Never infer status from PID loss.
    Native registration/event behavior remains an actual hosted binding point."""
    def __init__(self, identity):
        self.identity, self.events = identity, []
        self.queue = select.kqueue()
        event = select.kevent(identity['pid'], filter=select.KQ_FILTER_PROC,
                              flags=select.KQ_EV_ADD | select.KQ_EV_CLEAR,
                              fflags=NOTE_EXEC | NOTE_EXIT | NOTE_EXITSTATUS)
        self.queue.control([event], 0, 0)
    def poll(self):
        for e in self.queue.control([], 8, 0):
            need(not e.flags & select.KQ_EV_ERROR, 'helper process observation failed')
            self.events.append({'pid': int(e.ident), 'fflags': int(e.fflags), 'data': int(e.data), 'at': time.time()})
        return any(e['fflags'] & NOTE_EXIT for e in self.events)
    def close(self):
        self.queue.close()


class Deadline:
    def __init__(self, health, clock=time.monotonic, pause=time.sleep):
        self.health, self.clock, self.pause = health, clock, pause
    def wait(self, name, predicate, seconds=15):
        end = self.clock() + seconds
        while True:
            self.health()
            value = predicate()
            if value:
                return value
            if self.clock() >= end:
                raise TimeoutError(name)
            self.pause(.1)


def ordinary_exit(code):
    need(code is None or code == 0, 'production child exited abnormally: ' + str(code))
    return {'exitCode': code, 'exitStatusAvailable': code is not None}


def sequence(host, failed):
    """One ordered episode. Each mutating method is called once; errors escape
    to the outer snapshot/teardown, never restart this sequence or resend."""
    host.start()
    host.prepare_update()  # registers owned helper before any later fixture work
    host.seed()
    host.capture_before()
    if failed:
        host.arm()
    host.restart_once()
    host.old_exit()
    if failed:
        host.capture_failed_launch()
        host.restore_and_verify()
        host.explicit_recovery_launch()
    host.bind_Q()
    host.settle_restart()
    host.verify_resumption()
    host.ordinary_quit()
    host.reopen()
    host.verify_no_replay()


class Host:
    def __init__(self, runner, adapter, fixture, core, collectors, plan, contract, stages, root, evidence, tls, helper, bindings, failed):
        self.runner, self.a, self.f, self.core, self.h = runner, adapter, fixture, core, collectors
        self.plan, self.contract, self.stages = plan, contract, stages
        self.root, self.evidence, self.tls, self.helper, self.failed = root, evidence, tls, helper, failed
        self.bindings = bindings
        self.install = root / 'Application install with spaces'; self.install.mkdir()
        self.web = root / 'active web'; self.web.mkdir()
        self.temp = root / 'app temp'; self.temp.mkdir()
        self.bundle = self.install / 'yesman.app'; self.main = self.bundle / 'Contents/MacOS/Yes Man'
        self.data, self.config = Path.home() / 'yesman', Path.home() / '.pi/agent'
        self.process_observer = process_proof.Processes(self.data, observe=self.observe_owned)
        self.process = self.controller = self.feed = self.server = self.watcher = None
        self.expect_exit = False; self.command_results = {}; self.events = collections.deque(maxlen=4000)
        self.submissions = {}; self.before = {}; self.old_page = None; self.arm_plan = None; self.final = None
        self.bound = {}; self.owned_profile = False; self.ledger = ProviderLedger()
        self.staging_attempted = False; self.helper_ownership = None
        self.image_index = 0
        self.image_diagnostics = image_proof.Diagnostics(evidence / 'image-diagnostics.json')
        self.phase = 'initialization'
        self.diagnostic_owners = []
        self.diagnostics = native_diagnostics.Journal(evidence / 'diagnostics')
        self.timer = Deadline(self.health)
        self.log = (evidence / 'app.log').open('ab', buffering=0)
    def note(self, kind, **fields):
        with (self.evidence / 'attachment.jsonl').open('a') as f:
            f.write(json.dumps({'at': time.time(), 'kind': kind, **fields}, default=str) + '\n')
    def health(self):
        self.process_observer.poll()
        if self.watcher:
            self.watcher.poll()
        need(not self.controller or not self.controller.failed, 'terminal controller/provider/page error')
        if self.process and not self.expect_exit:
            need(self.process.poll() is None, 'app exited before the current observation completed')
    def image(self, stage):
        inventory = image_proof.bundle_inventory(self.a, self.install, self.bundle, stage['mainSHA256'], 'image', self.image_diagnostics, alias_data=self.data)
        need(inventory['sha256'] == stage['bundleInventorySHA256'], 'installed bundle inventory differs from verified artifact')
        self.checked(['codesign', '--verify', '--deep', '--strict', str(self.bundle)], 'codesign')
        return inventory
    def checked(self, argv, label, seconds=30, env=None):
        self.diagnostics.emit('subprocess', self.phase, operation=label, status='started')
        try:
            value = subprocess.run(argv, capture_output=True, timeout=seconds, env=env)
        except subprocess.TimeoutExpired as e:
            self.diagnostics.emit('subprocess', self.phase, operation=label, status='timeout', exitCode=None)
            self.note('command-timeout', label=label, stdout=(e.stdout or b'')[-4096:].decode(errors='replace'), stderr=(e.stderr or b'')[-4096:].decode(errors='replace'))
            raise
        self.diagnostics.emit('subprocess', self.phase, operation=label, status='completed', exitCode=value.returncode)
        self.note('external-command', label=label, exit=value.returncode, stdout=value.stdout[-4096:].decode(errors='replace'), stderr=value.stderr[-4096:].decode(errors='replace'))
        need(value.returncode == 0, label + ' failed')
        return value
    def switch_web(self, index):
        for p in list(self.web.iterdir()):
            shutil.rmtree(p) if p.is_dir() else p.unlink()
        shutil.copytree(self.plan['Stages'][index]['Web'], self.web, dirs_exist_ok=True)
        path = self.web / 'index.html'; original = path.read_text()
        driver = self.f.driver_text(self.endpoints['driverEndpoint'])
        # Readonly Crew is projected in page memory; original mutation set,
        # command journal and terminal/no-retry behavior stay sealed.
        driver = fixture_admission.driver(driver)
        path.write_text(inject_page(original, driver))
        self.note('normal-page-plus-observer', stage=index, originalSHA256=sha(original.encode()), driverSHA256=sha(driver.encode()))
    def launch(self, index):
        self.phase = 'launch'
        self.image_index = index
        self.switch_web(index)
        self.env = self.core.app_environment(os.environ, self.data, self.config, self.web, self.temp, self.plan['Feed'])
        self.env.update(NPM_CONFIG_USERCONFIG=str(self.data / '.runtime/pi-managed/npm-public-cache.ini'), NPM_CONFIG_GLOBALCONFIG=os.devnull, NPM_CONFIG_REGISTRY='https://registry.npmjs.org')
        child = subprocess.Popen([str(self.main)], cwd=self.root, env=self.env, stdin=subprocess.DEVNULL, stdout=self.log, stderr=self.log)
        self.process = self.owned(child.pid, child=child)
        self.process_observer.register(self.process)
        self.expect_exit = False
        self.note('explicit-verified-main-launch', stage=index, pid=self.process.pid)
    def observe_owned(self, pid):
        # Use the same bound alias observation for registered main roots.
        # Other descendants retain raw kernel identity and no signalling.
        for owned in self.diagnostic_owners:
            if owned.pid == pid:
                return owned.observe(pid)
        return identity(pid)
    def owned(self, pid, child=None):
        value = native_diagnostics.DiagnosticOwned(pid, self.main, child=child,
            journal=self.diagnostics, phase=lambda: self.phase,
            role='initial-S-main' if self.image_index == 0 else 'successor-Q-main',
            shim=self.data / '.runtime/github-app-gh/bin/gh',
            expected_sha256=self.stages[self.image_index]['mainSHA256'])
        self.diagnostic_owners.append(value)
        return value
    def start(self):
        c = self.contract; scenario = 'failed-launch-recovery' if self.failed else 'update'
        runtime = {'schema': 1, 'run': uuid.uuid4().hex, 'target': 'darwin-arm64', 'scenario': scenario,
                   'source': HEAD, 'tree': TREE, 'helperLiteralSHA256': c['helperLiteralSHA256'],
                   'runtimeSHA256': c['runtimeSHA256'], 'extensionSHA256': c['extensionSHA256'],
                   'eligible': ['active/lead'], 'paused': ['paused/lead']}
        for key, stage in zip(('S', 'Q'), self.stages):
            runtime[key] = dict(stage, source=HEAD, tree=TREE, evidence=stage['verificationReceipt'])
        self.f.validate_contract(runtime); self.runtime = runtime
        host = self
        class Observed(self.core.Controller):
            def note(self, kind, **fields):
                with self.lock:
                    row = super().note(kind, **fields)
                    host.ledger.record(row)
                    host.events.append(row)
                    return row
            def ack(self, kind, page, ident, result=None):
                super().ack(kind, page, ident, result)
                if kind == 'done':
                    host.command_results[ident] = result
        self.controller = Observed(runtime, self.evidence / 'controller.jsonl')
        self.endpoints = self.controller.start()
        seed, seed_receipt = fixture_admission.fresh_seed(self.a.seed_plan(Path.home(), self.endpoints['providerBase']))
        self.owned_profile = True  # fresh guard precedes the first write; retain partial seed on failure
        self.a.write_fresh_seed(seed)
        seed_receipt['writtenSettingsSHA256'] = sha(self.a.read_owned(self.data, self.data / 'settings.json', 1 << 20))
        need(seed_receipt['writtenSettingsSHA256'] == seed_receipt['settingsSHA256'], 'fresh fixture settings write differs')
        self.diagnostics.receipt('seed-overlay.json', seed_receipt)
        self.note('public-npm-cache-bootstrap', receipt=public_npm_cache.bootstrap(c['publicNpmCache'], self.data, read(QA / '../../internal/piruntime/assets/package-lock.json')))
        self.feed = self.runner.Feed(self.plan, 'archive'); self.feed.target = 1
        from urllib.parse import urlsplit
        url = urlsplit(self.plan['Feed']); need(url.hostname == '127.0.0.1' and url.port, 'owned loopback feed required')
        self.server = self.feed.serve(url.hostname, url.port, self.tls)
        with tarfile.open(self.plan['Stages'][0]['Archive']) as tar:
            tar.extractall(self.install, filter='data')
        self.image(self.stages[0]); self.launch(0)
        self.phase = 'initial-inspect'
        self.bound['before'] = self.inspect(0, initial=True)
        self.controller.select_page(self.page)
        self.wait_runtime()
        self.admit_model()
        self.phase = 'actors'
        for name in ('active', 'paused'):
            hired = self.command('HireNamed', [name]); need(hired['name'] == name and hired['hiredAt'], 'unexpected synthetic hire')
            self.submissions[name] = self.a.submit_command(name + '/lead', hired['hiredAt'], runtime['run'], int(time.time() * 1000), uuid.uuid4().hex + uuid.uuid4().hex[:4])
    def inspect(self, index, initial=False):
        stage = self.stages[index]
        def observation():
            with self.controller.lock:
                rows = list(self.controller.reports.items())
            found = [(page, v) for page, v in rows if v['about'].get('version') == stage['version'] and page != self.old_page and v.get('pageReady') and v.get('visibility') == 'visible' and (initial or v['about'].get('piState') == 'ready')]
            return found[-1] if found else None
        self.page, value = self.timer.wait('normal production page/bridge not observed', observation, 30)
        phase = 'inspect-initial' if initial else 'inspect-ready'
        native = self.timer.wait('exact main image not observed', lambda: image_proof.collect_native(self.h, self.install, self.bundle, stage['mainSHA256'], phase, self.image_diagnostics, alias_data=self.data) if self.runner.find(self.main) else None, 20)
        info = self.runner.naming.native(native['pid'], self.helper)
        need(info['processName'] == 'Yes Man' and any(w['onScreen'] and w['bounds'].get('Width', 0) > 100 for w in info['windows']), 'native window/name missing')
        bound = self.a.bind_bridge(value, native, self.data, stage, epoch='initial' if initial else 'resumed')
        self.note('bound-native-page', page=self.page, native=info, binding=bound, observation=value)
        return bound
    def wait_runtime(self):
        self.phase = 'runtime-ready'
        self.diagnostics.emit('phase', self.phase, status='entered')
        # Observe ordinary startup to ready before any fixture actor/mutation.
        # Cache bootstrap is not production installer/readiness proof.
        def ready():
            with self.controller.lock:
                about = self.controller.reports[self.page]['about']
                need(not about.get('piError') and about.get('piState') != 'failed', 'managed runtime preparation failed')
                return about.get('piState') == 'ready'
        self.timer.wait('offline production managed runtime not ready', ready, 60)
        self.note('managed-runtime-ready', receipt=capture_runtime(self.a, self.data, self.bindings, self.contract['runtimeSHA256']))
    def admit_model(self):
        self.phase = 'model-admission'
        with self.controller.lock:
            projected = self.controller.reports[self.page].get('modelAdmission')
        value = fixture_admission.retain_admission(projected)
        self.diagnostics.receipt('model-admission.json', value)
        need(value['admitted'], 'synthetic Crew model admission refused before Hire')
    def command(self, method, args):
        ident = self.controller.issue(method, args)
        self.timer.wait('mutation outcome unknown: ' + method, lambda: ident in self.command_results, 15)
        return self.command_results[ident]
    def prepare_update(self):
        self.phase = 'update'
        self.staging_attempted = True
        self.command('CheckForUpdates', [])
        def ready():
            with self.controller.lock:
                v = self.controller.reports[self.controller.page]['update']
            need(v.get('state') != 'failed', 'production updater failed')
            return v if v.get('state') == 'ready' and v.get('version') == self.stages[1]['version'] else None
        self.timer.wait('production update did not reach Ready', ready, 120)
        paths = list(self.install.glob('.yesman.app.update-*/app/yesman.app'))
        need(len(paths) == 1, 'unique staged Q missing'); self.staged = paths[0]
        self.watch_helper()
        need(self.h.bundle_inventory(self.staged)['sha256'] == self.stages[1]['bundleInventorySHA256'], 'staged Q inventory mismatch')
        self.checked(['codesign', '--verify', '--deep', '--strict', str(self.staged)], 'staged-codesign')
    def provider(self, actor, action):
        with self.ledger.lock:
            return [e for e in self.ledger.requests if e['agent'] == actor and e['action'] == action]
    def seed(self):
        self.phase = 'seed'
        for name in ('paused', 'active'):
            self.command('Submit', self.submissions[name]['args'])
            self.timer.wait('real managed-pi held request missing', lambda: self.provider(name + '/lead', 'hold'))
            if name == 'paused':
                self.command('Stop', ['paused/lead'])
                self.timer.wait('paused held stream did not cancel', lambda: any(r['agent'] == 'paused/lead' and self.ledger.snapshot()['terminals'].get(r['id']) == 'provider-disconnected' for r in self.ledger.snapshot()['requests']))
                self.timer.wait('durable pause missing', lambda: self.a.capture_actor(self.data, 'paused', self.runtime['run'])['leadPausedAt'])
        self.timer.wait('eligible lead is not Working', lambda: any(a['id'] == 'active/lead' and a['status'] == 'working' for a in self.controller.reports[self.controller.page]['state']['agents']))
    def capture(self, label, failed=False):
        proof = image_proof.capture_phase(self.h, self.a, self.f, self.install, self.data, self.bundle, self.runtime['run'], self.evidence / label, self.stages[self.image_index]['mainSHA256'], 'capture-' + label, self.image_diagnostics, failed_launch=failed)
        save(self.evidence / label / 'runtime.json', capture_runtime(self.a, self.data, self.bindings, self.contract['runtimeSHA256']))
        return proof
    def capture_before(self):
        self.phase = 'capture'
        self.bound['before'] = self.inspect(0)  # real managed pi is ready after the seed
        self.capture('before')
        self.before = {name: self.a.capture_actor(self.data, name, self.runtime['run']) for name in ('active', 'paused')}
    def watch_helper(self):
        # The complete host ps output is only an in-memory input; retain the
        # single exact owned helper row, never unrelated process arguments.
        value = subprocess.run(['/bin/ps', '-ww', '-axo', 'pid=,ppid=,command='], capture_output=True, timeout=5, check=True)
        identity = helper_rows(value.stdout.decode(), self.bound['before']['pid'], self.install, self.staged)
        self.helper_ownership = identity
        self.watcher = HelperExit(identity); self.note('helper-observer-armed', identity=identity)
    def arm(self):
        self.arm_plan = self.f.obstruction_plan(self.install, self.staged, self.runtime['run'], self.stages[1]['mainSHA256'])
        self.note('launch-obstruction', receipt=self.f.arm(self.arm_plan), plan=self.arm_plan)
    def restart_once(self):
        self.phase = 'restart'
        self.old_page = self.controller.page; self.switch_web(1)
        self.restart_id = self.controller.issue('RestartToUpdate', [])
        self.expect_exit = True; self.note('restart-issued-once', id=self.restart_id)
    def old_exit(self):
        result = self.process.wait(timeout=30)
        self.note('old-main-exit', **ordinary_exit(result))
        self.image_index = 1
    def capture_failed_launch(self):
        self.timer.wait('helper exit not observed', self.watcher.poll, 30)
        event = helper_failure(self.watcher.events, self.watcher.identity['pid'])
        need(not self.main.exists() and not self.runner.find(self.main) and not Path(self.watcher.identity['work']).exists(), 'failed installed-image state not established')
        with self.controller.lock:
            need(not any(v['about'].get('version') == self.stages[1]['version'] for v in self.controller.reports.values()), 'Q page appeared before recovery')
        self.final = self.a.capture_restart(self.data)
        need(self.final['names'] == ['intent.json'] and not self.final['claimedNames'], 'final intent missing/already claimed')
        value = self.final['files']['intent.json']['value']
        need(value['version'] == 3 and value['phase'] == 'final' and value['kind'] == 'update' and value['fromVersion'] == self.stages[0]['version'] and value['toVersion'] == self.stages[1]['version'], 'wrong final intent')
        self.capture('failed-launch', failed=True)
        self.note('failed-launch-established', helper=event, kernelEvents=self.watcher.events, finalSHA256=self.final['files']['intent.json']['sha256'])
    def restore_and_verify(self):
        current = self.a.capture_restart(self.data)
        need(current['names'] == self.final['names'] and current['files']['intent.json']['sha256'] == self.final['files']['intent.json']['sha256'], 'unclaimed intent changed')
        created = datetime.datetime.fromisoformat(current['files']['intent.json']['value']['createdAt'].replace('Z', '+00:00'))
        age = (datetime.datetime.now(datetime.timezone.utc) - created).total_seconds()
        need(0 <= age < 900, 'recovery intent expired')
        self.note('restored-main', receipt=self.f.recover(self.arm_plan, self.bundle), recoveryAgeSeconds=age)
        self.image(self.stages[1])
    def explicit_recovery_launch(self):
        self.launch(1)
    def bind_Q(self):
        self.bound['after'] = self.inspect(1)
        self.image(self.stages[1])
        if not self.failed:
            self.timer.wait('successful helper exit missing', self.watcher.poll, 10)
            exits = [e for e in self.watcher.events if e['fflags'] & NOTE_EXIT]
            need(any(e['fflags'] & NOTE_EXEC for e in self.watcher.events) and len(exits) == 1 and
                 exits[0]['fflags'] & NOTE_EXITSTATUS and os.WIFEXITED(exits[0]['data']) and
                 os.WEXITSTATUS(exits[0]['data']) == 0, 'helper did not finish its ordinary relaunch successfully')
            self.note('successful-helper-exit', events=self.watcher.events)
    def settle_restart(self):
        need(self.process.poll() is not None or self.process.pid == self.bound['after']['pid'], 'unexpected prior process identity')
        if self.controller.pending is not None:
            self.controller.confirm_restart_departure(self.restart_id)
        self.controller.select_page(self.page)
        if self.process.pid != self.bound['after']['pid']:
            self.process = self.owned(self.bound['after']['pid'])
            self.process_observer.register(self.process)
        self.expect_exit = False
    def verify_resumption(self):
        self.phase = 'resumption'
        self.timer.wait('one real provider continuation missing', lambda: len(self.provider('active/lead', 'continue')) == 1)
        self.timer.wait('provider continuation did not finish', lambda: any(e['kind'] == 'provider-continued' and e.get('agent') == 'active/lead' for e in self.events))
        self.timer.wait('recovered lead did not settle', lambda: any(a['id'] == 'active/lead' and a['status'] == 'idle' for a in self.controller.reports[self.page]['state']['agents']))
        self.timer.wait('completed restart result missing', lambda: self.a.capture_restart(self.data)['names'] == ['last.json'])
        self.after = {name: self.a.capture_actor(self.data, name, self.runtime['run']) for name in ('active', 'paused')}
        for name in ('active', 'paused'):
            self.note('actor-delta', actor=name, comparison=self.a.actor_delta(self.before[name], self.after[name], self.submissions[name], name == 'active'))
        self.timer.wait('old held stream did not cancel', lambda: self.ledger.snapshot()['terminals'].get(self.provider('active/lead', 'hold')[0]['id']) == 'provider-disconnected')
        self.provider_snapshot = self.ledger.assert_complete()
        save(self.evidence / 'provider-complete.json', self.provider_snapshot)
        captured = self.a.capture_restart(self.data); last = captured['files']['last.json']['value']
        need(last['kind'] == 'update' and last['fromVersion'] == self.stages[0]['version'] and last['toVersion'] == last['version'] == self.stages[1]['version'] and not last.get('unfinished') and not last.get('rejected'), 'incomplete/wrong update result')
        need([(a['yesman'], a['name'], a['outcome']) for a in last['agents']] == [('active', 'lead', 'resumed')], 'unexpected restart outcomes')
        if self.failed:
            self.note('same-final-result', binding=self.a.bind_final_to_result(self.final, captured))
        self.last = captured
        self.capture('after')
        need(any(r['stage'] == 1 and r['file'] == Path(self.plan['Stages'][1]['Archive']).name for r in self.feed.requests), 'production archive download not observed')
    def ordinary_quit(self):
        self.phase = 'quit'
        self.expect_exit = True
        self.process.send_signal(signal.SIGTERM); result = self.process.wait(timeout=20)
        self.note('ordinary-quit', **ordinary_exit(result)); self.process = None
    def reopen(self):
        self.phase = 'reopen'
        self.old_page = self.page; self.launch(1); self.bound['reopen'] = self.inspect(1)
    def verify_no_replay(self):
        # Bounded quiet observation, after a new normal page has actually loaded.
        end = time.monotonic() + 2
        while time.monotonic() < end:
            self.health(); time.sleep(.1)
        now = self.a.capture_restart(self.data)
        need(now['names'] == ['last.json'] and now['files']['last.json']['sha256'] == self.last['files']['last.json']['sha256'], 'ordinary reopen changed/replayed checkpoint')
        for name in ('active', 'paused'):
            current = self.a.capture_actor(self.data, name, self.runtime['run'])
            # The sealed comparator checks absolute canonical user/origin counts
            # and preserved prefixes, so it is valid again after ordinary open.
            self.a.actor_delta(self.after[name], current, self.submissions[name], name == 'active')
            need(current['session']['users'] == self.after[name]['session']['users'], 'ordinary reopen added a canonical input')
        need(self.ledger.assert_complete() == self.provider_snapshot, 'ordinary reopen added a provider request or terminal')
        save(self.evidence / 'provider-reopen.json', self.ledger.snapshot())
        self.capture('reopen')
        save(self.evidence / 'episode.json', {'contractPassed': True, 'nativeAcceptance': False, 'independentRawEvidenceReviewRequired': True, 'scenario': self.runtime['scenario'], 'bindings': self.bound})
    def snapshot_failure(self, error):
        snapshot = {'error': type(error).__name__ + ': ' + str(error), 'pending': self.controller.pending if self.controller else None,
                    'controllerFailed': self.controller.failed if self.controller else None, 'events': list(self.events), 'providerLedger': self.ledger.snapshot(),
                    'helperEvents': self.watcher.events if self.watcher else None, 'runRootRetained': str(self.root),
                    'nativeReceipt': None, 'actorReceipts': {}, 'launchFailureReceipt': None}
        try:
            snapshot['nativeReceipt'] = image_proof.collect_native(self.h, self.install, self.bundle, self.stages[self.image_index]['mainSHA256'], 'failure', self.image_diagnostics, allow_absent=self.failed and self.expect_exit, alias_data=self.data)
        except Exception as e:
            snapshot['nativeCaptureError'] = str(e)
        for name in ('active', 'paused'):
            try:
                actor = self.a.capture_actor(self.data, name, self.runtime['run'])
                actor['rawHex'] = {k: v.hex() if v is not None else None for k, v in actor.pop('raw').items()}
                snapshot['actorReceipts'][name] = actor
            except Exception as e:
                snapshot['actorReceipts'][name] = {'captureError': str(e)}
        try:
            snapshot['restart'] = self.a.capture_restart(self.data)
            for value in snapshot['restart']['files'].values():
                value['rawHex'] = value.pop('raw').hex()
        except Exception as e:
            snapshot['restartCaptureError'] = str(e)
        self.process_observer.poll(force=True)
        snapshot['processObservation'] = self.process_observer.snapshot()
        save(self.evidence / 'failure.json', snapshot)
        if self.feed:
            self.runner.failure_snapshot(self.evidence / 'production-upgrade-failure.json', self.feed, {}, self.process, None, self.install, lambda _: self.main, self.plan)
    def close(self, success):
        errors = []; owners = []
        # Registration failure must not prevent draining a separately verified
        # main, and must never be reported as a proven helper drain.
        if self.staging_attempted and not self.watcher:
            try:
                paths = list(self.install.glob('.yesman.app.update-*/app/yesman.app'))
                need(len(paths) == 1, 'staged-helper ownership unresolved after update attempt')
                self.staged = paths[0]
                self.watch_helper()
            except Exception as error:
                errors.append('helper ownership unresolved: ' + str(error))
        try:
            if self.process:
                owners.append(self.process.drain())
            if self.watcher:
                end = time.monotonic() + 30
                while not self.watcher.poll() and time.monotonic() < end:
                    time.sleep(.1)
                need(self.watcher.poll(), 'staged helper unresolved; retain profile/root')
                owners.append({'helper': self.helper_ownership, 'kernelEvents': self.watcher.events, 'drained': True, 'signals': []})
            for pid in self.runner.find(self.main):
                owners.append(self.owned(pid).drain())
            need(not self.runner.find(self.main), 'owned main still running at teardown')
        except Exception as e:
            errors.append('ownership/drain unresolved: ' + str(e))
        self.process_observer.poll(force=True)
        process_observation = self.process_observer.snapshot()
        save(self.evidence / 'process-observations.json', process_observation)
        if process_observation['unresolvedObserved']:
            errors.append('observed owned descendant remains live or unverified')
        try:
            if self.controller:
                self.controller.close()
            if self.server:
                self.server.shutdown(); self.server.server_close()
        except Exception as e:
            errors.append(str(e))
        if self.watcher:
            self.watcher.close()
        self.log.close()
        self.image_diagnostics.close()
        io_observation = {'mainOutputFileClosed': self.log.closed,
                          'controllerThreadJoined': not self.controller or not self.controller.thread or not self.controller.thread.is_alive(),
                          'runtimePipeScope': 'only sampled owned installer/npm descriptor types; not every pipe'}
        if not io_observation['controllerThreadJoined']:
            errors.append('owned controller thread remains unjoined')
        observer_release = {'schema': 1, 'status': 'refused-unresolved-drain', 'observers': []}
        if not errors:
            try:
                for owned in self.diagnostic_owners:
                    observer_release['observers'].append(owned.release_observer())
                observer_release['status'] = 'released-before-full-profile-scan'
            except (OSError, ValueError) as error:
                observer_release['status'] = 'release-refused'
                observer_release['errorCode'] = native_diagnostics.error_code(error)
                errors.append('fixture observer release refused')
        try:
            self.diagnostics.receipt('observer-release.json', observer_release)
        except (OSError, ValueError):
            errors.append('fixture observer release receipt unavailable')
        failure_consumers = None
        if not success:
            failure_consumers = process_proof.profiles((('fixture-data', self.data), ('fixture-pi-config', self.config),
                ('fixture-native-app-data', Path.home() / 'Library/Application Support/yesman'),
                ('fixture-native-preferences', Path.home() / 'Library/Preferences/dev.idy.yesman.plist')))
            if any(row['status'] not in ('absent', 'no-observed-consumer') for row in failure_consumers['paths']):
                errors.append('failed profile consumer remains or observation unknown')
        # On failure retain the actual default fixture profiles for recovery;
        # do not start the second scenario over a failed/partial first one.
        if success and not errors and self.owned_profile:
            try:
                # Go normal shutdown drains its managed children. Independently
                # refuse handover if any profile still has an open consumer.
                profiles = (self.data, self.config, Path.home() / 'Library/Application Support/yesman', Path.home() / 'Library/Preferences/dev.idy.yesman.plist')
                for p in profiles:
                    if not p.exists():
                        continue
                    need(not p.is_symlink(), 'unexpected linked fixture profile')
                    args = ['lsof', '-t', '+D', str(p)] if p.is_dir() else ['lsof', '-t', '--', str(p)]
                    check = subprocess.run(args, capture_output=True, timeout=10)
                    need(check.returncode == 1 and not check.stdout and not check.stderr, 'fixture profile still in use/unknown lsof result')
                retained = self.root / 'retained synthetic profile'; retained.mkdir()
                for i, p in enumerate(profiles):
                    if p.exists():
                        need(not p.is_symlink(), 'unexpected linked fixture profile')
                        os.rename(p, retained / str(i))
            except Exception as e:
                errors.append('profile retention: ' + str(e))
        save(self.evidence / 'teardown.json', {'success': success and not errors, 'errors': errors, 'owners': owners, 'unresolvedOwnership': bool(errors), 'helperOwnership': self.helper_ownership, 'rootRetained': str(self.root), 'lsofScope': 'observed open profile consumers only', 'processObservation': process_observation, 'ioObservation': io_observation, 'failureProfileConsumers': failure_consumers, 'observerRelease': observer_release})
        need(not errors, 'teardown incomplete: ' + '; '.join(errors))


def run(stack, runner, plan_path, evidence_path, contract_path):
    provenance.first_attempt()
    packet = Path(__file__).resolve().parent
    bindings = obj(packet / 'source-binding.json')
    need(sha(read(runner.__file__)) == bindings['patchedRunnerSHA256'], 'runner overlay changed')
    plan_path = Path(plan_path).resolve(); plan = obj(plan_path); plan['_path'] = str(plan_path)
    for s in plan.get('Stages', []):
        for field in ('Archive', 'Manifest', 'Web'):
            if not Path(s[field]).is_absolute():
                s[field] = str(plan_path.parent / s[field])
    contract_path = Path(contract_path).resolve()
    contract = obj(contract_path)
    execution = None
    if contract.get('schema') == 2:
        import consume_r5
        consumer_root = contract_path.parent
        need(contract == {'schema': 2, 'kind': 'fresh-consumer-contract',
             'originalContract': 'original/inputs/contract.json',
             'executionEnvelope': 'consumer-envelope.json',
             'executionEnvelopeSHA256': sha(read(consumer_root / 'consumer-envelope.json'))},
             'consumer contract shape/binding differs')
        need(plan_path == consumer_root / 'original/inputs/plan.json', 'consumer plan path differs')
        execution = consume_r5.envelope(consumer_root)
        contract = obj(consumer_root / contract['originalContract'])
    source_root = Path(runner.__file__).resolve().parent.parent
    stages = preflight(contract, plan, source_root, bindings, plan_path, os.environ.get('GITHUB_RUN_ID'), execution)
    adapter, fixture, core, collectors = dependencies(packet.parent)
    fixture.hosted_arm64(); runner.naming.hosted()
    need(not (Path.home() / '.pi/agent').exists(), 'VM already has pi configuration')
    runner_temp = Path(os.environ['RUNNER_TEMP']).resolve()
    evidence = Path(evidence_path).absolute()
    fixture.plain_path(evidence)
    need(evidence.is_relative_to(runner_temp) and not evidence.exists(), 'new evidence directory inside RUNNER_TEMP required')
    evidence.mkdir(parents=True, exist_ok=False)
    root = runner_temp / ('t199-attachment-' + uuid.uuid4().hex); root.mkdir()
    safe_env = core.app_environment(os.environ, Path.home() / 'yesman', Path.home() / '.pi/agent', root, root, plan['Feed'])
    safe_env.update(GOCACHE=str(root / 'go-cache'), GOPATH=str(root / 'go-path'), GOMODCACHE=str(root / 'go-mod'), GOENV='off', GOTOOLCHAIN='local')
    # Existing public-key-only verifier remains authoritative for archive/delta
    # signatures. It supports any stage count; the historical Python loop was 4.
    try:
        result = subprocess.run([str(Path(contract['publicVerifier']).resolve()), str(plan_path), 'verify'], cwd=source_root, env=safe_env, capture_output=True, timeout=180)
    except subprocess.TimeoutExpired as error:
        (evidence / 'package-verifier.log').write_bytes((error.stdout or b'') + (error.stderr or b''))
        save(evidence / 'package-validation.json', {'completed': False, 'exit': None, 'error': 'package verifier timed out', 'rootRetained': str(root)})
        raise
    (evidence / 'package-verifier.log').write_bytes(result.stdout + result.stderr)
    save(evidence / 'package-validation.json', {'completed': True, 'exit': result.returncode, 'rootRetained': str(root)})
    need(result.returncode == 0, 'production archive/delta verification failed')
    helper = root / 'native-info'
    subprocess.run(['xcrun', 'swiftc', str(source_root / 'scripts/name-process-macos.swift'), '-o', str(helper)], env=safe_env, check=True, timeout=60)
    transport = module('t199_transport', source_root / 'scripts/upgrade-transport.py')
    tls, _, fingerprint = stack.enter_context(transport.trusted_transport())
    save(evidence / 'run-inputs.json', {'contract': contract, 'plan': plan, 'transportFingerprint': fingerprint, 'nativeAcceptance': False})
    for scenario in contract['scenarios']:
        runner.naming.hosted()  # each serial episode starts with absent defaults
        episode_root = root / scenario; episode_root.mkdir()
        episode_out = evidence / scenario; episode_out.mkdir()
        host = Host(runner, adapter, fixture, core, collectors, plan, contract, stages, episode_root, episode_out, tls, helper, bindings, scenario == 'failed-launch-recovery')
        native_diagnostics.execute_episode(host, scenario == 'failed-launch-recovery', sequence)
    save(evidence / 'attachment-result.json', {'contractPassed': True, 'nativeAcceptance': False, 'independentReviewRequired': True, 'candidate': HEAD, 'tree': TREE, 'scenarios': contract['scenarios']})
