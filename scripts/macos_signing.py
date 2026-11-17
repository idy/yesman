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
  cleanup  Given that `state` (SIGNING_STATE), removes that keychain's
           search-list entry, then the keychain and its directory, after
           checking that the directory is still the one setup made (device,
           inode, owner and the receipt's random nonce), through a
           descriptor of that directory held until it is deleted. No tool
           that takes the keychain's path deletes anything. A directory
           kept because processes might still use it (retained.json) is
           removed only once they have all stopped. Without a state it does
           nothing.
  verify   After the build, requires both apps and disk images to carry the
           prepared identity's notarized, stapled signature, and both apps
           Apple's Developer ID designated requirement of the team, each
           satisfying the other's.

Secrets arrive only in setup's environment. They are never printed, written
to GITHUB_OUTPUT/GITHUB_ENV or put in a child process's environment. The
certificate's passphrase and the app-specific password do reach `security
import` and `notarytool store-credentials` as arguments, the only way those
tools take them without a terminal; error messages never include arguments.

Every tool runs as the leader of a process group of its own (run_owned). On
a timeout, a TERM/INT/HUP or any failure, the processes of that group are
stopped and waited for before files are removed. When that cannot be
confirmed (a survivor, or members that cannot be listed), nothing those
processes may use is deleted: the directory keeps a retained.json naming the
group, the step fails, and a later cleanup removes it only after the group
is confirmed empty. Processes that leave the group (a new session or process
group) and a SIGKILL of this script are outside that guarantee.
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
import select
import shutil
import signal
import stat
import subprocess
import sys
import tempfile
import threading
import time

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
RETAINED = 'retained.json'  # kept because processes of these groups might still use the directory
RECEIPT_LIMIT = 4096  # bytes; the receipt holds one 64-hex nonce
DEADLINE = 300  # seconds for any one tool; notarytool validates credentials online
GRACE = ((signal.SIGTERM, 5.0), (signal.SIGKILL, 3.0))  # stopping a tool's process group
CAPTURE = 65536  # bytes kept of each captured stream: its end
DIRECTORY = os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW | os.O_NONBLOCK
# Values setup must not let reach a child under another name (the four Apple inputs).
KNOWN_VALUES = []
# Process groups this process started and could not confirm stopped, and
# their unreaped leaders: kept referenced so that nothing reaps them and the
# group IDs stay this process's while it lives.
UNDRAINED = []
_UNREAPED = []


class SigningError(Exception):
    """A failure whose message is safe to print: no secret, no argument list."""


class Undrained(SigningError):
    """A tool's process group could not be confirmed stopped: files it may use must stay."""


def drained():
    """Whether every process group this process started is confirmed stopped."""
    return not UNDRAINED


def clean_environment(environ, secret_values=()):
    """environ without signing, updater or upload credentials, for child processes.

    Refuses when a secret's value is still present under another name.
    """
    env = {k: v for k, v in environ.items() if k not in SECRET_NAMES and not SECRET_NAME.search(k)}
    for value in secret_values:
        if value and len(value) >= 8 and any(value in v for v in env.values()):
            raise SigningError('A credential appears in another environment variable; refusing to pass it to child processes')
    return env


# Signals. The first TERM/INT/HUP raises SigningError where the script is;
# later ones, and any that arrive while cleanup is shielded, are held so
# that stopping processes and removing files runs to the end. A held signal
# is raised when the outermost shield ends.
_signals = {'shield': 0, 'held': None, 'raised': False}


def stop(signum, frame):
    if _signals['shield'] or _signals['raised']:
        if _signals['held'] is None:
            _signals['held'] = signum
        return
    _signals['raised'] = True
    raise SigningError('Stopped by signal %d' % signum)


class shielded:
    def __enter__(self):
        _signals['shield'] += 1

    def __exit__(self, kind, value, traceback):
        _signals['shield'] -= 1
        if kind is None:
            raise_held()
        return False


def raise_held():
    if not _signals['shield'] and _signals['held'] is not None and not _signals['raised']:
        _signals['raised'] = True
        raise SigningError('Stopped by signal %d' % _signals['held'])


def install_signal_handlers():
    for number in (signal.SIGTERM, signal.SIGINT, signal.SIGHUP):
        signal.signal(number, stop)


class _Tail(threading.Thread):
    """Reads a stream to its end, keeping only its last CAPTURE bytes."""

    def __init__(self, stream):
        super().__init__(daemon=True)
        self.stream, self.data = stream, bytearray()
        self.start()

    def run(self):
        try:
            while True:
                chunk = self.stream.read1(65536) if hasattr(self.stream, 'read1') else self.stream.read(65536)
                if not chunk:
                    return
                self.data += chunk
                if len(self.data) > CAPTURE:
                    del self.data[:len(self.data) - CAPTURE]
        except (OSError, ValueError):
            return


class _ExitWatch:
    """Notices when a child exits without reaping it, so its process group ID stays reserved."""

    def __init__(self, pid):
        self.pid, self.kq, self.exited = pid, None, False
        if hasattr(os, 'waitid'):
            return
        self.kq = select.kqueue()
        event = select.kevent(pid, filter=select.KQ_FILTER_PROC, flags=select.KQ_EV_ADD | select.KQ_EV_ONESHOT, fflags=select.KQ_NOTE_EXIT)
        try:
            if self.kq.control([event], 1, 0):  # an exited (zombie) child reports at once
                self.exited = True
        except ProcessLookupError:
            self.exited = True

    def wait(self, timeout):
        """True once the child has exited; False when timeout (seconds, or None) passes first."""
        if self.exited:
            return True
        deadline = None if timeout is None else time.monotonic() + timeout
        if self.kq is None:
            while os.waitid(os.P_PID, self.pid, os.WEXITED | os.WNOHANG | os.WNOWAIT) is None:
                if deadline is not None and time.monotonic() >= deadline:
                    return False
                time.sleep(0.02)
            self.exited = True
            return True
        while not self.exited:
            remaining = None if deadline is None else deadline - time.monotonic()
            if remaining is not None and remaining <= 0:
                return False
            if self.kq.control(None, 1, remaining):
                self.exited = True
        return True

    def close(self):
        if self.kq is not None:
            self.kq.close()


def group_members(pgid):
    """Process IDs of the live (not zombie) processes of group pgid, or None when that cannot be told."""
    try:
        listing = subprocess.run(['/bin/ps', '-A', '-o', 'pid=,pgid=,stat='], capture_output=True, stdin=subprocess.DEVNULL,
                                 env={'PATH': '/usr/bin:/bin', 'LC_ALL': 'C'}, timeout=15)
    except (OSError, subprocess.SubprocessError):
        return None
    if listing.returncode != 0:
        return None
    members, seen = [], False
    for line in listing.stdout.decode(errors='replace').splitlines():
        fields = line.split()
        if len(fields) < 3:
            continue
        try:
            pid, group = int(fields[0]), int(fields[1])
        except ValueError:
            continue
        seen = True
        if group == pgid and not fields[2].startswith('Z'):
            members.append(pid)
    return members if seen else None


def stop_group(pgid):
    """Stops the processes of the group a run_owned child leads; returns [] or the survivors (None: unknown).

    Only valid while that child is not yet reaped: its process ID, and so the
    group ID, cannot then belong to anyone else.
    """
    members = group_members(pgid)
    if members == []:
        return []
    for number, grace in GRACE:
        try:
            os.killpg(pgid, number)
        except ProcessLookupError:
            return []
        except PermissionError:
            pass  # macOS: a group left with only its exited leader
        end = time.monotonic() + grace
        while True:
            members = group_members(pgid)
            if members == []:
                return []
            if time.monotonic() >= end:
                break
            time.sleep(0.05)
    return members


def run_owned(label, args, env, timeout=None, capture=True, cwd=None):
    """Runs args as the leader of a new process group; returns (exit code, captured output).

    After the leader exits, times out, or a signal or error interrupts the
    wait, every remaining process of the group is stopped and waited for;
    only then is the leader reaped. Survivors, or a group whose members
    cannot be listed, make this fail rather than report the tool done.
    """
    pipe = subprocess.PIPE if capture else None
    _signals['shield'] += 1  # a signal during the start is held, then raised below
    try:
        child = subprocess.Popen(args, env=env, cwd=cwd, stdin=subprocess.DEVNULL, stdout=pipe, stderr=pipe, start_new_session=True)
    finally:
        _signals['shield'] -= 1
    tails, interrupted, exited, problem = [], None, False, None
    try:
        tails = [_Tail(child.stdout), _Tail(child.stderr)] if capture else []
        raise_held()
        watch = _ExitWatch(child.pid)
        try:
            exited = watch.wait(timeout)
        finally:
            watch.close()
    except BaseException as error:  # a signal, or watching failed
        interrupted = error
    code = None
    with shielded():
        survivors = stop_group(child.pid)
        if survivors == []:  # only then is the leader reaped
            try:
                code = child.wait(timeout=5)
            except subprocess.TimeoutExpired:
                survivors = [child.pid]
        if survivors != []:
            _UNREAPED.append(child)
            UNDRAINED.append({'label': label, 'group': child.pid})
        for tail in tails:
            tail.join(2)
    if survivors is None:
        problem = 'could not tell whether the processes it started (group %d) have stopped' % child.pid
    elif survivors:
        problem = 'processes it started are still running (group %d: %s)' % (child.pid, ', '.join(str(p) for p in sorted(set(survivors))))
    if problem:
        if interrupted is not None:
            cause = str(interrupted) if isinstance(interrupted, SigningError) else type(interrupted).__name__
        elif not exited:
            cause = 'did not finish within %d seconds' % timeout
        else:
            cause = 'finished'
        raise Undrained('%s %s, but %s; files they may use are kept' % (label, cause, problem)) from interrupted
    if interrupted is not None:
        raise interrupted
    if not exited:
        raise SigningError('%s did not finish within %d seconds' % (label, timeout))
    output = b''.join(bytes(tail.data) for tail in tails)
    return code, output.decode(errors='replace')


def run(label, args, redact=(), check=True, timeout=None):
    """Run a tool; on failure report label and its redacted output, never args."""
    timeout = timeout or DEADLINE
    code, out = run_owned(label, args, clean_environment(os.environ, KNOWN_VALUES), timeout=timeout)
    for value in redact:
        if value:
            out = out.replace(value, '***')
    if check and code != 0:
        tail = '\n'.join(out.strip().splitlines()[-6:])
        raise SigningError('%s failed (exit %d)%s' % (label, code, (':\n' + tail) if tail else ''))
    return code, out


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


def require_hosted_runner():
    """Keychains and the search list are only changed on a disposable hosted runner."""
    if not (os.environ.get('GITHUB_ACTIONS') == 'true' and os.environ.get('RUNNER_ENVIRONMENT') == 'github-hosted'
            and os.environ.get('RUNNER_TEMP')):
        raise SigningError('Developer ID signing is prepared only on a GitHub-hosted runner; it would change this account\'s keychain search list')


def write_output(values):
    """Appends values to GITHUB_OUTPUT in one write; a failure raises."""
    for key, value in values.items():
        if '\n' in value or '\r' in value:
            raise SigningError('Refusing a multi-line step output')
    target = os.environ.get('GITHUB_OUTPUT')
    if target:
        text = ''.join('%s=%s\n' % (key, value) for key, value in values.items())
        try:
            with open(target, 'a') as out:
                out.write(text)
        except OSError as error:
            raise SigningError('Writing the step outputs failed: %s' % error.strerror)


def make_state():
    """A fresh directory of this invocation's own, and its receipt as an opaque step output."""
    folder = pathlib.Path(tempfile.mkdtemp(prefix='yesman-signing-', dir=os.environ['RUNNER_TEMP']))
    try:
        nonce = secrets.token_hex(32)
        fd = os.open(folder / RECEIPT, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
        with os.fdopen(fd, 'w') as out:
            out.write(json.dumps({'nonce': nonce}))
        info = os.lstat(folder)
    except BaseException:
        with shielded():
            for name in (RECEIPT,):
                try:
                    os.unlink(folder / name)
                except OSError:
                    pass
            os.rmdir(folder)
        raise
    state = {'dir': str(folder), 'dev': info.st_dev, 'ino': info.st_ino, 'nonce': nonce}
    return folder, base64.urlsafe_b64encode(json.dumps(state).encode()).decode()


def parse_state(text):
    try:
        state = json.loads(base64.urlsafe_b64decode(text.encode()))
        folder = pathlib.Path(state['dir'])
        assert folder.is_absolute() and folder.name not in ('', '.', '..')
        assert isinstance(state['dev'], int) and isinstance(state['ino'], int)
        assert re.fullmatch(r'[0-9a-f]{64}', state['nonce'])
    except Exception:
        raise SigningError('The signing state is not one that setup wrote')
    return folder, state


def read_small(fd, name):
    """The bytes of the small file name in the open directory fd, or None.

    Opened without blocking and without following links, then the open
    descriptor itself must be a regular file of ours, closed to others, with
    one link and at most RECEIPT_LIMIT bytes, read with an explicit bound: a
    FIFO, device, link, oversized or growing file is refused at once and
    left as it is. What is read is the file that descriptor opened; a later
    rename at that path does not change it.
    """
    try:
        receipt = os.open(name, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK, dir_fd=fd)
    except OSError:
        return None
    try:
        info = os.fstat(receipt)
        if (not stat.S_ISREG(info.st_mode) or info.st_uid != os.getuid() or info.st_mode & 0o077 or info.st_nlink != 1
                or info.st_size > RECEIPT_LIMIT):
            return None
        data = b''
        while len(data) <= RECEIPT_LIMIT:
            chunk = os.read(receipt, RECEIPT_LIMIT + 1 - len(data))
            if not chunk:
                break
            data += chunk
        after = os.fstat(receipt)
        if len(data) != info.st_size or (after.st_size, after.st_mtime_ns) != (info.st_size, info.st_mtime_ns):
            return None
        return data
    except OSError:
        return None
    finally:
        os.close(receipt)


def read_receipt(fd):
    """The nonce in the receipt of the open directory fd, or None."""
    data = read_small(fd, RECEIPT)
    try:
        nonce = json.loads(data).get('nonce', '') if data is not None else None
    except (ValueError, AttributeError):
        return None
    return nonce if isinstance(nonce, str) else None


def retained_groups(fd):
    """The process groups the retained.json of the open directory fd names; None without one."""
    try:
        os.stat(RETAINED, dir_fd=fd, follow_symlinks=False)
    except FileNotFoundError:
        return None
    except OSError:
        raise SigningError('Its %s cannot be read: leaving it alone' % RETAINED)
    data = read_small(fd, RETAINED)
    try:
        groups = json.loads(data)['groups']
        assert isinstance(groups, list) and groups and all(isinstance(g, int) and not isinstance(g, bool) and g > 1 for g in groups)
    except Exception:
        raise SigningError('Its %s cannot be read: leaving it alone' % RETAINED)
    return groups


def keep(fd):
    """Records in the open directory fd the groups that might still use it (merged with any already named)."""
    groups = sorted(set(retained_groups(fd) or []) | {entry['group'] for entry in UNDRAINED})
    new = RETAINED + '.new'
    try:
        os.unlink(new, dir_fd=fd)
    except FileNotFoundError:
        pass
    out = os.open(new, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600, dir_fd=fd)
    with os.fdopen(out, 'w') as handle:
        handle.write(json.dumps({'groups': groups}))
    os.replace(new, RETAINED, src_dir_fd=fd, dst_dir_fd=fd)
    return groups


def still_running(groups):
    """Why the processes of groups may still run, or None once every group is confirmed empty.

    Never signals: these groups are no longer this process's to stop.
    """
    for group in groups:
        members = group_members(group)
        if members is None:
            return 'could not tell whether the processes of group %d have stopped' % group
        if members:
            return 'processes of group %d are still running (%s)' % (group, ', '.join(str(p) for p in members))
    return None


def validate(fd, state):
    """Whether the open directory fd is the one setup made: same device and inode, ours, closed to others, its receipt's nonce."""
    try:
        info = os.fstat(fd)
    except OSError:
        return False
    if (not stat.S_ISDIR(info.st_mode) or (info.st_dev, info.st_ino) != (state['dev'], state['ino'])
            or info.st_uid != os.getuid() or info.st_mode & 0o077):
        return False
    nonce = read_receipt(fd)
    return nonce is not None and hmac.compare_digest(nonce, state['nonce'])


def owned(folder, state):
    """Whether the path folder is still the directory setup made."""
    try:
        fd = os.open(folder, DIRECTORY)
    except OSError:
        return False
    try:
        return validate(fd, state)
    finally:
        os.close(fd)


def names(parent_fd, name, fd):
    """Whether name in the open directory parent_fd is still the open directory fd."""
    try:
        entry, held = os.stat(name, dir_fd=parent_fd, follow_symlinks=False), os.fstat(fd)
    except OSError:
        return False
    return stat.S_ISDIR(entry.st_mode) and (entry.st_dev, entry.st_ino) == (held.st_dev, held.st_ino)


def empty(fd):
    """Deletes the contents of the open directory fd through it, not through paths; returns problems."""
    problems = []
    with os.scandir(fd) as listing:
        entries = [entry.name for entry in listing]
    for name in entries:
        try:
            info = os.stat(name, dir_fd=fd, follow_symlinks=False)
            if stat.S_ISDIR(info.st_mode):
                sub = os.open(name, DIRECTORY, dir_fd=fd)
                try:
                    held = os.fstat(sub)
                    if (held.st_dev, held.st_ino) != (info.st_dev, info.st_ino):
                        problems.append('%s changed while being removed' % name)
                        continue
                    problems += empty(sub)
                finally:
                    os.close(sub)
                os.rmdir(name, dir_fd=fd)
            else:
                os.unlink(name, dir_fd=fd)
        except OSError as error:
            problems.append('could not remove %s (%s)' % (name, error.strerror))
    return problems


def prepared(text):
    """The notary profile and keychain of a validated state, for the release build."""
    folder, state = parse_state(text)
    try:
        fd = os.open(folder, DIRECTORY)
    except OSError:
        fd = None
    try:
        ok = fd is not None and validate(fd, state)
        if ok:
            try:
                ok = stat.S_ISREG(os.stat(KEYCHAIN, dir_fd=fd, follow_symlinks=False).st_mode)
            except OSError:
                ok = False
    finally:
        if fd is not None:
            os.close(fd)
    if not ok:
        raise SigningError('The signing state does not name a keychain that setup prepared')
    return PROFILE, folder / KEYCHAIN


def remove(folder, state):
    """Removes the search-list entry of folder's keychain, then the keychain and folder, if setup made it.

    The directory is opened once, relative to its parent, and that
    descriptor is what gets checked and emptied: the keychain file is
    unlinked through it, never deleted by a tool that reopens its path
    (security delete-keychain would delete whatever keychain the path then
    names). Its entry is removed only while the parent still names that
    descriptor's directory, with rmdir, which cannot remove a directory
    holding anything. A directory moved away is emptied where it is;
    whatever replaced it is left alone.

    While processes this process started may still run, or those a
    retained.json names are not confirmed stopped, nothing is deleted.
    """
    keychain = folder / KEYCHAIN
    problems = []
    with shielded():
        try:
            parent = os.open(folder.parent, os.O_RDONLY | os.O_DIRECTORY | os.O_NONBLOCK)
        except OSError:
            parent = None
        held = None
        try:
            if parent is not None:
                try:
                    held = os.open(folder.name, DIRECTORY, dir_fd=parent)
                except FileNotFoundError:
                    held = None
                except OSError:
                    raise SigningError('%s is not the directory this setup made: leaving it alone' % folder)
            if held is not None:
                if not owned(folder, state) or not validate(held, state):
                    raise SigningError('%s is not the directory this setup made: leaving it alone' % folder)
                retain(held, folder)
                groups = retained_groups(held)
                busy = groups and still_running(groups)
                if busy:
                    raise SigningError('Kept %s: %s. Run the cleanup again once they have stopped' % (folder, busy))
            elif not drained():
                raise SigningError('Processes this step started may still run: nothing removed')
            try:
                # Only the list of paths changes here; no keychain file is opened or deleted.
                listed = search_list()
                if any(same_file(p, keychain) for p in listed):
                    set_search_list([p for p in listed if not same_file(p, keychain)])
                    if any(same_file(p, keychain) for p in search_list()):
                        problems.append('the keychain is still in the search list')
            except Undrained:
                if held is not None:
                    retain(held, folder)
                raise
            except SigningError as error:
                problems.append(str(error))
            if held is not None:
                if not validate(held, state):  # checked again just before deleting
                    raise SigningError('%s changed during cleanup: leaving it alone' % folder)
                retain(held, folder)
                # The held directory is setup's wherever it now is; the path is
                # only removed while it still leads there.
                moved = not owned(folder, state)
                problems += empty(held)
                if not moved and names(parent, folder.name, held):
                    try:
                        os.rmdir(folder.name, dir_fd=parent)
                    except OSError as error:
                        problems.append('%s remains (%s)' % (folder, error.strerror))
                else:
                    problems.append('%s was replaced during cleanup: the signing directory was emptied where it went, '
                                    'and what replaced it was left alone' % folder)
        finally:
            for fd in (held, parent):
                if fd is not None:
                    os.close(fd)
    if problems:
        raise SigningError('Signing cleanup incomplete: ' + '; '.join(problems))


def retain(fd, folder):
    """When this process has a process group not confirmed stopped, marks the open directory fd kept and fails."""
    if drained():
        return
    groups = keep(fd)
    raise SigningError('Kept %s (%s names process groups %s that might still use it); run the cleanup again once they have stopped'
                       % (folder, RETAINED, ', '.join(str(g) for g in groups)))


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
    # Every tool's environment is refused if one of these values sits under another name.
    KNOWN_VALUES[:] = [values[name] for name in SECRETS] + [''.join(values['MACOS_SIGNING_CERTIFICATE_P12'].split())]
    try:
        p12 = base64.b64decode(''.join(values['MACOS_SIGNING_CERTIFICATE_P12'].split()), validate=True)
    except (binascii.Error, ValueError):
        raise SigningError('MACOS_SIGNING_CERTIFICATE_P12 is not base64 of a .p12 file')
    if not p12:
        raise SigningError('MACOS_SIGNING_CERTIFICATE_P12 is empty')
    clean_environment(os.environ, KNOWN_VALUES)  # refuse an alias before making anything
    password = secrets.token_urlsafe(32)
    redact = [values[name] for name in SECRETS] + [password]

    folder, state_text = make_state()
    try:
        # Published before any keychain exists, so the always-run cleanup
        # step can remove what a killed setup leaves. If this write fails,
        # setup removes the directory itself.
        write_output({'state': state_text})
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
            with shielded():
                if drained():  # else security import may still be reading it
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
        # On a failure the scratch directory goes with the rest (remove).
        scratch = pathlib.Path(tempfile.mkdtemp(dir=folder))
        probe = scratch / 'probe.txt'
        probe.write_text('yesman signing preflight\n')
        run('Signing a preflight file', ['codesign', '--force', '--sign', sha1, '--timestamp=none', str(probe)], redact)
        run('Checking the Developer ID requirement', ['codesign', '--verify', '--strict', '-R=' + developer_id_requirement(team), str(probe)], redact)
        _, described = run('Describing the preflight signature', ['codesign', '-d', '--verbose=2', str(probe)], redact)
        if 'TeamIdentifier=%s\n' % team not in described + '\n':
            raise SigningError('The preflight signature names another team')
        shutil.rmtree(scratch)

        run('Storing the notary credentials', ['xcrun', 'notarytool', 'store-credentials', PROFILE, '--apple-id', values['MACOS_NOTARY_APPLE_ID'],
                                               '--team-id', team, '--password', values['MACOS_NOTARY_PASSWORD'], '--keychain', str(keychain)], redact)
        # The build reads these; a failed write fails setup and removes the keychain.
        write_output({'mode': 'developer-id', 'identity': sha1, 'team': team, 'name': name})
    except BaseException:
        # Leave nothing behind for the remaining steps of a failed job.
        with shielded():
            try:
                remove(folder, parse_state(state_text)[1])
            except SigningError as error:
                print('::error::' + str(error), file=sys.stderr)
        raise
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
    # A cancelled job's TERM (or HUP/INT) unwinds setup's own cleanup, which
    # stops the tools' processes first; KILL cannot be handled, which the
    # always-run cleanup step (given the published state) and the
    # disposable runner cover.
    install_signal_handlers()
    try:
        if args.command == 'team':
            print(declared_team())
        elif args.command == 'setup':
            setup()
        elif args.command == 'cleanup':
            with shielded():
                cleanup(os.environ.get('SIGNING_STATE', '').strip())
        else:
            verify(args.build, args.team, args.identity, args.evidence, args.arch or ['arm64', 'amd64'])
    except SigningError as error:
        print('::error::' + str(error), file=sys.stderr)
        sys.exit(1)


if __name__ == '__main__':
    main()
