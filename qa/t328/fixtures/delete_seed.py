#!/usr/bin/env python3
"""Create a fresh isolated Yes Man home and write its assertion manifest."""
import argparse
import datetime
import json
import os
from pathlib import Path
import subprocess

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument('home', type=Path, help='Nonexistent path for a disposable YESMAN_HOME')
parser.add_argument('--files', type=int, default=0, help='Files in later synthetic Bazel output')
args = parser.parse_args()
if args.files < 0:
    parser.error('--files cannot be negative')
root = args.home.absolute()
root.mkdir(mode=0o700)
now = datetime.datetime.now(datetime.timezone.utc).isoformat().replace('+00:00', 'Z')
rel = Path('.yesman/tmp/t12/display-fw')
bazel = rel / 'bazel-output/execroot/_main'

def workspace(name, label):
    p = root / 'yesmen' / name
    (p / '.yesman').mkdir(parents=True)
    (p / '.yesman/yesman.json').write_text(json.dumps({'hiredAt': now, 'displayName': label}))
    (p / 'local.txt').write_text(f'{name} local bytes\n')
    return p

def git(path, *args):
    env = {k: v for k, v in os.environ.items() if not k.startswith('GIT_')}
    env.update(GIT_CONFIG_GLOBAL=os.devnull, GIT_CONFIG_NOSYSTEM='1', GIT_TERMINAL_PROMPT='0')
    return subprocess.check_output(['/usr/bin/git', '-C', str(path), '-c', 'user.name=Fixture', '-c', 'user.email=fixture@example.invalid', '-c', 'commit.gpgSign=false', *args], env=env, text=True).strip()

def init_repo(path):
    path.mkdir(parents=True)
    git(path, 'init', '-q', '-b', 'main')
    (path / 'tracked.txt').write_text('synthetic Git bytes\n')
    git(path, 'add', 'tracked.txt')
    git(path, 'commit', '-qm', 'fixture')
    return git(path, 'rev-parse', 'HEAD')

plain = workspace('plain-delete', '普通删除夹具')
force = workspace('bazel-links', 'Bazel 悬空链接夹具')
held = workspace('yesman-5', '测试屏幕性能（隔离）')
for p in (force, held):
    at = p / bazel
    at.mkdir(parents=True)
    (at / '.git').symlink_to(p / 'gizos-display-pr/.git')
    (at / 'a-dirty-file').write_text('local fixture edits\n')
    for i in range(30):
        (at / f'link-{i}').symlink_to('a-dirty-file')
    for name in ('bazel-bin', 'bazel-out', 'generated'):
        (at / name).mkdir()

outside = root / 'outside'
outside.mkdir()
keep_repo = outside / 'independent'
keep_head = init_repo(keep_repo)
(force / bazel / 'outward-link').symlink_to(keep_repo, target_is_directory=True)
protected_repo = held / rel / 'host-jieli-sdk'
protected_head = init_repo(protected_repo)
consumer = outside / 'dependent-worktree'
git(protected_repo, 'worktree', 'add', '-qb', 'dependent', str(consumer))
assert git(consumer, 'rev-parse', 'HEAD') == protected_head
for start in range(0, args.files, 128):
    d = held / f'.yesman/tmp/t20/bazel-host/output/pkg-{start // 128:05}'
    d.mkdir(parents=True)
    for i in range(start, min(start + 128, args.files)):
        (d / f'file-{i}').touch()
manifest = {
    'home': str(root), 'ordinary': str(plain), 'force': str(force), 'protected': str(held),
    'forceMarker': str(force / bazel / '.git'), 'protectedMarker': str(held / bazel / '.git'),
    'protectedGitDir': str(protected_repo / '.git'), 'lateOutputFiles': args.files,
    'externalConsumer': str(consumer), 'externalConsumerHEAD': protected_head,
    'independentRepo': str(keep_repo), 'independentRepoHEAD': keep_head,
}
(root / 'fixture-manifest.json').write_text(json.dumps(manifest, indent=2) + '\n')
print(json.dumps(manifest, indent=2))
