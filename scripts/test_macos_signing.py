#!/usr/bin/env python3
"""Check Developer ID signing and the release build launcher without real credentials.

  (default)        scripts/macos_signing.py setup/cleanup/verify and
                   scripts/release_build.py, run from a fixture copy of the
                   scripts, against the stand-in Apple tools of
                   scripts/fixtures/macos-signing and a stand-in go; the
                   release and draft-smoke workflow steps; and where the
                   workflows let the signing inputs go. Needs macOS (csreq).
  --mygo           Also runs the real `go tool mygo build` of a committed copy
                   of this repository through the launcher, Apple tools and gh
                   stood in: both architectures, signing, notarization and
                   stapling before the update archives, failures stopping the
                   build, the same output as the tracked configuration for an
                   ad-hoc build, a plain `go tool mygo build` with no inputs,
                   and no credential in any compiler's environment.
                   Needs macOS with Go and Xcode; set GOCACHE/GOMODCACHE.
  --real-keychain  On a disposable GitHub-hosted macOS runner only: real
                   security/codesign reject a synthetic self-signed "Developer
                   ID Application" certificate and leave no keychain behind.
  --report PATH    Writes the results as JSON.

Nothing here reads a real keychain, signs with a real identity or contacts
Apple: the stand-ins pass only ad-hoc signing of temporary files to codesign.
Setup's "hosted runner" condition is simulated only around stand-in tools.
"""
import argparse
import base64
import hashlib
import json
import os
import pathlib
import plistlib
import re
import shutil
import signal
import stat
import subprocess
import sys
import tarfile
import tempfile
import time

SCRIPTS = pathlib.Path(__file__).resolve().parent
ROOT = SCRIPTS.parent
TOOLS = SCRIPTS / 'fixtures/macos-signing/apple_tools.py'
COPIED = ('macos_signing.py', 'check_macos_bundle.py', 'release_build.py')
TEAM = 'FXTEAM1234'
SHA = 'A1B2C3D4E5F60718293A4B5C6D7E8F9012345678'
NAME = 'Developer ID Application: Fixture Person (%s)' % TEAM
P12 = b'fixture pkcs12 bytes, not a real certificate'
SENTINELS = {
    'MACOS_SIGNING_CERTIFICATE_PASSWORD': 'fixture-p12-passphrase-7f3a',
    'MACOS_NOTARY_APPLE_ID': 'fixture-apple-id@example.invalid',
    'MACOS_NOTARY_PASSWORD': 'abcd-efgh-ijkl-mnop',
}
UPDATER_KEY = 'fixture-updater-key-not-real-9c41'
UPLOAD_TOKEN = 'fixture-upload-token-not-real-5d72'
UPDATER_PUBLIC_KEY = 'yrQPn0gg1/gASLxIQiUCIiUQ/csWENI0+up1cs3KWO0='  # must never change with signing
BASE_MYGO_JSON = '5c3c1c4a'  # see identity-and-updater-key-unchanged
LOGIN = ['/Users/fixture/Library/Keychains/login.keychain-db']
results = []


def record(case, **details):
    results.append(dict(case=case, **details))
    print('ok', case)


class Fixture:
    """A fixture source tree holding the scripts, stand-in tools first on PATH and a simulated hosted runner."""

    def __init__(self, temporary, name, scenario=None, team=TEAM, mygo=None):
        self.dir = pathlib.Path(temporary) / name
        self.state = self.dir / 'state'
        self.bin = self.dir / 'bin'
        self.source = self.dir / 'source'
        self.runner_temp = self.dir / 'runner-temp'
        for folder in (self.state, self.bin, self.source / 'scripts', self.source / 'packaging/macos', self.runner_temp):
            folder.mkdir(parents=True)
        (self.state / 'scenario.json').write_text(json.dumps(scenario or {}))
        for tool in ('security', 'codesign', 'xcrun', 'spctl', 'hdiutil', 'diskutil'):
            shim = self.bin / tool
            shim.write_text('#!/bin/sh\nexec "%s" "%s" %s "$@"\n' % (sys.executable, TOOLS, tool))
            shim.chmod(0o755)
        for script in COPIED:
            shutil.copy2(SCRIPTS / script, self.source / 'scripts' / script)
        self.set_team(team)
        (self.source / 'mygo.json').write_text(mygo if mygo is not None else (ROOT / 'mygo.json').read_text())
        for referenced in ('packaging/macos/permissions.entitlements.plist', 'packaging/windows/compatibility.nsh'):
            (self.source / referenced).parent.mkdir(parents=True, exist_ok=True)
            shutil.copy2(ROOT / referenced, self.source / referenced)
        (self.source / 'resources').mkdir()
        (self.source / 'resources/icon.png').write_bytes(b'png')
        (self.source / 'CHANGELOG.md').write_text('# Changelog\n')
        self.output = self.dir / 'github-output'
        self.output.write_text('')

    def set_team(self, team):
        (self.source / 'packaging/macos/signing.json').write_text(json.dumps({'teamId': team}) if team is not None else '')

    def env(self, inputs=None, hosted=True):
        env = {k: v for k, v in os.environ.items() if not k.startswith(('MACOS_SIGNING_', 'MACOS_NOTARY_', 'GITHUB_', 'RUNNER_'))}
        env.update(inputs or {})
        env.update(PATH=str(self.bin) + os.pathsep + env['PATH'], APPLE_TOOLS_STATE=str(self.state), GITHUB_OUTPUT=str(self.output),
                   PYTHONDONTWRITEBYTECODE='1')
        if hosted:
            env.update(GITHUB_ACTIONS='true', RUNNER_ENVIRONMENT='github-hosted', RUNNER_TEMP=str(self.runner_temp))
        return env

    def signing(self, *args, inputs=None, hosted=True, state=None):
        env = self.env(inputs, hosted)
        if state is not None:
            env['SIGNING_STATE'] = state
        return subprocess.run([sys.executable, str(self.source / 'scripts/macos_signing.py'), *args], env=env, capture_output=True, text=True, timeout=60)

    def calls(self, tool=None):
        log = self.state / 'log.jsonl'
        entries = [json.loads(line) for line in log.read_text().splitlines()] if log.exists() else []
        return [e for e in entries if tool is None or e['tool'] == tool]

    def search(self):
        file = self.state / 'search.json'
        return json.loads(file.read_text()) if file.exists() else None

    def outputs(self):
        return dict(line.split('=', 1) for line in self.output.read_text().splitlines())

    def folders(self):
        return sorted(self.runner_temp.glob('yesman-signing-*'))


def full_inputs(**changes):
    inputs = dict(SENTINELS, MACOS_SIGNING_CERTIFICATE_P12=base64.encodebytes(P12).decode())
    inputs.update(changes)
    return {k: v for k, v in inputs.items() if v is not None}


def good_identity():
    return {'identities': [{'sha1': SHA, 'name': NAME}], 'probeTeam': TEAM}


def assert_no_leak(fixture, process):
    """Secrets reach only their two consuming arguments, never output, files or child environments."""
    visible = process.stdout + process.stderr + fixture.output.read_text()
    for path in fixture.dir.rglob('*'):
        if path.is_file() and path.parent != fixture.state:
            visible += path.read_bytes().decode(errors='replace')
    for value in list(SENTINELS.values()) + [base64.encodebytes(P12).decode().strip(), P12.decode()]:
        assert value not in visible, 'secret leaked into output or files'
    for call in fixture.calls():
        assert call['env'] == [], ('signing input in a child environment', call)
        joined = '\0'.join(call['args'])
        allowed = []
        if call['args'][:1] == ['import']:
            allowed = [SENTINELS['MACOS_SIGNING_CERTIFICATE_PASSWORD']]
        if call['args'][:2] == ['notarytool', 'store-credentials']:
            allowed = [SENTINELS['MACOS_NOTARY_APPLE_ID'], SENTINELS['MACOS_NOTARY_PASSWORD']]
        for value in SENTINELS.values():
            assert value in allowed or value not in joined, ('secret in arguments', call['tool'], call['args'][:2])


def assert_clean(fixture):
    assert fixture.folders() == [], ('signing folder remains', fixture.folders())
    after = fixture.search()
    assert after is None or after == LOGIN, ('search list not restored', after)


def snapshot(folder):
    """Every path under folder with its kind and bytes, links unfollowed."""
    found = {}
    for path in sorted([folder] + list(folder.rglob('*'))):
        rel = str(path.relative_to(folder.parent))
        if path.is_symlink():
            found[rel] = 'link:' + os.readlink(path)
        elif path.is_dir():
            found[rel] = 'dir'
        else:
            found[rel] = hashlib.sha256(path.read_bytes()).hexdigest()
    return found


def prepared(temporary, name):
    """A fixture whose setup succeeded, and its state."""
    fixture = Fixture(temporary, name, good_identity())
    process = fixture.signing('setup', inputs=full_inputs())
    assert process.returncode == 0, process.stderr
    return fixture, fixture.outputs()['state']


def setup_cases(temporary):
    fixture = Fixture(temporary, 'unconfigured', team='')
    process = fixture.signing('setup', inputs={})
    assert process.returncode == 0 and fixture.outputs() == {'mode': 'adhoc'}, process.stderr
    assert fixture.calls() == [] and fixture.folders() == []
    record('unconfigured-stays-adhoc')

    fixture = Fixture(temporary, 'no-team-file', team=None)
    (fixture.source / 'packaging/macos/signing.json').unlink()
    process = fixture.signing('setup', inputs={})
    assert process.returncode == 0 and fixture.outputs() == {'mode': 'adhoc'}, process.stderr
    record('older-source-without-team-file-stays-adhoc')

    partial = {
        'team-only': (TEAM, {}),
        'secrets-without-team': ('', full_inputs()),
        'no-notary-password': (TEAM, full_inputs(MACOS_NOTARY_PASSWORD=None)),
        'no-notary-apple-id': (TEAM, full_inputs(MACOS_NOTARY_APPLE_ID=None)),
        'no-certificate-password': (TEAM, full_inputs(MACOS_SIGNING_CERTIFICATE_PASSWORD=None)),
        'blank-certificate': (TEAM, full_inputs(MACOS_SIGNING_CERTIFICATE_P12='  \n')),
        'blank-apple-id': (TEAM, full_inputs(MACOS_NOTARY_APPLE_ID=' \n')),
        # A set variable configures signing, even one holding only whitespace: never a silent ad-hoc build.
        'whitespace-password-only': ('', {'MACOS_NOTARY_PASSWORD': '  '}),
    }
    for name, (team, inputs) in partial.items():
        fixture = Fixture(temporary, 'partial-' + name, good_identity(), team=team)
        process = fixture.signing('setup', inputs=inputs)
        assert process.returncode == 1 and 'only partly configured' in process.stderr, process.stderr
        assert fixture.calls() == [] and fixture.outputs() == {} and fixture.folders() == []
        assert_no_leak(fixture, process)
        record('partial-config-fails', variant=name)

    for name, team_file, message in [
        ('nickname-not-team-id', json.dumps({'teamId': 'idy'}), 'must be the 10-character Team ID'),
        ('extra-keys', json.dumps({'teamId': TEAM, 'name': 'x'}), 'must hold exactly'),
        ('not-json', '{', 'not valid JSON'),
    ]:
        fixture = Fixture(temporary, name, good_identity())
        (fixture.source / 'packaging/macos/signing.json').write_text(team_file)
        process = fixture.signing('setup', inputs=full_inputs())
        assert process.returncode == 1 and message in process.stderr, process.stderr
        assert fixture.calls() == [] and fixture.outputs() == {} and fixture.folders() == []
        record('invalid-team-file-fails', variant=name)

    fixture = Fixture(temporary, 'not-base64', good_identity())
    process = fixture.signing('setup', inputs=full_inputs(MACOS_SIGNING_CERTIFICATE_P12='not base64 !!'))
    assert process.returncode == 1 and 'not base64' in process.stderr and fixture.calls() == [] and fixture.folders() == []
    record('invalid-input-fails', variant='not-base64')

    fixture = Fixture(temporary, 'not-hosted', good_identity())
    process = fixture.signing('setup', inputs=full_inputs(), hosted=False)
    assert process.returncode == 1 and 'GitHub-hosted runner' in process.stderr, process.stderr
    assert fixture.calls() == [] and fixture.outputs() == {}
    record('local-account-search-list-refused')

    # The full path, then cleanup with the state it wrote.
    fixture = Fixture(temporary, 'configured', good_identity())
    process = fixture.signing('setup', inputs=full_inputs())
    assert process.returncode == 0, process.stderr
    outputs = fixture.outputs()
    state = outputs.pop('state')
    assert outputs == {'mode': 'developer-id', 'identity': SHA, 'team': TEAM, 'name': NAME}, outputs
    [folder] = fixture.folders()
    keychain = folder / 'signing.keychain-db'
    assert keychain.is_file() and fixture.search() == [str(keychain)] + LOGIN
    assert oct(folder.stat().st_mode & 0o777) == '0o700' and oct((folder / 'receipt.json').stat().st_mode & 0o777) == '0o600'
    imported = [c for c in fixture.calls('security') if c['args'][0] == 'import'][0]
    assert imported['p12'] == {'mode': '0o600', 'folderMode': '0o700', 'sha256': hashlib.sha256(P12).hexdigest()}
    assert imported['args'][-2:] == ['-T', '/usr/bin/codesign'], 'the key access list names codesign'
    partition = [c for c in fixture.calls('security') if c['args'][0] == 'set-key-partition-list'][0]
    assert partition['args'][1:3] == ['-S', 'apple-tool:,apple:,codesign:'], partition['args'][:3]
    assert not (folder / 'identity.p12').exists(), 'the .p12 must not outlive its import'
    probe = [c for c in fixture.calls('codesign') if c.get('kind') == 'probe']
    assert probe and probe[0]['args'][:3] == ['--force', '--sign', SHA], probe
    requirement = [c['requirement'] for c in fixture.calls('codesign') if c.get('requirement')][0]
    assert 'certificate leaf[field.1.2.840.113635.100.6.1.13] exists' in requirement and 'leaf[subject.OU] = "%s"' % TEAM in requirement
    store = [c for c in fixture.calls('xcrun') if c['args'][:2] == ['notarytool', 'store-credentials']]
    assert len(store) == 1 and store[0]['args'][2] == 'yesman-release-notary', store
    assert store[0]['args'][store[0]['args'].index('--keychain') + 1] == str(keychain)
    assert store[0]['args'][store[0]['args'].index('--team-id') + 1] == TEAM
    assert_no_leak(fixture, process)
    process = fixture.signing('cleanup', state=state)
    assert process.returncode == 0, process.stderr
    assert_clean(fixture)
    assert fixture.signing('cleanup', state=state).returncode == 0, 'cleanup must be repeatable'
    assert fixture.signing('cleanup', state='').returncode == 0, 'cleanup without a state does nothing'
    record('configured-prepares-and-cleans', search=LOGIN)

    failures = {
        'wrong-team-certificate': ({'identities': [{'sha1': SHA, 'name': 'Developer ID Application: Fixture Person (OTHERTEAM1)'}]}, 'belongs to team OTHERTEAM1'),
        'apple-development-certificate': ({'identities': [{'sha1': SHA, 'name': 'Apple Development: fixture@example.invalid (%s)' % TEAM}]}, 'not a "Developer ID Application"'),
        'installer-certificate': ({'identities': [{'sha1': SHA, 'name': 'Developer ID Installer: Fixture Person (%s)' % TEAM}]}, 'not a "Developer ID Application"'),
        'two-identities': ({'identities': [{'sha1': SHA, 'name': NAME}, {'sha1': 'F' * 40, 'name': 'Apple Development: x (%s)' % TEAM}]}, 'exactly one signing identity'),
        'no-identity': ({'identities': []}, 'exactly one signing identity'),
        'expired-or-untrusted': ({'identities': [{'sha1': SHA, 'name': NAME, 'valid': False}]}, 'not valid on the runner'),
        'not-apple-issued': (dict(good_identity(), fail=['probe-requirement']), 'Checking the Developer ID requirement failed'),
        'probe-other-team': (dict(good_identity(), probeTeam='OTHERTEAM1'), 'names another team'),
        'wrong-p12-password': (dict(good_identity(), fail=['import']), 'Importing the signing certificate failed'),
        'keychain-not-created': (dict(good_identity(), fail=['create-keychain']), 'Creating the signing keychain failed'),
        'settings-fail': (dict(good_identity(), fail=['set-keychain-settings']), 'Configuring the signing keychain failed'),
        'unlock-fails': (dict(good_identity(), fail=['unlock-keychain']), 'Unlocking the signing keychain failed'),
        'partition-list-fails': (dict(good_identity(), fail=['set-key-partition-list']), 'Allowing codesign to use the key failed'),
        'probe-sign-fails': (dict(good_identity(), fail=['probe-sign']), 'Signing a preflight file failed'),
        'notary-credentials-rejected': (dict(good_identity(), fail=['store-credentials']), 'Storing the notary credentials failed'),
    }
    for name, (scenario, message) in failures.items():
        fixture = Fixture(temporary, name, scenario)
        process = fixture.signing('setup', inputs=full_inputs())
        assert process.returncode == 1 and message in process.stderr, (name, process.stderr)
        outputs = fixture.outputs()
        assert 'mode' not in outputs and 'state' in outputs, 'a failed setup hands the build no mode, the cleanup its state'
        assert_clean(fixture)
        assert_no_leak(fixture, process)
        if name in ('wrong-p12-password', 'notary-credentials-rejected'):
            assert '***' in process.stderr, 'tool output echoing a secret must be redacted'
        assert fixture.signing('cleanup', state=outputs['state']).returncode == 0
        record('setup-failure-cleans-up', variant=name)


def ownership_cases(temporary):
    """F2: cleanup removes only the directory this setup made."""
    # The independent review's case: a foreign directory at the old fixed
    # path, with a linked marker; setup refuses, the always-run cleanup runs.
    fixture = Fixture(temporary, 'foreign-linked-marker', good_identity(), team='')
    old = fixture.dir / 'tmp/yesman-release-signing'
    old.mkdir(parents=True)
    unrelated = fixture.dir / 'unrelated-regular-file'
    unrelated.write_text('not a setup receipt')
    (old / '.made-by-macos-signing').symlink_to(unrelated)
    (old / 'receipt.json').symlink_to(unrelated)
    (old / 'unrelated-important-file').write_text('preserve me')
    before = snapshot(old)
    process = fixture.signing('setup', inputs=full_inputs())
    assert process.returncode == 1 and 'only partly configured' in process.stderr
    process = fixture.signing('cleanup', state=fixture.outputs().get('state', ''))
    assert process.returncode == 0 and snapshot(old) == before and unrelated.read_text() == 'not a setup receipt'
    record('foreign-linked-marker-preserved', setupRefused=True, cleanupExit=process.returncode, foreignPreserved=True)

    def forged(folder, nonce, dev=None, ino=None):
        info = os.lstat(folder)
        state = {'dir': str(folder), 'dev': info.st_dev if dev is None else dev, 'ino': info.st_ino if ino is None else ino, 'nonce': nonce}
        return base64.urlsafe_b64encode(json.dumps(state).encode()).decode()

    nonce = 'a' * 64
    for name in ('linked-receipt', 'copied-receipt-other-nonce', 'group-readable'):
        fixture = Fixture(temporary, 'forged-' + name, good_identity())
        foreign = fixture.runner_temp / 'yesman-signing-foreign'
        foreign.mkdir(mode=0o700)
        (foreign / 'unrelated-important-file').write_text('preserve me')
        if name == 'linked-receipt':
            target = fixture.dir / 'nonce-file'
            target.write_text(json.dumps({'nonce': nonce}))
            (foreign / 'receipt.json').symlink_to(target)
        elif name == 'copied-receipt-other-nonce':
            (foreign / 'receipt.json').write_text(json.dumps({'nonce': 'b' * 64}))
        else:
            (foreign / 'receipt.json').write_text(json.dumps({'nonce': nonce}))
            foreign.chmod(0o750)
        before = snapshot(foreign)
        process = fixture.signing('cleanup', state=forged(foreign, nonce))
        assert process.returncode == 1 and 'leaving it alone' in process.stderr and snapshot(foreign) == before, (name, process.stderr)
        record('forged-state-refused', variant=name)

    # A directory setup really made, then replaced at the same path by one
    # holding a copy of its receipt.
    fixture, state = prepared(temporary, 'replaced-directory')
    [folder] = fixture.folders()
    moved = fixture.dir / 'moved-away'
    folder.rename(moved)
    folder.mkdir(mode=0o700)
    shutil.copy2(moved / 'receipt.json', folder / 'receipt.json')
    (folder / 'unrelated-important-file').write_text('preserve me')
    before = snapshot(folder)
    process = fixture.signing('cleanup', state=state)
    assert process.returncode == 1 and 'leaving it alone' in process.stderr and snapshot(folder) == before
    record('replaced-directory-refused')

    # TERM while importing: setup's own cleanup removes the .p12, keychain and directory.
    fixture = Fixture(temporary, 'term-during-import', good_identity())
    ready = fixture.dir / 'ready'
    (fixture.bin / 'security').write_text('#!%s\nimport os, pathlib, sys, time\nif sys.argv[1] == "import":\n    pathlib.Path(%r).touch()\n    time.sleep(30)\nos.execv(%r, [%r, %r, "security"] + sys.argv[1:])\n'
                                          % (sys.executable, str(ready), sys.executable, sys.executable, str(TOOLS)))
    process = subprocess.Popen([sys.executable, str(fixture.source / 'scripts/macos_signing.py'), 'setup'], env=fixture.env(full_inputs()),
                               stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
    limit = time.monotonic() + 15
    while not ready.exists() and process.poll() is None and time.monotonic() < limit:
        time.sleep(0.05)
    assert ready.exists()
    process.send_signal(signal.SIGTERM)
    _, stderr = process.communicate(timeout=20)
    assert process.returncode == 1 and 'Stopped by signal' in stderr, (process.returncode, stderr)
    assert_clean(fixture)
    assert fixture.signing('cleanup', state=fixture.outputs()['state']).returncode == 0
    record('term-during-import-cleans-up', exit=process.returncode)

    # A tool that never returns: the deadline fails setup, which cleans up.
    fixture = Fixture(temporary, 'tool-deadline', good_identity())
    (fixture.bin / 'security').write_text('#!%s\nimport os, sys, time\nif sys.argv[1] == "set-key-partition-list":\n    time.sleep(30)\nos.execv(%r, [%r, %r, "security"] + sys.argv[1:])\n'
                                          % (sys.executable, sys.executable, sys.executable, str(TOOLS)))
    code = 'import sys; sys.argv = ["macos_signing.py", "setup"]; sys.path.insert(0, %r); import macos_signing as m; m.DEADLINE = 2; m.main()' % str(fixture.source / 'scripts')
    process = subprocess.run([sys.executable, '-c', code], env=fixture.env(full_inputs()), capture_output=True, text=True, timeout=60)
    assert process.returncode == 1 and 'did not finish within 2 seconds' in process.stderr, process.stderr
    assert_clean(fixture)
    record('tool-deadline-cleans-up')


def alive(pid):
    r = subprocess.run(['ps', '-p', str(pid), '-o', 'stat='], capture_output=True, text=True)
    return r.returncode == 0 and r.stdout.strip() != '' and not r.stdout.strip().startswith('Z')


def descendant_tool(fixture, tool, trigger, pidfile, parent_sleep=30):
    """A stand-in tool that, on trigger, starts a long-lived descendant and keeps running."""
    code = ('#!%s\nimport os, pathlib, subprocess, sys, time\nif %s:\n'
            '    child = subprocess.Popen([%r, "-c", "import time; time.sleep(20)"])\n'
            '    pathlib.Path(%r).write_text(str(child.pid))\n    time.sleep(%d)\n') % (sys.executable, trigger, sys.executable, str(pidfile), parent_sleep)
    if tool != 'go':
        code += 'os.execv(%r, [%r, %r, %r] + sys.argv[1:])\n' % (sys.executable, sys.executable, str(TOOLS), tool)
    (fixture.bin / tool).write_text(code)
    (fixture.bin / tool).chmod(0o755)


def lifecycle_cases(temporary):
    """R2: deletion through the held directory, non-blocking receipts, owned process groups, aliases and outputs."""
    # A directory setup made, replaced after cleanup's second ownership check: the replacement survives.
    fixture, state = prepared(temporary, 'replaced-after-check')
    [folder] = fixture.folders()
    moved = fixture.dir / 'moved-away'
    code = ('import os, pathlib, sys\nsys.path.insert(0, %r)\nimport macos_signing as m\noriginal, calls = m.owned, [0]\n'
            'def owned(folder, state):\n    calls[0] += 1\n    result = original(folder, state)\n'
            '    if calls[0] == 2 and result:\n        folder.rename(%r)\n        folder.mkdir(mode=0o700)\n'
            '        (folder / "unrelated").write_text("keep")\n    return result\n'
            'm.owned = owned\nsys.argv = ["m", "cleanup"]\nm.main()\n') % (str(fixture.source / 'scripts'), str(moved))
    env = fixture.env()
    env['SIGNING_STATE'] = state
    process = subprocess.run([sys.executable, '-c', code], env=env, capture_output=True, text=True, timeout=60)
    assert process.returncode == 1 and 'replaced during cleanup' in process.stderr, process.stderr
    assert (folder / 'unrelated').read_text() == 'keep' and moved.is_dir() and list(moved.iterdir()) == []
    record('replacement-after-check-survives')

    # A FIFO receipt is refused at once, by cleanup and by the build's state check.
    fixture, state = prepared(temporary, 'fifo-receipt')
    [folder] = fixture.folders()
    (folder / 'receipt.json').unlink()
    os.mkfifo(folder / 'receipt.json', 0o600)
    started = time.monotonic()
    process = fixture.signing('cleanup', state=state)
    assert process.returncode == 1 and 'leaving it alone' in process.stderr and time.monotonic() - started < 10, process.stderr
    assert stat.S_ISFIFO(os.lstat(folder / 'receipt.json').st_mode)
    code = 'import sys\nsys.path.insert(0, %r)\nimport macos_signing as m\ntry:\n    m.prepared(%r)\nexcept m.SigningError:\n    print("refused")\n' % (
        str(fixture.source / 'scripts'), state)
    process = subprocess.run([sys.executable, '-c', code], capture_output=True, text=True, timeout=10)
    assert process.stdout.strip() == 'refused', process.stdout + process.stderr
    os.unlink(folder / 'receipt.json')
    shutil.rmtree(folder)
    record('fifo-receipt-refused-without-waiting')

    # A tool's descendants are stopped on a deadline and on TERM/INT/HUP, before files go.
    fixture = Fixture(temporary, 'deadline-descendant', good_identity())
    pidfile = fixture.dir / 'child-pid'
    descendant_tool(fixture, 'security', 'sys.argv[1] == "import"', pidfile)
    code = 'import sys; sys.argv = ["macos_signing.py", "setup"]; sys.path.insert(0, %r); import macos_signing as m; m.DEADLINE = 2; m.main()' % str(fixture.source / 'scripts')
    process = subprocess.run([sys.executable, '-c', code], env=fixture.env(full_inputs()), capture_output=True, text=True, timeout=60)
    assert process.returncode == 1 and 'did not finish within 2 seconds' in process.stderr, process.stderr
    assert not alive(int(pidfile.read_text())), 'the descendant must be stopped'
    assert_clean(fixture)
    record('deadline-stops-descendants')
    for number in (signal.SIGTERM, signal.SIGINT, signal.SIGHUP):
        fixture = Fixture(temporary, 'signal-descendant-%d' % number, good_identity())
        pidfile = fixture.dir / 'child-pid'
        descendant_tool(fixture, 'security', 'sys.argv[1] == "import"', pidfile)
        process = subprocess.Popen([sys.executable, str(fixture.source / 'scripts/macos_signing.py'), 'setup'], env=fixture.env(full_inputs()),
                                   stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
        limit = time.monotonic() + 15
        while not pidfile.exists() and process.poll() is None and time.monotonic() < limit:
            time.sleep(0.05)
        process.send_signal(number)
        _, stderr = process.communicate(timeout=30)
        assert process.returncode == 1 and 'Stopped by signal' in stderr, stderr
        assert not alive(int(pidfile.read_text())) and not list(fixture.dir.rglob('identity.p12'))
        assert_clean(fixture)
        record('signal-stops-descendants', signal=int(number))

    # The release launcher: TERM stops MyGo's whole group before the key, token and config go.
    fixture = Fixture(temporary, 'launch-term-descendant', {})
    pidfile = fixture.dir / 'child-pid'
    descendant_tool(fixture, 'go', 'True', pidfile)
    bystander = subprocess.Popen([sys.executable, '-c', 'import time; time.sleep(30)'], start_new_session=True)
    env = fixture.env({'MYGO_UPDATER_PRIVATE_KEY': UPDATER_KEY})
    process = subprocess.Popen([sys.executable, str(fixture.source / 'scripts/release_build.py'), '--platform', 'linux/amd64', '--system', 'Linux'],
                               env=env, cwd=fixture.source, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
    limit = time.monotonic() + 15
    while not pidfile.exists() and process.poll() is None and time.monotonic() < limit:
        time.sleep(0.05)
    process.send_signal(signal.SIGTERM)
    _, stderr = process.communicate(timeout=30)
    assert process.returncode == 1 and 'Stopped by signal' in stderr, stderr
    assert not alive(int(pidfile.read_text())) and alive(bystander.pid), 'only the launcher\'s own group is stopped'
    assert not list((fixture.source / 'build').iterdir()) and not list(fixture.runner_temp.glob('yesman-release-*'))
    bystander.kill()
    bystander.wait()
    record('launcher-term-stops-own-group-only')

    # An Apple input's value under another name is refused before any tool runs.
    fixture = Fixture(temporary, 'apple-value-alias', good_identity())
    process = fixture.signing('setup', inputs=dict(full_inputs(), SOME_ALIAS=SENTINELS['MACOS_NOTARY_PASSWORD']))
    assert process.returncode == 1 and 'another environment variable' in process.stderr and fixture.calls() == [] and fixture.folders() == []
    record('apple-value-alias-refused')

    # A failed GITHUB_OUTPUT write, first or last, fails setup and leaves nothing.
    for ordinal in (1, 2):
        fixture = Fixture(temporary, 'output-failure-%d' % ordinal, good_identity())
        code = ('import sys\nsys.path.insert(0, %r)\nimport macos_signing as m\noriginal, n = m.write_output, [0]\n'
                'def output(values):\n    n[0] += 1\n    if n[0] == %d:\n        raise m.SigningError("injected")\n    return original(values)\n'
                'm.write_output = output\nsys.argv = ["m", "setup"]\nm.main()\n') % (str(fixture.source / 'scripts'), ordinal)
        process = subprocess.run([sys.executable, '-c', code], env=fixture.env(full_inputs()), capture_output=True, text=True, timeout=60)
        assert process.returncode == 1 and fixture.folders() == [], (ordinal, process.stderr)
        assert_clean(fixture)
        record('output-failure-cleans-up', write=ordinal)


def stubborn_tool(fixture, tool, trigger, pidfile, parent_exits=False):
    """A stand-in tool that, on trigger, starts a descendant ignoring TERM; it then sleeps, or exits at once."""
    child = 'import os, pathlib, signal, time; signal.signal(signal.SIGTERM, signal.SIG_IGN); pathlib.Path(%r).write_text(str(os.getpid())); time.sleep(40)' % str(pidfile)
    code = ('#!%s\nimport os, pathlib, subprocess, sys, time\nif %s:\n'
            '    subprocess.Popen([%r, "-c", %r], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)\n'
            '    while not pathlib.Path(%r).exists():\n        time.sleep(0.01)\n'
            '    %s\n') % (sys.executable, trigger, sys.executable, child, str(pidfile), 'sys.exit(0)' if parent_exits else 'time.sleep(40)')
    if tool != 'go':
        code += 'os.execv(%r, [%r, %r, %r] + sys.argv[1:])\n' % (sys.executable, sys.executable, str(TOOLS), tool)
    (fixture.bin / tool).write_text(code)
    (fixture.bin / tool).chmod(0o755)


def gone(pid, seconds=5):
    end = time.monotonic() + seconds
    while alive(pid) and time.monotonic() < end:
        time.sleep(0.05)
    return not alive(pid)


# Run inside the process under test only: its group listing fails, and/or it may not signal groups.
UNKNOWN = 'm.group_members = lambda pgid: None\n'
DENIED = 'def denied(pgid, number):\n    raise PermissionError(1, "injected killpg failure")\nm.os.killpg = denied\n'
SHORT = 'import signal\nm.GRACE = ((signal.SIGTERM, 0.3), (signal.SIGKILL, 0.3))\n'


def retention_cases(temporary):
    """R3: no path-reopening deletion; files kept while a process group is not confirmed stopped, removed once it is."""
    # A keychain swapped in at the path after the last check is never deleted: no tool deletes by path.
    for hook in ('names', 'set_search_list'):
        fixture, state = prepared(temporary, 'keychain-swap-%s' % hook)
        [folder] = fixture.folders()
        moved = fixture.dir / 'moved-away'
        code = ('import os, pathlib, sys\nsys.path.insert(0, %r)\nimport macos_signing as m\noriginal, calls = m.%s, [0]\n'
                'def hooked(*args):\n    result = original(*args)\n    calls[0] += 1\n    if calls[0] == 1:\n'
                '        folder = pathlib.Path(%r)\n        folder.rename(%r)\n        folder.mkdir(mode=0o700)\n'
                '        (folder / m.KEYCHAIN).write_text("someone else\'s keychain")\n        (folder / "other").write_text("keep")\n    return result\n'
                'm.%s = hooked\nsys.argv = ["m", "cleanup"]\nm.main()\n') % (str(fixture.source / 'scripts'), hook, str(folder), str(moved), hook)
        env = fixture.env()
        env['SIGNING_STATE'] = state
        process = subprocess.run([sys.executable, '-c', code], env=env, capture_output=True, text=True, timeout=60)
        assert process.returncode == 1, process.stderr
        assert (folder / 'signing.keychain-db').read_text() == "someone else's keychain" and (folder / 'other').read_text() == 'keep'
        assert moved.is_dir() and list(moved.iterdir()) == [], 'setup\'s own directory is emptied where it went'
        assert not [c for c in fixture.calls('security') if c['args'][:1] == ['delete-keychain']]
        assert fixture.search() == LOGIN
        record('swapped-keychain-survives', hook=hook)

    # Setup: a tool's descendant ignores TERM and its group cannot be signalled. The p12, keychain and
    # directory stay, with the use record made before the tool ran; the always-run cleanup refuses while
    # the group lives and removes once it is empty.
    fixture = Fixture(temporary, 'setup-undrained', good_identity())
    pidfile = fixture.dir / 'child-pid'
    stubborn_tool(fixture, 'security', 'sys.argv[1] == "import"', pidfile)
    code = 'import sys; sys.argv = ["m", "setup"]; sys.path.insert(0, %r); import macos_signing as m\nm.DEADLINE = 2\n%s%sm.main()\n' % (
        str(fixture.source / 'scripts'), SHORT, DENIED)
    process = subprocess.run([sys.executable, '-c', code], env=fixture.env(full_inputs()), capture_output=True, text=True, timeout=60)
    assert process.returncode == 1 and 'files they may use are kept' in process.stderr and 'Kept ' in process.stderr, process.stderr
    [folder] = fixture.folders()
    groups = use_records(folder)
    assert len(groups) == 1, groups
    assert alive(int(pidfile.read_text())) and (folder / 'identity.p12').is_file() and (folder / 'signing.keychain-db').is_file()
    before = snapshot(folder)
    state = fixture.outputs()['state']
    process = fixture.signing('cleanup', state=state)
    assert process.returncode == 1 and 'still running' in process.stderr and snapshot(folder) == before, process.stderr
    for group in groups:
        os.killpg(group, signal.SIGKILL)  # the fixture's own processes
    assert gone(int(pidfile.read_text()))
    process = fixture.signing('cleanup', state=state)
    assert process.returncode == 0, process.stderr
    assert_clean(fixture)
    record('setup-undrained-kept-then-cleaned', groups=len(groups))

    # The launcher: MyGo exits, leaving a descendant that ignores TERM. With the group unknown (and,
    # in one case, not signallable) the key, token and config stay, marked; --remove-retained removes
    # them only once the group is confirmed empty, signalling nothing.
    for case, injected in (('unknown-and-denied', UNKNOWN + DENIED), ('unknown', UNKNOWN)):
        fixture = Fixture(temporary, 'launch-undrained-' + case, {})
        pidfile = fixture.dir / 'child-pid'
        stubborn_tool(fixture, 'go', 'True', pidfile, parent_exits=True)
        bystander = subprocess.Popen([sys.executable, '-c', 'import time; time.sleep(30)'], start_new_session=True)
        code = ('import sys; sys.argv = ["b", "--platform", "linux/amd64", "--system", "Linux"]; sys.path.insert(0, %r)\n'
                'import macos_signing as m, release_build as b\n%s%sb.main()\n') % (str(fixture.source / 'scripts'), SHORT, injected)
        env = fixture.env({'MYGO_UPDATER_PRIVATE_KEY': UPDATER_KEY})
        with open(fixture.dir / 'launcher.log', 'w+') as log:  # not a pipe: the descendant holds it
            process = subprocess.run([sys.executable, '-c', code], env=env, cwd=fixture.source, stdout=log, stderr=log, timeout=60)
            log.seek(0)
            output = log.read()
        kept = sorted(fixture.runner_temp.glob('yesman-release-*')) + sorted((fixture.source / 'build').glob('.release-config-*'))
        assert process.returncode == 1 and 'could not tell' in output and '--remove-retained' in output, output
        assert len(kept) == 2 and (kept[0] / 'updater.key').read_text().strip() == UPDATER_KEY and (kept[1] / 'mygo.json').is_file()
        assert all(len(use_records(k)) == 1 for k in kept)
        pid = int(pidfile.read_text())
        if case == 'unknown-and-denied':
            assert alive(pid)
            process = subprocess.run([sys.executable, str(fixture.source / 'scripts/release_build.py'), '--remove-retained', *map(str, kept)],
                                     env=fixture.env(), capture_output=True, text=True, timeout=60)
            assert process.returncode == 1 and 'still running' in process.stderr and all(k.is_dir() for k in kept), process.stderr
            assert alive(pid), 'removing kept files signals nothing'
            os.kill(pid, signal.SIGKILL)  # the fixture's own process
        assert gone(pid), 'with signals allowed the group was stopped; only its listing failed'
        process = subprocess.run([sys.executable, str(fixture.source / 'scripts/release_build.py'), '--remove-retained', *map(str, kept)],
                                 env=fixture.env(), capture_output=True, text=True, timeout=60)
        assert process.returncode == 0 and not any(os.path.lexists(k) for k in kept), process.stderr
        assert alive(bystander.pid), 'an unrelated group is never signalled'
        bystander.kill()
        bystander.wait()
        record('launcher-undrained-kept-then-removed', injected=case)

    # --remove-retained leaves alone a directory that no failed run kept.
    fixture = Fixture(temporary, 'remove-retained-refuses', {})
    other = fixture.runner_temp / 'yesman-release-other'
    other.mkdir(mode=0o700)
    (other / 'updater.key').write_text('not ours to remove')
    process = subprocess.run([sys.executable, str(fixture.source / 'scripts/release_build.py'), '--remove-retained', str(other)],
                             env=fixture.env(), capture_output=True, text=True, timeout=60)
    assert process.returncode == 1 and (other / 'updater.key').is_file(), process.stderr
    record('remove-retained-needs-marker')

    use_record_cases(temporary)


def use_records(folder):
    return sorted(int(p.name[4:]) for p in pathlib.Path(folder).iterdir() if p.name.startswith('use-'))


def use_record_cases(temporary):
    """R4 review: use records exist before a tool runs; failing to make one means the tool never runs."""
    # Making the record fails (no space, or its fsync fails): the tool is not started, setup cleans up.
    for case, injected in (
        ('create-enospc', 'orig = m.os.open\ndef opened(name, *a, **k):\n    if str(name).startswith("use-"):\n'
                          '        raise OSError(28, "injected no space")\n    return orig(name, *a, **k)\nm.os.open = opened\n'),
        ('fsync-eio', 'orig = m.os.fsync\ndef synced(fd):\n    raise OSError(5, "injected fsync failure")\nm.os.fsync = synced\n'),
    ):
        fixture = Fixture(temporary, 'record-' + case, good_identity())
        code = 'import sys; sys.argv = ["m", "setup"]; sys.path.insert(0, %r); import macos_signing as m\n%sm.main()\n' % (
            str(fixture.source / 'scripts'), injected)
        process = subprocess.run([sys.executable, '-c', code], env=fixture.env(full_inputs()), capture_output=True, text=True, timeout=60)
        assert process.returncode == 1 and 'was not started' in process.stderr, process.stderr
        tools = [c['args'][:1] for c in fixture.calls()]
        assert tools in ([], [['list-keychains']] * len(tools)), ('no tool but cleanup\'s search-list reads ran', tools)
        assert_clean(fixture)
        record('use-record-failure-starts-nothing', variant=case)

    # Withdrawing a record fails after a confirmed stop: setup still succeeds; the stale records name
    # groups with no member, which cleanup confirms before removing everything.
    fixture = Fixture(temporary, 'record-withdraw-fails', good_identity())
    code = ('import sys; sys.argv = ["m", "setup"]; sys.path.insert(0, %r); import macos_signing as m\n'
            'orig = m.os.unlink\ndef unlinked(name, *a, **k):\n    if str(name).startswith("use-"):\n        raise OSError(5, "injected")\n'
            '    return orig(name, *a, **k)\nm.os.unlink = unlinked\nm.main()\n') % str(fixture.source / 'scripts')
    process = subprocess.run([sys.executable, '-c', code], env=fixture.env(full_inputs()), capture_output=True, text=True, timeout=60)
    assert process.returncode == 0, process.stderr
    [folder] = fixture.folders()
    assert len(use_records(folder)) >= 5, 'every setup tool left its record'
    process = fixture.signing('cleanup', state=fixture.outputs()['state'])
    assert process.returncode == 0, process.stderr
    assert_clean(fixture)
    record('stale-use-records-confirmed-empty-then-cleaned')

    # A malformed record, or another step holding the directory's lock: cleanup removes nothing.
    for case in ('malformed', 'temporary', 'locked'):
        fixture, state = prepared(temporary, 'record-' + case)
        [folder] = fixture.folders()
        holder = None
        if case == 'malformed':
            (folder / 'use-abc').write_text('')
        elif case == 'temporary':
            (folder / '.use-123.tmp').write_text('{"gr')
        else:
            holder = subprocess.Popen([sys.executable, '-c', 'import fcntl, os, sys, time\nfd = os.open(sys.argv[1], os.O_RDONLY)\n'
                                       'fcntl.flock(fd, fcntl.LOCK_EX)\nprint("held", flush=True)\ntime.sleep(30)\n', str(folder)],
                                      stdout=subprocess.PIPE, text=True)
            assert holder.stdout.readline().strip() == 'held'
        before = snapshot(folder)
        code = 'import sys; sys.path.insert(0, %r); import macos_signing as m\nm.LOCK_WAIT = 0.5\nsys.argv = ["m", "cleanup"]\nm.main()\n' % str(
            fixture.source / 'scripts')
        env = fixture.env()
        env['SIGNING_STATE'] = state
        process = subprocess.run([sys.executable, '-c', code], env=env, capture_output=True, text=True, timeout=60)
        expected = 'in use by another step' if case == 'locked' else 'unknown use record'
        assert process.returncode == 1 and expected in process.stderr and snapshot(folder) == before, process.stderr
        if holder:
            holder.kill()
            holder.wait()
            assert fixture.signing('cleanup', state=state).returncode == 0
            assert_clean(fixture)
        else:
            shutil.rmtree(folder)
        record('cleanup-refuses-unreadable-use', variant=case)

    # The Developer ID launcher borrows setup's keychain: MyGo's group is recorded in the signing directory
    # before MyGo runs, so the always-run cleanup keeps the keychain while a descendant may use it.
    fixture, state = prepared(temporary, 'borrowed-keychain')
    [folder] = fixture.folders()
    pidfile = fixture.dir / 'child-pid'
    stubborn_tool(fixture, 'go', 'True', pidfile, parent_exits=True)
    code = ('import sys; sys.argv = ["b", "--platform", "darwin/arm64", "--system", "macOS", "--signing-mode", "developer-id", '
            '"--signing-identity", %r]; sys.path.insert(0, %r)\nimport macos_signing as m, release_build as b\n%s%sb.main()\n') % (
        SHA, str(fixture.source / 'scripts'), SHORT, UNKNOWN + DENIED)
    env = fixture.env({'MYGO_UPDATER_PRIVATE_KEY': UPDATER_KEY})
    env['SIGNING_STATE'] = state
    with open(fixture.dir / 'launcher.log', 'w+') as log:
        process = subprocess.run([sys.executable, '-c', code], env=env, cwd=fixture.source, stdout=log, stderr=log, timeout=60)
        log.seek(0)
        output = log.read()
    pid = int(pidfile.read_text())
    assert process.returncode == 1 and 'could not tell' in output and 'signing keychain' in output, output
    assert len(use_records(folder)) == 1 and alive(pid)
    before = snapshot(folder)
    cleanup = fixture.signing('cleanup', state=state)
    assert cleanup.returncode == 1 and 'still running' in cleanup.stderr and snapshot(folder) == before, cleanup.stderr
    assert alive(pid), 'cleanup signals nothing'
    os.kill(pid, signal.SIGKILL)  # the fixture's own process
    assert gone(pid)
    assert fixture.signing('cleanup', state=state).returncode == 0
    kept = sorted(fixture.runner_temp.glob('yesman-release-*')) + sorted((fixture.source / 'build').glob('.release-config-*'))
    process = subprocess.run([sys.executable, str(fixture.source / 'scripts/release_build.py'), '--remove-retained', *map(str, kept)],
                             env=fixture.env(), capture_output=True, text=True, timeout=60)
    assert process.returncode == 0 and not any(os.path.lexists(k) for k in kept), process.stderr
    assert_clean(fixture)
    record('borrowed-keychain-kept-while-build-may-use-it')

    # The borrowed directory is gone before the build: MyGo is not started, the launcher's files go.
    fixture, state = prepared(temporary, 'borrowed-gone')
    report, real_bin = fake_go(fixture)
    code = ('import sys; sys.argv = ["b", "--platform", "darwin/arm64", "--system", "macOS", "--signing-mode", "developer-id", '
            '"--signing-identity", %r]; sys.path.insert(0, %r)\nimport macos_signing as m, release_build as b\n'
            'orig = m.record_use\ndef recorded(participant, group):\n    if participant.label.startswith(%r):\n'
            '        (__import__("pathlib").Path(participant.label) / "receipt.json").unlink()\n    return orig(participant, group)\n'
            'm.record_use = recorded\nb.main()\n') % (SHA, str(fixture.source / 'scripts'), str(fixture.runner_temp / 'yesman-signing-'))
    env = fixture.env({'MYGO_UPDATER_PRIVATE_KEY': UPDATER_KEY})
    env['SIGNING_STATE'] = state
    env['PATH'] = str(real_bin) + os.pathsep + env['PATH']
    process = subprocess.run([sys.executable, '-c', code], env=env, cwd=fixture.source, capture_output=True, text=True, timeout=60)
    assert process.returncode == 1 and 'was not started' in process.stderr and not report.exists(), process.stderr
    assert not list(fixture.runner_temp.glob('yesman-release-*')) and not list((fixture.source / 'build').iterdir())
    record('borrowed-directory-changed-build-not-started')


def admitted_tool(fixture, effect, seconds):
    """A stand-in go that reports its PID and config once running, then stays for seconds."""
    (fixture.bin / 'go').write_text(
        '#!%s\nimport json, os, pathlib, sys, time\nconfig = json.loads((pathlib.Path(sys.argv[-1]) / "mygo.json").read_text())\n'
        'pathlib.Path(%r).write_text(json.dumps({"pid": os.getpid(), "key": config["updates"]["privateKey"]}))\ntime.sleep(%s)\n'
        % (sys.executable, str(effect), seconds))
    (fixture.bin / 'go').chmod(0o755)


# Raises once from os.close of the gate's release end, after the real close and once the tool runs.
CLOSE_INTERRUPTED = (
    'import errno, pathlib, time\n'
    'written, closing, released = m.os.write, m.os.close, []\n'
    'def write(fd, data):\n'
    '    count = written(fd, data)\n'
    '    if data == b"1":\n'
    '        released.append(fd)\n'
    '    return count\n'
    'def close(fd):\n'
    '    closing(fd)\n'
    '    if released and fd == released[0]:\n'
    '        released.clear()\n'
    '        end = time.monotonic() + 5\n'
    '        while not pathlib.Path(EFFECT).exists() and time.monotonic() < end:\n'
    '            time.sleep(0.01)\n'
    '        raise InterruptedError(errno.EINTR, "injected close failure after the gate opened")\n'
    'm.os.write, m.os.close = write, close\n')


def admission_cases(temporary):
    """R5 review: once the gate may have let a tool through, any error stops it, or keeps its files as undrained."""
    for case, injected, seconds in (
        ('gate-close-interrupted', CLOSE_INTERRUPTED, 30),
        ('gate-close-interrupted-unconfirmed', CLOSE_INTERRUPTED + UNKNOWN + DENIED, 30),
        ('stop-fails', 'def failing(pgid):\n    raise RuntimeError("injected stop failure")\nm.stop_group = failing\n', 0),
    ):
        fixture, state = prepared(temporary, 'admitted-' + case)
        [folder] = fixture.folders()
        effect = fixture.dir / 'effect'
        admitted_tool(fixture, effect, seconds)
        code = ('import sys; sys.argv = ["b", "--platform", "darwin/arm64", "--system", "macOS", "--signing-mode", "developer-id", '
                '"--signing-identity", %r]; sys.path.insert(0, %r)\nimport macos_signing as m, release_build as b\nEFFECT = %r\n%s%sb.main()\n') % (
            SHA, str(fixture.source / 'scripts'), str(effect), SHORT, injected)
        env = fixture.env({'MYGO_UPDATER_PRIVATE_KEY': UPDATER_KEY})
        env['SIGNING_STATE'] = state
        with open(fixture.dir / 'launcher.log', 'w+') as log:  # not a pipe: a kept tool holds it
            process = subprocess.run([sys.executable, '-c', code], env=env, cwd=fixture.source, stdout=log, stderr=log, timeout=60)
            log.seek(0)
            output = log.read()
        observed = json.loads(effect.read_text())
        pid, key = observed['pid'], pathlib.Path(observed['key'])
        kept = sorted(fixture.runner_temp.glob('yesman-release-*')) + sorted((fixture.source / 'build').glob('.release-config-*'))
        assert process.returncode == 1, output
        if case == 'gate-close-interrupted':
            # Stopped and reaped like any interrupted tool; only then are its files removed.
            assert 'supervising it failed' in output and gone(pid, 1), output
            assert kept == [] and not use_records(folder), (kept, output)
            assert fixture.signing('cleanup', state=state).returncode == 0
            assert_clean(fixture)
        else:
            # Not confirmed stopped: the key, config and borrowed keychain stay, recorded, until it is.
            assert '--remove-retained' in output and key.read_text().strip() == UPDATER_KEY and len(kept) == 2, output
            assert use_records(folder) and all(use_records(k) for k in kept)
            if case != 'stop-fails':
                assert alive(pid)
                before = snapshot(folder)
                cleanup = fixture.signing('cleanup', state=state)
                assert cleanup.returncode == 1 and 'still running' in cleanup.stderr and snapshot(folder) == before, cleanup.stderr
                assert alive(pid), 'cleanup signals nothing'
                os.killpg(pid, signal.SIGKILL)  # the fixture's own group
            assert gone(pid)
            assert fixture.signing('cleanup', state=state).returncode == 0
            process = subprocess.run([sys.executable, str(fixture.source / 'scripts/release_build.py'), '--remove-retained', *map(str, kept)],
                                     env=fixture.env(), capture_output=True, text=True, timeout=60)
            assert process.returncode == 0 and not any(os.path.lexists(k) for k in kept), process.stderr
            assert_clean(fixture)
        record('admitted-tool-error-stops-or-keeps', variant=case)


def password_cases(temporary):
    """R5 review: passwords reach security and notarytool byte for byte; only base64 and the Apple ID are normalized."""
    for case, certificate, notary in (
        ('leading-space', ' fixture certificate password', ' fixture notary password'),
        ('trailing-space', 'fixture certificate password ', 'fixture notary password\n'),
        ('tabs', '\tfixture\tcertificate\t', '\tfixture-notary\t'),
        ('all-whitespace', '   ', ' \t '),
        ('unicode', 'f\u00efxture c\u00e9rtificate\u00a0', '\u3000fixture notary'),
    ):
        fixture = Fixture(temporary, 'password-' + case, good_identity())
        inputs = full_inputs(MACOS_SIGNING_CERTIFICATE_PASSWORD=certificate, MACOS_NOTARY_PASSWORD=notary,
                             MACOS_NOTARY_APPLE_ID='\n%s\n' % SENTINELS['MACOS_NOTARY_APPLE_ID'])
        process = fixture.signing('setup', inputs=inputs)
        assert process.returncode == 0, process.stderr
        [imported] = [c['args'] for c in fixture.calls('security') if c['args'][:1] == ['import']]
        [stored] = [c['args'] for c in fixture.calls('xcrun') if c['args'][:2] == ['notarytool', 'store-credentials']]
        assert imported[imported.index('-P') + 1] == certificate, 'certificate password changed'
        assert stored[stored.index('--password') + 1] == notary, 'notary password changed'
        assert stored[stored.index('--apple-id') + 1] == SENTINELS['MACOS_NOTARY_APPLE_ID'], 'the Apple ID is trimmed'
        for value in (certificate, notary):
            assert not value.strip() or value not in process.stdout + process.stderr + fixture.output.read_text(), 'password printed'
        assert fixture.signing('cleanup', state=fixture.outputs()['state']).returncode == 0
        assert_clean(fixture)
        record('passwords-kept-exactly', variant=case)


def replacement_cases(temporary):
    """R5 review: the launcher removes only the directories it made, through their held descriptors."""
    for target in ('private', 'project'):
        fixture = Fixture(temporary, 'replaced-' + target, {})
        moved, unknown = fixture.dir / 'moved-away', b'not this run\'s'
        (fixture.bin / 'go').write_text(
            '#!%s\nimport json, pathlib, sys\nproject = pathlib.Path(sys.argv[-1])\n'
            'config = json.loads((project / "mygo.json").read_text())\nprivate = pathlib.Path(config["updates"]["privateKey"]).parent\n'
            'victim = %s\nvictim.rename(%r)\nvictim.mkdir(mode=0o700)\n(victim / "unknown").write_bytes(%r)\nprint(victim)\n'
            % (sys.executable, target, str(moved), unknown))
        (fixture.bin / 'go').chmod(0o755)
        process = launch(fixture, ['--platform', 'linux/amd64', '--system', 'Linux'])
        victim = pathlib.Path(process.stdout.splitlines()[-1])
        assert process.returncode == 1 and 'was replaced while the build ran' in process.stderr, process.stderr
        assert (victim / 'unknown').read_bytes() == unknown and sorted(p.name for p in victim.iterdir()) == ['unknown']
        assert moved.is_dir() and list(moved.iterdir()) == [], 'this run\'s directory, key included, is emptied where it went'
        others = [p for p in list(fixture.runner_temp.iterdir()) + list((fixture.source / 'build').iterdir()) if p.resolve() != victim.resolve()]
        assert others == [], others
        record('launcher-removes-only-its-directories', variant=target)


def signatures(**changes):
    """Stand-in signatures of a correct two-architecture build, with per-key changes."""
    dr = 'identifier "dev.idy.yesman" and anchor apple generic and certificate 1[field.1.2.840.113635.100.6.2.6] /* exists */ and certificate leaf[field.1.2.840.113635.100.6.1.13] /* exists */ and certificate leaf[subject.OU] = %s' % TEAM
    leaf = 'fixture leaf certificate'
    base = {'team': TEAM, 'dr': dr, 'leaf': leaf}
    found = {key: dict(base) for key in ('darwin-arm64/yesman.app', 'darwin-amd64/yesman.app', 'darwin-arm64/yesman-0.1.0-arm64.dmg', 'darwin-amd64/yesman-0.1.0-amd64.dmg')}
    for key, value in changes.items():
        found[key].update(value)
    return found, hashlib.sha1(leaf.encode()).hexdigest().upper()


def verify_cases(temporary):
    both = lambda change: {'darwin-arm64/yesman.app': change, 'darwin-amd64/yesman.app': change}  # noqa: E731
    for name, changes, message in [
        ('signed-notarized-both', {}, None),
        ('adhoc-app', {'darwin-amd64/yesman.app': {'adhoc': True, 'team': None}}, 'ad-hoc'),
        ('other-team-app', {'darwin-arm64/yesman.app': {'team': 'OTHERTEAM1'}}, 'another team'),
        ('not-developer-id', {'darwin-arm64/yesman.app': {'developerID': False}}, 'Developer ID Application requirement'),
        ('no-hardened-runtime', {'darwin-arm64/yesman.app': {'flags': '0x0(none)'}}, 'Hardened runtime'),
        ('no-secure-timestamp', {'darwin-amd64/yesman.app': {'timestamp': False}}, 'secure timestamp'),
        ('app-not-stapled', {'darwin-arm64/yesman.app': {'stapled': False}}, 'stapled notarization ticket'),
        ('dmg-not-stapled', {'darwin-amd64/yesman-0.1.0-amd64.dmg': {'stapled': False}}, 'stapled notarization ticket'),
        ('gatekeeper-rejects', {'darwin-amd64/yesman.app': {'gatekeeper': False}}, 'Gatekeeper'),
        ('other-certificate', {'darwin-arm64/yesman-0.1.0-arm64.dmg': {'leaf': 'another leaf'}}, 'another certificate'),
        ('nested-invalid', {'darwin-arm64/yesman.app': {'deepValid': False}}, 'Nested signature'),
        ('wrong-identifier', {'darwin-arm64/yesman.app': {'identifier': 'dev.wrong.app'}}, 'Unexpected signing identifier'),
        # The independent review's weak-DR case: equal, mutually satisfied and still unsafe.
        ('weak-dr-always', both({'dr': 'always'}), 'not the Developer ID requirement'),
        ('dr-without-team', both({'dr': 'identifier "dev.idy.yesman" and anchor apple generic'}), 'not the Developer ID requirement'),
        ('dr-other-team', both({'dr': signatures()[0]['darwin-arm64/yesman.app']['dr'].replace(TEAM, 'OTHERTEAM1')}), 'not the Developer ID requirement'),
        ('dr-one-arch-differs', {'darwin-amd64/yesman.app': {'dr': 'identifier "dev.idy.yesman" and anchor apple generic and certificate leaf = H"%s"' % ('0' * 40)}}, 'not the Developer ID requirement'),
        ('requirement-not-mutual', {'darwin-amd64/yesman.app': {'satisfies': []}}, 'does not satisfy the designated requirement of darwin-arm64'),
    ]:
        found, leaf = signatures(**changes)
        fixture = Fixture(temporary, 'verify-' + name, {'signatures': found})
        build = fixture.dir / 'build'
        for key in found:
            path = build / key
            if key.endswith('.app'):
                path.mkdir(parents=True)
            else:
                path.write_bytes(b'dmg')
        evidence = fixture.dir / 'evidence'
        process = fixture.signing('verify', str(build), str(evidence), '--team', TEAM, '--identity', leaf)
        if message is None:
            assert process.returncode == 0, process.stderr
            proof = json.loads((evidence / 'macos-signing.json').read_text())
            assert set(proof['architectures']) == {'arm64', 'amd64'} and proof['identity'] == leaf
            assert proof['architectures']['arm64']['app']['designatedRequirement'] == proof['architectures']['amd64']['app']['designatedRequirement']
        else:
            assert process.returncode == 1 and message in process.stderr, (name, process.stderr)
            assert not (evidence / 'macos-signing.json').exists()
        record('verify', variant=name, accepted=message is None)


def fake_go(fixture, mode='record'):
    """A go that records what the launcher hands it, and runs the gh it finds once."""
    report = fixture.dir / 'go-call.json'
    real_gh = fixture.dir / 'real-bin/gh'
    real_gh.parent.mkdir(exist_ok=True)
    real_gh.write_text('#!%s\nimport json, os, sys\nwith open(%r, "a") as out:\n    out.write(json.dumps({"args": sys.argv[1:], "token": os.environ.get("GH_TOKEN") == %r}) + "\\n")\n'
                       % (sys.executable, str(fixture.dir / 'gh-calls.jsonl'), UPLOAD_TOKEN))
    real_gh.chmod(0o755)
    go = fixture.bin / 'go'
    go.write_text('''#!%s
import json, os, pathlib, subprocess, sys
project = pathlib.Path(sys.argv[-1])
config = json.loads((project / "mygo.json").read_text())
key = config.get("updates", {}).get("privateKey")
values = [os.environ[k] for k in os.environ]
record = {
    "args": sys.argv[1:-1], "cwd": os.getcwd(), "project": str(project), "config": config,
    "names": sorted(k for k in os.environ if k in ("GH_TOKEN", "MYGO_UPDATER_PRIVATE_KEY", "SIGNING_STATE") or k.startswith(("MACOS_SIGNING", "MACOS_NOTARY"))),
    "secretValue": any(%r in v or %r in v for v in values),
    "resources": os.readlink(project / "resources") if (project / "resources").is_symlink() else None,
    "keyFile": {"mode": oct(os.stat(key).st_mode & 0o777), "matches": pathlib.Path(key).read_text().strip() == %r} if key else None,
}
gh = subprocess.run(["gh", "release", "view", "v0"], capture_output=True)
record["ghExit"] = gh.returncode
pathlib.Path(%r).write_text(json.dumps(record))
sys.exit(%d)
''' % (sys.executable, UPDATER_KEY, UPLOAD_TOKEN, UPDATER_KEY, str(report), 3 if mode == 'fail' else 0))
    go.chmod(0o755)
    return report, real_gh.parent


def launch(fixture, args, state='', inputs=None, real_bin=None):
    env = fixture.env(dict({'MYGO_UPDATER_PRIVATE_KEY': UPDATER_KEY, 'GH_TOKEN': UPLOAD_TOKEN}, **(inputs or {})))
    if real_bin:
        env['PATH'] = str(fixture.bin) + os.pathsep + str(real_bin) + os.pathsep + env['PATH'].split(os.pathsep, 1)[1]
    env['SIGNING_STATE'] = state
    return subprocess.run([sys.executable, str(fixture.source / 'scripts/release_build.py'), *args], env=env, capture_output=True, text=True,
                          cwd=fixture.source, timeout=60)


def launcher_cases(temporary):
    """The release build step and its launcher, with a stand-in go."""
    text = (ROOT / '.github/workflows/release.yml').read_text()
    step = text.split('      - name: Build and upload\n', 1)[1].split('\n      - name:', 1)[0]
    assert 'run: python3 scripts/release_build.py --platform "$PLATFORMS" --system "$SYSTEM" --signing-mode "$SIGNING_MODE" --signing-identity "$SIGNING_IDENTITY" --upload' in step
    assert 'SIGNING_STATE: ${{ steps.signing.outputs.state }}' in step
    record('release-build-step-uses-launcher')

    tracked = json.loads((ROOT / 'mygo.json').read_text())
    for name, system, platform, mode, identity, use_state, expected in [
        ('macos-adhoc', 'macOS', 'darwin/arm64,darwin/amd64', 'adhoc', '', False, []),
        ('macos-developer-id', 'macOS', 'darwin/arm64,darwin/amd64', 'developer-id', SHA, True, ['-sign', SHA]),
        ('windows', 'Windows', 'windows/amd64,windows/arm64', '', '', False, []),
        ('linux', 'Linux', 'linux/amd64,linux/arm64', '', '', False, []),
        ('developer-id-no-identity', 'macOS', 'darwin/arm64', 'developer-id', '', True, None),
        ('developer-id-name-not-sha', 'macOS', 'darwin/arm64', 'developer-id', 'Developer ID Application: x', True, None),
        ('developer-id-no-state', 'macOS', 'darwin/arm64', 'developer-id', SHA, False, None),
        ('developer-id-forged-state', 'macOS', 'darwin/arm64', 'developer-id', SHA, 'forged', None),
        ('no-mode', 'macOS', 'darwin/arm64', '', '', False, None),
        ('adhoc-with-state', 'macOS', 'darwin/arm64', 'adhoc', '', True, None),
        ('linux-signing-mode', 'Linux', 'linux/amd64', 'developer-id', SHA, False, None),
        ('linux-building-darwin', 'Linux', 'darwin/arm64', '', '', False, None),
    ]:
        if use_state:
            fixture, state = prepared(temporary, 'launch-' + name)
            if use_state == 'forged':
                state = base64.urlsafe_b64encode(json.dumps({'dir': str(fixture.runner_temp), 'dev': 1, 'ino': 1, 'nonce': 'c' * 64}).encode()).decode()
        else:
            fixture, state = Fixture(temporary, 'launch-' + name, {}), ''
        report, real_bin = fake_go(fixture)
        process = launch(fixture, ['--platform', platform, '--system', system, '--signing-mode', mode, '--signing-identity', identity, '--upload'],
                         state, real_bin=real_bin)
        if expected is None:
            assert process.returncode == 1 and not report.exists(), (name, process.stderr)
        else:
            assert process.returncode == 0, (name, process.stderr)
            call = json.loads(report.read_text())
            assert call['args'] == ['tool', 'mygo', 'build', '-platform', platform, '-upload'] + expected, call['args']
            assert call['cwd'] == str(fixture.source.resolve()) and pathlib.Path(call['project']).parent == (fixture.source / 'build').resolve()
            assert call['names'] == [] and not call['secretValue'], 'no credential in the build environment'
            assert call['keyFile'] == {'mode': '0o600', 'matches': True}, call['keyFile']
            assert call['ghExit'] == 0, 'the gh wrapper reaches the real gh'
            gh = [json.loads(line) for line in (fixture.dir / 'gh-calls.jsonl').read_text().splitlines()]
            assert gh == [{'args': ['release', 'view', 'v0'], 'token': True}], gh
            config = call['config']
            assert config['updates']['publicKey'] == tracked['updates']['publicKey'] and config['version'] == tracked['version']
            assert config['identifier'] == tracked['identifier'] and config['main'] == str(fixture.source.resolve())
            assert config['out'] == str(fixture.source.resolve() / 'build') and config['icon'] == tracked['icon']
            assert call['resources'] == str(fixture.source.resolve() / 'resources')
            # v0.1.39's packaging fields: names carried as they are, the NSIS include made absolute.
            assert config['linux'] == tracked['linux'] and config['windows']['installDirName'] == tracked['windows']['installDirName']
            assert config['windows']['installerInclude'] == str(fixture.source.resolve() / tracked['windows']['installerInclude'])
            assert config['macos']['entitlements'] == str(fixture.source.resolve() / tracked['macos']['entitlements'])
            if mode == 'developer-id':
                [folder] = fixture.folders()
                assert config['macos']['notarize'] == {'keychainProfile': 'yesman-release-notary', 'keychain': str(folder / 'signing.keychain-db')}
            else:
                assert 'notarize' not in config['macos']
            assert sorted(p.name for p in (fixture.source / 'build').iterdir()) == [], 'the derived project is removed'
        record('launcher-arguments', variant=name, refused=expected is None)

    # Credentials and configuration the launcher refuses.
    for name, change, message in [
        ('upload-without-key', {'inputs': {'MYGO_UPDATER_PRIVATE_KEY': ''}}, 'needs MYGO_UPDATER_PRIVATE_KEY'),
        ('key-under-another-name', {'inputs': {'SOME_OTHER_VAR': 'x' + UPDATER_KEY}}, 'appears in another environment variable'),
        ('unknown-config-key', {'mygo': dict(tracked, frontendDist='web')}, 'does not carry'),
        ('discovered-package-json', {'file': 'package.json'}, 'would not be visible'),
        ('unknown-windows-key', {'mygo': dict(tracked, windows=dict(tracked['windows'], certificate='x'))}, 'does not carry'),
        ('main-set', {'mygo': dict(tracked, main='./cmd/app')}, 'does not carry'),
        ('include-absolute', {'mygo': dict(tracked, windows=dict(tracked['windows'], installerInclude='/etc/hosts'))}, 'relative path inside'),
        ('include-parent', {'mygo': dict(tracked, windows=dict(tracked['windows'], installerInclude='../outside.nsh'))}, 'relative path inside'),
        ('include-missing', {'mygo': dict(tracked, windows=dict(tracked['windows'], installerInclude='packaging/windows/none.nsh'))}, 'not a file'),
        ('include-through-link', {'mygo': dict(tracked, windows=dict(tracked['windows'], installerInclude='linked/compatibility.nsh')),
                                  'link': 'linked'}, 'through a link'),
    ]:
        fixture = Fixture(temporary, 'refuse-' + name, {}, mygo=json.dumps(change['mygo']) if 'mygo' in change else None)
        if 'file' in change:
            (fixture.source / change['file']).write_text('{}')
        if 'link' in change:
            (fixture.source / change['link']).symlink_to(fixture.source / 'packaging/windows', target_is_directory=True)
        report, real_bin = fake_go(fixture)
        process = launch(fixture, ['--platform', 'linux/amd64', '--system', 'Linux', '--upload'], inputs=change.get('inputs'), real_bin=real_bin)
        assert process.returncode == 1 and message in process.stderr and not report.exists(), (name, process.stderr)
        assert not (fixture.source / 'build').exists() or list((fixture.source / 'build').iterdir()) == []
        record('launcher-refuses', variant=name)

    fixture = Fixture(temporary, 'launch-build-fails', {})
    report, real_bin = fake_go(fixture, 'fail')
    process = launch(fixture, ['--platform', 'linux/amd64', '--system', 'Linux', '--upload'], real_bin=real_bin)
    assert process.returncode == 1 and 'mygo build failed (exit 3)' in process.stderr
    assert list((fixture.source / 'build').iterdir()) == []
    record('launcher-failure-removes-private-files')


def step_script(workflow, step):
    text = (ROOT / '.github/workflows' / workflow).read_text()
    block = text.split('      - name: %s\n' % step, 1)[1]
    block = re.split(r'\n      - (?:name|uses):', block, 1)[0]
    run = block.split('        run: |\n', 1)[1]
    lines = [line[10:] if line.startswith('          ') else line.strip() for line in run.splitlines()]
    return '\n'.join(lines) + '\n'


def workflow_cases(temporary):
    smoke = step_script('macos-draft-smoke.yml', 'Verify unchanged final app startup')
    for variant, team, extra in [('adhoc', '', ['--expect-adhoc']), ('team', TEAM, ['--expect-team-id', TEAM]), ('older-tag', None, []), ('bad-team', 'idy', None)]:
        fixture = Fixture(temporary, 'smoke-' + variant, team=team)
        if variant == 'older-tag':
            (fixture.source / 'scripts/macos_signing.py').unlink()
            (fixture.source / 'packaging/macos/signing.json').unlink()
        called = fixture.dir / 'called'
        # The step's own team lookup runs for real; check_macos_startup is recorded.
        python = fixture.bin / 'python3'
        python.write_text('#!/bin/sh\ncase "$1" in scripts/macos_signing.py) exec "%s" "$@" ;; esac\nprintf "%%s\\n" "$@" > "%s"\n' % (sys.executable, called))
        python.chmod(0o755)
        env = dict(fixture.env(), RUNNER_TEMP='/r', RELEASE_TAG='v0.1.0', TARGET_ARCH='arm64', RELEASE_SOURCE='0' * 40)
        process = subprocess.run(['bash', '-e', '-c', smoke], env=env, capture_output=True, text=True, cwd=fixture.source)
        if extra is None:
            assert process.returncode != 0 and not called.exists() and 'Team ID' in process.stderr, process.stderr
        else:
            assert process.returncode == 0, process.stderr
            assert called.read_text().splitlines() == ['scripts/check_macos_startup.py', '/r/draft/yesman.app', 'web', '/r/startup-evidence',
                                                       '--release-tag', 'v0.1.0', '--arch', 'arm64', '--source', '0' * 40] + extra
        record('draft-smoke-policy-from-tag-source', variant=variant)

    # Where the signing inputs may go: the secrets to one release step only;
    # no repository variable decides the signing of an already built release.
    allowed = {('release.yml', 'Prepare macOS signing'): {'secrets.MACOS_SIGNING_CERTIFICATE_P12', 'secrets.MACOS_SIGNING_CERTIFICATE_PASSWORD',
                                                          'secrets.MACOS_NOTARY_APPLE_ID', 'secrets.MACOS_NOTARY_PASSWORD'}}
    seen = {}
    for workflow in sorted((ROOT / '.github/workflows').glob('*.yml')):
        step = None
        text = workflow.read_text()
        assert 'toJSON(secrets)' not in text and 'secrets: inherit' not in text, workflow.name
        for line in text.splitlines():
            named = re.match(r'\s+- name: (.+)$', line)
            if named:
                step = named.group(1)
            for use in re.findall(r'(?:secrets|vars)\.(?:MACOS_[A-Z0-9_]+|MYGO_UPDATER_PRIVATE_KEY)', line):
                seen.setdefault((workflow.name, step), set()).add(use)
    allowed[('release.yml', 'Build and upload')] = {'secrets.MYGO_UPDATER_PRIVATE_KEY'}
    # Inherited from v0.1.39, not confined: its dispatch-only upgrade gate signs
    # private QA successors with the existing key (scripts/prepare-upgrade-qa.py),
    # whose own build still passes the key to its compilers. That gap is tracked
    # and fixed separately; this only records where the key is placed today.
    allowed[('application-upgrade-draft.yml', 'Package and sign private QA successors')] = {'secrets.MYGO_UPDATER_PRIVATE_KEY'}
    assert seen == allowed, seen
    record('signing-inputs-confined', steps=sorted('%s: %s' % key for key in seen))

    raw = (ROOT / 'mygo.json').read_bytes()
    config = json.loads(raw)
    assert config['identifier'] == 'dev.idy.yesman' and config['updates']['publicKey'] == UPDATER_PUBLIC_KEY
    assert 'signingIdentity' not in config['macos'] and 'notarize' not in config['macos'], 'the tracked configuration stays ad hoc'
    assert config['macos']['entitlements'] == 'packaging/macos/permissions.entitlements.plist'
    team = json.loads((ROOT / 'packaging/macos/signing.json').read_text())
    assert set(team) == {'teamId'}
    record('identity-and-updater-key-unchanged', trackedTeam=team['teamId'] or None)


def go_env():
    env = dict(os.environ)
    for name in ('GOCACHE', 'GOMODCACHE', 'GOPATH', 'HOME', 'GOTMPDIR', 'TMPDIR'):
        assert env.get(name), name + ' must be set to a private folder for --mygo'
    env.update(GOENV='off', GOTOOLCHAIN='local', GOFLAGS='-mod=readonly', CGO_ENABLED='1', MACOSX_DEPLOYMENT_TARGET='13.0',
               CGO_CFLAGS='-O2 -g -mmacosx-version-min=13.0', PYTHONDONTWRITEBYTECODE='1')
    for name in ('GH_TOKEN', 'GITHUB_TOKEN', 'MYGO_UPDATER_PRIVATE_KEY', 'SIGNING_STATE'):
        env.pop(name, None)
    return env


VERIFIER = '''package main

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
)

func main() {
	pub, _ := base64.StdEncoding.DecodeString(os.Args[1])
	var m struct{ Size int64; Signature string }
	data, _ := os.ReadFile(os.Args[2])
	if json.Unmarshal(data, &m) != nil { os.Exit(2) }
	archive, _ := os.ReadFile(os.Args[3])
	sum := sha256.Sum256(archive)
	sig, _ := base64.StdEncoding.DecodeString(m.Signature)
	if int64(len(archive)) != m.Size || !ed25519.Verify(pub, sum[:], sig) { fmt.Println("bad"); os.Exit(1) }
	fmt.Println("ok")
}
'''


def mygo_cases(temporary):
    """The real MyGo v0.9.0 build of a committed copy of this repository, through the launcher."""
    assert sys.platform == 'darwin', '--mygo needs macOS'
    temporary = pathlib.Path(temporary)
    source = temporary / 'mygo-source'
    files = subprocess.check_output(['git', 'ls-files', '-z', '--cached', '--others', '--exclude-standard'], cwd=ROOT).decode().split('\0')
    for name in filter(None, files):
        target = source / name
        target.parent.mkdir(parents=True, exist_ok=True)
        if (ROOT / name).is_file():
            shutil.copy2(ROOT / name, target)
    env = go_env()
    keys = temporary / 'update-keys'
    subprocess.run(['go', 'tool', 'mygo', 'keygen', '-o', str(keys)], cwd=source, env=env, check=True, capture_output=True, timeout=300)
    public = (keys / 'mygo-update.pub').read_text().strip()
    config = json.loads((source / 'mygo.json').read_text())
    committed = json.loads((ROOT / 'mygo.json').read_text())
    # A throwaway update key, and no deltas from published releases: the
    # only differences from the committed configuration.
    config['updates']['publicKey'] = public
    config['updates']['deltas'] = 0
    (source / 'mygo.json').write_text(json.dumps(config, indent=2))
    git = ['git', '-c', 'user.name=fixture', '-c', 'user.email=fixture@example.invalid', '-c', 'commit.gpgsign=false']
    subprocess.run(['git', 'init', '-q'], cwd=source, env=env, check=True)
    subprocess.run(git + ['add', '-A'], cwd=source, env=env, check=True)
    subprocess.run(git + ['commit', '-q', '-m', 'fixture'], cwd=source, env=env, check=True)
    revision = subprocess.check_output(['git', 'rev-parse', 'HEAD'], cwd=source, env=env).decode().strip()
    key = (keys / 'mygo-update.key').read_text().strip()
    verifier = temporary / 'verifier'
    verifier.mkdir()
    (verifier / 'go.mod').write_text('module verifier\n\ngo 1.21\n')
    (verifier / 'main.go').write_text(VERIFIER)
    subprocess.run(['go', 'build', '-o', str(temporary / 'verify-update'), '.'], cwd=verifier, env=env, check=True, timeout=300)

    # F1: the ordinary entry, no inputs, no flags beyond the platform.
    plain = subprocess.run(['go', 'tool', 'mygo', 'build', '-platform', 'darwin/arm64', '-skip-dmg'], cwd=source, env=env, capture_output=True, text=True, timeout=600)
    assert plain.returncode == 0, plain.stdout[-2000:] + plain.stderr[-2000:]
    tracked_app = source / 'build/darwin-arm64/yesman.app'
    assert 'Signature=adhoc' in subprocess.run(['/usr/bin/codesign', '-dv', str(tracked_app)], capture_output=True, text=True).stderr
    tracked_tree = comparable(tracked_app, env)
    # v0.1.39's layout through the repository's packaging adapter: the stable
    # bundle path, the "Yes Man" main image and the old executable name as a link.
    with open(tracked_app / 'Contents/Info.plist', 'rb') as info:
        assert plistlib.load(info)['CFBundleExecutable'] == 'Yes Man'
    assert tracked_tree['Contents/MacOS/yesman'] == 'link:Yes Man' and tracked_tree['Contents/MacOS/Yes Man'].startswith('go:')
    built = json.loads(subprocess.check_output(['go', 'version', '-m', '-json', str(tracked_app / 'Contents/MacOS/Yes Man')], env=env))
    settings = {s['Key']: s['Value'] for s in built['Settings']}
    assert settings['vcs.revision'] == revision and settings['vcs.modified'] == 'false', settings
    shutil.rmtree(source / 'build')
    record('plain-build-without-inputs-succeeds', exit=plain.returncode, mainImage='Yes Man')

    def build(name, platforms, mode, identity, scenario, state='', upload=False):
        fixture = Fixture(temporary, 'mygo-' + name, scenario)
        trace = fixture.dir / 'children.jsonl'
        observer = '#!%s\nimport json, os, sys\nwith open(%r, "a") as out:\n    out.write(json.dumps({"tool": %%r, "names": sorted(k for k in os.environ if k in ("GH_TOKEN", "MYGO_UPDATER_PRIVATE_KEY")), "value": any(%r in v or %r in v for v in os.environ.values())}) + "\\n")\nos.execv(%%r, [%%r] + sys.argv[1:])\n' % (
            sys.executable, str(trace), key, UPLOAD_TOKEN)
        cc = fixture.bin / 'observed-clang'
        cc.write_text(observer % ('clang', '/usr/bin/clang', '/usr/bin/clang'))
        real_go = shutil.which('go', path=env['PATH'])
        (fixture.bin / 'go').write_text(observer % ('go', real_go, real_go))
        for path in (cc, fixture.bin / 'go'):
            path.chmod(0o755)
        report, real_bin = fake_go(fixture)  # only for its stand-in real gh
        (fixture.bin / 'go').write_text(observer % ('go', real_go, real_go))
        run_env = dict(env, PATH=str(fixture.bin) + os.pathsep + str(real_bin) + os.pathsep + env['PATH'], APPLE_TOOLS_STATE=str(fixture.state),
                       CC=str(cc), MYGO_UPDATER_PRIVATE_KEY=key, SIGNING_STATE=state)
        if upload:
            run_env['GH_TOKEN'] = UPLOAD_TOKEN
        shutil.rmtree(source / 'build', ignore_errors=True)
        system = 'macOS' if platforms.startswith('darwin') else 'Linux'
        process = subprocess.run([sys.executable, str(source / 'scripts/release_build.py'), '--platform', platforms, '--system', system,
                                  '--signing-mode', mode, '--signing-identity', identity] + (['--upload'] if upload else []),
                                 cwd=source, env=run_env, capture_output=True, text=True, timeout=1200)
        children = [json.loads(line) for line in trace.read_text().splitlines()] if trace.exists() else []
        return fixture, process, children

    def archives(arch):
        stage = source / 'build' / ('darwin-' + arch)
        return sorted(stage.glob('*.tar.gz')) if stage.exists() else []

    # Signed through the launcher, uploaded to a stand-in gh.
    signer, state = prepared(temporary, 'mygo-signer')
    fixture, process, children = build('signed', 'darwin/arm64,darwin/amd64', 'developer-id', SHA, {}, state, upload=True)
    assert process.returncode == 0, process.stdout[-3000:] + process.stderr[-3000:]
    keychain = signer.folders()[0] / 'signing.keychain-db'
    order = [(c['tool'], c['args']) for c in fixture.calls() if not c.get('passthrough') and c['tool'] in ('codesign', 'xcrun')]
    for arch in ('arm64', 'amd64'):
        stage = source / 'build' / ('darwin-' + arch)
        staging = {pathlib.Path(a).parent for _, args in order for a in args if re.search(r'/\.staging-darwin-%s-\d+/yesman\.app$' % arch, a)}
        assert len(staging) == 1, (arch, staging)
        staging = staging.pop()
        dmg_name = 'yesman-%s-%s.dmg' % (committed['version'], arch)
        app, dmg = str(staging / 'yesman.app'), str(staging / dmg_name)
        expected = [
            ('codesign', ['--force', '--deep', '--sign', SHA, '--options', 'runtime', '--timestamp', '--entitlements']),
            ('codesign', ['--force', '--timestamp', '--sign', SHA, dmg]),
            ('xcrun', ['notarytool', 'submit', dmg, '--keychain-profile', 'yesman-release-notary', '--wait', '--output-format', 'json', '--keychain', str(keychain)]),
            ('xcrun', ['stapler', 'staple', dmg]),
            ('xcrun', ['stapler', 'staple', app]),
        ]
        mine = [(tool, args) for tool, args in order if any(app in a or dmg in a for a in args)]
        assert len(mine) == len(expected), (arch, mine)
        assert mine[0][1][:8] == expected[0][1] and mine[0][1][-1] == app, mine[0]
        assert pathlib.Path(mine[0][1][8]).read_bytes() == (ROOT / committed['macos']['entitlements']).read_bytes(), 'entitlements changed'
        assert mine[1:] == expected[1:], (arch, mine[1:])
        [archive] = archives(arch)
        with tarfile.open(archive) as tar:
            assert 'yesman.app/Contents/.fixture-notarization-ticket' in tar.getnames(), 'the update archive must hold the stapled app'
        assert (stage / dmg_name).read_bytes().endswith(b'fixture-notarization-ticket'), 'the disk image must be stapled'
        manifest = stage / ('update-darwin-%s.json' % arch)
        verified = subprocess.run([str(temporary / 'verify-update'), public, str(manifest), str(archive)], capture_output=True, text=True)
        assert verified.stdout.strip() == 'ok', 'the archive must carry the updater key\'s signature'
        binary = subprocess.check_output(['go', 'version', '-m', '-json', str(stage / 'yesman.app/Contents/MacOS/yesman')], env=env)
        settings = {s['Key']: s['Value'] for s in json.loads(binary)['Settings']}
        assert settings['vcs.revision'] == revision and settings['vcs.modified'] == 'false', settings
    sums = (source / 'build/SHA256SUMS').read_text().splitlines()
    for line in sums:
        digest, name = line.split(None, 1)
        paths = list((source / 'build').rglob(name.lstrip('*')))
        assert paths and all(hashlib.sha256(p.read_bytes()).hexdigest() == digest for p in paths), name
    gh = [json.loads(line) for line in (fixture.dir / 'gh-calls.jsonl').read_text().splitlines()]
    assert gh and all(call['token'] for call in gh) and any(call['args'][:2] == ['release', 'upload'] for call in gh), gh
    clang = [c for c in children if c['tool'] == 'clang']
    assert clang and children and all(c['names'] == [] and not c['value'] for c in children), [c for c in children if c['names'] or c['value']][:3]
    assert [p.name for p in (source / 'build').iterdir() if p.name.startswith('.release-config-')] == []
    record('mygo-signed-notarized-before-archives', architectures=['arm64', 'amd64'], compilerChildren=len(clang), goChildren=len(children) - len(clang),
           credentialInChild=False, ghCalls=len(gh))

    # Failures stop before any update file or upload.
    for name, scenario, message in [
        ('codesign-fails', {'fail': ['app-sign']}, 'errSecInternalComponent'),
        ('notarization-invalid', {'fail': ['notarize']}, 'notarization failed (Invalid'),
        ('staple-fails', {'fail': ['staple']}, 'Error 65'),
    ]:
        fixture, process, _ = build(name, 'darwin/arm64', 'developer-id', SHA, scenario, state, upload=True)
        assert process.returncode == 1 and message in process.stdout + process.stderr, (name, process.stdout[-2000:], process.stderr[-2000:])
        assert archives('arm64') == [] and not list((source / 'build').rglob('update-*.json'))
        gh_log = fixture.dir / 'gh-calls.jsonl'
        assert not gh_log.exists() or not any(json.loads(line)['args'][:2] == ['release', 'upload'] for line in gh_log.read_text().splitlines())
        calls = [c for c in fixture.calls() if not c.get('passthrough')]
        if name == 'codesign-fails':
            assert not [c for c in calls if c['tool'] == 'xcrun'], calls
        if name == 'notarization-invalid':
            assert not [c for c in calls if c['args'][:2] == ['stapler', 'staple']], calls
        record('mygo-failure-stops-before-archives', variant=name)
    assert signer.signing('cleanup', state=state).returncode == 0

    # Ad hoc through the launcher: the same app as the tracked configuration
    # builds, with no notarization, and archives signed with the updater key.
    fixture, process, children = build('adhoc', 'darwin/arm64,darwin/amd64', 'adhoc', '', {})
    assert process.returncode == 0, process.stdout[-3000:] + process.stderr[-3000:]
    assert not [c for c in fixture.calls('xcrun') if not c.get('passthrough')], 'ad-hoc builds must not notarize'
    signed = [c for c in fixture.calls('codesign') if '--sign' in c['args']]
    assert signed and all(c['args'][c['args'].index('--sign') + 1] == '-' and '--options' not in c['args'] for c in signed), signed
    candidate = comparable(source / 'build/darwin-arm64/yesman.app', env)
    assert candidate == tracked_tree, ('the derived configuration builds the tracked configuration\'s app',
                                       sorted(k for k in set(candidate) | set(tracked_tree) if candidate.get(k) != tracked_tree.get(k)))
    for arch in ('arm64', 'amd64'):
        [archive] = archives(arch)
        verified = subprocess.run([str(temporary / 'verify-update'), public, str(source / 'build/darwin-{0}/update-darwin-{0}.json'.format(arch)), str(archive)],
                                  capture_output=True, text=True)
        assert verified.stdout.strip() == 'ok'
    assert children and all(c['names'] == [] and not c['value'] for c in children)
    record('mygo-adhoc-launcher-matches-tracked', architectures=['arm64', 'amd64'])

    # Linux and Windows rows: no signing, archives signed, nothing else changes.
    for platforms, system in [('linux/amd64', 'Linux'), ('windows/amd64', 'Windows')]:
        shutil.rmtree(source / 'build', ignore_errors=True)
        direct = subprocess.run(['go', 'tool', 'mygo', 'build', '-platform', platforms], cwd=source, env=dict(env, CGO_ENABLED='0', MYGO_UPDATER_PRIVATE_KEY=key),
                                capture_output=True, text=True, timeout=600)
        assert direct.returncode == 0, direct.stderr[-2000:]
        stage = source / 'build' / platforms.replace('/', '-')
        expected = {p.name for p in stage.iterdir()}
        tracked_manifest = json.loads((stage / ('update-%s.json' % platforms.replace('/', '-'))).read_text())
        tracked_members = archive_members(stage, env)
        fixture = Fixture(temporary, 'mygo-' + system, {})
        shutil.rmtree(source / 'build')
        process = subprocess.run([sys.executable, str(source / 'scripts/release_build.py'), '--platform', platforms, '--system', system],
                                 cwd=source, env=dict(env, CGO_ENABLED='0', MYGO_UPDATER_PRIVATE_KEY=key), capture_output=True, text=True, timeout=600)
        assert process.returncode == 0, process.stderr[-2000:]
        assert {p.name for p in stage.iterdir()} == expected
        manifest = json.loads((stage / ('update-%s.json' % platforms.replace('/', '-'))).read_text())
        assert {k: manifest[k] for k in ('version', 'url', 'notes')} == {k: tracked_manifest[k] for k in ('version', 'url', 'notes')}
        # The same files in the update archive: v0.1.39's "Yes Man" main image
        # and the legacy launcher under the old name, built from this module.
        members = archive_members(stage, env)
        assert members == tracked_members, sorted(k for k in set(members) | set(tracked_members) if members.get(k) != tracked_members.get(k))
        suffix = '.exe' if system == 'Windows' else ''
        assert members.get('Yes Man' + suffix, '').startswith('go:') and members.get('yesman' + suffix, '').startswith('go:'), sorted(members)
        [archive] = [p for p in stage.iterdir() if p.name.endswith('.tar.gz') or p.name.endswith('.zip')] or [None]
        if archive:
            verified = subprocess.run([str(temporary / 'verify-update'), public, str(stage / ('update-%s.json' % platforms.replace('/', '-'))), str(archive)],
                                      capture_output=True, text=True)
            assert verified.stdout.strip() == 'ok'
        record('mygo-launcher-matches-tracked', platform=platforms, files=len(expected))
    subprocess.run(['git', 'status', '--porcelain'], cwd=source, env=env, check=True)


def archive_members(stage, env):
    """The update archive of a Linux or Windows stage: each member's digest, or its normalized Go build information."""
    [archive] = [p for p in stage.iterdir() if p.name.endswith(('.tar.gz', '.zip'))]
    with tempfile.TemporaryDirectory() as work:
        work = pathlib.Path(work)
        if archive.name.endswith('.zip'):
            import zipfile
            with zipfile.ZipFile(archive) as z:
                assert not [m for m in z.namelist() if m.startswith('/') or '..' in pathlib.PurePosixPath(m).parts]
                z.extractall(work)
        else:
            with tarfile.open(archive) as t:
                names = t.getnames()
                assert not [m for m in names if m.startswith('/') or '..' in pathlib.PurePosixPath(m).parts], names
                t.extractall(work)
        return comparable(work, env, executables=(b'\x7fELF', b'MZ'))


MACHO = (b'\xcf\xfa\xed\xfe', b'\xca\xfe\xba\xbe')


def comparable(app, env, executables=None):
    """What two builds of one source must share: every file but the signature, links, and each Go binary's build information.

    The packaging adapter stages the pinned MyGo in a fresh temporary
    directory each run, which go records as the module's replacement path;
    that path, and so the binary's bytes and build ID, differ between any two
    builds. Everything else of the build information (revision, VCS state,
    every module version, every build setting) must be equal.
    """
    app, found = pathlib.Path(app), {}
    for p in sorted(app.rglob('*')):
        rel = str(p.relative_to(app))
        if '_CodeSignature' in p.parts:
            continue
        if p.is_symlink():
            found[rel] = 'link:' + os.readlink(p)
        elif p.is_file():
            data = p.read_bytes()
            magic = executables or MACHO
            listed = (subprocess.run(['go', 'version', '-m', '-json', str(p)], env=env, capture_output=True, text=True)
                      if any(data.startswith(m) for m in magic) else None)
            if listed is not None and listed.returncode == 0:
                info = json.loads(listed.stdout)
                for dep in info.get('Deps') or []:
                    if dep.get('Replace') and os.path.isabs(dep['Replace'].get('Path', '')):
                        assert pathlib.Path(dep['Replace']['Path']).name == 'mygo' and dep['Path'] == 'github.com/egoist/mygo', dep
                        dep['Replace']['Path'] = '<staged pinned MyGo>'
                found[rel] = 'go:' + hashlib.sha256(json.dumps(info, sort_keys=True).encode()).hexdigest()
            else:
                found[rel] = hashlib.sha256(data).hexdigest()
    return found


def tree(app, skip_signature=False):
    """Every file of an app bundle and its digest."""
    app = pathlib.Path(app)
    return {str(p.relative_to(app)): hashlib.sha256(p.read_bytes()).hexdigest() for p in sorted(app.rglob('*'))
            if p.is_file() and not (skip_signature and '_CodeSignature' in p.parts)}


def real_keychain_cases(temporary):
    """Real security/codesign: a synthetic self-signed identity is refused, nothing remains."""
    assert sys.platform == 'darwin' and os.environ.get('GITHUB_ACTIONS') == 'true' and os.environ.get('RUNNER_ENVIRONMENT') == 'github-hosted', \
        'Refusing to touch a keychain outside a disposable GitHub-hosted runner'
    team = 'SYNTHTEAM1'
    fixture = Fixture(temporary, 'real', team=team)
    shutil.rmtree(fixture.bin)
    fixture.bin.mkdir()  # no stand-ins: the real tools
    folder = fixture.dir
    passphrase = base64.b32encode(os.urandom(15)).decode()
    openssl = '/usr/bin/openssl'
    subprocess.run([openssl, 'req', '-x509', '-newkey', 'rsa:2048', '-nodes', '-days', '1', '-keyout', str(folder / 'key.pem'), '-out', str(folder / 'cert.pem'),
                    '-subj', '/CN=Developer ID Application: Synthetic Fixture (%s)/OU=%s' % (team, team),
                    '-addext', 'extendedKeyUsage=codeSigning', '-addext', 'keyUsage=critical,digitalSignature'], check=True, capture_output=True)
    subprocess.run([openssl, 'pkcs12', '-export', '-inkey', str(folder / 'key.pem'), '-in', str(folder / 'cert.pem'), '-out', str(folder / 'identity.p12'),
                    '-passout', 'pass:' + passphrase], check=True, capture_output=True)
    listed = subprocess.check_output(['security', 'list-keychains', '-d', 'user']).decode()
    inputs = dict(MACOS_SIGNING_CERTIFICATE_P12=base64.b64encode((folder / 'identity.p12').read_bytes()).decode(),
                  MACOS_SIGNING_CERTIFICATE_PASSWORD=passphrase, MACOS_NOTARY_APPLE_ID='synthetic@example.invalid', MACOS_NOTARY_PASSWORD='synthetic-not-a-password')
    process = fixture.signing('setup', inputs=inputs)
    assert process.returncode == 1 and 'not valid on the runner' in process.stdout + process.stderr, process.stdout + process.stderr
    assert passphrase not in process.stdout + process.stderr
    assert fixture.folders() == [], 'keychain folder remains'
    assert subprocess.check_output(['security', 'list-keychains', '-d', 'user']).decode() == listed, 'search list changed'
    outputs = fixture.outputs()
    assert 'mode' not in outputs and fixture.signing('cleanup', state=outputs['state']).returncode == 0
    record('real-keychain-refuses-self-signed', team=team)


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument('--mygo', action='store_true')
    parser.add_argument('--real-keychain', action='store_true')
    parser.add_argument('--report')
    args = parser.parse_args()
    with tempfile.TemporaryDirectory(prefix='yesman-signing-test-') as temporary:
        setup_cases(temporary)
        ownership_cases(temporary)
        lifecycle_cases(temporary)
        retention_cases(temporary)
        admission_cases(temporary)
        password_cases(temporary)
        replacement_cases(temporary)
        verify_cases(temporary)
        launcher_cases(temporary)
        workflow_cases(temporary)
        if args.real_keychain:
            real_keychain_cases(temporary)
        if args.mygo:
            mygo_cases(temporary)
    if args.report:
        pathlib.Path(args.report).parent.mkdir(parents=True, exist_ok=True)
        pathlib.Path(args.report).write_text(json.dumps(results, indent=2))
    print('%d signing checks passed' % len(results))


if __name__ == '__main__':
    main()
