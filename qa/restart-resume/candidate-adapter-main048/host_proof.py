"""Small host-proof collection pieces for the authorized ARM64 runner.

No native command runs on import. Pure parsers/inventory work on owned fixture
files; collect_native()/capture_phase() require the disposable hosted guard.
Codesign and archive/signature verification stay in existing release validators.
"""
import json
import os
from pathlib import Path
import stat
import subprocess
from adapter import need, read_owned, sha, capture_actor, capture_restart, load_prototype


def process_rows(raw, main_path):
    need(len(raw) <= 1 << 20, 'process snapshot budget')
    matches = []
    for line in raw.decode().splitlines():
        fields = line.strip().split(None, 2)
        if len(fields) != 3:
            continue
        pid, ppid, comm = fields
        if comm == str(main_path):
            need(pid.isdigit() and ppid.isdigit(), 'bad native PID')
            matches.append({'pid': int(pid), 'ppid': int(ppid), 'executable': comm})
    return matches


def bundle_inventory(bundle, require_main=True):
    bundle = Path(bundle).absolute()
    need(not bundle.is_symlink() and bundle.name == 'yesman.app', 'wrong bundle root')
    rows = {}
    for p in sorted(bundle.rglob('*')):
        need(len(rows) < 5000, 'bundle inventory budget')
        rel = p.relative_to(bundle).as_posix()
        mode = p.lstat().st_mode
        if stat.S_ISDIR(mode):
            continue
        if stat.S_ISLNK(mode):
            target = os.readlink(p)
            need(not os.path.isabs(target) and '..' not in Path(target).parts, 'external bundle link')
            rows[rel] = {'link': target}
        else:
            b = read_owned(bundle, p, 256 << 20)
            rows[rel] = {'sha256': sha(b), 'bytes': len(b), 'mode': stat.S_IMODE(mode)}
    need(not require_main or 'Contents/MacOS/Yes Man' in rows, 'main missing from recovered bundle')
    return {'entries': rows, 'sha256': sha(json.dumps(rows, sort_keys=True).encode())}


def collect_native(bundle):
    fixture, _ = load_prototype(); fixture.hosted_arm64()
    bundle = fixture.plain_path(bundle)
    need(bundle.is_relative_to(Path(os.environ['RUNNER_TEMP']).absolute()), 'native image outside owned fixture install')
    main = bundle / 'Contents/MacOS/Yes Man'
    result = subprocess.run(['/bin/ps', '-ww', '-axo', 'pid=,ppid=,comm='], capture_output=True, timeout=5, check=True)
    matches = process_rows(result.stdout, main)
    # Return only exact image rows, never the VM's unrelated commands/env.
    return {'matches': matches, 'instanceCount': len(matches), 'pid': matches[0]['pid'] if len(matches) == 1 else None,
            'mainSHA256': fixture.hash_file(main)['sha256'] if main.exists() else None}


def helper_error(log_bytes):
    """Keep a bounded excerpt for review, not an assertion that a timeout was
    an actual launch failure. The native adapter must also observe missing main,
    helper termination, zero Q processes/pages and the stable unclaimed final.
    """
    tail = log_bytes[-16384:].decode(errors='replace')
    lines = [x for x in tail.splitlines() if any(tag in x for tag in
             ('LSOpen', 'LaunchServices', 'kLSNoExecutableErr', 'no executable', 'could not be launched', 'cannot be opened'))]
    return {'candidateLines': lines[-12:], 'launchFailureProven': False, 'tailSHA256': sha(log_bytes[-16384:])}


def capture_phase(data, bundle, run, output, failed_launch=False):
    fixture, _ = load_prototype(); fixture.hosted_arm64()
    output = fixture.plain_path(output)
    need(output.is_relative_to(Path(os.environ['RUNNER_TEMP']).absolute()) and not output.exists(), 'new owned proof output required')
    output.mkdir()
    proof = {'run': run, 'native': collect_native(bundle), 'inventory': bundle_inventory(bundle, require_main=not failed_launch), 'actors': {}}
    for actor in ('active', 'paused'):
        value = capture_actor(data, actor, run)
        raw = value.pop('raw')
        folder = output / actor; folder.mkdir()
        files = {}
        for name, b in raw.items():
            if b is None:
                files[name] = {'absent': True}; continue
            (folder / name).write_bytes(b)
            files[name] = {'sha256': sha(b), 'bytes': len(b), 'path': actor + '/' + name}
        value['rawFiles'] = files
        proof['actors'][actor] = value
    restart = capture_restart(data)
    for name, v in restart['files'].items():
        (output / ('restart-' + name)).write_bytes(v.pop('raw'))
    proof['restart'] = restart
    proof['nativeAcceptance'] = False
    (output / 'capture.json').write_text(json.dumps(proof, indent=2) + '\n')
    return proof
