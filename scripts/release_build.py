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
resources/ linked), in a fresh directory under the ignored build/. The build
still runs `go tool mygo build` from the repository root, so the repository's
packaging adapter (tools/mygo -> tools/package -> the pinned MyGo) checks the
tracked mygo.json and builds as it would; MyGo takes the derived directory as
the project directory while go still builds this module, so binaries keep
their exact, clean source revision. windows.installerInclude and
macos.entitlements must be regular files inside the repository reached
without links; their paths are made absolute. Developer ID mode adds
macos.notarize, naming the keychain that `macos_signing.py setup` prepared,
whose state it validates first. The tracked mygo.json is never changed:
plain `go tool mygo build` stays an ad-hoc build. The files are readable by
this user while the step runs, as anything on the runner is; they are removed
when it ends. Both directories are held open from creation to removal: they
are written, recorded in and emptied through their descriptors, MyGo is
admitted only while their paths still lead to them, and one moved away is
emptied where it went while whatever replaced it is left alone and the step
fails.

MyGo runs as the leader of a process group of its own. When it ends, fails,
or a TERM/INT/HUP reaches the launcher, every process left in that group is
stopped and waited for before the key, token and configuration are deleted.
Before MyGo runs, its group is recorded (use-<group>) in both directories
and, in Developer ID mode, in setup's signing directory, whose keychain MyGo
uses; if a record cannot be made MyGo is not started. When the stop cannot
be confirmed (a survivor, or members that cannot be listed) the step fails
and the records stay, so nothing is taken from processes that may still read
it: the always-run signing cleanup keeps the keychain, and
`release_build.py --remove-retained DIR...` removes the launcher's
directories, only once every recorded group is confirmed empty; the
disposable runner goes with them otherwise. Processes
that leave the group (a new session) and a SIGKILL of the launcher are not
covered; the files then stay until the disposable runner goes.
"""
import argparse
import copy
import json
import os
import pathlib
import re
import shlex
import shutil
import sys
import tempfile

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent))
from macos_signing import (DIRECTORY, Participant, SigningError, borrow, clean_environment, drained, empty,  # noqa: E402
                           install_signal_handlers, kept, locked, names, run_owned, shielded, use_groups)

ROOT = pathlib.Path(__file__).resolve().parent.parent
# Each key of mygo.json that the derived configuration knows how to carry.
# Another key, as one with a path, must be taught here before releases build.
KNOWN = {
    '': {'name', 'slug', 'identifier', 'version', 'icon', 'copyright', 'license', 'out', 'windows', 'linux', 'macos', 'updates'},
    'windows': {'installDirName', 'installerInclude'},
    'linux': {'command'},
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
    # The Go package: this module's root, with its embeds and VCS state. The
    # packaging adapter (tools/package) builds the legacy launcher from
    # main + "/tools/launcher", so main must stay the module root (a tracked
    # main is an unknown key above, refused).
    config['main'] = str(root)
    config['out'] = str(root / tracked.get('out', 'dist'))
    macos = config.setdefault('macos', {})
    if 'entitlements' in macos:
        macos['entitlements'] = str(inside(root, macos['entitlements'], 'macos.entitlements'))
    if notarize:
        macos['notarize'] = notarize
    include = tracked.get('windows', {}).get('installerInclude')
    if include is not None:
        config['windows']['installerInclude'] = str(inside(root, include, 'windows.installerInclude'))
    if 'updates' in config:
        if (root / 'CHANGELOG.md').is_file():
            config['updates']['changelog'] = str(root / 'CHANGELOG.md')
        if key_file:
            config['updates']['privateKey'] = str(key_file)
    return config


def inside(root, relative, what):
    """root/relative, a regular file reached without links and staying within root."""
    if not isinstance(relative, str) or not relative or os.path.isabs(relative) or '\\' in relative:
        raise SigningError('mygo.json %s must be a relative path inside the repository' % what)
    path, parts = root, pathlib.PurePosixPath(relative).parts
    for part in parts:
        if part in ('', '.', '..'):
            raise SigningError('mygo.json %s must be a relative path inside the repository' % what)
        path = path / part
        if path.is_symlink():
            raise SigningError('mygo.json %s must not go through a link' % what)
    if not path.is_file():
        raise SigningError('mygo.json %s is not a file in the repository' % what)
    return path


class Owned:
    """A fresh 0700 directory this run made, held open from creation to removal.

    It is written, recorded in and emptied through its descriptor. Its
    pathname, which MyGo reads, is trusted only while it still leads to that
    descriptor's directory (current): before MyGo is admitted, and when it
    is removed. A directory moved away while the build ran is emptied where
    it went; whatever took its place is left alone and the cleanup fails.
    """

    def __init__(self, parent, prefix):
        self.path = pathlib.Path(tempfile.mkdtemp(prefix=prefix, dir=parent))
        self.parent = os.open(self.path.parent, os.O_RDONLY | os.O_DIRECTORY | os.O_NONBLOCK)
        try:
            self.fd = os.open(self.path.name, DIRECTORY, dir_fd=self.parent)
        except OSError:
            os.close(self.parent)
            raise SigningError('%s changed as it was made' % self.path)
        info = os.fstat(self.fd)
        if info.st_uid != os.getuid() or info.st_mode & 0o077 or os.listdir(self.fd):
            self.close()
            raise SigningError('%s changed as it was made' % self.path)

    def current(self, fd=None):
        """Whether the path still leads, through its parent, to the held directory."""
        try:
            entry, held = os.stat(self.path, follow_symlinks=False), os.fstat(self.fd)
        except OSError:
            return False
        return (entry.st_dev, entry.st_ino) == (held.st_dev, held.st_ino) and names(self.parent, self.path.name, self.fd)

    def participant(self):
        return Participant(self.fd, str(self.path), self.current)

    def write(self, name, text, mode=0o600, folder=None):
        fd = os.open(name, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600, dir_fd=self.fd if folder is None else folder)
        with os.fdopen(fd, 'w') as out:
            os.fchmod(out.fileno(), mode)
            out.write(text)

    def remove(self):
        """Removes the held directory's contents and, while the path still leads there, its entry; returns problems."""
        label = str(self.path)
        try:
            with locked(self.fd, label):
                kept(self.fd, label)
                moved = not self.current()
                problems = empty(self.fd)
                if moved or not names(self.parent, self.path.name, self.fd):
                    return problems + ['%s was replaced while the build ran: this run\'s directory was emptied where it went, '
                                       'and what replaced it was left alone' % label]
                if not problems:
                    try:
                        os.rmdir(self.path.name, dir_fd=self.parent)
                    except OSError as error:
                        problems.append('%s remains (%s)' % (label, error.strerror))
                return problems
        except SigningError as error:
            return [str(error)]

    def close(self):
        for fd in (self.fd, self.parent):
            try:
                os.close(fd)
            except OSError:
                pass


def gh_wrapper(private, real_gh):
    """A gh that alone receives the upload token."""
    os.mkdir('bin', 0o700, dir_fd=private.fd)
    tools = os.open('bin', DIRECTORY, dir_fd=private.fd)
    try:
        token_file = private.path / 'token'
        private.write('gh', '#!/bin/sh\nGH_TOKEN=$(cat %s) || exit 1\nexport GH_TOKEN\nexec %s "$@"\n'
                      % (shlex.quote(str(token_file)), shlex.quote(real_gh)), mode=0o700, folder=tools)
    finally:
        os.close(tools)
    return private.path / 'bin'


def build(platforms, system, mode, identity, state, upload, root=ROOT):
    key = os.environ.get('MYGO_UPDATER_PRIVATE_KEY', '').strip()  # base64: MyGo ignores surrounding whitespace itself
    token = os.environ.get('GH_TOKEN', '').strip()  # a GitHub token holds no whitespace
    if upload and not (key and token):
        raise SigningError('Uploading needs MYGO_UPDATER_PRIVATE_KEY and GH_TOKEN')
    targets = [p for p in platforms.split(',') if p]
    if not targets or not all(re.fullmatch(r'(darwin|linux|windows)/(amd64|arm64|universal)', p) for p in targets):
        raise SigningError('Unexpected platforms: ' + platforms)
    darwin = any(p.startswith('darwin/') for p in targets)
    notarize, sign, borrowed = None, [], None
    if system == 'macOS':
        if not all(p.startswith('darwin/') for p in targets):
            raise SigningError('The macOS row builds only darwin platforms')
        if mode == 'developer-id':
            if not re.fullmatch(r'[0-9A-F]{40}', identity):
                raise SigningError('No prepared signing identity')
            # MyGo's codesign and notarytool use setup's keychain: that
            # directory is a participant of the build like the launcher's own.
            profile, keychain, borrowed = borrow(state)
            notarize, sign = {'keychainProfile': profile, 'keychain': str(keychain)}, ['-sign', identity]
        elif mode == 'adhoc':
            if identity or state:
                raise SigningError('An ad-hoc build takes no signing identity or state')
        else:
            raise SigningError('Unknown macOS signing mode: %r' % mode)
    elif mode or identity or state or darwin:
        raise SigningError('Only the macOS row signs or builds darwin platforms')

    try:
        _build(targets, upload, sign, notarize, borrowed, key, token, root)
    finally:
        if borrowed is not None:
            os.close(borrowed.fd)


def _build(targets, upload, sign, notarize, borrowed, key, token, root):
    real_gh = shutil.which('gh') if token else None
    if token and not real_gh:
        raise SigningError('Uploading needs the GitHub CLI')
    env = clean_environment(os.environ, [key, token])
    tracked = json.loads((root / 'mygo.json').read_text())
    owned, failure = [], None
    try:
        private = Owned(os.environ.get('RUNNER_TEMP') or None, 'yesman-release-')
        owned.append(private)
        key_file = None
        if key:
            key_file = private.path / 'updater.key'
            private.write('updater.key', key + '\n')
        if token:
            private.write('token', token)
            env['PATH'] = str(gh_wrapper(private, real_gh)) + os.pathsep + env.get('PATH', '')
        (root / 'build').mkdir(exist_ok=True)
        project = Owned(root / 'build', '.release-config-')
        owned.append(project)
        project.write('mygo.json', json.dumps(derive(tracked, root, key_file, notarize), indent=2) + '\n')
        if (root / 'resources').is_dir():
            os.symlink(str(root / 'resources'), 'resources', target_is_directory=True, dir_fd=project.fd)
        command = ['go', 'tool', 'mygo', 'build', '-platform', ','.join(targets)] + (['-upload'] if upload else []) + sign + [str(project.path)]
        print('Building:', ' '.join(shlex.quote(c) for c in command[:-1]), '<derived project>', flush=True)
        # MyGo and everything it starts (go, compilers, codesign, xcrun, gh)
        # run in one process group of their own, recorded in every directory
        # they use before they run (each still the one this run made); on
        # failure or a signal the group is stopped and waited for before the
        # files are removed.
        participants = [o.participant() for o in owned] + ([borrowed] if borrowed else [])
        code, _ = run_owned('mygo build', command, env, capture=False, cwd=root, participants=participants)
        if code != 0:
            raise SigningError('mygo build failed (exit %d)' % code)
    except BaseException as error:
        failure = error
        raise
    finally:
        problems = []
        with shielded():
            if drained():
                for folder in owned:
                    problems += folder.remove()
            else:
                # Their use records (made before MyGo started) stay; nothing
                # is written now. The always-run signing cleanup honours the
                # record in a borrowed signing directory the same way.
                folders = [str(f.path) for f in owned]
                print('::error::Kept %s%s: processes MyGo started might still read them. Once they have stopped: '
                      'python3 scripts/release_build.py --remove-retained %s'
                      % (', '.join(folders), ' and the signing keychain' if borrowed else '', ' '.join(shlex.quote(k) for k in folders)),
                      file=sys.stderr)
            for folder in owned:
                folder.close()
        if problems:
            message = 'Release build cleanup incomplete: ' + '; '.join(problems)
            if failure is None:
                raise SigningError(message)
            print('::error::' + message, file=sys.stderr)


def remove_retained(path):
    """Removes a directory this launcher kept, once every process group its use records name is confirmed empty."""
    path = pathlib.Path(os.path.abspath(path))
    try:
        parent = os.open(path.parent, os.O_RDONLY | os.O_DIRECTORY | os.O_NONBLOCK)
    except OSError:
        raise SigningError('%s is not a directory this launcher kept' % path)
    try:
        try:
            held = os.open(path.name, DIRECTORY, dir_fd=parent)
        except OSError:
            raise SigningError('%s is not a directory this launcher kept' % path)
        try:
            with locked(held, str(path)):
                info = os.fstat(held)
                ours = info.st_uid == os.getuid() and not info.st_mode & 0o077
                if not ours or not use_groups(held, str(path)):
                    raise SigningError('%s is not a directory this launcher kept: leaving it alone' % path)
                kept(held, str(path))
                problems = empty(held)
                if not problems and names(parent, path.name, held):
                    os.rmdir(path.name, dir_fd=parent)
                elif not problems:
                    problems.append('it was replaced meanwhile; what replaced it was left alone')
        finally:
            os.close(held)
    finally:
        os.close(parent)
    if problems:
        raise SigningError('%s not fully removed: %s' % (path, '; '.join(problems)))
    print('Removed', path)


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument('--platform')
    parser.add_argument('--system', choices=('macOS', 'Windows', 'Linux'))
    parser.add_argument('--signing-mode', default='')
    parser.add_argument('--signing-identity', default='')
    parser.add_argument('--upload', action='store_true')
    parser.add_argument('--remove-retained', nargs='+', metavar='DIR', help='remove directories a failed run kept, once their processes have stopped')
    args = parser.parse_args()
    if not args.remove_retained and not (args.platform and args.system):
        parser.error('--platform and --system are required')
    install_signal_handlers()
    try:
        if args.remove_retained:
            with shielded():
                for path in args.remove_retained:
                    remove_retained(path)
            return
        build(args.platform, args.system, args.signing_mode, args.signing_identity,
              os.environ.get('SIGNING_STATE', '').strip(), args.upload)
    except SigningError as error:
        print('::error::' + str(error), file=sys.stderr)
        sys.exit(1)


if __name__ == '__main__':
    main()
