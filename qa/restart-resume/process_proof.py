"""Bounded read-only observations of already owned native-fixture processes.
No argv/env is retained and this module never signals a discovered process.
"""
import hashlib
import os
from pathlib import Path
import re
import shlex
import stat
import subprocess
import time

from guards import identity


def tool_file(path, limit):
    """Observe an exact public tool path selected by a scoped child command."""
    fd = None
    try:
        path = Path(path)
        fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
        before = os.fstat(fd)
        if not stat.S_ISREG(before.st_mode) or before.st_size > limit:
            return {'status': 'refused'}
        digest = hashlib.sha256()
        count = 0
        while True:
            raw = os.read(fd, min(1 << 20, limit + 1 - count))
            if not raw:
                break
            count += len(raw)
            if count > limit:
                return {'status': 'refused'}
            digest.update(raw)
        after = os.fstat(fd)
        fields = lambda value: (value.st_dev, value.st_ino, value.st_size, value.st_mtime_ns, value.st_ctime_ns)
        if fields(before) != fields(after) or count != before.st_size:
            return {'status': 'changed'}
        return {'status': 'observed', 'path': str(path), 'bytes': count, 'sha256': digest.hexdigest()}
    except OSError:
        return {'status': 'unavailable'}
    finally:
        if fd is not None:
            os.close(fd)


class Processes:
    def __init__(self, data, observe=identity):
        self.data = Path(data).absolute()
        self.observe = observe
        self.roots = {}
        self.children = {}
        self.last = 0
        self.status = 'not-sampled'

    def register(self, owned):
        if len(self.roots) >= 4:
            raise ValueError('owned root observation budget')
        self.roots[owned.pid] = owned.owner

    def role(self, command):
        try:
            args = shlex.split(command)
        except ValueError:
            return 'owned-descendant', None
        staging = self.data / '.runtime/pi-managed'
        def is_staging(value, basename=None):
            path = Path(value)
            return path.is_absolute() and path.is_relative_to(staging) and any(x.startswith('.prepare-') for x in path.parts) and (basename is None or path.name == basename)
        if any(is_staging(arg, 'install.mjs') for arg in args):
            return 'runtime-installer', None
        if '--prefix' in args and args.index('--prefix') + 1 < len(args) and is_staging(args[args.index('--prefix') + 1]):
            cli = next((arg for arg in args if arg.endswith('/npm/bin/npm-cli.js') and Path(arg).is_absolute()), None)
            if cli and 'ci' in args:
                return 'pinned-npm-ci', cli
        return 'owned-descendant', None

    def pipes(self, owner):
        try:
            if self.observe(owner['pid']) != owner:
                return {'status': 'departed-before-sample'}
            value = subprocess.run(['lsof', '-a', '-p', str(owner['pid']), '-Fft'], capture_output=True, timeout=3)
            if len(value.stdout) > 64 << 10 or value.stderr or value.returncode not in (0, 1):
                return {'status': 'unknown'}
            if self.observe(owner['pid']) != owner:
                return {'status': 'departed-during-sample'}
            types = [line[1:] for line in value.stdout.decode().splitlines() if line.startswith('t')]
            return {'status': 'observed', 'pipeOrFIFODescriptors': sum(x in ('PIPE', 'FIFO') for x in types)}
        except (OSError, ValueError, subprocess.TimeoutExpired):
            return {'status': 'unknown'}

    def poll(self, force=False):
        if not force and time.monotonic() - self.last < .25:
            return
        self.last = time.monotonic()
        try:
            result = subprocess.run(['/bin/ps', '-ww', '-axo', 'pid=,ppid=,pgid=,command='], capture_output=True, timeout=5)
            if result.returncode or len(result.stdout) > 2 << 20:
                self.status = 'snapshot-unavailable'
                return
            parsed = []
            for line in result.stdout.decode().splitlines():
                match = re.fullmatch(r'\s*(\d+)\s+(\d+)\s+(\d+)\s+(.*)', line)
                if match:
                    parsed.append((int(match[1]), int(match[2]), int(match[3]), match[4]))
            parents = set()
            for pid, owner in {**self.roots, **{pid: row['owner'] for pid, row in self.children.items()}}.items():
                if self.observe(pid) == owner:
                    parents.add(pid)
            for _ in range(8):
                added = False
                for pid, parent, group, command in parsed:
                    if parent not in parents or pid in parents or pid in self.roots:
                        continue
                    owner = self.observe(pid)
                    if owner is None or owner['uid'] != os.getuid():
                        continue
                    if pid not in self.children:
                        if len(self.children) >= 128:
                            self.status = 'observation-budget'
                            return
                        role, npm = self.role(command)
                        row = {'owner': owner, 'ppid': parent, 'pgid': group, 'role': role}
                        if role in ('runtime-installer', 'pinned-npm-ci'):
                            row['node'] = tool_file(owner['executable'], 128 << 20)
                            row['pipes'] = self.pipes(owner)
                        if npm:
                            row['npmCLI'] = tool_file(npm, 1 << 20)
                        self.children[pid] = row
                    parents.add(pid)
                    added = True
                if not added:
                    break
            self.status = 'sampled'
        except (OSError, ValueError, subprocess.TimeoutExpired):
            self.status = 'snapshot-unavailable'

    def snapshot(self):
        rows = []
        for row in self.children.values():
            value = dict(row)
            try:
                current = self.observe(row['owner']['pid'])
                value['state'] = 'exited' if current is None else 'same-incarnation-live' if current == row['owner'] else 'identity-changed'
            except ValueError:
                value['state'] = 'unknown'
            rows.append(value)
        return {'scope': 'sampled descendants of verified owned mains only; no universal process/pipe claim',
                'samplingStatus': self.status, 'children': rows,
                'unresolvedObserved': any(row['state'] != 'exited' for row in rows),
                'installerSeen': any(row['role'] == 'runtime-installer' for row in rows),
                'npmSeen': any(row['role'] == 'pinned-npm-ci' for row in rows)}


def profiles(paths):
    rows = []
    for role, path in paths:
        path = Path(path)
        row = {'pathRole': role}
        if path.is_symlink():
            row['status'] = 'refused-link'
        elif not path.exists():
            row['status'] = 'absent'
        else:
            args = ['lsof', '-t', '+D', str(path)] if path.is_dir() else ['lsof', '-t', '--', str(path)]
            try:
                value = subprocess.run(args, capture_output=True, timeout=5)
                ids = value.stdout.decode().split()
                row.update(exit=value.returncode, stdoutBytes=len(value.stdout), stderrBytes=len(value.stderr))
                row['status'] = 'no-observed-consumer' if value.returncode == 1 and not value.stdout and not value.stderr else 'observed-consumer' if value.returncode in (0, 1) and ids and all(x.isdigit() for x in ids) and not value.stderr else 'unknown'
                if row['status'] == 'observed-consumer':
                    row['PIDs'] = sorted({int(x) for x in ids})[:128]
            except (OSError, subprocess.TimeoutExpired):
                row['status'] = 'unknown'
        rows.append(row)
    return {'scope': 'observed open consumers only', 'paths': rows}
