#!/usr/bin/env python3
"""Prepare, remove and verify the Developer ID signing of a macOS release build.

Used only by the macOS row of .github/workflows/release.yml; see
docs/macos-signing.md. Three commands:

  setup    With no signing configuration, says the build is ad hoc, as before.
           With all of it, makes a short-lived keychain at mygo.json's
           macos.notarize.keychain holding only the Developer ID Application
           identity of MACOS_SIGNING_TEAM_ID (usable only by codesign) and
           the notarytool profile macos.notarize.keychainProfile, and proves
           the identity before anything is built. Part of the configuration,
           or any failure, fails: it never falls back to ad-hoc signing.
  cleanup  Removes that keychain, its folder and its search-list entry. Always
           run it; it is safe when setup did nothing or failed half way.
  verify   After the build, requires both apps and disk images to carry the
           prepared identity's notarized, stapled signature, and the apps the
           same designated requirement, each satisfying the other's.

Secrets arrive only in setup's environment. They are never printed, written
to GITHUB_OUTPUT/GITHUB_ENV or passed to child processes' environments. The
certificate's passphrase and the app-specific password do reach `security
import` and `notarytool store-credentials` as arguments, the only way those
tools take them without a terminal; error messages never include arguments.
"""
import argparse
import base64
import binascii
import json
import os
import pathlib
import re
import secrets
import shutil
import subprocess
import sys
import tempfile

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent))
from check_macos_bundle import IDENTIFIER, TEAM_ID, check_developer_id, developer_id_requirement  # noqa: E402

ROOT = pathlib.Path(__file__).resolve().parent.parent
TEAM = 'MACOS_SIGNING_TEAM_ID'  # a repository variable: it is public in every signature
SECRETS = ('MACOS_SIGNING_CERTIFICATE_P12', 'MACOS_SIGNING_CERTIFICATE_PASSWORD', 'MACOS_NOTARY_APPLE_ID', 'MACOS_NOTARY_PASSWORD')
INPUTS = (TEAM,) + SECRETS
IDENTITY_NAME = re.compile(r'Developer ID Application: [^\n\r"]+ \(([A-Z0-9]{10})\)')
IDENTITY_LINE = re.compile(r'^\s*\d+\) ([0-9A-F]{40}) "([^"\n]*)"', re.M)
CODESIGN = '/usr/bin/codesign'
KEYCHAIN_DIR = 'yesman-release-signing'
MARKER = '.made-by-macos-signing'


class SigningError(Exception):
    """A failure whose message is safe to print: no secret, no argument list."""


made = []  # the signing folder, once this run of setup has made it


def child_environment():
    """The environment of every tool run here: none of the signing inputs."""
    return {k: v for k, v in os.environ.items() if k not in INPUTS}


def run(label, args, redact=(), check=True):
    """Run a tool; on failure report label and its redacted output, never args."""
    process = subprocess.run(args, capture_output=True, env=child_environment(), stdin=subprocess.DEVNULL)
    out = (process.stdout + process.stderr).decode(errors='replace')
    for value in redact:
        if value:
            out = out.replace(value, '***')
    if check and process.returncode != 0:
        tail = '\n'.join(out.strip().splitlines()[-6:])
        raise SigningError('%s failed (exit %d)%s' % (label, process.returncode, (':\n' + tail) if tail else ''))
    return process.returncode, out


def configured_keychain(config):
    """The notary profile and keychain path that mygo.json gives MyGo."""
    notarize = (json.loads(pathlib.Path(config).read_text()).get('macos') or {}).get('notarize') or {}
    profile, keychain = notarize.get('keychainProfile') or '', notarize.get('keychain') or ''
    if not re.fullmatch(r'[A-Za-z0-9][A-Za-z0-9._-]*', profile):
        raise SigningError('mygo.json macos.notarize.keychainProfile must be a plain profile name')
    path = pathlib.PurePosixPath(keychain)
    if not path.is_absolute() or path.name != 'signing.keychain-db' or path.parent.name != KEYCHAIN_DIR or '..' in path.parts:
        raise SigningError('mygo.json macos.notarize.keychain must be an absolute .../%s/signing.keychain-db' % KEYCHAIN_DIR)
    return profile, pathlib.Path(keychain)


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


def setup(config):
    values = {name: os.environ.get(name, '').strip() for name in INPUTS}
    present = [name for name in INPUTS if values[name]]
    if not present:
        print('macOS signing is not configured: building ad hoc, as before.')
        write_output({'mode': 'adhoc'})
        return
    missing = [name for name in INPUTS if not values[name]]
    if missing:
        raise SigningError('macOS signing is only partly configured; missing: %s. Set all of %s, or none to build ad hoc.'
                           % (', '.join(missing), ', '.join(INPUTS)))
    team = values[TEAM]
    if not TEAM_ID.fullmatch(team):
        raise SigningError('%s must be the 10-character Team ID shown in the Apple Developer account Membership details' % TEAM)
    profile, keychain = configured_keychain(config)
    folder = keychain.parent
    if os.path.lexists(folder):
        raise SigningError('%s already exists: refusing to reuse a signing keychain' % folder)
    secret_values = [values[name] for name in SECRETS]
    password = secrets.token_urlsafe(32)
    redact = secret_values + [password]
    try:
        p12 = base64.b64decode(''.join(values['MACOS_SIGNING_CERTIFICATE_P12'].split()), validate=True)
    except (binascii.Error, ValueError):
        raise SigningError('MACOS_SIGNING_CERTIFICATE_P12 is not base64 of a .p12 file')
    if not p12:
        raise SigningError('MACOS_SIGNING_CERTIFICATE_P12 is empty')
    os.mkdir(folder, 0o700)
    made.append(folder)
    (folder / MARKER).touch(0o600)  # cleanup removes only a folder that setup made
    (folder / 'search-list.json').write_text(json.dumps(search_list()))
    run('Creating the signing keychain', ['security', 'create-keychain', '-p', password, str(keychain)], redact)
    if not keychain.is_file():
        raise SigningError('The signing keychain was not created at the configured path')
    run('Configuring the signing keychain', ['security', 'set-keychain-settings', '-lut', '21600', str(keychain)], redact)
    run('Unlocking the signing keychain', ['security', 'unlock-keychain', '-p', password, str(keychain)], redact)
    certificate = folder / 'identity.p12'
    descriptor = os.open(certificate, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    try:
        with os.fdopen(descriptor, 'wb') as out:
            out.write(p12)
        # Only codesign may use the private key, without a prompt.
        run('Importing the signing certificate', ['security', 'import', str(certificate), '-k', str(keychain), '-f', 'pkcs12',
                                                  '-P', values['MACOS_SIGNING_CERTIFICATE_PASSWORD'], '-T', CODESIGN], redact)
    finally:
        certificate.unlink()
    run('Allowing codesign to use the key', ['security', 'set-key-partition-list', '-S', 'apple-tool:,apple:,codesign:', '-s', '-k', password, str(keychain)], redact)
    # codesign, as MyGo runs it, finds identities through the search list.
    set_search_list([str(keychain)] + [p for p in json.loads((folder / 'search-list.json').read_text()) if not same_file(p, keychain)])

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
        raise SigningError('The Developer ID certificate belongs to team %s, not %s=%s' % (match.group(1), TEAM, team))

    # Sign a scratch file as MyGo will sign the app, through the search list,
    # and let codesign evaluate Apple's Developer ID policy for this team.
    with tempfile.TemporaryDirectory(dir=folder) as scratch:
        probe = pathlib.Path(scratch) / 'probe.txt'
        probe.write_text('yesman signing preflight\n')
        run('Signing a preflight file', ['codesign', '--force', '--sign', sha1, '--timestamp=none', str(probe)], redact)
        run('Checking the Developer ID requirement', ['codesign', '--verify', '--strict', '-R=' + developer_id_requirement(team), str(probe)], redact)
        _, described = run('Describing the preflight signature', ['codesign', '-d', '--verbose=2', str(probe)], redact)
        if 'TeamIdentifier=%s\n' % team not in described + '\n':
            raise SigningError('The preflight signature names another team')

    run('Storing the notary credentials', ['xcrun', 'notarytool', 'store-credentials', profile, '--apple-id', values['MACOS_NOTARY_APPLE_ID'],
                                           '--team-id', team, '--password', values['MACOS_NOTARY_PASSWORD'], '--keychain', str(keychain)], redact)
    write_output({'mode': 'developer-id', 'identity': sha1, 'team': team, 'name': name})
    print('Prepared Developer ID signing: %s (%s), notary profile %s' % (name, sha1, profile))


def cleanup(config):
    _, keychain = configured_keychain(config)
    folder = keychain.parent
    problems = []
    if os.path.lexists(folder):
        if folder.is_symlink() or not folder.is_dir() or not (folder / MARKER).is_file():
            raise SigningError('%s is not a folder that setup made: leaving it alone' % folder)
        if keychain.exists():
            code, _ = run('Deleting the signing keychain', ['security', 'delete-keychain', str(keychain)], check=False)
            if code != 0 and keychain.exists():
                problems.append('could not delete the keychain')
        shutil.rmtree(folder, ignore_errors=True)
    try:
        listed = search_list()
        if any(same_file(p, keychain) for p in listed):
            set_search_list([p for p in listed if not same_file(p, keychain)])
            if any(same_file(p, keychain) for p in search_list()):
                problems.append('the keychain is still in the search list')
    except SigningError as error:
        problems.append(str(error))
    if os.path.lexists(folder):
        problems.append('%s remains' % folder)
    if problems:
        raise SigningError('Signing cleanup incomplete: ' + '; '.join(problems))
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
    print('Verified Developer ID signatures, notarization tickets and one designated requirement for', ', '.join(architectures))


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument('--config', default=str(ROOT / 'mygo.json'))
    commands = parser.add_subparsers(dest='command', required=True)
    commands.add_parser('setup')
    commands.add_parser('cleanup')
    check = commands.add_parser('verify')
    check.add_argument('build')
    check.add_argument('evidence')
    check.add_argument('--team', required=True)
    check.add_argument('--identity', required=True)
    check.add_argument('--arch', action='append', choices=('arm64', 'amd64'))
    args = parser.parse_args()
    try:
        if args.command == 'setup':
            try:
                setup(args.config)
            except BaseException:
                # Leave nothing behind for the remaining steps of a failed job.
                if made:
                    try:
                        cleanup(args.config)
                    except SigningError as error:
                        print('::error::' + str(error), file=sys.stderr)
                raise
        elif args.command == 'cleanup':
            cleanup(args.config)
        else:
            verify(args.build, args.team, args.identity, args.evidence, args.arch or ['arm64', 'amd64'])
    except SigningError as error:
        print('::error::' + str(error), file=sys.stderr)
        sys.exit(1)


if __name__ == '__main__':
    main()
