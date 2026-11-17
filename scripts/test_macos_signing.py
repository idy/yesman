#!/usr/bin/env python3
"""Check Developer ID signing of releases without a real certificate or Apple account.

  (default)        scripts/macos_signing.py setup/cleanup/verify against the
                   stand-in Apple tools of scripts/fixtures/macos-signing, the
                   release workflow's real build and draft-smoke step scripts,
                   and where the workflows let the signing inputs go.
  --mygo           Also runs the real `go tool mygo build` of a copy of this
                   repository, Apple tools stood in, for both architectures:
                   signing, notarization and stapling come before the update
                   archives and checksums, and each failure stops the build.
                   Needs macOS with Go and Xcode; set GOCACHE/GOMODCACHE.
  --real-keychain  On a disposable GitHub-hosted macOS runner only: real
                   security/codesign reject a synthetic self-signed "Developer
                   ID Application" certificate and leave no keychain behind.
  --report PATH    Writes the results as JSON.

Nothing here reads a real keychain, signs with a real identity or contacts
Apple: the stand-ins pass only ad-hoc signing of temporary files to codesign.
"""
import argparse
import base64
import hashlib
import json
import os
import pathlib
import re
import shutil
import subprocess
import sys
import tarfile
import tempfile

SCRIPTS = pathlib.Path(__file__).resolve().parent
ROOT = SCRIPTS.parent
TOOLS = SCRIPTS / 'fixtures/macos-signing/apple_tools.py'
SIGNING = SCRIPTS / 'macos_signing.py'
TEAM = 'FXTEAM1234'
SHA = 'A1B2C3D4E5F60718293A4B5C6D7E8F9012345678'
NAME = 'Developer ID Application: Fixture Person (%s)' % TEAM
P12 = b'fixture pkcs12 bytes, not a real certificate'
SENTINELS = {
    'MACOS_SIGNING_CERTIFICATE_PASSWORD': 'fixture-p12-passphrase-7f3a',
    'MACOS_NOTARY_APPLE_ID': 'fixture-apple-id@example.invalid',
    'MACOS_NOTARY_PASSWORD': 'abcd-efgh-ijkl-mnop',
}
UPDATER_PUBLIC_KEY = 'yrQPn0gg1/gASLxIQiUCIiUQ/csWENI0+up1cs3KWO0='  # must never change with signing
results = []


def record(case, **details):
    results.append(dict(case=case, **details))
    print('ok', case)


class Fixture:
    """A temporary state folder, stand-in tools first on PATH and a mygo.json copy."""

    def __init__(self, temporary, name, scenario=None):
        self.dir = pathlib.Path(temporary) / name
        self.state = self.dir / 'state'
        self.bin = self.dir / 'bin'
        self.state.mkdir(parents=True)
        self.bin.mkdir()
        (self.state / 'scenario.json').write_text(json.dumps(scenario or {}))
        for tool in ('security', 'codesign', 'xcrun', 'spctl', 'hdiutil', 'diskutil'):
            shim = self.bin / tool
            shim.write_text('#!/bin/sh\nexec "%s" "%s" %s "$@"\n' % (sys.executable, TOOLS, tool))
            shim.chmod(0o755)
        config = json.loads((ROOT / 'mygo.json').read_text())
        self.keychain = self.dir / 'tmp/yesman-release-signing/signing.keychain-db'
        self.keychain.parent.parent.mkdir(parents=True)
        config['macos']['notarize']['keychain'] = str(self.keychain)
        self.config = self.dir / 'mygo.json'
        self.config.write_text(json.dumps(config))
        self.output = self.dir / 'github-output'
        self.output.write_text('')

    def env(self, inputs=None):
        env = {k: v for k, v in os.environ.items() if not k.startswith(('MACOS_SIGNING_', 'MACOS_NOTARY_'))}
        env.update(inputs or {})
        env.update(PATH=str(self.bin) + os.pathsep + env['PATH'], APPLE_TOOLS_STATE=str(self.state), GITHUB_OUTPUT=str(self.output))
        return env

    def signing(self, *args, inputs=None):
        return subprocess.run([sys.executable, str(SIGNING), '--config', str(self.config), *args], env=self.env(inputs), capture_output=True, text=True)

    def calls(self, tool=None):
        log = self.state / 'log.jsonl'
        entries = [json.loads(line) for line in log.read_text().splitlines()] if log.exists() else []
        return [e for e in entries if tool is None or e['tool'] == tool]

    def search(self):
        file = self.state / 'search.json'
        return json.loads(file.read_text()) if file.exists() else None

    def outputs(self):
        return dict(line.split('=', 1) for line in self.output.read_text().splitlines())


def full_inputs(**changes):
    inputs = dict(SENTINELS, MACOS_SIGNING_TEAM_ID=TEAM, MACOS_SIGNING_CERTIFICATE_P12=base64.encodebytes(P12).decode())
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


def assert_clean(fixture, before):
    assert not os.path.lexists(fixture.keychain.parent), 'signing folder remains'
    after = fixture.search()
    assert after is None or after == before, ('search list not restored', after)


def setup_cases(temporary):
    login = ['/Users/fixture/Library/Keychains/login.keychain-db']

    fixture = Fixture(temporary, 'unconfigured')
    process = fixture.signing('setup', inputs={})
    assert process.returncode == 0 and fixture.outputs() == {'mode': 'adhoc'}, process.stderr
    assert fixture.calls() == [] and not fixture.keychain.parent.exists()
    record('unconfigured-stays-adhoc')

    partial = {
        'team-only': {'MACOS_SIGNING_TEAM_ID': TEAM},
        'team-as-secret-not-variable': full_inputs(MACOS_SIGNING_TEAM_ID=None),
        'no-notary-password': full_inputs(MACOS_NOTARY_PASSWORD=None),
        'blank-certificate': full_inputs(MACOS_SIGNING_CERTIFICATE_P12='  \n'),
    }
    for name, inputs in partial.items():
        fixture = Fixture(temporary, 'partial-' + name, good_identity())
        process = fixture.signing('setup', inputs=inputs)
        assert process.returncode == 1 and 'only partly configured' in process.stderr, process.stderr
        assert fixture.calls() == [] and fixture.outputs() == {} and not fixture.keychain.parent.exists()
        assert_no_leak(fixture, process)
        record('partial-config-fails', variant=name)

    for name, inputs, message in [
        ('bad-team-id', full_inputs(MACOS_SIGNING_TEAM_ID='idy'), 'must be the 10-character Team ID'),
        ('not-base64', full_inputs(MACOS_SIGNING_CERTIFICATE_P12='not base64 !!'), 'not base64'),
    ]:
        fixture = Fixture(temporary, name, good_identity())
        process = fixture.signing('setup', inputs=inputs)
        assert process.returncode == 1 and message in process.stderr, process.stderr
        assert fixture.calls() == [] and fixture.outputs() == {}
        record('invalid-input-fails', variant=name)

    # The full path, then cleanup.
    fixture = Fixture(temporary, 'configured', good_identity())
    process = fixture.signing('setup', inputs=full_inputs())
    assert process.returncode == 0, process.stderr
    assert fixture.outputs() == {'mode': 'developer-id', 'identity': SHA, 'team': TEAM, 'name': NAME}
    assert fixture.keychain.is_file() and fixture.search() == [str(fixture.keychain)] + login
    folder_mode = oct(fixture.keychain.parent.stat().st_mode & 0o777)
    assert folder_mode == '0o700', folder_mode
    imported = [c for c in fixture.calls('security') if c['args'][0] == 'import'][0]
    assert imported['p12'] == {'mode': '0o600', 'folderMode': '0o700', 'sha256': hashlib.sha256(P12).hexdigest()}
    assert imported['args'][-2:] == ['-T', '/usr/bin/codesign'], 'the key must be usable only by codesign'
    assert not (fixture.keychain.parent / 'identity.p12').exists(), 'the .p12 must not outlive its import'
    probe = [c for c in fixture.calls('codesign') if c.get('kind') == 'probe']
    assert probe and probe[0]['args'][:3] == ['--force', '--sign', SHA], probe
    requirement = [c['requirement'] for c in fixture.calls('codesign') if c.get('requirement')][0]
    assert 'certificate leaf[field.1.2.840.113635.100.6.1.13] exists' in requirement and 'leaf[subject.OU] = "%s"' % TEAM in requirement
    store = [c for c in fixture.calls('xcrun') if c['args'][:2] == ['notarytool', 'store-credentials']]
    assert len(store) == 1 and store[0]['args'][2] == 'yesman-release-notary', store
    assert store[0]['args'][store[0]['args'].index('--keychain') + 1] == str(fixture.keychain)
    assert store[0]['args'][store[0]['args'].index('--team-id') + 1] == TEAM
    assert_no_leak(fixture, process)
    process = fixture.signing('cleanup')
    assert process.returncode == 0, process.stderr
    assert_clean(fixture, login)
    assert fixture.signing('cleanup').returncode == 0, 'cleanup must be repeatable'
    record('configured-prepares-and-cleans', search=login)

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
        'probe-sign-fails': (dict(good_identity(), fail=['probe-sign']), 'Signing a preflight file failed'),
        'notary-credentials-rejected': (dict(good_identity(), fail=['store-credentials']), 'Storing the notary credentials failed'),
    }
    for name, (scenario, message) in failures.items():
        fixture = Fixture(temporary, name, scenario)
        process = fixture.signing('setup', inputs=full_inputs())
        assert process.returncode == 1 and message in process.stderr, (name, process.stderr)
        assert 'mode' not in fixture.outputs(), 'a failed setup must not hand the build a mode'
        assert_clean(fixture, login)
        assert_no_leak(fixture, process)
        if name in ('wrong-p12-password', 'notary-credentials-rejected'):
            assert '***' in process.stderr, 'tool output echoing a secret must be redacted'
        record('setup-failure-cleans-up', variant=name)

    # A folder setup did not make is refused and left alone.
    fixture = Fixture(temporary, 'foreign-folder', good_identity())
    fixture.keychain.parent.mkdir()
    (fixture.keychain.parent / 'someone-elses').write_text('keep')
    process = fixture.signing('setup', inputs=full_inputs())
    assert process.returncode == 1 and 'refusing to reuse' in process.stderr and (fixture.keychain.parent / 'someone-elses').exists()
    assert fixture.signing('cleanup').returncode == 1 and (fixture.keychain.parent / 'someone-elses').exists()
    record('foreign-folder-untouched')

    real = Fixture(temporary, 'committed-config')
    profile = subprocess.run([sys.executable, '-c', 'import sys; sys.path.insert(0, %r); import macos_signing as m; print(m.configured_keychain(%r))' % (str(SCRIPTS), str(ROOT / 'mygo.json'))],
                             capture_output=True, text=True, env=real.env())
    assert profile.returncode == 0 and "'yesman-release-notary'" in profile.stdout, profile.stderr
    record('committed-config-accepted')


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
        ('different-requirements', {'darwin-amd64/yesman.app': {'dr': 'identifier "dev.idy.yesman" and anchor apple generic and certificate leaf = H"00"'}}, 'different designated requirements'),
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


def step_script(workflow, step):
    text = (ROOT / '.github/workflows' / workflow).read_text()
    block = text.split('      - name: %s\n' % step, 1)[1]
    block = re.split(r'\n      - (?:name|uses):', block, 1)[0]
    run = block.split('        run: |\n', 1)[1]
    lines = [line[10:] if line.startswith('          ') else line.strip() for line in run.splitlines()]
    return '\n'.join(lines) + '\n'


def workflow_cases(temporary):
    folder = pathlib.Path(temporary) / 'workflow'
    folder.mkdir()
    fake = folder / 'bin'
    fake.mkdir()
    called = folder / 'called'
    for name in ('go', 'python3'):
        (fake / name).write_text('#!/bin/sh\nprintf "%%s\\n" "%s" "$@" > "%s"\n' % (name, called))
        (fake / name).chmod(0o755)
    build = step_script('release.yml', 'Build and upload')
    for system, platform, mode, identity, expected in [
        ('macOS', 'darwin/arm64,darwin/amd64', 'adhoc', '', ['-skip-notarize']),
        ('macOS', 'darwin/arm64,darwin/amd64', 'developer-id', SHA, ['-sign', SHA]),
        ('macOS', 'darwin/arm64,darwin/amd64', 'developer-id', '', None),
        ('macOS', 'darwin/arm64,darwin/amd64', 'developer-id', 'Developer ID Application: x', None),
        ('macOS', 'darwin/arm64,darwin/amd64', '', '', None),
        ('Windows', 'windows/amd64,windows/arm64', '', '', ['-skip-notarize']),
        ('Linux', 'linux/amd64,linux/arm64', 'developer-id', SHA, ['-skip-notarize']),
    ]:
        if called.exists():
            called.unlink()
        env = dict(os.environ, PATH=str(fake) + os.pathsep + os.environ['PATH'], PLATFORMS=platform, SYSTEM=system, SIGNING_MODE=mode, SIGNING_IDENTITY=identity)
        process = subprocess.run(['bash', '-e', '-c', build], env=env, capture_output=True, text=True)
        if expected is None:
            assert process.returncode != 0 and not called.exists(), (system, mode, identity)
        else:
            assert process.returncode == 0, process.stderr
            assert called.read_text().splitlines() == ['go', 'tool', 'mygo', 'build', '-platform', platform, '-upload'] + expected
        record('release-build-arguments', system=system, mode=mode or None, identity=bool(identity), refused=expected is None)

    smoke = step_script('macos-draft-smoke.yml', 'Verify unchanged final app startup')
    for team, extra in [('', []), (TEAM, ['--expect-team-id', TEAM])]:
        if called.exists():
            called.unlink()
        env = dict(os.environ, PATH=str(fake) + os.pathsep + os.environ['PATH'], RUNNER_TEMP='/r', RELEASE_TAG='v0.1.0', TARGET_ARCH='arm64', RELEASE_SOURCE='0' * 40, EXPECTED_TEAM_ID=team)
        process = subprocess.run(['bash', '-e', '-c', smoke], env=env, capture_output=True, text=True)
        assert process.returncode == 0, process.stderr
        assert called.read_text().splitlines()[1:] == ['scripts/check_macos_startup.py', '/r/draft/yesman.app', 'web', '/r/startup-evidence',
                                                       '--release-tag', 'v0.1.0', '--arch', 'arm64', '--source', '0' * 40] + extra
        record('draft-smoke-signature-policy', expectTeam=bool(team))

    # Where the signing inputs may go: secrets to one release step, the
    # public Team ID also to the draft gate; nothing to CI, review or pages.
    allowed = {('release.yml', 'Prepare macOS signing'): {'secrets.MACOS_SIGNING_CERTIFICATE_P12', 'secrets.MACOS_SIGNING_CERTIFICATE_PASSWORD',
                                                          'secrets.MACOS_NOTARY_APPLE_ID', 'secrets.MACOS_NOTARY_PASSWORD', 'vars.MACOS_SIGNING_TEAM_ID'},
               ('macos-draft-smoke.yml', 'Verify unchanged final app startup'): {'vars.MACOS_SIGNING_TEAM_ID'}}
    seen = {}
    for workflow in sorted((ROOT / '.github/workflows').glob('*.yml')):
        step = None
        text = workflow.read_text()
        assert 'toJSON(secrets)' not in text and 'secrets: inherit' not in text, workflow.name
        for line in text.splitlines():
            named = re.match(r'\s+- name: (.+)$', line)
            if named:
                step = named.group(1)
            for use in re.findall(r'(?:secrets|vars)\.MACOS_[A-Z0-9_]+', line):
                seen.setdefault((workflow.name, step), set()).add(use)
    assert seen == allowed, seen
    record('signing-inputs-confined', steps=sorted('%s: %s' % key for key in seen))

    config = json.loads((ROOT / 'mygo.json').read_text())
    assert config['identifier'] == 'dev.idy.yesman' and config['updates']['publicKey'] == UPDATER_PUBLIC_KEY
    assert 'signingIdentity' not in config['macos'], 'the identity comes from the prepared keychain, not the repository'
    assert config['macos']['entitlements'] == 'packaging/macos/permissions.entitlements.plist'
    record('identity-and-updater-key-unchanged')


def go_env(temporary):
    env = dict(os.environ)
    for name in ('GOCACHE', 'GOMODCACHE', 'GOPATH', 'HOME', 'GOTMPDIR', 'TMPDIR'):
        assert env.get(name), name + ' must be set to a private folder for --mygo'
    env.update(GOENV='off', GOTOOLCHAIN='local', GOFLAGS='-mod=mod', CGO_ENABLED='1', MACOSX_DEPLOYMENT_TARGET='13.0', CGO_CFLAGS='-O2 -g -mmacosx-version-min=13.0')
    for name in ('GH_TOKEN', 'GITHUB_TOKEN', 'MYGO_UPDATER_PRIVATE_KEY'):
        env.pop(name, None)
    return env


def mygo_cases(temporary):
    """The real MyGo v0.9.0 build of a copy of this repository, Apple tools stood in."""
    assert sys.platform == 'darwin', '--mygo needs macOS'
    source = pathlib.Path(temporary) / 'source'
    files = subprocess.check_output(['git', 'ls-files', '-z', '--cached', '--others', '--exclude-standard'], cwd=ROOT).decode().split('\0')
    for name in filter(None, files):
        target = source / name
        target.parent.mkdir(parents=True, exist_ok=True)
        if (ROOT / name).is_file():
            shutil.copy2(ROOT / name, target)
    env = go_env(temporary)
    keys = pathlib.Path(temporary) / 'update-keys'
    subprocess.run(['go', 'tool', 'mygo', 'keygen', '-o', str(keys)], cwd=source, env=env, check=True, capture_output=True)
    config = json.loads((source / 'mygo.json').read_text())
    committed = json.loads((ROOT / 'mygo.json').read_text())
    # A throwaway update key, and no deltas from published releases: the
    # only differences from the committed configuration.
    config['updates']['publicKey'] = (keys / 'mygo-update.pub').read_text().strip()
    config['updates']['deltas'] = 0
    (source / 'mygo.json').write_text(json.dumps(config, indent=2))
    del config['updates']['deltas']
    config['updates']['publicKey'] = committed['updates']['publicKey']
    assert config == committed
    env['MYGO_UPDATER_PRIVATE_KEY'] = (keys / 'mygo-update.key').read_text().strip()
    notarize = committed['macos']['notarize']

    def build(name, platforms, args, scenario):
        fixture = Fixture(temporary, 'mygo-' + name, scenario)
        out = fixture.dir / 'out'
        run_env = dict(env, PATH=str(fixture.bin) + os.pathsep + env['PATH'], APPLE_TOOLS_STATE=str(fixture.state))
        process = subprocess.run(['go', 'tool', 'mygo', 'build', '-platform', platforms, '-o', str(out), *args], cwd=source, env=run_env, capture_output=True, text=True)
        return fixture, out, process

    def archives(out, arch):
        return sorted((out / ('darwin-' + arch)).glob('*.tar.gz')) if (out / ('darwin-' + arch)).exists() else []

    # Configured: the release workflow passes -sign <identity>.
    fixture, out, process = build('signed', 'darwin/arm64,darwin/amd64', ['-sign', SHA], {})
    assert process.returncode == 0, process.stderr[-3000:]
    order = [(c['tool'], c['args']) for c in fixture.calls() if not c.get('passthrough') and c['tool'] in ('codesign', 'xcrun')]
    for arch in ('arm64', 'amd64'):
        stage = out / ('darwin-' + arch)
        # MyGo signs, notarizes and packs in a staging folder that it renames
        # to darwin-<arch> only once everything there succeeded.
        staging = {pathlib.Path(a).parent for _, args in order for a in args if re.search(r'/\.staging-darwin-%s-\d+/yesman\.app$' % arch, a)}
        assert len(staging) == 1, (arch, staging)
        staging = staging.pop()
        dmg_name = 'yesman-%s-%s.dmg' % (committed['version'], arch)
        app, dmg = str(staging / 'yesman.app'), str(staging / dmg_name)
        expected = [
            ('codesign', ['--force', '--deep', '--sign', SHA, '--options', 'runtime', '--timestamp', '--entitlements']),
            ('codesign', ['--force', '--timestamp', '--sign', SHA, dmg]),
            ('xcrun', ['notarytool', 'submit', dmg, '--keychain-profile', notarize['keychainProfile'], '--wait', '--output-format', 'json', '--keychain', notarize['keychain']]),
            ('xcrun', ['stapler', 'staple', dmg]),
            ('xcrun', ['stapler', 'staple', app]),
        ]
        mine = [(tool, args) for tool, args in order if any(app in a or dmg in a for a in args)]
        assert len(mine) == len(expected), (arch, mine)
        assert mine[0][1][:8] == expected[0][1] and mine[0][1][-1] == app, mine[0]
        assert pathlib.Path(mine[0][1][8]).read_bytes() == (ROOT / committed['macos']['entitlements']).read_bytes(), 'entitlements changed'
        assert mine[1:] == expected[1:], (arch, mine[1:])
        [archive] = archives(out, arch)
        with tarfile.open(archive) as tar:
            assert 'yesman.app/Contents/.fixture-notarization-ticket' in tar.getnames(), 'the update archive must hold the stapled app'
        assert (stage / dmg_name).read_bytes().endswith(b'fixture-notarization-ticket'), 'the disk image must be stapled'
        manifest = json.loads((stage / ('update-darwin-%s.json' % arch)).read_text())
        assert manifest['size'] == archive.stat().st_size and manifest['signature']
    sums = list(out.rglob('SHA256SUMS'))
    for line in (sums[0].read_text().splitlines() if sums else []):
        digest, name = line.split(None, 1)
        paths = list(out.rglob(name.lstrip('*')))  # install.sh is one script for both
        assert paths and all(hashlib.sha256(p.read_bytes()).hexdigest() == digest for p in paths), name
    record('mygo-signed-notarized-before-archives', architectures=['arm64', 'amd64'], checksums=bool(sums))

    for name, scenario, args, message, reached in [
        ('codesign-fails', {'fail': ['app-sign']}, ['-sign', SHA], 'errSecInternalComponent', 'no-notarization'),
        ('notarization-invalid', {'fail': ['notarize']}, ['-sign', SHA], 'notarization failed (Invalid', 'no-staple'),
        ('staple-fails', {'fail': ['staple']}, ['-sign', SHA], 'Error 65', 'staple'),
        ('adhoc-without-skip-notarize', {}, [], 'notarization needs a Developer ID signing identity', 'nothing'),
    ]:
        fixture, out, process = build(name, 'darwin/arm64', args, scenario)
        assert process.returncode != 0 and message in process.stdout + process.stderr, (name, process.stdout[-2000:], process.stderr[-2000:])
        if out.exists():
            assert archives(out, 'arm64') == [] and not list(out.rglob('update-*.json')), 'a failed build must not leave update files'
        calls = [c for c in fixture.calls() if not c.get('passthrough')]
        if reached == 'nothing':
            assert not [c for c in calls if c['tool'] in ('codesign', 'xcrun')], calls
        if reached == 'no-notarization':
            assert not [c for c in calls if c['tool'] == 'xcrun'], calls
        if reached == 'no-staple':
            assert not [c for c in calls if c['args'][:2] == ['stapler', 'staple']], calls
        record('mygo-failure-stops-before-archives', variant=name)

    # Unconfigured: ad hoc as before, with notarization explicitly skipped.
    fixture, out, process = build('adhoc', 'darwin/arm64,darwin/amd64', ['-skip-notarize'], {})
    assert process.returncode == 0, process.stderr[-3000:]
    assert not [c for c in fixture.calls('xcrun') if not c.get('passthrough')], 'ad-hoc builds must not notarize'
    signed = [c for c in fixture.calls('codesign') if '--sign' in c['args']]
    assert signed and all(c['args'][c['args'].index('--sign') + 1] == '-' and '--options' not in c['args'] for c in signed), signed
    assert not [c for c in signed if c['args'][-1].endswith('.dmg')], 'ad-hoc disk images stay unsigned'
    for arch in ('arm64', 'amd64'):
        assert len(archives(out, arch)) == 1
        app = out / ('darwin-' + arch) / 'yesman.app'
        assert 'Signature=adhoc' in subprocess.run(['/usr/bin/codesign', '-dv', str(app)], capture_output=True, text=True).stderr
    record('mygo-unconfigured-adhoc-unchanged', architectures=['arm64', 'amd64'])


def real_keychain_cases(temporary):
    """Real security/codesign: a synthetic self-signed identity is refused, nothing remains."""
    assert sys.platform == 'darwin' and os.environ.get('GITHUB_ACTIONS') == 'true' and os.environ.get('RUNNER_ENVIRONMENT') == 'github-hosted', \
        'Refusing to touch a keychain outside a disposable GitHub-hosted runner'
    folder = pathlib.Path(temporary) / 'real'
    folder.mkdir()
    team = 'SYNTHTEAM1'
    passphrase = base64.b32encode(os.urandom(15)).decode()
    openssl = '/usr/bin/openssl'
    subprocess.run([openssl, 'req', '-x509', '-newkey', 'rsa:2048', '-nodes', '-days', '1', '-keyout', str(folder / 'key.pem'), '-out', str(folder / 'cert.pem'),
                    '-subj', '/CN=Developer ID Application: Synthetic Fixture (%s)/OU=%s' % (team, team),
                    '-addext', 'extendedKeyUsage=codeSigning', '-addext', 'keyUsage=critical,digitalSignature'], check=True, capture_output=True)
    subprocess.run([openssl, 'pkcs12', '-export', '-inkey', str(folder / 'key.pem'), '-in', str(folder / 'cert.pem'), '-out', str(folder / 'identity.p12'),
                    '-passout', 'pass:' + passphrase], check=True, capture_output=True)
    config = json.loads((ROOT / 'mygo.json').read_text())
    keychain = folder / 'yesman-release-signing/signing.keychain-db'
    config['macos']['notarize']['keychain'] = str(keychain)
    (folder / 'mygo.json').write_text(json.dumps(config))
    listed = subprocess.check_output(['security', 'list-keychains', '-d', 'user']).decode()
    env = {k: v for k, v in os.environ.items() if not k.startswith(('MACOS_SIGNING_', 'MACOS_NOTARY_'))}
    env.update(MACOS_SIGNING_TEAM_ID=team, MACOS_SIGNING_CERTIFICATE_P12=base64.b64encode((folder / 'identity.p12').read_bytes()).decode(),
               MACOS_SIGNING_CERTIFICATE_PASSWORD=passphrase, MACOS_NOTARY_APPLE_ID='synthetic@example.invalid', MACOS_NOTARY_PASSWORD='synthetic-not-a-password',
               GITHUB_OUTPUT=str(folder / 'output'))
    process = subprocess.run([sys.executable, str(SIGNING), '--config', str(folder / 'mygo.json'), 'setup'], env=env, capture_output=True, text=True)
    assert process.returncode == 1 and 'not valid on the runner' in process.stderr, process.stdout + process.stderr
    assert passphrase not in process.stdout + process.stderr
    assert not os.path.lexists(keychain.parent), 'keychain folder remains'
    assert subprocess.check_output(['security', 'list-keychains', '-d', 'user']).decode() == listed, 'search list changed'
    assert not (folder / 'output').exists() or 'mode=' not in (folder / 'output').read_text()
    record('real-keychain-refuses-self-signed', team=team)


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument('--mygo', action='store_true')
    parser.add_argument('--real-keychain', action='store_true')
    parser.add_argument('--report')
    args = parser.parse_args()
    with tempfile.TemporaryDirectory(prefix='yesman-signing-test-') as temporary:
        setup_cases(temporary)
        verify_cases(temporary)
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
