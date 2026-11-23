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

from cleanup_diagnostics import bulk_preview_matches, checked_path, fixture_journal

KIT = Path(__file__).resolve().parent
KEY = 'yrQPn0gg1/gASLxIQiUCIiUQ/csWENI0+up1cs3KWO0='
CALL = b'const call = (method, ...args) => mygo.call(`Yesman.${method}`, ...args);'
OBSERVER_CALL = b'const call = (method, ...args) => window.__t304Call(mygo, method, ...args);'
EXECUTION_FILES = ('hosted.py','observer.js','signature.go','windows.swift','fixtures/cleanup.py',
                   'fixtures/delete_seed.py','fixtures/delete_verify.py','fixtures/links.json','fixtures/seven.py',
                   'cleanup_diagnostics.py')
EPISODE = 'cleanup-only'
QA_REF = 'refs/heads/qa/t322-v0.1.64-cleanup'
QA_BASE = 'acea9a4018e2530db9af63a5aead8705ba1fe266'
RELEASE_SOURCE = '68811d382ad5eac649d429630c21332c53f5cb59'
T319_VERDICT_SHA = '65be470d2920e354c9ff460efdaddb9ea9a6f56f1d4cdfa763b45f418d4081fa'
COMBINATION = {
    '#141': {
        'originalHead': '10b269b22a88ba0f0e9df5ce4e86d8ffce3fec98',
        'head': 'acd8b81e31588cf566df18ee4e7eaea64144b8b9',
        'base': 'cec767fcea6f70e65936632b91e79f8434ada786',
        'preview': '7da4d7d477407201b44706ac2a4350b3dc84e85f',
        'tree': '0712bb215528160e1c10f112f74b233cd86e019a',
    },
    '#142': {
        'head': 'e03f77c0da8cc37035b36ca6cbe8ca8a2ab3eb99',
        'base': '02b7b752056bca826673001b0143c1ed1ebdef87',
        'preview': 'cc0dfb4767205fbc23738ad3fa43d51436c259cd',
        'tree': '4a3f88437588d84bccdc406d61f3e8c047732b0c',
        'integratedCommit': 'cec767fcea6f70e65936632b91e79f8434ada786',
    },
}
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
}
APPROVALS = {
    'ordinary-before-commit': ('DeleteWorkspace', 'plain-delete', 'Delete permanently'),
    'seven-before-commit': ('ForceDeleteWorkspace', 'yesman-5', 'Force Delete'),
    'review-before-commit': ('ConfirmWorktreeCleanup', 'cleanup-latency', 'Remove worktree'),
    'bulk-before-commit': ('ConfirmWorktreeCleanup', 'cleanup-latency', 'Permanently remove 28 worktrees'),
}
LOSS_WARNINGS = {
    'ordinary-before-commit': ('#deletion-body',
        'Its agents stop. Its conversations, tasks, Knowledge, Working Logs and workspace files are permanently deleted.'),
    'seven-before-commit': ('#force-deletion-risk',
        'Force Delete permanently removes these local checkouts and the rest of this workspace, including uncommitted changes and untracked files. Git data stored outside the workspace is kept.'),
    'bulk-before-commit': ('#worktree-cleanup-hint',
        'This permanently deletes the listed directories, including uncommitted edits, untracked and ignored files, and build output. No recovery reference is created.'),
}
BULK = sorted(['latency-00'] + [f'latency-{n:02d}' for n in range(3, 29)])
BULK_OFFER = frozenset(BULK + ['latency-02'])


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


def verify_combination(inclusion, adoption_path, adoption_sha256, review_path):
    assert set(inclusion) == set(COMBINATION), 'Expected both adopted PRs'
    for pr, expected in COMBINATION.items():
        assert all(inclusion[pr].get(key) == value for key, value in expected.items()), f'{pr} source binding differs'
    assert adoption_path and review_path and re.fullmatch(r'[0-9a-f]{64}', adoption_sha256 or ''), 'Adopted t311 evidence is required'
    assert digest(adoption_path) == adoption_sha256, 'Lead adoption receipt digest differs'
    adoption = load(adoption_path)
    assert adoption['schema'] == 1 and adoption['reviewTask'] == 't311' and adoption['decision'] == 'adopted'
    assert adoption['repository'] == 'idy/yesman' and adoption['pr'] == 141
    assert adoption['combination'] == COMBINATION['#141'], 'Lead adopted another combination'
    assert adoption['reviewSHA256'] == digest(review_path), 'Independent review receipt digest differs'
    receipt = load(review_path)
    assert receipt['integration_ready'] is True and receipt['source_verdict'].lower() == 'pass', 'Independent review is not ready for integration'
    assert receipt['platform_passed'] == 16 and not receipt.get('missing'), 'Independent platform evidence is incomplete'
    reviewed = COMBINATION['#141']
    assert receipt['source'] == reviewed['head']
    assert receipt.get('base', receipt.get('main')) == reviewed['base']
    assert receipt.get('tree', receipt['preview']['tree']) == reviewed['tree']
    assert receipt['preview']['sha'] == reviewed['preview'] and receipt['preview']['tree'] == reviewed['tree']
    assert receipt['preview']['parents'] == [reviewed['base'], reviewed['head']]
    merged = inclusion['#141'].get('integratedCommit')
    assert re.fullmatch(r'[0-9a-f]{40}', merged or '') and merged not in reviewed.values(), 'Actual #141 squash is required'
    return adoption


def verify_release_source(repo, source, tree, inclusion):
    git = lambda *args: run(['/usr/bin/git', '-C', str(repo), *args]).strip()
    assert git('rev-parse', '--is-shallow-repository') == 'false', 'Release checkout requires complete history'
    assert (git('rev-parse', 'HEAD'), git('rev-parse', 'HEAD^{tree}')) == (source, tree), 'Release checkout differs'
    assert not git('status', '--porcelain', '--untracked-files=all'), 'Release checkout is dirty'
    for pr, review in inclusion.items():
        integrated = review['integratedCommit']
        assert integrated != source, f'{pr} needs a distinct final release commit'
        git('merge-base', '--is-ancestor', integrated, source)
        assert git('rev-parse', integrated + '^{tree}') == review['tree'], f'{pr} squash tree differs'
        assert git('show', '-s', '--format=%P', integrated) == review['base'], f'{pr} squash parent differs'


def require_first_attempt(attempt):
    assert attempt == '1', 'Hosted acceptance permits only Actions attempt 1'


def require_episode(episode, ref, contract=None):
    assert episode == EPISODE and ref == QA_REF, 'Explicit cleanup-only episode and exact QA ref required'
    if contract is not None:
        assert (contract['schema'], contract['mode'], contract['episode'], contract['qaBase']) == (3, 'candidate', EPISODE, QA_BASE)
        assert (contract['tag'], contract['source'], contract['releaseID'], contract['assetID'], contract['manifestID']) == (
            'v0.1.64', RELEASE_SOURCE, 455729493, 783812678, 783812889), 'Different release operands'
        verdict_path = KIT / 't319-verdict.json'
        reuse = contract['reuse']
        assert reuse['verdictSHA256'] == digest(verdict_path) == T319_VERDICT_SHA
        verdict = load(verdict_path)
        assert (verdict['source'], verdict['draft'], verdict['qaHead'], verdict['standardRun'], verdict['customRun']) == (
            RELEASE_SOURCE, 455729493, QA_BASE, 45225972788, 45228181690)
        assert (verdict['ordinary'], verdict['seven'], verdict['standardStartup'], verdict['customResult']) == ('PASS','PASS','PASS','FAIL')
        assert reuse['completedScenarios'] == ['ordinary', 'seven'] and reuse['standardRun'] == verdict['standardRun']
        assert reuse['failedCustomRun'] == verdict['customRun'] and reuse['overallPackageAcceptance'] is False


def verify_inputs(args, root, evidence):
    contract = load(args.contract)
    require_episode(args.episode, QA_REF, contract)
    assert contract['repository'] == 'idy/yesman'
    tag, source, arch = (contract[k] for k in ('tag', 'source', 'arch'))
    assert arch == 'arm64' and re.fullmatch(r'v0\.1\.(0|[1-9][0-9]*)', tag)
    for revision in [source, contract['tree'], *contract['features'].values()]:
        assert re.fullmatch(r'[0-9a-f]{40}', revision), revision
    assert contract['mode'] in ('published-control', 'candidate')
    if contract['mode'] == 'candidate':
        assert contract['features'] == {pr: row['head'] for pr, row in COMBINATION.items()}
        verify_combination(contract['inclusion'], args.adoption, contract['leadAdoptionSHA256'], args.review)
        verify_release_source(args.source_dir.resolve(), source, contract['tree'], contract['inclusion'])
        assert all(type(contract[k]) is int and contract[k] > 0 for k in ('releaseID','assetID','manifestID','archiveBytes'))
        assert re.fullmatch(r'[0-9a-f]{64}',contract['independentBindingSHA256'])
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
    if contract['mode'] == 'candidate':
        assert downloaded['draft'] is True and downloaded['manifestID'] == contract['manifestID']
        assert downloaded['contractSHA256']==digest(args.contract)
        require_first_attempt(downloaded['runAttempt'])
        assert (downloaded['episode'], downloaded['qaRef']) == (EPISODE, QA_REF)
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
                return result
            finally:
                self.timings['seedMS'] = (time.monotonic_ns()-start)/1e6
        result = self.fixture('delete_seed.py',self.profile,'--files','25000' if self.scenario=='seven' else '0')
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
            payload = {path: digest(root/path/'local.txt') for path in sorted(expected)}
            for path in ('dependent-worktree','independent'):
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

    def accept(self, event):
        step = event['step']
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
        if step in ('ordinary-after-commit','seven-after-commit'):
            self.removed = ['plain-delete' if step.startswith('ordinary') else 'yesman-5']
        commit_rows = [row for row in event['data']['rpc'] if row['method'] in {approval[0] for approval in APPROVALS.values()}]
        if step.endswith('-after-commit'):
            method = APPROVALS[step.replace('-after-', '-before-')][0]
            assert len(commit_rows)==1 and commit_rows[0]['method']==method and commit_rows[0]['ok'] is True
        else:
            assert not commit_rows
        disk = self.verify()
        retained = self.retained_payload()
        intent = {}
        if step == 'seven-hold-preserve':
            assert event['view']['open'] and any(row['text']=='Check again' and not row['disabled'] and not row['hidden'] for row in event['view']['buttons'])
            intent['move'] = self.fixture('seven.py','move',self.profile,KIT/'fixtures/links.json')
            self.preserved = True
            intent['verifiedAfterMove'] = self.verify()
        if step in APPROVALS:
            method,target,label = APPROVALS[step]
            assert event['view']['open'] and any(row['text']==label and not row['disabled'] and not row['hidden'] for row in event['view']['buttons'])
            if step in LOSS_WARNINGS:
                warning_selector, warning_text = LOSS_WARNINGS[step]
                assert event.get('lossWarning') == {'selector': warning_selector, 'visible': True, 'text': warning_text}, f'Missing exact visible loss warning for {step}'
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
        if step == 'complete':
            self.finished.set()
        return intent.get('approval',{})

    @contextmanager
    def serve(self):
        replay = self
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
                self.reply(204 if self.path=='/'+replay.token else 404)

            def do_POST(self):
                if self.path != '/'+replay.token:
                    self.reply(404)
                    return
                try:
                    assert replay.failed is None and not replay.finished.is_set()
                    size = int(self.headers.get('Content-Length','0'))
                    assert 0 < size <= 131072
                    result = replay.accept(json.loads(self.rfile.read(size)))
                except Exception as error:
                    replay.failed = str(error) or type(error).__name__
                    try:
                        write(replay.result/'failure.json', {'position':replay.position,'error':replay.failed})
                    finally:
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
        diagnostic = {'episode': EPISODE, 'timings': self.timings, 'lastAcceptedCheckpoint': self.position,
                      'expectedRemovedFromAcceptedRecords': self.removed, 'childTeardown': self.child_receipt}
        profile = None
        try:
            profile = checked_path(self.scope, 'fixture.noindex')
            diagnostic['gh'] = fixture_journal(profile)
        except (OSError, ValueError) as error:
            diagnostic['gh'] = {'status': 'unavailable', 'errorType': type(error).__name__, 'records': []}
        if profile is not None and self.fixture_rows:
            try:
                diagnostic['terminalDisk'] = {'verified': True, 'disk': self.verify(), 'retainedPayloadSHA256': self.retained_payload()}
            except Exception as error:
                diagnostic['terminalDisk'] = {'verified': False, 'errorType': type(error).__name__}
        diagnostic['teardownErrors'] = self.teardown_errors
        write(self.result/'diagnostics.json', diagnostic)
        return diagnostic

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
                    inquiries = diagnostic['gh']['records']
                    assert any(row['branch']=='fixture:latency-01' and row['delaySeconds']==.2 and row['phase']=='response-written' for row in inquiries)
                    assert any(row['branch']=='fixture:latency-29' and row['delaySeconds']==30 and row['phase']=='start' for row in inquiries)
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
    require_first_attempt(os.environ.get('GITHUB_RUN_ATTEMPT'))
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


def launch(args, root, evidence, contract, executable, bundle_files):
    require_episode(args.episode, os.environ.get('GITHUB_REF'), contract)
    scenario = 'cleanup'
    helper = root/'window-info'
    run(['xcrun','swiftc',str(KIT/'windows.swift'),'-o',str(helper)])
    scope, result = root/scenario, evidence/scenario
    scope.mkdir()
    result.mkdir()
    profile = scope/'fixture.noindex'
    env = fixture_env(scope)
    replay = Replay(scenario,scope,profile,result,env,contract)
    with replay.preserve_diagnostics():
        write(result/'seed.json', replay.seed())
        write(result/'initial-disk.json',{'disk':replay.verify(),'retainedPayloadSHA256':replay.retained_payload()})
        with replay.serve() as endpoint:
            config = dict(scenario=scenario, episode=EPISODE, source=contract['source'], profile=str(profile), endpoint=endpoint+'/'+replay.token,
                          labels=LABELS, limits=LIMITS, hires=['cleanup-latency'])
            web = scope/'observer-web'
            write(result/'instrumentation.json',instrument(args.source_dir/'web',web,config))
            env.update(YESMAN_HOME=str(profile),YESMAN_PI='/usr/bin/false',PI_CODING_AGENT_DIR=str(scope/'pi'),
                       YESMAN_WEB_DIR=str(web),HTTP_PROXY=endpoint,HTTPS_PROXY=endpoint,ALL_PROXY=endpoint,NO_PROXY='localhost,127.0.0.1,::1',
                       PATH=str(profile/'fixture-bin')+':'+env['PATH'])
            process = None
            try:
                with (result/'app.log').open('x') as log:
                    process = subprocess.Popen([str(executable)],cwd=scope,env=env,stdout=log,stderr=subprocess.STDOUT)
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
                def check_bundle():
                    assert files(executable.parents[2]) == bundle_files
                    run(['codesign','--verify','--deep','--strict',str(executable.parents[2])])
                    write(result/'postexit-bundle.json', {'inventoryUnchanged':True,'strictSignatureVerified':True})
                replay.teardown('bundle-recheck', check_bundle)
    write(result/'pass.json',{'scenario':scenario,'episode':EPISODE,'completedScenarios':[scenario],
          'overallPackageAcceptance':False,'source':contract['source'],'releaseID':contract['releaseID'],
          'assetID':contract['assetID'],'binarySHA256':contract['binarySHA256'],'runID':os.getenv('GITHUB_RUN_ID'),
          'runSHA':os.getenv('GITHUB_SHA'),'runAttempt':os.getenv('GITHUB_RUN_ATTEMPT'),
          **SCOPE,
          'stages':list(SCENARIOS[scenario])})


def main():
    if not __debug__:
        raise RuntimeError('Acceptance assertions must be enabled')
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('operation',choices=['verify-only','replay'])
    parser.add_argument('--episode', required=True, choices=[EPISODE])
    for field in ('contract','source-dir','archive','manifest','receipt','scratch','evidence'):
        parser.add_argument('--'+field,type=Path,required=True)
    parser.add_argument('--adoption', type=Path)
    parser.add_argument('--review', type=Path)
    args = parser.parse_args()
    if args.operation=='replay':
        require_episode(args.episode, os.environ.get('GITHUB_REF'))
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
            assert contract['mode']=='candidate'
            launch(args,args.scratch,args.evidence,contract,executable,bundle_files)
    except Exception as error:
        detail = error.stderr if isinstance(error,subprocess.CalledProcessError) else None
        try:
            write(args.evidence/'failure.json',{'type':type(error).__name__,'error':str(error),'stderr':detail})
        except OSError as evidence_error:
            error.add_note(f'Primary failure receipt write error: {type(evidence_error).__name__}')
        raise


if __name__=='__main__':
    main()
