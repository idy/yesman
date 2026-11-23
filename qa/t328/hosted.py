#!/usr/bin/env python3
"""Verify the official archive offline, or exercise it on a fresh GitHub-hosted Mac."""
import argparse
from contextlib import contextmanager
from hashlib import sha256
from http.server import BaseHTTPRequestHandler, HTTPServer
import json
import os
from pathlib import Path, PurePosixPath
import re
import shutil
import signal
import stat
import subprocess
import sys
import tarfile
import threading
import time
from uuid import uuid4

from cleanup_diagnostics import (CLEANUP_JOURNAL_STEPS, bulk_preview_matches, checked_path, fixture_journal,
                                 journal_frontier, shim_diagnostics, valid_cleanup_snapshot, validate_cleanup_journal)
from binding import QA_REF, TAG, EPISODES, check_contract, check_inputs, check_source

KIT = Path(__file__).resolve().parent
KEY = 'yrQPn0gg1/gASLxIQiUCIiUQ/csWENI0+up1cs3KWO0='
CALL = b'const call = (method, ...args) => mygo.call(`Yesman.${method}`, ...args);'
OBSERVER_CALL = b'const call = (method, ...args) => window.__t304Call(mygo, method, ...args);'
EXECUTION_FILES = ('hosted.py','observer.js','signature.go','windows.swift','fixtures/cleanup.py',
                   'fixtures/delete_seed.py','fixtures/delete_verify.py','fixtures/links.json','fixtures/seven.py',
                   'cleanup_diagnostics.py','binding.py','fixtures/recovery.py','check_receipts.py')
SCOPE = {'nativeWindow': True, 'nativeMenuSelection': False, 'menuSelection': 'observer-supplied action ID',
         'controlInput': 'synthetic DOM events and click()', 'domHandlers': True, 'productionGoBridge': True,
         'diskScope': 'synthetic fixtures', 'pristineStartup': 'separate dual-Mac gate'}
LEGACY_ALIAS = 'yesman.app/Contents/MacOS/yesman'
LIMITS = dict(holdMs=6000, forceMs=125000, forceCommitMs=150000, slowMs=6500,
              openMs=4500, singleMs=4500, bulkMs=16000, cleanupCommitMs=125000)
LABELS = dict(timeout=r'timed?\s*out|time limit|did not finish|took too long',
              openPR=r'open pull request', primary=r'primary', inUse=r'in use|using (this|the) worktree|open files')
SCENARIOS = {
    'ordinary': ('bound', 'ordinary-preview', 'ordinary-cancel', 'ordinary-before-commit', 'ordinary-after-commit', 'complete'),
    'seven': ('bound', 'seven-hold-cancel-ready', 'seven-cancel', 'seven-hold-preserve', 'seven-force-offer',
              'seven-before-commit', 'seven-after-commit', 'complete'),
    'cleanup': ('bound', 'bulk-checking-cancel', 'slow-diagnostic', 'slow-retry-cancel', 'open-pr-kept', 'review-preview',
                'review-cancel', 'review-before-commit', 'review-after-commit', 'bulk-before-commit', 'bulk-after-commit', 'complete'),
    'recovery-live': ('bound', 'recovery-live-cancel', 'recovery-before-commit', 'recovery-interrupted', 'complete'),
    'recovery-cold': ('bound', 'recovery-cold-cancel', 'cold-before-commit', 'cold-after-commit', 'complete'),
}
APPROVALS = {
    'ordinary-before-commit': ('DeleteWorkspace', 'plain-delete', 'Delete permanently'),
    'seven-before-commit': ('ForceDeleteWorkspace', 'yesman-5', 'Force Delete'),
    'review-before-commit': ('ConfirmWorktreeCleanup', 'cleanup-latency', 'Remove worktree'),
    'bulk-before-commit': ('ConfirmWorktreeCleanup', 'cleanup-latency', 'Permanently remove 28 worktrees'),
    'recovery-before-commit': ('ForceDeleteWorkspace', 'yesman-5', 'Force Delete'),
    'cold-before-commit': ('ForceDeleteWorkspace', '$staged', 'Delete remaining files'),
}
LOSS_WARNINGS = {
    'ordinary-before-commit': ('#deletion-body',
        'Its agents stop. Its conversations, tasks, Knowledge, Working Logs and workspace files are permanently deleted.'),
    'seven-before-commit': ('#force-deletion-risk',
        'Force Delete permanently removes these local checkouts and the rest of this workspace, including uncommitted changes and untracked files. Git data stored outside the workspace is kept.'),
    'bulk-before-commit': ('#worktree-cleanup-hint',
        'This permanently deletes the listed directories, including uncommitted edits, untracked and ignored files, and build output. No recovery reference is created.'),
    'recovery-before-commit': ('#force-deletion-risk',
        'Force Delete permanently removes these local checkouts and the rest of this workspace, including uncommitted changes and untracked files. Git data stored outside the workspace is kept.'),
    'cold-before-commit': ('#force-deletion-risk',
        'This permanently removes the remaining files and local changes at the listed paths.'),
}
BULK = sorted(['latency-00'] + [f'latency-{n:02d}' for n in range(3, 29)])
BULK_OFFER = frozenset(BULK + ['latency-02'])
RECOVERY_STORES = frozenset([
    '.yesman/tmp/t12/display-fw/host-jieli-sdk/.git',
    *[f'.yesman/tmp/t14/h106-app/{name}/idf/.git' for name in ('host','host-config-check','host-fix')],
    '.yesman/tmp/t3/display-fw/host-esp/idf/.git',
    '.yesman/tmp/t15/bk-rgb-opt/sdk-inspect.git',
    '.yesman/tmp/t23/bk-rgb-opt/firmware-build-src/.git',
])
RECOVERY_EARLIER_MARKER = '.yesman/tmp/t12/display-fw/bazel-output/execroot/_main/.git'
RECOVERY_GIT_LOSS = 'This deletes local Git history, branches, tags and objects at these paths. Other checkouts that point here may stop working. Outside repositories, including bare repositories, may also stop working if they borrow Git objects from these stores.'


def recovery_paths(cold):
    stores = RECOVERY_STORES
    markers = frozenset(row['marker'] for row in load(KIT/'fixtures/links.json')['markers'] if row['kind']=='symlink')
    assert len(markers)==10 and RECOVERY_EARLIER_MARKER in markers
    if cold:
        stores |= {f'{store}/modules/components/module-{index:02}'
                   for store in RECOVERY_STORES if '/h106-app/' in store or '/host-esp/' in store
                   for index in range(28)}
        markers -= {RECOVERY_EARLIER_MARKER}
    return {'localStores':sorted(stores),'markers':sorted(markers)}


def write(path, data):
    with path.open('x') as file:
        json.dump(data, file, indent=2, ensure_ascii=False)
        file.write('\n')


def load(path):
    return json.loads(path.read_bytes())


def digest(path):
    return sha256(path.read_bytes()).hexdigest()


def run(argv, env=None, timeout=80):
    return subprocess.check_output(argv, env=env, text=True, stderr=subprocess.PIPE, timeout=timeout)


def files(root):
    rows = {}
    for path in sorted(root.rglob('*')):
        mode = path.lstat().st_mode
        if stat.S_ISLNK(mode):
            assert root.suffix=='.app' and path.relative_to(root).as_posix()=='Contents/MacOS/yesman' and os.readlink(path)=='Yes Man'
            rows['Contents/MacOS/yesman'] = {'symlink':'Yes Man'}
            continue
        assert stat.S_ISREG(mode) or stat.S_ISDIR(mode), f'Unexpected entry type: {path}'
        if stat.S_ISREG(mode):
            rows[path.relative_to(root).as_posix()] = {'mode': stat.S_IMODE(mode), 'sha256': digest(path)}
    return rows


def verify_inputs(args, root, evidence):
    contract = load(args.contract)
    check_contract(contract)
    assert digest(args.contract) == args.contract_sha256
    tag, source, arch = (contract[k] for k in ('tag', 'source', 'arch'))
    repo = args.source_dir.resolve()
    git = lambda *operands: run(['/usr/bin/git', '-C', str(repo), *operands]).strip()
    check_source(contract, git)
    assert load(repo/'mygo.json')['version'] == TAG[1:]
    assert set(contract['kitFiles']) == set(EXECUTION_FILES)
    assert all(digest(KIT/name) == expected for name, expected in contract['kitFiles'].items())
    assert digest(args.archive) == contract['archiveSHA256'] and args.archive.stat().st_size == contract['archiveBytes']
    assert digest(args.manifest) == contract['manifestSHA256']
    assert args.archive.name == f'yesman-{tag[1:]}-darwin-{arch}.tar.gz'
    downloaded = load(args.receipt)
    asset = downloaded['asset']
    assert (downloaded['tag'], downloaded['source'], downloaded['arch'], downloaded['releaseID'], asset['id']) == (
        tag, source, arch, contract['releaseID'], contract['assetID'])
    assert (asset['name'], asset['size'], asset['digest'], downloaded['sha256']) == (
        args.archive.name, contract['archiveBytes'], 'sha256:' + contract['archiveSHA256'], contract['archiveSHA256'])
    assert downloaded['draft'] is True and downloaded['manifestID'] == contract['manifestID']
    assert downloaded['contractSHA256']==args.contract_sha256
    assert args.manifest.stat().st_size==contract['manifestBytes']
    assert (downloaded['episode'], downloaded['qaRef']) == (args.episode, QA_REF)
    check_inputs(args.episode, downloaded['qaRef'], downloaded['runAttempt'], downloaded['qaHead'])
    if args.operation == 'replay':
        assert (downloaded['qaHead'],downloaded['runID'],downloaded['runAttempt']) == (os.environ['GITHUB_SHA'], os.environ['GITHUB_RUN_ID'],os.environ['GITHUB_RUN_ATTEMPT'])
    manifest = load(args.manifest)
    assert manifest['version'] == tag[1:] and manifest['size'] == contract['archiveBytes']
    assert manifest['url'] == f'https://github.com/idy/yesman/releases/download/{tag}/{args.archive.name}'
    go_env = dict(os.environ, GOPROXY='off', GOSUMDB='off', GOENV='off', GOWORK='off', GOTOOLCHAIN='local')
    signature = run(['go', 'run', str(KIT/'signature.go'), str(args.archive), KEY, manifest['signature']], go_env)
    repo = args.source_dir.resolve()
    assert load(repo/'mygo.json')['updates']['publicKey'] == KEY
    web = files(repo/'web')
    assert web and {'index.html','app.js','workspace-delete.js','worktree-cleanup.js'} <= set(web)
    assert web == contract['webFiles'], 'Frontend file tree differs from ship-source inventory'
    release = root/'release'
    release.mkdir()
    with tarfile.open(args.archive, 'r:gz') as archive:
        members = archive.getmembers()
        assert len(members) < 10000
        for member in members:
            path = PurePosixPath(member.name)
            assert path.parts and path.parts[0]=='yesman.app' and '..' not in path.parts
            assert member.isfile() or member.isdir() or (member.name==LEGACY_ALIAS and member.issym() and member.linkname=='Yes Man')
        assert LEGACY_ALIAS in {member.name for member in members if member.issym()}
        assert len({PurePosixPath(member.name) for member in members}) == len(members)
        assert all(not member.name.startswith(LEGACY_ALIAS+'/') for member in members)
        archive.extractall(release, filter='data')
    app = release/'yesman.app'
    # The tagged tools are authoritative for minimum SDK, Mach-O, clean source and Apple signature.
    sys.path.insert(0, str(repo/'scripts'))
    from check_macos_bundle import inspect
    bundle = inspect(app, tag, arch, source)
    executable = app/'Contents/MacOS'/bundle['info']['CFBundleExecutable']
    assert bundle['binarySHA256'] == contract['binarySHA256']
    binary = executable.read_bytes()
    for name in web:
        data = (repo/'web'/name).read_bytes()
        assert data and data in binary, f'Embedded resource differs: {name}'
    bundle_files = files(app)
    write(evidence/'package.json', {'contract': contract, 'archiveReceipt': downloaded, 'signature': signature.strip(),
          'appleSignature': subprocess.run(['codesign','-d','--verbose=4',str(app)], text=True, capture_output=True, check=True).stderr,
          'bundle': bundle, 'bundleFiles': bundle_files, 'embeddedFilesChecked': len(web)})
    return contract, executable, bundle_files


def instrument(web, target, config):
    shutil.copytree(web, target)
    index = target/'index.html'
    app = target/'app.js'
    original_index, original_app = index.read_bytes(), app.read_bytes()
    assert original_app.count(CALL) == 1 and original_index.count(b'<head>') == 1
    js = (KIT/'observer.js').read_bytes()
    assert js.count(b'__T304_CONFIG__') == 1 and b'</script>' not in js.lower()
    script = b'\n<script>\n' + js.replace(b'__T304_CONFIG__', json.dumps(config, ensure_ascii=True).encode()) + b'\n</script>\n'
    index.write_bytes(original_index.replace(b'<head>', b'<head>' + script, 1))
    app.write_bytes(original_app.replace(CALL, OBSERVER_CALL, 1))
    assert index.read_bytes().replace(script, b'', 1) == original_index
    assert app.read_bytes().replace(OBSERVER_CALL, CALL, 1) == original_app
    unchanged, changed = files(web), files(target)
    assert unchanged.keys() == changed.keys()
    assert {name for name in unchanged if unchanged[name] != changed[name]} == {'index.html', 'app.js'}
    return {name: {'packaged': unchanged[name], 'observer': changed[name]} for name in ('index.html','app.js')}


class Replay:
    def __init__(self, scenario, scope, profile, result, env, contract):
        self.scenario, self.scope, self.profile = scenario, scope, profile
        self.result, self.env, self.contract = result, env, contract
        self.position, self.child, self.last_ms = 0, None, -1
        self.finished, self.window_admitted, self.failed = threading.Event(), threading.Event(), None
        self.removed, self.preserved = [], False
        self.payload_baseline = None
        self.token = uuid4().hex
        self.fixture_rows = {}
        self.timings = {'seedMS': None, 'verifyMS': []}
        self.teardown_errors = []
        self.child_receipt = None
        self.failure_snapshot = None
        self.failure_wire = None
        self.cleanup_prefix = []
        self.app_pid = None
        self.journal_frontiers, self.journal_checks = {}, {}

    def fixture(self, script, *args):
        return json.loads(run([sys.executable,'-B',str(KIT/'fixtures'/script), *map(str,args)], self.env))

    def seed(self):
        if self.scenario == 'cleanup':
            start = time.monotonic_ns()
            try:
                result = self.fixture('cleanup.py','seed',self.profile)
                records = load(self.profile / 'yesmen/cleanup-latency/.yesman/worktrees.json')
                self.fixture_rows = {row['dir']: row for row in records}
                assert len(records) == len(self.fixture_rows) == result['worktrees'] == 32
                result['checkoutIdentities'] = {r['dir']: {'instance':r['id'],'head':r['head']} for r in records}
                return result
            finally:
                self.timings['seedMS'] = (time.monotonic_ns()-start)/1e6
        if self.scenario=='recovery-cold':
            return self.fixture('recovery.py','pending',self.profile)
        result = self.fixture('delete_seed.py',self.profile,'--files','25000' if self.scenario=='seven' else '0')
        if self.scenario=='recovery-live':
            self.fixture('seven.py','seed',self.profile,KIT/'fixtures/links.json')
            return self.fixture('recovery.py','seed',self.profile)
        if self.scenario == 'seven':
            result['seven'] = self.fixture('seven.py','seed',self.profile,KIT/'fixtures/links.json')
        return result

    def retained_payload(self):
        root = self.profile/'yesmen'
        if self.scenario=='cleanup':
            tree = root/'cleanup-latency'
            expected = {f'latency-{n:02d}' for n in range(30)} | {'safe-review','keep-primary'}
            expected -= set(self.removed)
            payload = {path: digest(tree/path/'README') for path in sorted(expected)}
        else:
            expected = {'plain-delete','bazel-links','yesman-5'} - set(self.removed)
            if self.scenario.startswith('recovery-'):
                expected.discard('yesman-5')
            payload = {path: digest(root/path/'local.txt') for path in sorted(expected)}
            for path in (('independent',) if self.scenario.startswith('recovery-') else ('dependent-worktree','independent')):
                payload['outside/'+path] = digest(self.profile/'outside'/path/'tracked.txt')
        if self.payload_baseline is None:
            self.payload_baseline = payload
        assert payload == {path: expected for path, expected in self.payload_baseline.items() if path not in self.removed}, 'Retained fixture payload changed'
        return payload

    def verify(self):
        if self.scenario == 'cleanup':
            start = time.monotonic_ns()
            try:
                return self.fixture('cleanup.py','verify',self.profile,'--removed',*self.removed)
            finally:
                self.timings['verifyMS'].append((time.monotonic_ns()-start)/1e6)
        if self.scenario.startswith('recovery-'):
            phase = ('complete' if self.removed else 'pending') if self.scenario=='recovery-cold' else ('interrupted' if self.removed else 'live')
            return self.fixture('recovery.py',phase,self.profile)
        workspaces = {'yesman-5','plain-delete','bazel-links'} - set(self.removed)
        actual = {p.name for p in (self.profile/'yesmen').iterdir()}
        assert actual == workspaces, ('Unexpected workspace or staged remainder', actual, workspaces)
        if self.preserved:
            paths = self.profile/'yesmen'
            assert (paths/'plain-delete/local.txt').read_text() == 'plain-delete local bytes\n'
            assert (paths/'bazel-links/local.txt').read_text() == 'bazel-links local bytes\n'
            return self.fixture('seven.py','after' if self.removed else 'preserved',self.profile,KIT/'fixtures/links.json')
        result = self.fixture('delete_verify.py',self.profile/'fixture-manifest.json',
                              *(['--ordinary-deleted'] if self.removed else []))
        if self.scenario == 'seven':
            paths = load(self.profile/'seven-seeded.json')['repositories']
            assert len(paths)==7 and all((self.profile/'yesmen/yesman-5'/p).is_dir() for p in paths)
        return result

    def late_user(self):
        target = self.profile/'yesmen/cleanup-latency/latency-02'
        assert self.child is None and target.is_dir()
        self.child = subprocess.Popen(['/bin/sleep','135'], cwd=target, env=self.env)
        for _ in range(20):
            observation = subprocess.run(['/usr/sbin/lsof','-a','-p',str(self.child.pid),'-d','cwd','-Fn'],
                                         capture_output=True,text=True,timeout=5)
            if 'n'+str(target) in observation.stdout.splitlines():
                return {'pid':self.child.pid,'cwd':str(target),'lsof':observation.stdout.splitlines()}
            time.sleep(.05)
        raise AssertionError('Stock lsof could not prove the late-use cwd')

    def record_cleanup_failure(self, wire):
        assert self.scenario == 'cleanup' and isinstance(wire, str)
        if self.failure_wire is not None:
            assert wire == self.failure_wire, 'The failure envelope changed'
            return False
        assert self.failed is None and not self.finished.is_set()
        event = json.loads(wire)
        assert isinstance(event, dict) and event.get('scenario') == 'cleanup' and event.get('step') == 'failed'
        assert type(event.get('occurrence')) is int and event['occurrence'] == self.position+1
        assert valid_cleanup_snapshot(event, terminal=False) and event['ms'] > self.last_ms
        calls = event['data']['cleanupRPCs']
        assert len(calls) >= len(self.cleanup_prefix)
        for old, new in zip(self.cleanup_prefix, calls):
            assert all(new.get(key) == old[key] for key in ('id','method','started','target','related') if key in old)
            if 'finished' in old:
                assert all(new.get(key) == old[key] for key in ('finished','ok'))
        assert all(row['started'] >= self.last_ms for row in calls[len(self.cleanup_prefix):])
        self.failure_snapshot, self.failure_wire = event, wire
        self.failed = event['data']['error']
        self.teardown('failed-snapshot', lambda: write(self.result/f'{event["occurrence"]:02}.json', event))
        self.teardown('failure-receipt', lambda: write(self.result/'failure.json',
            {'position':self.position,'error':self.failed}))
        return True

    def accept_failure_diagnostic(self, event):
        first = self.failure_snapshot
        assert self.scenario == 'cleanup' and first and first['data']['diagnosticPending']
        assert isinstance(event, dict) and event.get('step') == 'failure-diagnostic'
        assert event.get('scenario') == 'cleanup' and type(event.get('occurrence')) is int
        assert event['occurrence'] == first['occurrence']+1
        assert valid_cleanup_snapshot(event, terminal=True) and event['ms'] >= first['ms']
        before, after = first['data'], event['data']
        assert all(after[k] == before[k] for k in ('failedAt','deadline','failedCall'))
        old, new = before['cleanupRPCs'], after['cleanupRPCs']
        assert len(new) >= len(old)
        for previous, current in zip(old, new):
            assert all(current.get(key) == value for key, value in previous.items())
            assert {key for key in current.keys()-previous.keys()} <= {'cancelID','finished','ok','error','result'}
            if 'finished' not in previous and 'finished' in current:
                assert current['finished'] >= first['ms']
        assert all(row['method'] == 'CancelWorktreeCleanup' and row['started'] >= first['ms'] and row['related'] <= len(old)
                   for row in new[len(old):])
        write(self.result/'failure-diagnostic.json', event)

    def accept(self, event):
        step = event['step']
        assert self.scenario != 'cleanup' or step not in {'failed','failure-diagnostic'}
        assert event['scenario'] == self.scenario and event['occurrence'] == self.position+1
        assert event['ms'] > self.last_ms
        self.last_ms = event['ms']
        event_path = self.result/f'{event["occurrence"]:02}.json'
        write(event_path,event)
        if step == 'failed':
            raise AssertionError(event['data']['error'])
        assert SCENARIOS[self.scenario][self.position] == step, ('Unexpected phase',step)
        self.position += 1
        assert all(row['step'] not in {'error','unhandled'} for row in event['data']['events']), 'Browser exception'
        if step == 'bound':
            assert event['data']['profile'] == str(self.profile) and event['data']['windowID']
            assert 'AppleWebKit/' in event['data']['userAgent']
            assert event['data']['url'] == 'mygo://localhost/', event['data']['url']
            assert self.window_admitted.wait(40), 'Parent did not observe the fixture native window'
        if step.endswith('-cancel'):
            assert not event['view']['open'] and any(row['step']=='button-click' and row['data']['label']=='Cancel' for row in event['data']['events'])
        if step in ('review-after-commit','bulk-after-commit'):
            expected = ['safe-review'] if step.startswith('review') else BULK
            assert sorted(event['data']['removed']) == expected
            if step.startswith('bulk'):
                assert self.child and self.child.poll() is None
                assert event['data']['late']['dir']=='latency-02' and not event['data']['late'].get('removed')
            self.removed = sorted(set(self.removed+expected))
        if step in ('recovery-interrupted','cold-after-commit'):
            self.removed = ['yesman-5']
        if step in ('ordinary-after-commit','seven-after-commit'):
            self.removed = ['plain-delete' if step.startswith('ordinary') else 'yesman-5']
        commit_rows = [row for row in event['data']['rpc'] if row['method'] in {approval[0] for approval in APPROVALS.values()}]
        if step=='recovery-interrupted':
            assert len(commit_rows)==1 and commit_rows[0]['method']=='ForceDeleteWorkspace' and commit_rows[0]['ok'] is False
            assert 'Deletion is incomplete at' in commit_rows[0]['error'] and event['view']['open']
        elif step.endswith('-after-commit'):
            method = APPROVALS[step.replace('-after-', '-before-')][0]
            assert len(commit_rows)==1 and commit_rows[0]['method']==method and commit_rows[0]['ok'] is True
        else:
            assert not commit_rows
        disk = self.verify()
        retained = self.retained_payload()
        intent = {}
        if self.scenario == 'cleanup' and step in CLEANUP_JOURNAL_STEPS:
            journal = fixture_journal(self.profile)
            assert journal['status'] in ('missing','recorded')
            frontier = journal_frontier(journal['records'])
            self.journal_frontiers[step] = intent['ghFrontier'] = frontier
            self.journal_checks[step] = event['data']['cleanupRPCs']
        if step == 'seven-hold-preserve':
            assert event['view']['open'] and any(row['text']=='Check again' and not row['disabled'] and not row['hidden'] for row in event['view']['buttons'])
            intent['move'] = self.fixture('seven.py','move',self.profile,KIT/'fixtures/links.json')
            self.preserved = True
            intent['verifiedAfterMove'] = self.verify()
        if step in APPROVALS:
            method,target,label = APPROVALS[step]
            if target=='$staged':
                target = disk['entry']
            assert event['view']['open'] and any(row['text']==label and not row['disabled'] and not row['hidden'] for row in event['view']['buttons'])
            if step in LOSS_WARNINGS:
                warning_selector, warning_text = LOSS_WARNINGS[step]
                assert event.get('lossWarning') == {'selector': warning_selector, 'visible': True, 'text': warning_text}, f'Missing exact visible loss warning for {step}'
            if step in ('recovery-before-commit','cold-before-commit'):
                assert event['view']['focus']=='Cancel'
                wanted = 'Review remaining files…' if step.startswith('cold-') else 'Force Delete…'
                assert any(row['step']=='button-click' and row['data']['label']==wanted for row in event['data']['events'])
                assert event['data']['target']==target
                cold = step=='cold-before-commit'
                paths = recovery_paths(cold)
                reviews = [r for r in event['data']['rpc'] if r['method']=='ReviewWorkspaceForceDeletion' and r['ok'] is True]
                assert reviews
                for row in reviews:
                    review = row['review']
                    assert row['target']['entry']==target and review['hasToken'] is True and not review.get('problem')
                    assert all(sorted(review[key])==expected for key,expected in paths.items())
                assert all(f'yesmen/{target}/{p}' in event['view']['text'] for expected in paths.values() for p in expected)
                assert event['data']['gitLossWarning']==RECOVERY_GIT_LOSS
            if step == 'seven-before-commit':
                assert self.preserved and event['view']['focus'] == 'Cancel'
                assert any(row['step']=='button-click' and row['data']['label']=='Force Delete…' for row in event['data']['events'])
            if step == 'bulk-before-commit':
                terminal = [row for row in event['data']['rpc'] if row['method'] == 'CheckWorktreeCleanup'
                            and row.get('preview', {}).get('target') == 'heading' and row.get('ok') is True]
                assert len(terminal) == 1 and bulk_preview_matches(terminal[0]['preview'], self.fixture_rows, BULK_OFFER, LABELS), 'Exact bulk checkout identities are required'
                timing = [row['data'] for row in event['data']['events'] if row['step'] == 'bulk-terminal']
                assert len(timing) == 1 and timing[0]['offered'] == 28 and timing[0]['exact'] is True
                assert 0 <= timing[0]['elapsedMs'] <= LIMITS['bulkMs']
                intent['lateUse'] = self.late_user()
            intent['approval'] = {'method':method,'target':target}
        write(self.result/f'{event["occurrence"]:02}.disk.json', {'step':step,'disk':disk,'retainedPayloadSHA256':retained,'intent':intent})
        if self.scenario == 'cleanup':
            self.cleanup_prefix = event['data'].get('cleanupRPCs', [])
        if step == 'complete':
            self.finished.set()
        return intent.get('approval',{})

    @contextmanager
    def serve(self):
        replay = self
        diagnostic_timer = None

        class Collector(BaseHTTPRequestHandler):
            def setup(self):
                super().setup()
                self.connection.settimeout(6)

            def log_message(self, *args):
                pass

            def reply(self, status, result=None):
                self.send_response(status)
                self.send_header('Access-Control-Allow-Origin','*')
                self.send_header('Access-Control-Allow-Methods','POST, OPTIONS')
                self.send_header('Access-Control-Allow-Headers','Content-Type')
                self.send_header('Access-Control-Allow-Private-Network','true')
                self.send_header('Content-Type','application/json')
                self.end_headers()
                if result is not None:
                    self.wfile.write(json.dumps(result).encode())

            def do_CONNECT(self):
                self.reply(403)

            def do_GET(self):
                self.reply(403)

            def do_OPTIONS(self):
                self.reply(204 if self.path in {'/'+replay.token, '/'+replay.token+'/failure'} else 404)

            def failure_post(self, envelope):
                nonlocal diagnostic_timer
                assert set(envelope) in ({'first'}, {'first','terminal'})
                fresh = replay.record_cleanup_failure(envelope['first'])
                first = replay.failure_snapshot
                if 'terminal' in envelope:
                    assert first['data']['diagnosticPending'] and not replay.finished.is_set()
                    try:
                        replay.accept_failure_diagnostic(envelope['terminal'])
                    except Exception as error:
                        replay.teardown_errors.append({'phase':'failure-diagnostic','type':type(error).__name__})
                        raise
                    finally:
                        replay.finished.set()
                elif fresh and first['data']['diagnosticPending']:
                    # Allow transport/receipt time after the browser's fixed horizon.
                    remaining = (first['data']['deadline'] - first['ms']) / 1000
                    diagnostic_timer = threading.Timer(remaining+1, replay.finished.set)
                    diagnostic_timer.start()
                elif fresh:
                    replay.finished.set()

            def do_POST(self):
                failure_path = self.path == '/'+replay.token+'/failure' and replay.scenario == 'cleanup'
                if not failure_path and self.path != '/'+replay.token:
                    self.reply(404)
                    return
                try:
                    size = int(self.headers.get('Content-Length','0'))
                    assert 0 < size <= 131072
                    raw = self.rfile.read(size)
                    assert len(raw) == size
                    event = json.loads(raw)
                    assert isinstance(event, dict)
                except (ValueError, AssertionError, OSError, RecursionError):
                    if not failure_path and replay.failed is None and not replay.finished.is_set():
                        replay.failed = 'Invalid collector request'
                        replay.teardown('failure-receipt', lambda: write(replay.result/'failure.json',
                            {'position':replay.position,'error':replay.failed}))
                        replay.finished.set()
                    self.reply(422)
                    return
                if failure_path:
                    try:
                        self.failure_post(event)
                    except (AssertionError, KeyError, TypeError, ValueError, OSError, RecursionError):
                        self.reply(422)
                    else:
                        self.reply(204)
                    return
                if replay.failed is not None or replay.finished.is_set() or event.get('step') == 'failure-diagnostic':
                    self.reply(422)
                    return
                try:
                    result = replay.accept(event)
                except Exception as error:
                    replay.failed = str(error) or type(error).__name__
                    replay.teardown('failure-receipt', lambda: write(replay.result/'failure.json',
                        {'position':replay.position,'error':replay.failed}))
                    replay.finished.set()
                    self.reply(422)
                    return
                self.reply(200,result)
        with HTTPServer(('127.0.0.1',0), Collector) as server:
            thread = threading.Thread(target=server.serve_forever,daemon=True)
            thread.start()
            try:
                yield f'http://127.0.0.1:{server.server_port}'
            finally:
                self.teardown('collector-stop', server.shutdown)
                self.teardown('collector-join', lambda: thread.join(timeout=5))
                if thread.is_alive():
                    self.teardown_errors.append({'phase': 'collector-join', 'type': 'ThreadStillRunning'})
                if diagnostic_timer:
                    diagnostic_timer.cancel()
                    diagnostic_timer.join(timeout=2)
                if self.child is not None:
                    self.teardown('late-use-child-stop', self.stop_late_child)

    def teardown(self, phase, action):
        try:
            action()
        except Exception as error:
            self.teardown_errors.append({'phase': phase, 'type': type(error).__name__})

    def stop_late_child(self):
        code = stop(self.child)
        assert self.child.poll() == code
        self.child_receipt = {'pid': self.child.pid, 'returncode': code, 'joined': True}

    def collect_diagnostics(self):
        diagnostic = {'episode': 'cleanup-only', 'timings': self.timings, 'enteredCheckpoint': self.position,
                      'expectedRemovedFromAcceptedRecords': self.removed, 'childTeardown': self.child_receipt}
        profile = None
        try:
            profile = checked_path(self.scope, 'fixture.noindex')
            diagnostic['gh'] = fixture_journal(profile)
        except (OSError, ValueError) as error:
            diagnostic['gh'] = {'status': 'unavailable', 'errorType': type(error).__name__, 'records': []}
        if profile is not None:
            try:
                diagnostic['ghShim'] = shim_diagnostics(profile)
            except (OSError, ValueError) as error:
                diagnostic['ghShim'] = {'status': 'unavailable', 'errorType': type(error).__name__, 'records': []}
        if self.failure_snapshot:
            diagnostic['bridgeCapture'] = ('recorded' if (self.result/'failure-diagnostic.json').is_file() else
                'unavailable' if self.failure_snapshot['data']['diagnosticPending'] else 'settled-at-failure')
        if profile is not None and self.fixture_rows:
            try:
                diagnostic['terminalDisk'] = {'verified': True, 'disk': self.verify(), 'retainedPayloadSHA256': self.retained_payload()}
            except Exception as error:
                diagnostic['terminalDisk'] = {'verified': False, 'errorType': type(error).__name__}
        diagnostic['teardownErrors'] = self.teardown_errors
        write(self.result/'diagnostics.json', diagnostic)
        return diagnostic

    def deletion_disk_snapshot(self):
        if self.scenario.startswith('recovery-'):
            return self.fixture('recovery.py','snapshot',self.profile)
        root = self.profile/'yesmen'
        if not root.is_dir():
            return {'workspaceRootPresent':False}
        assert root.resolve(strict=True)==root
        entries = sorted(p.name for p in root.iterdir())
        assert len(entries)<=20
        return {'workspaceRootPresent':True,'entries':entries,
                'payloadSHA256':{name:digest(root/name/'local.txt') for name in ('plain-delete','bazel-links','yesman-5')
                                 if (root/name/'local.txt').is_file() and (root/name/'local.txt').resolve()==root/name/'local.txt'}}

    @contextmanager
    def preserve_deletion_diagnostics(self):
        primary = None
        try:
            yield
        except BaseException as error:
            primary = error
            raise
        finally:
            try:
                diagnostic = {'episode':'deletion-only','scenario':self.scenario,
                              'enteredCheckpoint':self.position,
                              'primaryErrorType':type(primary).__name__ if primary is not None else None}
                self.teardown('disk-snapshot', lambda: diagnostic.update(diskSnapshot=self.deletion_disk_snapshot()))
                diagnostic['terminalDisk'] = {'verified':False}
                self.teardown('terminal-disk-verification', lambda: diagnostic.update(terminalDisk={
                    'verified':True,'disk':self.verify(),'retainedPayloadSHA256':self.retained_payload()}))
                diagnostic['teardownErrors'] = self.teardown_errors
                write(self.result/'deletion-diagnostics.json', diagnostic)
                if primary is None:
                    assert not self.teardown_errors and diagnostic['terminalDisk']['verified'], self.teardown_errors
            except Exception as diagnostic_error:
                if primary is None:
                    raise
                primary.add_note(f'Deletion evidence error: {type(diagnostic_error).__name__}')

    @contextmanager
    def preserve_diagnostics(self):
        primary = None
        try:
            yield
        except BaseException as error:
            primary = error
            raise
        finally:
            try:
                diagnostic = self.collect_diagnostics()
                if primary is None:
                    assert not self.teardown_errors, self.teardown_errors
                    assert diagnostic['terminalDisk']['verified'] and diagnostic['gh']['status'] == 'recorded'
                    validate_cleanup_journal(self.app_pid, self.journal_frontiers, diagnostic['gh'],
                                             diagnostic['ghShim'], self.journal_checks)
                    assert self.child_receipt and self.child_receipt['joined']
            except Exception as diagnostic_error:
                if primary is None:
                    raise
                primary.add_note(f'Cleanup evidence error: {type(diagnostic_error).__name__}')


def fixture_env(scope):
    return {'HOME': str(Path.home()), 'PATH':'/usr/bin:/bin:/usr/sbin:/sbin', 'LANG':'en_US.UTF-8',
            'SHELL':'/usr/bin/false','TMPDIR':str(scope),'T304_SANDBOX':str(scope),
            'GIT_CONFIG_GLOBAL':'/dev/null','GIT_CONFIG_NOSYSTEM':'1','GIT_TERMINAL_PROMPT':'0'}


def hosted_guard():
    assert sys.platform=='darwin' and os.uname().machine=='arm64'
    assert os.environ.get('GITHUB_ACTIONS')=='true' and os.environ.get('RUNNER_ENVIRONMENT')=='github-hosted'
    assert os.environ.get('RUNNER_OS')=='macOS' and os.environ.get('GITHUB_REPOSITORY')=='idy/yesman'
    assert subprocess.run(['/usr/bin/pgrep','-x','Yes Man|yesman'],stdout=subprocess.DEVNULL).returncode==1
    for relative in ('yesman','Library/Application Support/yesman','Library/Preferences/dev.idy.yesman.plist',
                     'Library/WebKit/dev.idy.yesman','Library/HTTPStorages/dev.idy.yesman'):
        assert not (Path.home()/relative).exists(), f'Hosted runner already has Yes Man data at {relative}'


def stop(process):
    if process.poll() is None:
        process.send_signal(signal.SIGTERM)
        try:
            return process.wait(timeout=15)
        except subprocess.TimeoutExpired:
            process.kill()
    return process.wait(timeout=5)


def launch(args, root, evidence, contract, executable, bundle_files, scenario, owned_pids):
    helper = root/'window-info'
    scope = root/('recovery' if scenario.startswith('recovery-') else scenario)
    result = evidence/scenario
    scope.mkdir(exist_ok=scenario=='recovery-cold')
    result.mkdir()
    profile = scope/'fixture.noindex'
    env = fixture_env(scope)
    replay = Replay(scenario,scope,profile,result,env,contract)
    diagnostics = replay.preserve_diagnostics if scenario=='cleanup' else replay.preserve_deletion_diagnostics
    with diagnostics():
        write(result/'seed.json', replay.seed())
        write(result/'initial-disk.json',{'disk':replay.verify(),'retainedPayloadSHA256':replay.retained_payload()})
        with replay.serve() as endpoint:
            config = dict(scenario=scenario, episode=args.episode, source=contract['source'], profile=str(profile), endpoint=endpoint+'/'+replay.token,
                          labels=LABELS, limits=LIMITS, hires=(['cleanup-latency'] if scenario=='cleanup' else
                              ['bazel-links','plain-delete'] if scenario=='recovery-cold' else ['bazel-links','plain-delete','yesman-5']))
            if scenario.startswith('recovery-'):
                config['recoveryPaths'] = recovery_paths(scenario=='recovery-cold')
            if scenario=='recovery-cold':
                config['stage'] = replay.fixture('recovery.py','pending',profile)['entry']
            if scenario=='seven':
                config['markers'] = [row['marker'] for row in load(KIT/'fixtures/links.json')['markers'] if row['kind']=='symlink']
            web = scope/('observer-web-'+scenario)
            write(result/'instrumentation.json',instrument(args.source_dir/'web',web,config))
            env.update(YESMAN_HOME=str(profile),YESMAN_PI='/usr/bin/false',PI_CODING_AGENT_DIR=str(scope/'pi'),
                       YESMAN_WEB_DIR=str(web),HTTP_PROXY=endpoint,HTTPS_PROXY=endpoint,ALL_PROXY=endpoint,NO_PROXY='localhost,127.0.0.1,::1',
                       PATH=str(profile/'fixture-bin')+':'+env['PATH'])
            process = None
            try:
                with (result/'app.log').open('x') as log:
                    process = subprocess.Popen([str(executable)],cwd=scope,env=env,stdout=log,stderr=subprocess.STDOUT)
                    assert process.pid not in owned_pids
                    owned_pids.add(process.pid)
                    end = time.monotonic()+40
                    while time.monotonic()<end:
                        assert process.poll() is None, 'App exited before showing its window'
                        windows = json.loads(run([str(helper),str(process.pid)],timeout=5))
                        if any(w['onScreen'] and w['bounds'].get('Width',0)>100 and w['bounds'].get('Height',0)>100 for w in windows):
                            break
                        time.sleep(.2)
                    else:
                        raise AssertionError('No visible window owned by the verified executable PID')
                    mapped = run(['/usr/sbin/lsof','-a','-p',str(process.pid),'-d','txt','-Fin'], env, timeout=8)
                    assert 'n'+str(executable) in mapped.splitlines(), 'The launched PID did not map the pinned executable'
                    write(result/'window.json', {'pid':process.pid,'windows':windows,'binarySHA256':digest(executable),
                          'device':executable.stat().st_dev,'inode':executable.stat().st_ino,
                          'processStart':run(['/bin/ps','-p',str(process.pid),'-o','lstart=']).strip()})
                    replay.app_pid = process.pid
                    replay.window_admitted.set()
                    assert replay.finished.wait(380), 'Replay timed out'
                    assert replay.failed is None, replay.failed
                    assert replay.position==len(SCENARIOS[scenario])
            finally:
                if process:
                    def stop_app():
                        code = stop(process)
                        write(result/'exit.json', {'pid':process.pid,'exitCode':code})
                        windows = json.loads(run([str(helper),str(process.pid)],timeout=5))
                        write(result/'postexit-windows.json', {'pid':process.pid,'windows':windows})
                        assert code==0 and not windows
                    replay.teardown('app-stop', stop_app)
                    if scenario!='cleanup' and not (result/'postexit-windows.json').exists():
                        def capture_windows_after_stop_error():
                            windows = json.loads(run([str(helper),str(process.pid)],timeout=5))
                            write(result/'postexit-windows.json', {'pid':process.pid,'windows':windows})
                        replay.teardown('app-window-snapshot', capture_windows_after_stop_error)
                def check_bundle():
                    assert files(executable.parents[2]) == bundle_files
                    run(['codesign','--verify','--deep','--strict',str(executable.parents[2])])
                    write(result/'postexit-bundle.json', {'inventoryUnchanged':True,'strictSignatureVerified':True})
                replay.teardown('bundle-recheck', check_bundle)
    assert not replay.teardown_errors, replay.teardown_errors
    write(result/'pass.json',{'scenario':scenario,'episode':args.episode,'completedScenarios':[scenario],
          'overallPackageAcceptance':False,'source':contract['source'],'releaseID':contract['releaseID'],
          'assetID':contract['assetID'],'binarySHA256':contract['binarySHA256'],'runID':os.getenv('GITHUB_RUN_ID'),
          'runSHA':os.getenv('GITHUB_SHA'),'runAttempt':os.getenv('GITHUB_RUN_ATTEMPT'),
          'contractSHA256':args.contract_sha256,'qaRef':os.getenv('GITHUB_REF'),
          **SCOPE,
          'stages':list(SCENARIOS[scenario])})


def run_episode(args, contract, executable, bundle_files):
    helper = args.scratch/'window-info'
    owned_pids = set()
    assert args.episode == 'cleanup-only'
    scenarios = ('cleanup',)
    recovery = args.scratch/'recovery'
    profile = recovery/'fixture.noindex'
    def recovery_action(phase):
        return json.loads(run([sys.executable,'-B',str(KIT/'fixtures/recovery.py'),phase,str(profile)],fixture_env(recovery)))
    try:
        run(['xcrun','swiftc',str(KIT/'windows.swift'),'-o',str(helper)])
        for scenario in scenarios:
            if scenario=='recovery-cold':
                write(args.evidence/'release-fault.json', recovery_action('release-fault'))
            launch(args,args.scratch,args.evidence,contract,executable,bundle_files,scenario,owned_pids)
    except BaseException as error:
        if args.episode=='deletion-only':
            terminal = {'episode':args.episode,'primaryErrorType':type(error).__name__,'restorationAttempted':False}
            teardown = []
            if (profile/'yesmen').is_dir():
                try:
                    terminal['beforeRestoration'] = recovery_action('snapshot')
                    write(args.evidence/'deletion-before-restoration.json',terminal['beforeRestoration'])
                except Exception as secondary:
                    teardown.append({'phase':'before-restoration','type':type(secondary).__name__})
            if (profile/'recovery-fixture.json').is_file():
                terminal['restorationAttempted'] = True
                try:
                    restored = recovery_action('restore-on-error')
                    terminal['afterRestoration'] = restored['disk']
                    teardown.extend({'phase':'restore-fault',**row} for row in restored['errors'])
                except Exception as secondary:
                    teardown.append({'phase':'restore-fault','type':type(secondary).__name__})
                    try:
                        terminal['afterRestoration'] = recovery_action('snapshot')
                    except Exception as snapshot_error:
                        teardown.append({'phase':'after-restoration','type':type(snapshot_error).__name__})
            terminal['teardownErrors'] = teardown
            try:
                write(args.evidence/'deletion-episode-failure.json', terminal)
            except Exception as secondary:
                error.add_note(f'Deletion episode evidence error: {type(secondary).__name__}')
        raise
    write(args.evidence/'episode-pass.json', {'episode':args.episode,'completedScenarios':list(scenarios),
          'overallPackageAcceptance':False,'source':contract['source'],'releaseID':contract['releaseID'],
          'contractSHA256':args.contract_sha256,'runID':os.environ['GITHUB_RUN_ID'],'runAttempt':'1',
          'qaHead':os.environ['GITHUB_SHA'],'qaRef':os.environ['GITHUB_REF'],**SCOPE})


def main():
    if not __debug__:
        raise RuntimeError('Acceptance assertions must be enabled')
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('operation',choices=['verify-only','replay'])
    parser.add_argument('--episode', required=True, choices=sorted(EPISODES))
    for field in ('contract','source-dir','archive','manifest','receipt','scratch','evidence'):
        parser.add_argument('--'+field,type=Path,required=True)
    parser.add_argument('--contract-sha256',required=True)
    args = parser.parse_args()
    if args.operation=='replay':
        check_inputs(args.episode, os.environ.get('GITHUB_REF'), os.environ.get('GITHUB_RUN_ATTEMPT'), os.environ.get('GITHUB_SHA'))
        hosted_guard()
        assert args.scratch.resolve().is_relative_to(Path(os.environ['RUNNER_TEMP']).resolve())
        assert args.evidence.resolve().is_relative_to(Path(os.environ['RUNNER_TEMP']).resolve())
    args.scratch.mkdir(mode=0o700)
    args.evidence.mkdir(mode=0o700)
    args.scratch = args.scratch.resolve(strict=True)
    args.evidence = args.evidence.resolve(strict=True)
    try:
        contract, executable, bundle_files = verify_inputs(args,args.scratch,args.evidence)
        if args.operation=='replay':
            run_episode(args,contract,executable,bundle_files)
    except Exception as error:
        detail = error.stderr if isinstance(error,subprocess.CalledProcessError) else None
        try:
            write(args.evidence/'failure.json',{'type':type(error).__name__,'error':str(error),'stderr':detail})
        except OSError as evidence_error:
            error.add_note(f'Primary failure receipt write error: {type(evidence_error).__name__}')
        raise


if __name__=='__main__':
    main()
