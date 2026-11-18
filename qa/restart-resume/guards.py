"""QA-only pure provider oracle and Darwin process ownership. Import is inert."""
import ctypes
import errno
import os
import signal
import subprocess
import threading
import time
from pathlib import Path


def need(ok, why):
    if not ok:
        raise ValueError(why)


class ProviderLedger:
    """All accepted requests, without eviction; controller calls under its lock."""
    def __init__(self):
        self.lock = threading.RLock()
        self.requests = []
        self.terminals = {}

    def record(self, row):
        with self.lock:
            if row['kind'] == 'provider-request':
                need(not self.requests or row['seq'] > self.requests[-1]['id'], 'provider IDs must increase')
                self.requests.append({'id': row['seq'], 'agent': row['agent'], 'action': row['action']})
                need(len(self.requests) <= 3, 'additional provider request')
            elif row['kind'] in ('provider-disconnected', 'provider-continued'):
                action = row.get('action', 'continue')
                matches = [r for r in self.requests if r['agent'] == row['agent'] and r['action'] == action and r['id'] not in self.terminals]
                need(len(matches) == 1, 'provider terminal lacks unique open request')
                self.terminals[matches[0]['id']] = row['kind']

    def snapshot(self):
        with self.lock:
            return {'requests': [dict(r) for r in self.requests], 'terminals': dict(self.terminals)}

    def assert_complete(self):
        value = self.snapshot()
        need([(r['agent'], r['action']) for r in value['requests']] == [('paused/lead', 'hold'), ('active/lead', 'hold'), ('active/lead', 'continue')], 'unexpected complete provider request sequence')
        need([value['terminals'].get(r['id']) for r in value['requests']] == ['provider-disconnected', 'provider-disconnected', 'provider-continued'], 'held streams must cancel and continuation must complete')
        return value


def identity(pid):
    """Darwin kernel start time and executable, never ps PID alone."""
    import struct
    lib = ctypes.CDLL('/usr/lib/libproc.dylib', use_errno=True)
    buf = ctypes.create_string_buffer(136)
    lib.proc_pidinfo.argtypes = [ctypes.c_int, ctypes.c_int, ctypes.c_uint64, ctypes.c_void_p, ctypes.c_int]
    n = lib.proc_pidinfo(pid, 3, 0, buf, len(buf))  # PROC_PIDTBSDINFO
    if n == 0 and ctypes.get_errno() == errno.ESRCH:
        return None
    need(n == 136, 'process birth identity unavailable')
    row = struct.unpack('=12I16s32s5IiQQ', buf.raw)
    if row[1] == 5:  # SZOMB; retained child is already exited
        return None
    path = ctypes.create_string_buffer(4096)
    lib.proc_pidpath.argtypes = [ctypes.c_int, ctypes.c_void_p, ctypes.c_uint32]
    need(lib.proc_pidpath(pid, path, len(path)) > 0, 'process executable identity unavailable')
    return {'pid': pid, 'executable': path.value.decode(), 'startSeconds': row[-2], 'startMicroseconds': row[-1], 'uid': row[5]}


class Owned:
    def __init__(self, pid, executable, child=None, observe=identity):
        self.pid, self.child, self.observe = pid, child, observe
        self.owner = observe(pid)
        need(self.owner and self.owner['uid'] == os.getuid() and self.owner['executable'] == str(Path(executable).absolute()), 'owned process identity differs')
        self.returncode = None
        self.signals = []

    def poll(self):
        if self.child is not None:
            result = self.child.poll()
            if result is not None:
                self.returncode = result
                return result
        current = self.observe(self.pid)
        if current is None:
            self.returncode = 0 if self.child is None else self.child.poll()
            return self.returncode
        need(current == self.owner, 'process incarnation changed; signalling refused')
        return None

    def send_signal(self, sig):
        if self.poll() is not None:
            return
        need(self.observe(self.pid) == self.owner, 'owned identity changed before signal')
        os.kill(self.pid, sig)
        self.signals.append(int(sig))

    def terminate(self): self.send_signal(signal.SIGTERM)
    def kill(self): self.send_signal(signal.SIGKILL)

    def wait(self, timeout):
        end = time.monotonic() + timeout
        while self.poll() is None:
            if time.monotonic() >= end:
                raise subprocess.TimeoutExpired('owned process', timeout)
            time.sleep(.1)
        return self.returncode if self.child else None

    def drain(self):
        graceful = True
        if self.poll() is None:
            self.terminate()
            try:
                self.wait(20)
            except subprocess.TimeoutExpired:
                graceful = False
                self.kill()
                self.wait(10)
        return {'owner': self.owner, 'signals': self.signals, 'drained': self.poll() is not None, 'graceful': graceful, 'exitCode': self.returncode if self.child else None, 'exitStatusAvailable': self.child is not None}
