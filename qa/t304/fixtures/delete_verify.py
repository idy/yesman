#!/usr/bin/env python3
"""Verify the before/after disk state of a synthetic deletion fixture."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import subprocess

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument('manifest', type=Path)
parser.add_argument('--ordinary-deleted', action='store_true')
parser.add_argument('--force-deleted', action='store_true')
args = parser.parse_args()
m = json.loads(args.manifest.read_text())
root = args.manifest.resolve().parent
assert root == Path(m['home']).resolve()
for kind, removed in [('ordinary', args.ordinary_deleted), ('force', args.force_deleted), ('protected', False)]:
    p = Path(m[kind])
    assert p.parent == root / 'yesmen'
    if removed:
        assert not os.path.lexists(p), f'{kind} workspace is still present'
        assert not list(p.parent.glob('.deleting-*.' + p.name)), f'{kind} workspace has a staged remainder'
    else:
        assert (p / 'local.txt').read_text() == p.name + ' local bytes\n'
        assert (p / '.yesman/yesman.json').is_file()
for kind in ['force', 'protected']:
    marker = Path(m[kind+'Marker'])
    p = Path(m[kind])
    assert marker.is_relative_to(p)
    if kind == 'force' and args.force_deleted:
        assert not os.path.lexists(marker)
    else:
        assert marker.is_symlink() and not marker.exists()
        assert os.readlink(marker) == str(p / 'gizos-display-pr/.git')
        assert (marker.parent / 'a-dirty-file').read_text() == 'local fixture edits\n'

env = {k: v for k, v in os.environ.items() if not k.startswith('GIT_')}
env.update(GIT_CONFIG_GLOBAL=os.devnull, GIT_CONFIG_NOSYSTEM='1', GIT_TERMINAL_PROMPT='0')
def git(path, *args):
    return subprocess.check_output(['/usr/bin/git', '-C', str(path), *args], env=env, text=True).strip()
for kind, branch in [('externalConsumer', 'dependent'), ('independentRepo', 'main')]:
    p = Path(m[kind])
    assert p.parent == root / 'outside'
    assert git(p, 'rev-parse', 'HEAD') == m[kind+'HEAD']
    assert git(p, 'show-ref', '--verify', f'refs/heads/{branch}').split()[0] == m[kind+'HEAD']
    assert (p / 'tracked.txt').read_bytes() == b'synthetic Git bytes\n'
    assert git(p, 'show', 'HEAD:tracked.txt') == 'synthetic Git bytes'
protected = Path(m['protectedGitDir'])
assert protected.is_relative_to(m['protected']) and protected.is_dir() and not protected.is_symlink()
assert (protected / 'worktrees/dependent-worktree/gitdir').read_text().strip() == str(Path(m['externalConsumer']) / '.git')
print(json.dumps({'ordinaryDeleted': args.ordinary_deleted, 'forceDeleted': args.force_deleted,
                  'protectedGitDir': str(protected), 'protectedLinksAndBothExternalGitRepositories': 'verified',
                  'protectedHeadFileSHA256': hashlib.sha256((protected / 'HEAD').read_bytes()).hexdigest()}))
