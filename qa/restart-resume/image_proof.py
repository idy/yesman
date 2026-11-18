"""Read-only proof of the exact bound App main; mutable fixture readers stay sealed.

A valid App installs its private gh shim with a hard link to its main. Only the
fixed installed Contents/MacOS/Yes Man receives this hash-bound read policy.
"""
import hashlib
import json
import os
from pathlib import Path
import re
import stat
import subprocess

MAIN = 'Contents/MacOS/Yes Man'
LIMIT = 512 << 20
PHASES = {'image', 'inspect-initial', 'inspect-ready', 'capture-before',
          'capture-failed-launch', 'capture-after', 'capture-reopen', 'failure'}
CODES = {'ok', 'absent', 'path-refused', 'open-refused', 'not-regular',
         'size-refused', 'not-executable', 'owner-refused', 'identity-changed', 'hash-mismatch'}


def need(value, code):
    if not value:
        raise ValueError('bound App main proof: ' + code)


def sha(raw):
    return hashlib.sha256(raw).hexdigest()


def identity(value):
    return (value.st_dev, value.st_ino, value.st_mode, value.st_uid, value.st_nlink,
            value.st_size, value.st_mtime_ns, value.st_ctime_ns)


def metadata(value):
    kind = 'regular' if stat.S_ISREG(value.st_mode) else 'symlink' if stat.S_ISLNK(value.st_mode) else 'other'
    return {'kind': kind, 'dev': value.st_dev, 'ino': value.st_ino,
            'mode': stat.S_IMODE(value.st_mode), 'uid': value.st_uid,
            'nlink': value.st_nlink, 'bytes': value.st_size,
            'mtimeNs': value.st_mtime_ns, 'ctimeNs': value.st_ctime_ns}


class Diagnostics:
    """At most one small fixed-role record per phase; no path/error/env text."""
    def __init__(self, output):
        self.rows = {}
        self.fd = os.open(output, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)

    def record(self, phase, code, before=None, after=None, shim=None):
        need(phase in PHASES and code in CODES, 'path-refused')
        row = {'phase': phase, 'pathRole': 'installed-bound-main', 'code': code}
        if before is not None:
            row['before'] = metadata(before)
        if after is not None:
            row['after'] = metadata(after)
        if shim is not None:
            row['knownShim'] = shim
        self.rows[phase] = row
        raw = (json.dumps({'schema': 1, 'rows': self.rows}, sort_keys=True) + '\n').encode()
        need(len(raw) <= 8192, 'size-refused')
        held = os.fstat(self.fd)
        need(stat.S_ISREG(held.st_mode) and held.st_nlink == 1, 'identity-changed')
        os.lseek(self.fd, 0, os.SEEK_SET)
        offset = 0
        while offset < len(raw):
            written = os.write(self.fd, raw[offset:])
            need(written > 0, 'open-refused')
            offset += written
        os.ftruncate(self.fd, len(raw))
        os.fsync(self.fd)

    def close(self):
        if self.fd is not None:
            os.close(self.fd)
            self.fd = None


def shim_observation(data, main):
    """Metadata only for the product's fixed private alias; never follow it."""
    if data is None:
        return {'pathRole': 'app-private-gh-shim', 'relation': 'not-observed'}
    path = Path(data).absolute() / '.runtime/github-app-gh/bin/gh'
    try:
        for parent in path.parents:
            value = parent.lstat()
            if not stat.S_ISDIR(value.st_mode) or stat.S_ISLNK(value.st_mode):
                return {'pathRole': 'app-private-gh-shim', 'relation': 'refused'}
        value = path.lstat()
        if not stat.S_ISREG(value.st_mode):
            return {'pathRole': 'app-private-gh-shim', 'relation': 'refused'}
        return {'pathRole': 'app-private-gh-shim', 'relation': 'same-file' if main is not None and (value.st_dev, value.st_ino) == (main.st_dev, main.st_ino) else 'different-file', 'stat': metadata(value)}
    except FileNotFoundError:
        return {'pathRole': 'app-private-gh-shim', 'relation': 'absent'}
    except OSError:
        return {'pathRole': 'app-private-gh-shim', 'relation': 'refused'}


def main_image(install, bundle, expected, phase, diagnostic=None, allow_absent=False, alias_data=None):
    """Held-descriptor bounded read, stable named identity and exact expected hash.
    Reading this known immutable image does not require nlink==1. No caller can
    select an arbitrary path, follow a link, mutate it or omit the bound digest.
    """
    need(phase in PHASES and isinstance(expected, str) and re.fullmatch('[0-9a-f]{64}', expected), 'path-refused')
    install, bundle = Path(install).absolute(), Path(bundle).absolute()
    path = bundle / MAIN
    before = after = None
    code = 'path-refused'
    fd = None
    try:
        need(bundle == install / 'yesman.app', code)
        parents = []
        for parent in [*reversed(path.parents)]:
            value = parent.lstat()
            need(stat.S_ISDIR(value.st_mode) and not stat.S_ISLNK(value.st_mode), code)
            parents.append((parent, value.st_dev, value.st_ino))
        code = 'absent'
        try:
            named = path.lstat()
        except FileNotFoundError:
            need(allow_absent, code)
            if diagnostic:
                diagnostic.record(phase, code, shim=shim_observation(alias_data, None))
            return None
        code = 'path-refused'
        need(not stat.S_ISLNK(named.st_mode), code)
        code = 'open-refused'
        fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK | getattr(os, 'O_CLOEXEC', 0))
        before = os.fstat(fd)
        code = 'not-regular'
        need(stat.S_ISREG(before.st_mode), code)
        code = 'size-refused'
        need(0 < before.st_size <= LIMIT, code)
        code = 'not-executable'
        need(before.st_mode & 0o111, code)
        code = 'owner-refused'
        need(before.st_uid == os.geteuid(), code)
        code = 'identity-changed'
        need(before.st_nlink >= 1 and identity(named) == identity(before), code)
        digest = hashlib.sha256()
        count = 0
        while True:
            raw = os.read(fd, min(1 << 20, LIMIT + 1 - count))
            if not raw:
                break
            count += len(raw)
            code = 'size-refused'
            need(count <= LIMIT, code)
            digest.update(raw)
        after = os.fstat(fd)
        code = 'identity-changed'
        need(identity(before) == identity(after) and count == before.st_size, code)
        need(identity(path.lstat()) == identity(after), code)
        for parent, dev, ino in parents:
            value = parent.lstat()
            need(stat.S_ISDIR(value.st_mode) and (value.st_dev, value.st_ino) == (dev, ino), code)
        code = 'hash-mismatch'
        need(digest.hexdigest() == expected, code)
        if diagnostic:
            diagnostic.record(phase, 'ok', before, after, shim_observation(alias_data, after))
        return {'sha256': digest.hexdigest(), 'bytes': count, 'mode': stat.S_IMODE(after.st_mode),
                'dev': after.st_dev, 'ino': after.st_ino, 'nlink': after.st_nlink}
    except (OSError, ValueError):
        if diagnostic:
            diagnostic.record(phase, code, before, after, shim_observation(alias_data, after or before))
        raise ValueError('bound App main proof: ' + code) from None
    finally:
        if fd is not None:
            os.close(fd)


def bundle_inventory(adapter, install, bundle, expected, phase, diagnostic, require_main=True, alias_data=None):
    bundle = Path(bundle).absolute()
    main = main_image(install, bundle, expected, phase, diagnostic, allow_absent=not require_main, alias_data=alias_data)
    rows = {}
    for path in sorted(bundle.rglob('*')):
        need(len(rows) < 5000, 'size-refused')
        relative = path.relative_to(bundle).as_posix()
        mode = path.lstat().st_mode
        if stat.S_ISDIR(mode):
            continue
        if relative == MAIN:
            need(main is not None, 'identity-changed')
            rows[relative] = {k: main[k] for k in ('sha256', 'bytes', 'mode')}
        elif stat.S_ISLNK(mode):
            target = os.readlink(path)
            need(not os.path.isabs(target) and '..' not in Path(target).parts, 'path-refused')
            rows[relative] = {'link': target}
        else:
            # All other immutable package entries and every mutable fixture
            # record keep the original nlink==1 verified-handle reader.
            raw = adapter.read_owned(bundle, path, 256 << 20)
            rows[relative] = {'sha256': sha(raw), 'bytes': len(raw), 'mode': stat.S_IMODE(mode)}
    need(not require_main or MAIN in rows, 'absent')
    return {'entries': rows, 'sha256': sha(json.dumps(rows, sort_keys=True).encode())}


def collect_native(original, install, bundle, expected, phase, diagnostic, allow_absent=False, alias_data=None):
    main = Path(bundle) / MAIN
    image = main_image(install, bundle, expected, phase, diagnostic, allow_absent, alias_data)
    value = subprocess.run(['/bin/ps', '-ww', '-axo', 'pid=,ppid=,comm='], capture_output=True, timeout=5, check=True)
    matches = original.process_rows(value.stdout, main)
    return {'matches': matches, 'instanceCount': len(matches),
            'pid': matches[0]['pid'] if len(matches) == 1 else None,
            'mainSHA256': image['sha256'] if image else None, 'mainIdentity': image}


def capture_phase(original, adapter, fixture, install, data, bundle, run, output, expected, phase, diagnostic, failed_launch=False):
    """Same sealed actor/checkpoint readers; only the exact main proof differs."""
    fixture.hosted_arm64()
    output = fixture.plain_path(output)
    need(output.is_relative_to(Path(os.environ['RUNNER_TEMP']).absolute()) and not output.exists(), 'path-refused')
    output.mkdir()
    proof = {'run': run,
             'native': collect_native(original, install, bundle, expected, phase, diagnostic, failed_launch, data),
             'inventory': bundle_inventory(adapter, install, bundle, expected, phase, diagnostic, not failed_launch, data),
             'actors': {}}
    for actor in ('active', 'paused'):
        value = adapter.capture_actor(data, actor, run)
        raw = value.pop('raw')
        folder = output / actor
        folder.mkdir()
        files = {}
        for name, body in raw.items():
            if body is None:
                files[name] = {'absent': True}
                continue
            (folder / name).write_bytes(body)
            files[name] = {'sha256': sha(body), 'bytes': len(body), 'path': actor + '/' + name}
        value['rawFiles'] = files
        proof['actors'][actor] = value
    restart = adapter.capture_restart(data)
    for name, value in restart['files'].items():
        (output / ('restart-' + name)).write_bytes(value.pop('raw'))
    proof['restart'] = restart
    proof['nativeAcceptance'] = False
    (output / 'capture.json').write_text(json.dumps(proof, indent=2) + '\n')
    return proof
