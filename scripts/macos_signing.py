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
           that takes the keychain's path deletes anything. While a use
           record (below) names a process group not confirmed empty, or a
           record is malformed, it removes nothing. Without a state it does
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

Every tool runs as the leader of a process group of its own (run_owned). Its
first process waits until the group is recorded, as an empty file
use-<group>, in each directory the tool may use (setup's signing directory;
for the release build also the launcher's own directories); if a record
cannot be made the tool is not started. On a timeout, a TERM/INT/HUP or any
failure, the processes of that group are stopped and waited for; only then
are the records withdrawn and files removed. When that cannot be confirmed
(a survivor, or members that cannot be listed), the records stay and nothing
is written or deleted: the step fails, and a later cleanup removes the
directory only once every recorded group is confirmed empty. Processes that
leave the group (a new session or process group) and a SIGKILL of this
script while a tool runs are outside that guarantee; the records then keep
the directory until the disposable runner goes, or its group is found empty.
"""
import argparse
import base64
import binascii
import errno
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
# A directory a tool may use holds use-<its process group> from before the
# tool runs until its group is confirmed stopped (run_owned).
USE = re.compile(r'use-([1-9][0-9]{0,9})\Z')
LOCK_WAIT = 10  # seconds to wait for another step's lock on a directory
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
# The participant directories of the tools run() starts now (setup's signing directory).
USING = []


class SigningError(Exception):
    """A failure whose message is safe to print: no secret, no argument list."""


class Undrained(SigningError):
    """A tool's process group could not be confirmed stopped: files it may use must stay."""


def drained():
    """Whether every process group this process started is confirmed stopped."""
    return not UNDRAINED


class Participant:
    """An open directory a tool may use; check(fd) says whether it is still the expected one.

    parent, when given, is a held descriptor of its parent directory that
    close() closes with fd.
    """

    def __init__(self, fd, label, check=None, parent=None):
        self.fd, self.label, self.check, self.parent = fd, label, check, parent

    def close(self):
        for fd in (self.fd, self.parent):
            if fd is not None:
                try:
                    os.close(fd)
                except OSError:
                    pass


def leads(path, parent, fd):
    """Whether the pathname path, which tools open, still leads to the open directory fd, through the held parent."""
    try:
        entry, held = os.stat(path, follow_symlinks=False), os.fstat(fd)
    except OSError:
        return False
    return (entry.st_dev, entry.st_ino) == (held.st_dev, held.st_ino) and names(parent, path.name, fd)


def held(folder, check):
    """A Participant for the directory folder, which tools use by pathname, or None if it cannot be opened.

    folder is opened through its held parent. Its check passes only while
    folder's pathname still leads, through that parent, to the held
    directory and check(fd) holds: a directory renamed away, or a parent
    replaced, between opening and a tool's admission is not admitted, as
    the tool would open whatever the pathname then names. The caller closes
    it (close()).
    """
    try:
        parent = os.open(folder.parent, os.O_RDONLY | os.O_DIRECTORY | os.O_NONBLOCK)
    except OSError:
        return None
    try:
        fd = os.open(folder.name, DIRECTORY, dir_fd=parent)
    except OSError:
        os.close(parent)
        return None
    return Participant(fd, str(folder), lambda fd: leads(folder, parent, fd) and check(fd), parent)


class locked:
    """An exclusive flock on an open directory, waited for at most LOCK_WAIT seconds."""

    def __init__(self, fd, label):
        self.fd, self.label = fd, label

    def __enter__(self):
        import fcntl
        end = time.monotonic() + LOCK_WAIT
        while True:
            try:
                fcntl.flock(self.fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
                return self
            except BlockingIOError:
                if time.monotonic() >= end:
                    raise SigningError('%s is in use by another step: leaving it alone' % self.label)
                time.sleep(0.05)

    def __exit__(self, *exc):
        import fcntl
        fcntl.flock(self.fd, fcntl.LOCK_UN)
        return False


def record_use(participant, group):
    """Records, durably, that group may use the participant directory; raises when it cannot.

    The record is the empty file use-<group>: its name says everything, so
    there is no content to write partially. Created under the directory's
    lock after checking that the directory is still the expected one, so a
    directory a cleanup is emptying never gets a new user.
    """
    with locked(participant.fd, participant.label):
        if participant.check is not None and not participant.check(participant.fd):
            raise SigningError('%s is no longer the directory this run uses' % participant.label)
        name = 'use-%d' % group
        try:
            fd = os.open(name, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600, dir_fd=participant.fd)
        except FileExistsError:
            # Left by an earlier group of that ID, which cannot exist any more while this one does.
            if not stat.S_ISREG(os.stat(name, dir_fd=participant.fd, follow_symlinks=False).st_mode):
                raise SigningError('%s holds an unexpected %s' % (participant.label, name))
        else:
            try:
                os.fsync(fd)
            finally:
                os.close(fd)
        os.fsync(participant.fd)


def withdraw_use(participant, group):
    """Removes the record of a group confirmed stopped; a record left behind names a group cleanup finds empty."""
    try:
        os.unlink('use-%d' % group, dir_fd=participant.fd)
        os.fsync(participant.fd)
    except OSError:
        pass


def use_groups(fd, label):
    """The process groups the use records of the open directory fd name; a malformed record raises."""
    groups = []
    with os.scandir(fd) as listing:
        names = [entry.name for entry in listing]
    for name in names:
        if not (name.startswith('use-') or name.startswith('.use-')):
            continue
        match = USE.match(name)
        try:
            regular = stat.S_ISREG(os.stat(name, dir_fd=fd, follow_symlinks=False).st_mode)
        except OSError:
            regular = False
        if not match or not regular:
            raise SigningError('%s holds an unknown use record %r: leaving it alone' % (label, name))
        groups.append(int(match.group(1)))
    return sorted(groups)


class using:
    """Makes run() record its tools' use of these participants (none: tools that use no directory)."""

    def __init__(self, participants):
        self.participants = list(participants)

    def __enter__(self):
        self.saved = USING[:]
        USING[:] = self.participants

    def __exit__(self, *exc):
        USING[:] = self.saved
        return False


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


# The first process of every owned tool: it waits on a pipe until the
# supervisor has recorded its group (its own process ID) in every directory
# the tool may use, then becomes the tool. Anything but the go-ahead byte,
# including the supervisor going away, ends it before the tool runs.
GATE = r'''import os, sys
fd = int(sys.argv[1])
try:
    go = os.read(fd, 1)
except OSError:
    go = b''
os.close(fd)
if go != b'1':
    os._exit(125)
try:
    os.execvp(sys.argv[2], sys.argv[2:])
except OSError as error:
    os.write(2, ('cannot run %s: %s\n' % (sys.argv[2], error.strerror)).encode())
    os._exit(127)
'''


def run_owned(label, args, env, timeout=None, capture=True, cwd=None, participants=()):
    """Runs args as the leader of a new process group; returns (exit code, captured output).

    Before the tool itself can run, its group is recorded in every
    participant directory (record_use); if that fails anywhere, the tool is
    not started. After the leader exits, times out, or a signal or error
    interrupts the wait, every remaining process of the group is stopped and
    waited for; only then is the leader reaped and the records withdrawn.
    Survivors, or a group whose members cannot be listed, make this fail
    rather than report the tool done, and leave the records in place.

    From the moment the tool may have been let through the gate (just before
    the go-ahead byte is written) its group is listed in UNDRAINED, and it
    leaves the list only once the group is confirmed empty and the leader
    reaped. Any error on the way (writing or closing the gate, watching,
    stopping) therefore leaves drained() false: callers delete files only on
    that positive confirmation, never because the stop was not reached.
    """
    pipe = subprocess.PIPE if capture else None
    admitted = False  # bound before anything can fail, so a failed pipe or start raises its own error
    _signals['shield'] += 1  # a signal during the start is held, then raised below
    try:
        gate, release = os.pipe()
        try:
            child = subprocess.Popen([sys.executable, '-I', '-c', GATE, str(gate)] + list(args), env=env, cwd=cwd, stdin=subprocess.DEVNULL,
                                     stdout=pipe, stderr=pipe, start_new_session=True, pass_fds=(gate,))
        except BaseException:
            os.close(release)
            raise
        finally:
            os.close(gate)
        # From here on the child is this function's: every path below either
        # reaps it after a confirmed stop or keeps it listed as undrained.
        entry = {'label': label, 'group': child.pid}
        recorded = []
        try:
            try:
                for participant in participants:
                    record_use(participant, child.pid)
                    recorded.append(participant)
            except BaseException as error:
                refused = error
            else:
                refused = None
                # Listed before the go-ahead byte can reach the gate.
                _UNREAPED.append(child)
                UNDRAINED.append(entry)
                admitted = True
                held, release = release, None  # never closed twice: a retried close could hit a reused descriptor
                try:
                    if os.write(held, b'1') != 1:
                        raise OSError(errno.EIO, 'the go-ahead byte was not written')
                finally:
                    os.close(held)
        finally:
            if release is not None:
                # Not admitted: no go-ahead byte was written, so the gate ends
                # without running the tool once its pipe closes.
                held, release = release, None
                try:
                    os.close(held)
                except OSError:
                    pass
        if refused is not None:
            _refuse(child, entry, recorded, label, participant, refused)
    except BaseException as error:
        if not admitted:
            raise
        interrupted = error  # the tool may run: it is stopped below like any other
    else:
        interrupted = None
    finally:
        _signals['shield'] -= 1
    tails, exited, problem, survivors, code = [], False, None, None, None
    try:
        if interrupted is None:
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
    finally:
        with shielded():
            survivors = stop_group(child.pid)
            if survivors == []:  # only then is the leader reaped
                try:
                    code = child.wait(timeout=5)
                except subprocess.TimeoutExpired:
                    survivors = [child.pid]
            if survivors == []:
                # Confirmed: the only way off the undrained list.
                UNDRAINED.remove(entry)
                _UNREAPED.remove(child)
                for participant in participants:
                    withdraw_use(participant, child.pid)
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
    if isinstance(interrupted, OSError):
        raise SigningError('%s stopped: supervising it failed (%s)' % (label, interrupted.strerror or type(interrupted).__name__)) from interrupted
    if interrupted is not None:
        raise interrupted
    if not exited:
        raise SigningError('%s did not finish within %d seconds' % (label, timeout))
    output = b''.join(bytes(tail.data) for tail in tails)
    return code, output.decode(errors='replace')


def _refuse(child, entry, recorded, label, participant, error):
    """Ends a child whose gate was never opened, withdraws its records and raises 'was not started'."""
    # No go-ahead byte was written: nothing but the waiting first process ran.
    _UNREAPED.append(child)
    UNDRAINED.append(entry)
    try:
        os.kill(child.pid, signal.SIGKILL)
    except ProcessLookupError:
        pass
    child.wait()
    UNDRAINED.remove(entry)
    _UNREAPED.remove(child)
    for stream in (child.stdout, child.stderr):
        if stream:
            stream.close()
    for done in recorded:
        withdraw_use(done, child.pid)
    raise SigningError('%s was not started: its use of %s could not be recorded (%s)'
                       % (label, participant.label, error.strerror if isinstance(error, OSError) else error)) from error


def run(label, args, redact=(), check=True, timeout=None):
    """Run a tool; on failure report label and its redacted output, never args."""
    timeout = timeout or DEADLINE
    code, out = run_owned(label, args, clean_environment(os.environ, KNOWN_VALUES), timeout=timeout, participants=list(USING))
    for value in redact:
        if value.strip():  # whitespace alone reveals nothing, and replacing it would garble the output
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


def borrow(text):
    """Opens and checks the signing directory of a state for a tool that will use its keychain.

    Returns the notary profile, the keychain and a Participant whose use is
    recorded before the tool runs, so that the always-run cleanup leaves the
    keychain alone while that tool's processes may run. The tool is given
    the keychain's pathname, so the record is made only while that pathname
    still leads to the checked directory (held). The caller closes the
    participant (close()).
    """
    folder, state = parse_state(text)

    def check(fd):
        if not validate(fd, state):
            return False
        try:
            return stat.S_ISREG(os.stat(KEYCHAIN, dir_fd=fd, follow_symlinks=False).st_mode)
        except OSError:
            return False
    participant = held(folder, check)
    if participant is None or not participant.check(participant.fd):
        if participant is not None:
            participant.close()
        raise SigningError('The signing state does not name a keychain that setup prepared')
    return PROFILE, folder / KEYCHAIN, participant


def kept(fd, label):
    """Raises unless every process that may use the open directory fd is confirmed stopped.

    Use records (use-<group>) are made before a tool runs and withdrawn only
    once its group is confirmed stopped; a record still present, or one that
    cannot be read as a record, means its group must be confirmed empty
    first. Groups this process could not stop count too. Nothing is
    signalled: those groups are not this process's to stop.
    """
    groups = use_groups(fd, label)
    busy = still_running(groups) if groups else None
    if busy:
        raise SigningError('Kept %s: %s; run the cleanup again once they have stopped' % (label, busy))
    if not drained():
        raise SigningError('Kept %s: processes this step started (groups %s) might still use it'
                           % (label, ', '.join(str(entry['group']) for entry in UNDRAINED)))


def remove(folder, state):
    """Removes the search-list entry of folder's keychain, then the keychain and folder, if setup made it.

    The directory is opened once, relative to its parent, and that
    descriptor is what gets checked, locked and emptied: the keychain file
    is unlinked through it, never deleted by a tool that reopens its path
    (security delete-keychain would delete whatever keychain the path then
    names). Its entry is removed only while the parent still names that
    descriptor's directory, with rmdir, which cannot remove a directory
    holding anything. A directory moved away is emptied where it is;
    whatever replaced it is left alone.

    Nothing is deleted while a use record names a group not confirmed empty,
    a record is malformed, or this process has a group it could not stop
    (kept). The lock keeps a tool from being admitted meanwhile.
    """
    keychain = folder / KEYCHAIN
    problems = []
    with shielded(), using(()):
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
            if held is None:
                if not drained():
                    raise SigningError('Processes this step started may still run: nothing removed')
                problems += unlist(keychain)
            else:
                with locked(held, str(folder)):
                    if not owned(folder, state) or not validate(held, state):
                        raise SigningError('%s is not the directory this setup made: leaving it alone' % folder)
                    kept(held, str(folder))
                    problems += unlist(keychain)
                    if not validate(held, state):  # checked again just before deleting
                        raise SigningError('%s changed during cleanup: leaving it alone' % folder)
                    kept(held, str(folder))
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


def unlist(keychain):
    """Takes keychain out of the user search list; only the list of paths changes. Returns problems."""
    try:
        listed = search_list()
        if any(same_file(p, keychain) for p in listed):
            set_search_list([p for p in listed if not same_file(p, keychain)])
            if any(same_file(p, keychain) for p in search_list()):
                return ['the keychain is still in the search list']
    except Undrained:
        raise
    except SigningError as error:
        return [str(error)]
    return []


def inputs(environ):
    """Whether any signing input is set at all, and the values setup uses.

    Any variable that exists and is not empty counts as configuring signing,
    so a value that turns out unusable fails setup instead of building ad hoc.
    The passwords are opaque and kept byte for byte, spaces included (a
    certificate exported with a password that begins or ends with a space
    imports only with exactly that password); one is missing only when
    unset or empty. Only formats that define their own whitespace are
    normalized: the .p12's base64 ignores all whitespace, and an Apple ID (an
    email address) cannot begin or end with whitespace, so a pasted newline
    there is dropped. Either one is missing when nothing else is left.
    """
    raw = {name: environ.get(name, '') for name in SECRETS}
    values = dict(raw)
    values['MACOS_SIGNING_CERTIFICATE_P12'] = ''.join(raw['MACOS_SIGNING_CERTIFICATE_P12'].split())
    values['MACOS_NOTARY_APPLE_ID'] = raw['MACOS_NOTARY_APPLE_ID'].strip()
    return any(raw.values()), values


def setup(root=ROOT):
    configured, values = inputs(os.environ)
    team = declared_team(root)
    if not team and not configured:
        print('macOS signing is not configured: building ad hoc, as before.')
        write_output({'mode': 'adhoc'})
        return
    missing = ([] if team else ['the teamId of %s' % TEAM_FILE]) + [name for name in SECRETS if not values[name]]
    if missing:
        raise SigningError('macOS signing is only partly configured; missing: %s. Set the Team ID and all of %s, or none to build ad hoc.'
                           % (', '.join(missing), ', '.join(SECRETS)))
    require_hosted_runner()
    # Every tool's environment is refused if one of these values sits under another name.
    KNOWN_VALUES[:] = [os.environ[name] for name in SECRETS] + [values[name] for name in SECRETS]
    try:
        p12 = base64.b64decode(values['MACOS_SIGNING_CERTIFICATE_P12'], validate=True)
    except (binascii.Error, ValueError):
        raise SigningError('MACOS_SIGNING_CERTIFICATE_P12 is not base64 of a .p12 file')
    if not p12:
        raise SigningError('MACOS_SIGNING_CERTIFICATE_P12 is empty')
    clean_environment(os.environ, KNOWN_VALUES)  # refuse an alias before making anything
    password = secrets.token_urlsafe(32)
    redact = [values[name] for name in SECRETS] + [password]

    folder, state_text = make_state()
    signing = None
    try:
        # Every tool setup runs records its use of the directory before it
        # runs (run_owned), so a cleanup never removes what a tool still uses.
        state = parse_state(state_text)[1]
        # Its tools are given pathnames in folder: admitted only while folder still leads to it (held).
        signing = held(folder, lambda fd: validate(fd, state))
        if signing is None:
            raise SigningError('%s changed as it was made' % folder)
        with using([signing]):
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
    finally:
        if signing is not None:
            signing.close()
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
