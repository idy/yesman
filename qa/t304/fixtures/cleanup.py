#!/usr/bin/env python3
"""Seed temporary data for the official macOS App. This program never starts it."""
import argparse
import datetime
import json
import os
from pathlib import Path
import secrets
import subprocess
import sys

parser = argparse.ArgumentParser()
parser.add_argument('operation', choices=('seed', 'verify'))
parser.add_argument('profile', type=Path)
parser.add_argument('--removed', nargs='*', default=[])
args = parser.parse_args()
profile = args.profile.resolve()
workspace = profile / 'yesmen' / 'cleanup-latency'
meta = workspace / '.yesman'
repo = profile / 'repos' / 'github.com' / 'fixture' / 'cleanup-latency.git'
refs_path = profile / 'fixture-refs.json'
branches = [f'latency-{i:02d}' for i in range(30)]
kept = {'latency-01', 'latency-29', 'keep-primary'}
env = dict(os.environ, GIT_CONFIG_GLOBAL=os.devnull, GIT_CONFIG_NOSYSTEM='1', GIT_TERMINAL_PROMPT='0', GIT_OPTIONAL_LOCKS='0')


def git(cwd, *arguments):
    return subprocess.check_output(['git', '-C', str(cwd), *arguments], env=env, text=True).strip()


def save(path, value):
    path.write_text(json.dumps(value, indent=2) + '\n')


if args.operation == 'verify':
    expected = set(args.removed)
    assert expected <= set(branches + ['safe-review']) - kept, expected
    assert refs_path.is_file() and meta.is_dir()
    records = json.loads((meta / 'worktrees.json').read_text())
    removed = {record['dir'] for record in records if record.get('removed')}
    assert removed == expected, (removed, expected)
    registrations = git(repo, 'worktree', 'list', '--porcelain').splitlines()
    for directory in branches + ['keep-primary', 'safe-review']:
        path = workspace / directory
        assert path.exists() == (directory not in expected), directory
        assert ('worktree ' + str(path) in registrations) == (directory not in expected), directory
        if directory not in expected:
            assert (path / 'README').is_file(), directory
            if directory not in {'safe-review', 'latency-01', 'latency-29'}:
                assert (path / 'local-only.txt').read_text() == directory + '\n'
    before = json.loads(refs_path.read_text())
    after = set(git(repo, 'for-each-ref', '--format=%(refname) %(objectname)').splitlines())
    assert set(before) <= after
    recovery = {f"refs/yesman/worktrees/{record['id']} {record['head']}" for record in records
                if record['dir'] == 'safe-review' and record.get('removed')}
    additions = after - set(before)
    metadata = tuple(f"refs/yesman/worktree-metadata/{record['id']}/" for record in records
                     if record['dir'] == 'safe-review' and record.get('removed'))
    assert recovery <= additions and all(ref.startswith(metadata) for ref in additions - recovery), (additions, recovery)
    print(json.dumps({'total': len(records), 'removed': sorted(expected), 'protected': sorted(kept), 'original_refs_preserved': True}))
    sys.exit(0)

profile.mkdir(parents=True, exist_ok=False)
meta.mkdir(parents=True)
origin = profile / 'fixture-origin'
origin.mkdir()
git(origin, 'init', '-q', '-b', 'main')
(origin / 'README').write_text('Isolated worktree latency fixture\n')
git(origin, 'add', 'README')
git(origin, '-c', 'user.name=Fixture', '-c', 'user.email=fixture@example.invalid', 'commit', '-qm', 'Fixture')
repo.parent.mkdir(parents=True)
git(origin, 'clone', '-q', '--bare', str(origin), str(repo))
git(repo, 'remote', 'set-url', 'origin', 'https://github.com/fixture/cleanup-latency.git')
head = git(repo, 'rev-parse', 'HEAD')
git(repo, 'update-ref', 'refs/remotes/origin/main', head)
git(repo, 'symbolic-ref', 'refs/remotes/origin/HEAD', 'refs/remotes/origin/main')
now = datetime.datetime.now(datetime.timezone.utc).isoformat().replace('+00:00', 'Z')
worktrees, tasks, workers = [], [], []
for i, directory in enumerate(branches + ['keep-primary', 'safe-review'], 1):
    path = workspace / directory
    detached = directory == 'safe-review'
    purpose = 'review' if detached else ('primary' if directory == 'keep-primary' else 'author')
    arguments = ('--detach', str(path), head) if detached else ('-b', directory, str(path), head)
    git(repo, 'worktree', 'add', '-q', *arguments)
    private = Path(git(path, 'rev-parse', '--absolute-git-dir'))
    marker = secrets.token_hex(16)
    (private / 'yesman-instance').write_text(marker)
    if directory not in {'safe-review', 'latency-01', 'latency-29'}:
        (path / 'local-only.txt').write_text(directory + '\n')
    owner = f'fixture-{i:02d}'
    task = f't{i}'
    workers.append({'name': owner, 'title': directory, 'task': directory, 'startedAt': now, 'finishedAt': now})
    tasks.append({'id': task, 'title': directory, 'status': 'done', 'owner': owner, 'ownerSince': now,
                  'createdAt': now, 'updatedAt': now, 'doneAt': now, 'doneOwner': owner})
    # Legacy registered fixture records are shown in desktop cleanup. Automatic
    # reclamation requires a recorded root and leaves them for the test.
    worktrees.append({'id': marker, 'dir': directory, 'repo': 'github.com/fixture/cleanup-latency', 'owner': owner,
                      'ownerSince': now, 'purpose': purpose, 'task': task, 'branch': '' if detached else directory, 'head': head})
save(meta / 'yesman.json', {'name': workspace.name, 'displayName': 'Worktree Timing Fixture', 'path': str(workspace), 'hiredAt': now})
save(meta / 'workers.json', workers)
save(meta / 'board.json', {'tasks': tasks, 'next': len(tasks)})
save(meta / 'worktrees.json', worktrees)
save(profile / 'settings.json', {'autoFetch': 0})
save(refs_path, git(repo, 'for-each-ref', '--format=%(refname) %(objectname)').splitlines())
commands = profile / 'fixture-bin'
commands.mkdir()
shim = commands / 'gh'
shim.write_text(r'''#!/usr/bin/env python3
import json, os, sys, time
from pathlib import Path
from urllib.parse import parse_qs, urlsplit
args = sys.argv[1:]
if args == ['--version']:
    print('gh version 2.103.3 (synthetic fixture)')
    sys.exit(0)
if len(args) == 2 and args[0] == 'api':
    url = urlsplit(args[1])
    query = parse_qs(url.query)
    head = query.get('head', [''])[0]
    if url.path == 'repos/fixture/cleanup-latency/pulls' and query.get('state') == ['open'] and head.startswith('fixture:latency-'):
        delay = 30 if head == 'fixture:latency-29' else .20
        with (Path(__file__).resolve().parent.parent / 'fixture-gh.log').open('a') as log:
            log.write(json.dumps({'branch': head, 'delaySeconds': delay, 'pid': os.getpid()}) + '\n')
        time.sleep(delay)
        print('[{"number":1}]' if head == 'fixture:latency-01' else '[]')
        sys.exit(0)
print('[]')
''')
shim.chmod(0o755)
print(json.dumps({'profile': str(profile), 'workspace': str(workspace), 'repo': str(repo), 'worktrees': len(worktrees), 'gh_shim': str(shim)}))
