"""Freeze bounded native receipts without ever walking runtime/profile trees.
Snapshot failures are explicit manifest entries; they do not hide an App failure.
"""
import hashlib
import json
import os
from pathlib import Path
import stat
import sys

TOP = {'compatibility-reused.json', 'consumer-admission.json', 'entry-status.json',
       'attachment-result.json', 'package-validation.json', 'run-inputs.json'}
EPISODE = {'failure.json', 'teardown.json', 'process-observations.json',
           'image-diagnostics.json', 'provider-complete.json', 'provider-reopen.json',
           'episode.json', 'production-upgrade-failure.json'}
DIAGNOSTICS = {'process.jsonl', 'original-failure.json', 'snapshot-failure.json',
               'teardown-failure.json', 'teardown-start.json', 'episode-result.json',
               'seed-overlay.json', 'model-admission.json', 'observer-release.json'}
PHASE_FILES = {'proof.json', 'runtime.json'}
PER_FILE = 4 << 20
TOTAL = 16 << 20


def selected():
    yield from sorted(TOP)
    for episode in ('update', 'failed-launch-recovery'):
        for name in sorted(EPISODE):
            yield f'stateful/{episode}/{name}'
        for name in sorted(DIAGNOSTICS):
            yield f'stateful/{episode}/diagnostics/{name}'
        for phase in ('before', 'failed-launch', 'after', 'reopen'):
            for name in sorted(PHASE_FILES):
                yield f'stateful/{episode}/{phase}/{name}'


def bounded_read(root, relative):
    path = root / relative
    for parent in [root, *path.relative_to(root).parents]:
        actual = parent if parent.is_absolute() else root / parent
        value = actual.lstat()
        if not stat.S_ISDIR(value.st_mode):
            raise ValueError('refused-parent')
    fd = os.open(path, os.O_RDONLY | os.O_NONBLOCK | os.O_NOFOLLOW)
    try:
        before = os.fstat(fd)
        if not stat.S_ISREG(before.st_mode) or before.st_nlink != 1 or before.st_size > PER_FILE:
            raise ValueError('refused-file-or-budget')
        raw = b''
        while len(raw) <= PER_FILE:
            part = os.read(fd, min(65536, PER_FILE + 1 - len(raw)))
            if not part:
                break
            raw += part
        after = os.fstat(fd)
        named = path.lstat()
        signature = lambda st: (st.st_dev, st.st_ino, st.st_size, st.st_mtime_ns, st.st_ctime_ns, st.st_nlink)
        if len(raw) > PER_FILE or signature(before) != signature(after) or signature(after) != signature(named):
            raise ValueError('unstable-file')
        return raw
    finally:
        os.close(fd)


def freeze(source, output):
    source, output = Path(source).absolute(), Path(output).absolute()
    output.mkdir(parents=True, exist_ok=False)
    rows = {}
    total = 0
    for relative in selected():
        try:
            raw = bounded_read(source, relative)
            if total + len(raw) > TOTAL:
                raise ValueError('snapshot-total-budget')
            dest = output / relative
            dest.parent.mkdir(parents=True, exist_ok=True)
            fd = os.open(dest, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
            try:
                with os.fdopen(fd, 'wb') as stream:
                    stream.write(raw)
                    stream.flush()
                    os.fsync(stream.fileno())
            except BaseException:
                raise
            rows[relative] = {'status': 'frozen-original-bytes', 'bytes': len(raw),
                              'sha256': hashlib.sha256(raw).hexdigest()}
            total += len(raw)
        except FileNotFoundError:
            rows[relative] = {'status': 'absent'}
        except (OSError, ValueError):
            # No exception/path text; original failure and teardown are separate files.
            rows[relative] = {'status': 'unavailable-or-unstable'}
    value = {'schema': 1, 'scope': 'fixed-native-receipts-only', 'profileOrRuntimeBulkWalked': False,
             'entries': rows, 'frozenBytes': total, 'nativeAcceptance': False}
    (output / 'snapshot.json').write_text(json.dumps(value, indent=2) + '\n')
    return value


if __name__ == '__main__':
    freeze(*sys.argv[1:])
