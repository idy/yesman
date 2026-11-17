#!/usr/bin/env python3
"""Build (and upload) a release with MyGo, giving each credential only to its consumer.

Used by every row of .github/workflows/release.yml; see docs/macos-signing.md.

MyGo starts its children (go, the compilers and cgo's clang, codesign, xcrun,
hdiutil, makensis, gh) with its own environment. Credentials in the step's
environment would therefore reach every compiler. This launcher takes them
out of the environment:

- MYGO_UPDATER_PRIVATE_KEY is written to a 0600 file in a fresh 0700
  directory of this run, which a derived configuration names as
  updates.privateKey; only MyGo itself reads it, to sign the update archives,
  deltas and manifests.
- GH_TOKEN is written to a 0600 file there too; a gh wrapper first on MyGo's
  PATH reads it and execs the real gh with it, so only gh (release create,
  view, download and upload) has it.
- Every child gets the step's environment without any credential name, and
  the launcher refuses to start one if a credential's value remains under
  another name.

The derived configuration is mygo.json with its paths made absolute (and
resources/ linked), in a fresh directory under the ignored build/: MyGo takes
it as the project directory while go still builds this module, so binaries
keep their exact, clean source revision. Developer ID mode adds
macos.notarize, naming the keychain that `macos_signing.py setup` prepared,
whose state it validates first. The tracked mygo.json is never changed:
plain `go tool mygo build` stays an ad-hoc build. The files are readable by
this user while the step runs, as anything on the runner is; they are removed
when it ends.
"""
import argparse
import copy
import json
import os
import pathlib
import re
import shlex
import shutil
import signal
import subprocess
import sys
import tempfile

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent))
from macos_signing import SigningError, clean_environment, prepared, stop  # noqa: E402

ROOT = pathlib.Path(__file__).resolve().parent.parent
# Each key of mygo.json that the derived configuration knows how to carry.
# Another key, as one with a path, must be taught here before releases build.
KNOWN = {
    '': {'name', 'slug', 'identifier', 'version', 'icon', 'copyright', 'license', 'out', 'macos', 'updates'},
    'macos': {'minimumSystemVersion', 'entitlements', 'infoPlist'},
    'updates': {'publicKey', 'github', 'deltas'},
}
# What MyGo discovers relative to the project directory, which the derived
# directory would not show it.
DISCOVERED = ('package.json', 'frontend', '.mygo', 'mygo.config.ts', 'locales')


def derive(tracked, root, key_file, notarize=None):
    """The configuration MyGo reads from the derived project directory."""
    for section, allowed in KNOWN.items():
        keys = set(tracked.get(section, {}) if section else tracked)
        if keys - allowed:
            raise SigningError('mygo.json %s has %s, which scripts/release_build.py does not carry: teach it before releasing'
                               % (section or 'top level', ', '.join(sorted(keys - allowed))))
    for name in DISCOVERED:
        if os.path.lexists(root / name):
            raise SigningError('%s would not be visible from the derived project directory: teach scripts/release_build.py before releasing' % name)
    icon = tracked.get('icon', '')
    if icon and not icon.startswith('resources/'):
        raise SigningError('mygo.json icon must be under resources/')
    config = copy.deepcopy(tracked)
    config['main'] = str(root)  # the Go package: this module, with its embeds and VCS state
    config['out'] = str(root / tracked.get('out', 'dist'))
    macos = config.setdefault('macos', {})
    if 'entitlements' in macos:
        macos['entitlements'] = str(root / macos['entitlements'])
    if notarize:
        macos['notarize'] = notarize
    if 'updates' in config:
        if (root / 'CHANGELOG.md').is_file():
            config['updates']['changelog'] = str(root / 'CHANGELOG.md')
        if key_file:
            config['updates']['privateKey'] = str(key_file)
    return config


def write_private(path, text):
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
    with os.fdopen(fd, 'w') as out:
        out.write(text)


def gh_wrapper(folder, real_gh, token_file):
    """A gh that alone receives the upload token."""
    script = folder / 'gh'
    write_private(script, '#!/bin/sh\nGH_TOKEN=$(cat %s) || exit 1\nexport GH_TOKEN\nexec %s "$@"\n' % (shlex.quote(str(token_file)), shlex.quote(real_gh)))
    os.chmod(script, 0o700)


def build(platforms, system, mode, identity, state, upload, root=ROOT):
    key = os.environ.get('MYGO_UPDATER_PRIVATE_KEY', '').strip()
    token = os.environ.get('GH_TOKEN', '').strip()
    if upload and not (key and token):
        raise SigningError('Uploading needs MYGO_UPDATER_PRIVATE_KEY and GH_TOKEN')
    targets = [p for p in platforms.split(',') if p]
    if not targets or not all(re.fullmatch(r'(darwin|linux|windows)/(amd64|arm64|universal)', p) for p in targets):
        raise SigningError('Unexpected platforms: ' + platforms)
    darwin = any(p.startswith('darwin/') for p in targets)
    notarize, sign = None, []
    if system == 'macOS':
        if not all(p.startswith('darwin/') for p in targets):
            raise SigningError('The macOS row builds only darwin platforms')
        if mode == 'developer-id':
            if not re.fullmatch(r'[0-9A-F]{40}', identity):
                raise SigningError('No prepared signing identity')
            profile, keychain = prepared(state)
            notarize, sign = {'keychainProfile': profile, 'keychain': str(keychain)}, ['-sign', identity]
        elif mode == 'adhoc':
            if identity or state:
                raise SigningError('An ad-hoc build takes no signing identity or state')
        else:
            raise SigningError('Unknown macOS signing mode: %r' % mode)
    elif mode or identity or state or darwin:
        raise SigningError('Only the macOS row signs or builds darwin platforms')

    real_gh = shutil.which('gh') if token else None
    if token and not real_gh:
        raise SigningError('Uploading needs the GitHub CLI')
    env = clean_environment(os.environ, [key, token])
    tracked = json.loads((root / 'mygo.json').read_text())
    private = pathlib.Path(tempfile.mkdtemp(prefix='yesman-release-', dir=os.environ.get('RUNNER_TEMP') or None))
    project = None
    try:
        key_file = None
        if key:
            key_file = private / 'updater.key'
            write_private(key_file, key + '\n')
        if token:
            write_private(private / 'token', token)
            tools = private / 'bin'
            tools.mkdir(0o700)
            gh_wrapper(tools, real_gh, private / 'token')
            env['PATH'] = str(tools) + os.pathsep + env.get('PATH', '')
        (root / 'build').mkdir(exist_ok=True)
        project = pathlib.Path(tempfile.mkdtemp(prefix='.release-config-', dir=root / 'build'))
        (project / 'mygo.json').write_text(json.dumps(derive(tracked, root, key_file, notarize), indent=2) + '\n')
        if (root / 'resources').is_dir():
            (project / 'resources').symlink_to(root / 'resources', target_is_directory=True)
        command = ['go', 'tool', 'mygo', 'build', '-platform', ','.join(targets)] + (['-upload'] if upload else []) + sign + [str(project)]
        print('Building:', ' '.join(shlex.quote(c) for c in command[:-1]), '<derived project>', flush=True)
        result = subprocess.run(command, cwd=root, env=env, stdin=subprocess.DEVNULL)
        if result.returncode != 0:
            raise SigningError('mygo build failed (exit %d)' % result.returncode)
    finally:
        shutil.rmtree(private, ignore_errors=True)
        if project:
            shutil.rmtree(project, ignore_errors=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument('--platform', required=True)
    parser.add_argument('--system', required=True, choices=('macOS', 'Windows', 'Linux'))
    parser.add_argument('--signing-mode', default='')
    parser.add_argument('--signing-identity', default='')
    parser.add_argument('--upload', action='store_true')
    args = parser.parse_args()
    for number in (signal.SIGTERM, signal.SIGINT, signal.SIGHUP):
        signal.signal(number, stop)
    try:
        build(args.platform, args.system, args.signing_mode, args.signing_identity,
              os.environ.get('SIGNING_STATE', '').strip(), args.upload)
    except SigningError as error:
        print('::error::' + str(error), file=sys.stderr)
        sys.exit(1)


if __name__ == '__main__':
    main()
