"""Bounded QA diagnostics; no process policy changes and no raw argv/env/errors.
The original guards.Owned remains the authority for equality and signalling.
"""
import hashlib
import json
import os
from pathlib import Path
import stat
import time

from guards import Owned, identity

PHASES = {'initialization', 'launch', 'initial-inspect', 'runtime-ready', 'actors',
          'update', 'seed', 'capture', 'restart', 'resumption', 'quit', 'reopen',
          'failure-snapshot', 'teardown', 'finished'}
ROLES = {'initial-S-main', 'successor-Q-main', 'owned-main'}
FIELDS = ('pid', 'startSeconds', 'startMicroseconds', 'uid')


def need(value, why):
    if not value:
        raise ValueError(why)


def error_code(error):
    # Never serialize str(error), arbitrary paths, command lines or environments.
    kind = type(error).__name__
    return kind if kind in {'ValueError', 'RuntimeError', 'TimeoutError',
                            'TimeoutExpired', 'OSError', 'FileNotFoundError',
                            'AssertionError', 'SystemExit', 'KeyboardInterrupt'} else 'other-error'


def file_identity(path):
    try:
        path = Path(path).absolute()
        for parent in path.parents:
            st = parent.lstat()
            if not stat.S_ISDIR(st.st_mode):
                return {'status': 'refused-parent'}
        st = path.lstat()
        if not stat.S_ISREG(st.st_mode):
            return {'status': 'refused-file'}
        return {'status': 'observed', 'dev': st.st_dev, 'ino': st.st_ino,
                'mode': stat.S_IMODE(st.st_mode), 'uid': st.st_uid,
                'nlink': st.st_nlink, 'bytes': st.st_size,
                'mtimeNs': st.st_mtime_ns, 'ctimeNs': st.st_ctime_ns}
    except FileNotFoundError:
        return {'status': 'absent'}
    except OSError:
        return {'status': 'unavailable'}


def safe_identity(value, main, shim):
    if value is None:
        return None
    result = {key: value[key] for key in FIELDS if type(value.get(key)) is int}
    executable = value.get('executable')
    need(isinstance(executable, str) and len(executable.encode()) <= 4096,
         'process diagnostic identity unavailable')
    result['executableSHA256'] = hashlib.sha256(executable.encode()).hexdigest()
    result['executableRole'] = ('installed-main' if executable == str(main) else
                                'fixed-private-gh-shim' if shim and executable == str(shim) else 'other')
    result['completeNumericFields'] = len(result) == len(FIELDS) + 2
    return result


class Journal:
    """Held regular descriptor, fixed schemas, <=128 records/128KiB, fsync.
    One normal observation per phase, all mismatch/unknown events until budget.
    Exhaustion or failed persistence refuses; it never authorizes a signal.
    """
    def __init__(self, root):
        self.root = Path(root).absolute()
        self.root.mkdir(parents=True, exist_ok=False)
        self.fd = os.open(self.root / 'process.jsonl', os.O_WRONLY | os.O_CREAT |
                          os.O_EXCL | os.O_NOFOLLOW, 0o600)
        self.rows = 0
        self.bytes = 0
        self.seen = set()

    def emit(self, event, phase, **values):
        need(phase in PHASES and event in {'identity', 'phase', 'subprocess', 'failure', 'teardown'},
             'invalid diagnostic vocabulary')
        raw = (json.dumps({'schema': 1, 'seq': self.rows + 1, 'at': time.time(),
                           'event': event, 'phase': phase, **values}, sort_keys=True) + '\n').encode()
        need(len(raw) <= 8192 and self.rows < 128 and self.bytes + len(raw) <= 128 << 10,
             'process diagnostic budget')
        st = os.fstat(self.fd)
        named = (self.root / 'process.jsonl').lstat()
        need(stat.S_ISREG(st.st_mode) and st.st_nlink == 1 and
             (st.st_dev, st.st_ino) == (named.st_dev, named.st_ino), 'diagnostic identity changed')
        offset = 0
        while offset < len(raw):
            count = os.write(self.fd, raw[offset:])
            need(count > 0, 'diagnostic write failed')
            offset += count
        os.fsync(self.fd)
        self.rows += 1
        self.bytes += len(raw)

    def receipt(self, name, value):
        need(name in {'original-failure.json', 'snapshot-failure.json', 'teardown-failure.json',
                      'teardown-start.json', 'episode-result.json'}, 'invalid diagnostic receipt')
        raw = (json.dumps(value, sort_keys=True) + '\n').encode()
        need(len(raw) <= 8192, 'diagnostic receipt budget')
        fd = os.open(self.root / name, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
        try:
            offset = 0
            while offset < len(raw):
                count = os.write(fd, raw[offset:])
                need(count > 0, 'diagnostic write failed')
                offset += count
            os.fsync(fd)
        finally:
            os.close(fd)

    def close(self):
        if self.fd is not None:
            os.close(self.fd)
            self.fd = None


class BoundMain:
    """Held original inode plus mandatory package hash. Only the fixed gh alias
    may represent that same inode; matching bytes in another inode never qualify.
    Kernel PID/birth/uid equality is still checked by the original Owned class.
    """
    def __init__(self, main, expected):
        need(isinstance(expected, str) and len(expected) == 64 and
             all(ch in '0123456789abcdef' for ch in expected), 'bound main hash required')
        self.main, self.expected = Path(main).absolute(), expected
        need(file_identity(self.main).get('status') == 'observed', 'bound main path refused')
        self.fd = os.open(self.main, os.O_RDONLY | os.O_NONBLOCK | os.O_NOFOLLOW)
        self.cached = None
        self.original = os.fstat(self.fd)
        try:
            self.verify()
        except BaseException:
            self.close()
            raise

    def verify(self):
        held = os.fstat(self.fd)
        named = file_identity(self.main)
        need(stat.S_ISREG(held.st_mode) and held.st_uid == os.getuid() and held.st_mode & 0o111 and
             0 < held.st_size <= 512 << 20 and held.st_nlink >= 1 and
             (held.st_dev, held.st_ino) == (self.original.st_dev, self.original.st_ino) and
             named.get('status') == 'observed' and (named['dev'], named['ino']) == (held.st_dev, held.st_ino),
             'held bound main identity differs')
        signature = lambda st: (st.st_dev, st.st_ino, st.st_mode, st.st_uid, st.st_nlink,
                                st.st_size, st.st_mtime_ns, st.st_ctime_ns)
        current = signature(held)
        if current != self.cached:
            os.lseek(self.fd, 0, os.SEEK_SET)
            digest = hashlib.sha256()
            count = 0
            while count <= 512 << 20:
                raw = os.read(self.fd, min(1 << 20, (512 << 20) + 1 - count))
                if not raw:
                    break
                digest.update(raw)
                count += len(raw)
            need(count == held.st_size and digest.hexdigest() == self.expected and
                 signature(os.fstat(self.fd)) == current, 'held bound main hash/identity changed')
            self.cached = current
        return held, named

    def qualifies(self, executable, shim):
        if shim is None or executable != str(shim):
            return False
        held, named = self.verify()
        alias = file_identity(shim)
        need(alias.get('status') == 'observed' and
             (alias['dev'], alias['ino']) == (held.st_dev, held.st_ino) and
             alias['bytes'] == held.st_size and alias['uid'] == held.st_uid,
             'fixed executable alias differs from held bound main')
        # Check the held and both named identities again around admission.
        again, _ = self.verify()
        need(alias == file_identity(shim) and named == file_identity(self.main) and
             (again.st_dev, again.st_ino) == (held.st_dev, held.st_ino), 'fixed alias changed during observation')
        return True

    def close(self):
        if self.fd is not None:
            os.close(self.fd)
            self.fd = None


class DiagnosticOwned(Owned):
    """Record raw observations before the unchanged guard compares them.
    The sole qualified translation is a fixed gh path for the held/hash-bound
    main inode, causally exercised on Darwin. No arbitrary path or inode alias.
    """
    def __init__(self, pid, executable, child=None, observe=identity, *, journal,
                 phase=lambda: 'launch', role='owned-main', shim=None, expected_sha256):
        need(role in ROLES, 'invalid process diagnostic role')
        self.journal, self.phase, self.role = journal, phase, role
        self.main = Path(executable).absolute()
        self.shim = Path(shim).absolute() if shim else None
        self.status_child = child
        self.bound_main = BoundMain(self.main, expected_sha256)
        def observed(ident):
            expected = getattr(self, 'owner', None)
            try:
                current = observe(ident)
            except (OSError, ValueError) as error:
                self.record(expected, None, 'unknown', error_code(error), False)
                raise
            # No PID/birth/uid field is translated. An unknown/different field
            # will still reach Owned's original comparison and refusal.
            alias = False
            compared = current
            try:
                if current is not None and self.bound_main.qualifies(current.get('executable'), self.shim):
                    alias = True
                    compared = {**current, 'executable': str(self.main)}
            except (OSError, ValueError) as error:
                self.record(expected, current, 'alias-refused', error_code(error), False)
                raise
            state = 'initial' if expected is None else 'same' if compared == expected else 'departed' if current is None else 'mismatch'
            key = (self.role, self.phase(), state, alias)
            if state != 'same' or key not in journal.seen:
                self.record(expected, current, state, None, alias)
                journal.seen.add(key)
            return compared
        try:
            super().__init__(pid, executable, child=child, observe=observed)
        except BaseException:
            self.close_observer()
            raise

    def close_observer(self):
        self.bound_main.close()

    def record(self, expected, current, state, code, alias):
        phase = self.phase()
        need(phase in PHASES, 'invalid process diagnostic phase')
        main = file_identity(self.main)
        shim = file_identity(self.shim) if self.shim else {'status': 'not-selected'}
        same_file = (main.get('status') == shim.get('status') == 'observed' and
                     (main['dev'], main['ino']) == (shim['dev'], shim['ino']))
        different = [key for key in (*FIELDS, 'executable') if expected is not None and current is not None and expected.get(key) != current.get(key)]
        self.journal.emit('identity', phase, role=self.role, state=state, errorCode=code,
                          expected=safe_identity(expected, self.main, self.shim),
                          current=safe_identity(current, self.main, self.shim),
                          differentFields=different, main=main, fixedShim=shim,
                          mainAndShimSameFile=same_file, fixedAliasQualified=alias,
                          child={'held': self.status_child is not None,
                                 'returncode': self.status_child.returncode if self.status_child else None},
                          signals=list(getattr(self, 'signals', [])))


def execute_episode(host, failed, sequence):
    """One sequence, one teardown; preserve each failure independently.
    Diagnostic I/O failure is fatal but cannot skip the teardown attempt.
    """
    failure = None
    diagnostic_failed = False
    def retain(name, value):
        nonlocal diagnostic_failed
        try:
            host.diagnostics.receipt(name, value)
        except (OSError, ValueError):
            diagnostic_failed = True
    try:
        sequence(host, failed)
    except BaseException as error:
        failure = error
        retain('original-failure.json', {'phase': host.phase, 'errorCode': error_code(error)})
        host.phase = 'failure-snapshot'
        try:
            host.snapshot_failure(error)
        except BaseException as snapshot_error:
            retain('snapshot-failure.json', {'errorCode': error_code(snapshot_error)})
    host.phase = 'teardown'
    retain('teardown-start.json', {'sequenceFailed': failure is not None})
    teardown_failed = False
    try:
        host.close(failure is None)
    except BaseException as close_error:
        teardown_failed = True
        retain('teardown-failure.json', {'errorCode': error_code(close_error)})
        if failure is None:
            failure = close_error
    finally:
        retain('episode-result.json', {'sequenceFailed': (host.diagnostics.root / 'original-failure.json').exists(),
               'teardownFailed': teardown_failed, 'completed': failure is None and not diagnostic_failed,
               'diagnosticPersistenceFailed': diagnostic_failed, 'nativeAcceptance': False})
        for owned in getattr(host, 'diagnostic_owners', []):
            owned.close_observer()
        host.diagnostics.close()
    if failure is not None:
        raise failure
    need(not diagnostic_failed, 'mandatory native diagnostics persistence failed')
