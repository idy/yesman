#!/usr/bin/env python3
"""Prepare, remove and verify the Developer ID signing of a macOS release build.

Used by the macOS row of .github/workflows/release.yml; see
docs/macos-signing.md. Commands:

  team     Prints the Team ID that packaging/macos/signing.json declares, or
           nothing. The source of a release decides whether it is signed.
  setup    Without a Team ID and without signing secrets, says the build is
           ad hoc, as before. With the Team ID and all four secrets, and only
           on a disposable GitHub-hosted runner, makes a fresh directory of
           its own under RUNNER_TEMP holding a short-lived keychain with the
           team's Developer ID Application identity and the notarytool
           profile, proves the identity, and writes a receipt of the
           directory as the step output `state`. Anything partial, invalid
           or failing fails: it never falls back to ad-hoc signing.
  cleanup  Given that `state` (SIGNING_STATE), removes exactly that keychain,
           its search-list entry and its directory, after checking that the
           directory is still the one setup made (device, inode, owner and
           the receipt's random nonce). Without a state it does nothing.
  verify   After the build, requires both apps and disk images to carry the
           prepared identity's notarized, stapled signature, and both apps
           Apple's Developer ID designated requirement of the team, each
           satisfying the other's.

Secrets arrive only in setup's environment. They are never printed, written
to GITHUB_OUTPUT/GITHUB_ENV or put in a child process's environment. The
certificate's passphrase and the app-specific password do reach `security
import` and `notarytool store-credentials` as arguments, the only way those
tools take them without a terminal; error messages never include arguments.
"""
import argparse
import base64
import binascii
import hmac
import json
import os
import pathlib
import re
import secrets
import shutil
import signal
import stat
import subprocess
import sys
import tempfile

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent))
from check_macos_bundle import IDENTIFIER, TEAM_ID, check_developer_id, developer_id_requirement  # noqa: E402

ROOT = pathlib.Path(__file__).resolve().parent.parent
TEAM_FILE = pathlib.Path('packaging/macos/signing.json')
SECRETS = ('MACOS_SIGNING_CERTIFICATE_P12', 'MACOS_SIGNING_CERTIFICATE_PASSWORD', 'MACOS_NOTARY_APPLE_ID', 'MACOS_NOTARY_PASSWORD')
# Names that never reach a child process: these, and any matching SECRET_NAME.
SECRET_NAMES = SECRETS + ('GH_TOKEN', 'GITHUB_TOKEN', 'MYGO_UPDATER_PRIVATE_KEY', 'MYGO_WINDOWS_CERTIFICATE_PASSWORD', 'SIGNING_STATE')
SECRET_NAME = re.compile(r'TOKEN|SECRET|PASSWORD|PASSWD|PRIVATE|CREDENTIAL|P12', re.I)
IDENTITY_NAME = re.compile(r'Developer ID Application: [^\n\r"]+ \(([A-Z0-9]{10})\)')
IDENTITY_LINE = re.compile(r'^\s*\d+\) ([0-9A-F]{40}) "([^"\n]*)"', re.M)
CODESIGN = '/usr/bin/codesign'
PROFILE = 'yesman-release-notary'
KEYCHAIN = 'signing.keychain-db'
RECEIPT = 'receipt.json'
DEADLINE = 300  # seconds for any one tool; notarytool validates credentials online


class SigningError(Exception):
    """A failure whose message is safe to print: no secret, no argument list."""


def clean_environment(environ, secret_values=()):
    """environ without signing, updater or upload credentials, for child processes.

    Refuses when a secret's value is still present under another name.
    """
    env = {k: v for k, v in environ.items() if k not in SECRET_NAMES and not SECRET_NAME.search(k)}
    for value in secret_values:
        if value and len(value) >= 8 and any(value in v for v in env.values()):
            raise SigningError('A credential appears in another environment variable; refusing to pass it to child processes')
    return env


def run(label, args, redact=(), check=True, timeout=None):
    """Run a tool; on failure report label and its redacted output, never args."""
    timeout = timeout or DEADLINE
    try:
        process = subprocess.run(args, capture_output=True, env=clean_environment(os.environ), stdin=subprocess.DEVNULL, timeout=timeout)
    except subprocess.TimeoutExpired:
        raise SigningError('%s did not finish within %d seconds' % (label, timeout))
    out = (process.stdout[-65536:] + process.stderr[-65536:]).decode(errors='replace')
    for value in redact:
        if value:
            out = out.replace(value, '***')
    if check and process.returncode != 0:
        tail = '\n'.join(out.strip().splitlines()[-6:])
        raise SigningError('%s failed (exit %d)%s' % (label, process.returncode, (':\n' + tail) if tail else ''))
    return process.returncode, out


def declared_team(root=ROOT):
    """The Team ID of packaging/macos/signing.json: '' for ad hoc, else 10 characters."""
    path = pathlib.Path(root) / TEAM_FILE
    try:
        data = json.loads(path.read_text())
    except FileNotFoundError:
        return ''
    except ValueError:
        raise SigningError('%s is not valid JSON' % TEAM_FILE)
    if not isinstance(data, dict) or set(data) != {'teamId'} or not isinstance(data['teamId'], str):
        raise SigningError('%s must hold exactly {"teamId": "<Team ID or empty>"}' % TEAM_FILE)
    team = data['teamId']
    if team and not TEAM_ID.fullmatch(team):
        raise SigningError('%s teamId must be the 10-character Team ID shown in the Apple Developer account Membership details' % TEAM_FILE)
    return team


def search_list():
    _, out = run('Reading the keychain search list', ['security', 'list-keychains', '-d', 'user'])
    return [line.strip().strip('"') for line in out.splitlines() if line.strip()]


def set_search_list(paths):
    run('Setting the keychain search list', ['security', 'list-keychains', '-d', 'user', '-s', *paths])


def same_file(a, b):
    return os.path.realpath(a) == os.path.realpath(b)


def identities(keychain, valid):
    """(SHA-1, name) of the code-signing identities in keychain, all or only valid ones."""
    _, out = run('Listing signing identities', ['security', 'find-identity', *(['-v'] if valid else []), '-p', 'codesigning', str(keychain)])
    if not valid:
        out = out.split('Valid identities only', 1)[0]
    return IDENTITY_LINE.findall(out)


def write_output(values):
    for key, value in values.items():
        if '\n' in value or '\r' in value:
            raise SigningError('Refusing a multi-line step output')
    target = os.environ.get('GITHUB_OUTPUT')
    if target:
        with open(target, 'a') as out:
            for key, value in values.items():
                out.write('%s=%s\n' % (key, value))


def require_hosted_runner():
    """Keychains and the search list are only changed on a disposable hosted runner."""
    if not (os.environ.get('GITHUB_ACTIONS') == 'true' and os.environ.get('RUNNER_ENVIRONMENT') == 'github-hosted'
            and os.environ.get('RUNNER_TEMP')):
        raise SigningError('Developer ID signing is prepared only on a GitHub-hosted runner; it would change this account\'s keychain search list')


def make_state():
    """A fresh directory of this invocation's own, and its receipt as an opaque step output."""
    folder = pathlib.Path(tempfile.mkdtemp(prefix='yesman-signing-', dir=os.environ['RUNNER_TEMP']))
    nonce = secrets.token_hex(32)
    fd = os.open(folder / RECEIPT, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
    with os.fdopen(fd, 'w') as out:
        out.write(json.dumps({'nonce': nonce}))
    info = os.lstat(folder)
    state = {'dir': str(folder), 'dev': info.st_dev, 'ino': info.st_ino, 'nonce': nonce}
    return folder, base64.urlsafe_b64encode(json.dumps(state).encode()).decode()


def parse_state(text):
    try:
        state = json.loads(base64.urlsafe_b64decode(text.encode()))
        folder = pathlib.Path(state['dir'])
        assert folder.is_absolute() and isinstance(state['dev'], int) and isinstance(state['ino'], int)
        assert re.fullmatch(r'[0-9a-f]{64}', state['nonce'])
    except Exception:
        raise SigningError('The signing state is not one that setup wrote')
    return folder, state


def owned(folder, state):
    """Whether folder is still the directory setup made: same device and inode, ours, its receipt's nonce."""
    try:
        fd = os.open(folder, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
    except OSError:
        return False
    try:
        info = os.fstat(fd)
        if (info.st_dev, info.st_ino) != (state['dev'], state['ino']) or info.st_uid != os.getuid() or info.st_mode & 0o077:
            return False
        try:
            receipt = os.open(RECEIPT, os.O_RDONLY | os.O_NOFOLLOW, dir_fd=fd)
        except OSError:
            return False
        with os.fdopen(receipt) as source:
            if not stat.S_ISREG(os.fstat(source.fileno()).st_mode):
                return False
            try:
                nonce = json.loads(source.read(4096)).get('nonce', '')
            except ValueError:
                return False
        return hmac.compare_digest(str(nonce), state['nonce'])
    finally:
        os.close(fd)


def prepared(text):
    """The notary profile and keychain of a validated state, for the release build."""
    folder, state = parse_state(text)
    keychain = folder / KEYCHAIN
    if not owned(folder, state) or not stat.S_ISREG(os.lstat(keychain).st_mode):
        raise SigningError('The signing state does not name a keychain that setup prepared')
    return PROFILE, keychain


def remove(folder, state):
    """Remove the keychain of folder, its search-list entry and folder, if setup made it."""
    keychain = folder / KEYCHAIN
    problems = []
    if os.path.lexists(folder):
        if not owned(folder, state):
            raise SigningError('%s is not the directory this setup made: leaving it alone' % folder)
        if os.path.lexists(keychain) and stat.S_ISREG(os.lstat(keychain).st_mode):
            code, _ = run('Deleting the signing keychain', ['security', 'delete-keychain', str(keychain)], check=False)
            if code != 0 and os.path.lexists(keychain):
                problems.append('could not delete the keychain')
    try:
        listed = search_list()
        if any(same_file(p, keychain) for p in listed):
            set_search_list([p for p in listed if not same_file(p, keychain)])
            if any(same_file(p, keychain) for p in search_list()):
                problems.append('the keychain is still in the search list')
    except SigningError as error:
        problems.append(str(error))
    if os.path.lexists(folder):
        if not owned(folder, state):  # checked again just before deleting
            raise SigningError('%s changed during cleanup: leaving it alone' % folder)
        shutil.rmtree(folder, ignore_errors=True)
        if os.path.lexists(folder):
            problems.append('%s remains' % folder)
    if problems:
        raise SigningError('Signing cleanup incomplete: ' + '; '.join(problems))


def setup(root=ROOT):
    values = {name: os.environ.get(name, '').strip() for name in SECRETS}
    team = declared_team(root)
    if not team and not any(values.values()):
        print('macOS signing is not configured: building ad hoc, as before.')
        write_output({'mode': 'adhoc'})
        return
    missing = ([] if team else ['the teamId of %s' % TEAM_FILE]) + [name for name in SECRETS if not values[name]]
    if missing:
        raise SigningError('macOS signing is only partly configured; missing: %s. Set the Team ID and all of %s, or none to build ad hoc.'
                           % (', '.join(missing), ', '.join(SECRETS)))
    require_hosted_runner()
    try:
        p12 = base64.b64decode(''.join(values['MACOS_SIGNING_CERTIFICATE_P12'].split()), validate=True)
    except (binascii.Error, ValueError):
        raise SigningError('MACOS_SIGNING_CERTIFICATE_P12 is not base64 of a .p12 file')
    if not p12:
        raise SigningError('MACOS_SIGNING_CERTIFICATE_P12 is empty')
    password = secrets.token_urlsafe(32)
    redact = [values[name] for name in SECRETS] + [password]

    folder, state_text = make_state()
    # Written before any keychain exists, so the always-run cleanup step can
    # remove what a killed setup leaves.
    write_output({'state': state_text})
    try:
        keychain = folder / KEYCHAIN
        previous = search_list()
        run('Creating the signing keychain', ['security', 'create-keychain', '-p', password, str(keychain)], redact)
        if not keychain.is_file() or keychain.is_symlink():
            raise SigningError('The signing keychain was not created where setup asked')
        run('Configuring the signing keychain', ['security', 'set-keychain-settings', '-lut', '21600', str(keychain)], redact)
        run('Unlocking the signing keychain', ['security', 'unlock-keychain', '-p', password, str(keychain)], redact)
        certificate = folder / 'identity.p12'
        descriptor = os.open(certificate, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
        try:
            with os.fdopen(descriptor, 'wb') as out:
                out.write(p12)
            # The key's access list names codesign; the partition list below
            # lets Apple-signed tools and codesign use it without a prompt.
            run('Importing the signing certificate', ['security', 'import', str(certificate), '-k', str(keychain), '-f', 'pkcs12',
                                                      '-P', values['MACOS_SIGNING_CERTIFICATE_PASSWORD'], '-T', CODESIGN], redact)
        finally:
            certificate.unlink()
        run('Allowing codesign to use the key', ['security', 'set-key-partition-list', '-S', 'apple-tool:,apple:,codesign:', '-s', '-k', password, str(keychain)], redact)
        # codesign, as MyGo runs it, finds identities through the search list.
        set_search_list([str(keychain)] + [p for p in previous if not same_file(p, keychain)])

        every = identities(keychain, valid=False)
        valid = identities(keychain, valid=True)
        if len(every) != 1:
            raise SigningError('The .p12 must hold exactly one signing identity, the Developer ID Application one; it holds %d' % len(every))
        if len(valid) != 1:
            raise SigningError('The signing identity is not valid on the runner (expired, revoked or not issued by Apple)')
        sha1, name = valid[0]
        match = IDENTITY_NAME.fullmatch(name)
        if not match:
            raise SigningError('The identity is not a "Developer ID Application" certificate (Apple Development, Mac App Distribution and Installer certificates cannot be used)')
        if match.group(1) != team:
            raise SigningError('The Developer ID certificate belongs to team %s, not the teamId %s of %s' % (match.group(1), team, TEAM_FILE))

        # Sign a scratch file as MyGo will sign the app, through the search
        # list, and let codesign evaluate Apple's Developer ID policy for
        # this team.
        with tempfile.TemporaryDirectory(dir=folder) as scratch:
            probe = pathlib.Path(scratch) / 'probe.txt'
            probe.write_text('yesman signing preflight\n')
            run('Signing a preflight file', ['codesign', '--force', '--sign', sha1, '--timestamp=none', str(probe)], redact)
            run('Checking the Developer ID requirement', ['codesign', '--verify', '--strict', '-R=' + developer_id_requirement(team), str(probe)], redact)
            _, described = run('Describing the preflight signature', ['codesign', '-d', '--verbose=2', str(probe)], redact)
            if 'TeamIdentifier=%s\n' % team not in described + '\n':
                raise SigningError('The preflight signature names another team')

        run('Storing the notary credentials', ['xcrun', 'notarytool', 'store-credentials', PROFILE, '--apple-id', values['MACOS_NOTARY_APPLE_ID'],
                                               '--team-id', team, '--password', values['MACOS_NOTARY_PASSWORD'], '--keychain', str(keychain)], redact)
    except BaseException:
        # Leave nothing behind for the remaining steps of a failed job.
        try:
            remove(folder, parse_state(state_text)[1])
        except SigningError as error:
            print('::error::' + str(error), file=sys.stderr)
        raise
    write_output({'mode': 'developer-id', 'identity': sha1, 'team': team, 'name': name})
    print('Prepared Developer ID signing: %s (%s), notary profile %s' % (name, sha1, PROFILE))


def cleanup(state_text):
    if not state_text:
        print('This job prepared no signing keychain; nothing to remove.')
        return
    folder, state = parse_state(state_text)
    remove(folder, state)
    print('No signing keychain remains.')


def verify(build, team, identity, evidence, architectures):
    if not TEAM_ID.fullmatch(team or '') or not re.fullmatch(r'[0-9A-F]{40}', identity or ''):
        raise SigningError('verify needs the prepared team and identity')
    build, evidence = pathlib.Path(build), pathlib.Path(evidence)
    evidence.mkdir(parents=True, exist_ok=True)
    found = {}
    with tempfile.TemporaryDirectory() as work:
        for arch in architectures:
            stage = build / ('darwin-' + arch)
            app = stage / 'yesman.app'
            dmgs = sorted(stage.glob('*.dmg'))
            if not app.is_dir() or len(dmgs) != 1:
                raise SigningError('Expected one app and one disk image in ' + str(stage))
            try:
                found[arch] = {'app': check_developer_id(app, team, 'app', identity, work),
                               'dmg': check_developer_id(dmgs[0], team, 'dmg', identity, work)}
            except AssertionError as error:
                raise SigningError('darwin-%s: %s' % (arch, error))
    requirements = {arch: found[arch]['app']['designatedRequirement'] for arch in architectures}
    if len(set(requirements.values())) != 1:
        raise SigningError('The architectures have different designated requirements: %s' % json.dumps(requirements))
    for arch in architectures:
        for other in architectures:
            if other != arch:
                app = build / ('darwin-' + other) / 'yesman.app'
                if run('Cross-checking requirements', ['codesign', '--verify', '--strict', '-R=' + requirements[arch], str(app)], check=False)[0] != 0:
                    raise SigningError('darwin-%s does not satisfy the designated requirement of darwin-%s' % (other, arch))
    (evidence / 'macos-signing.json').write_text(json.dumps({'team': team, 'identity': identity, 'identifier': IDENTIFIER, 'architectures': found}, indent=2))
    print('Verified Developer ID signatures, notarization tickets and the designated requirement for', ', '.join(architectures))


def stop(signum, frame):
    raise SigningError('Stopped by signal %d' % signum)


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    commands = parser.add_subparsers(dest='command', required=True)
    commands.add_parser('team')
    commands.add_parser('setup')
    commands.add_parser('cleanup')
    check = commands.add_parser('verify')
    check.add_argument('build')
    check.add_argument('evidence')
    check.add_argument('--team', required=True)
    check.add_argument('--identity', required=True)
    check.add_argument('--arch', action='append', choices=('arm64', 'amd64'))
    args = parser.parse_args()
    # A cancelled job's TERM (or HUP/INT) unwinds setup's own cleanup; KILL
    # cannot be handled, which the always-run cleanup step and the
    # disposable runner cover.
    for number in (signal.SIGTERM, signal.SIGINT, signal.SIGHUP):
        signal.signal(number, stop)
    try:
        if args.command == 'team':
            print(declared_team())
        elif args.command == 'setup':
            setup()
        elif args.command == 'cleanup':
            cleanup(os.environ.get('SIGNING_STATE', '').strip())
        else:
            verify(args.build, args.team, args.identity, args.evidence, args.arch or ['arm64', 'amd64'])
    except SigningError as error:
        print('::error::' + str(error), file=sys.stderr)
        sys.exit(1)


if __name__ == '__main__':
    main()
