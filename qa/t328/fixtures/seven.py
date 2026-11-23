#!/usr/bin/env python3
"""Seven synthetic repository roots derived from t303's fixture and final census."""
import hashlib
import json
import os
from pathlib import Path
import subprocess
import sys

phase, fixture_arg, *optional_inventory = sys.argv[1:]
assert len(optional_inventory) <= 1
fixture = Path(fixture_arg).resolve(strict=True)
fixture_root = Path(os.environ['T304_SANDBOX']).resolve(strict=True)
assert fixture.parent == fixture_root and not Path(fixture_arg).is_symlink()
assert fixture.name == 'fixture.noindex' and optional_inventory
manifest = json.loads((fixture / 'fixture-manifest.json').read_text())
assert Path(manifest['home']).resolve() == fixture
workspace = fixture / 'yesmen/yesman-5'
assert Path(manifest['protected']) == workspace
assert (workspace / 'local.txt').read_bytes() == b'yesman-5 local bytes\n' if phase != 'after' else not os.path.lexists(workspace)
outside = fixture / 'outside'
assert not outside.is_symlink() and outside.resolve() == outside
backup = outside / 'preserved'
paths = ['.yesman/tmp/t12/display-fw/host-jieli-sdk'] + [
    '.yesman/tmp/t14/h106-app/' + branch + '/idf'
    for branch in ('host', 'host-config-check', 'host-fix')
]
paths.append('.yesman/tmp/t3/display-fw/host-esp/idf')
module_paths = paths[1:]
extended = bool(optional_inventory)
if extended:
    bare_path = '.yesman/tmp/t15/bk-rgb-opt/sdk-inspect.git'
    normal_path = '.yesman/tmp/t23/bk-rgb-opt/firmware-build-src'
    paths += [bare_path, normal_path]
env = {k: v for k, v in os.environ.items() if not k.startswith('GIT_')}
env.update(GIT_CONFIG_GLOBAL=os.devnull, GIT_CONFIG_NOSYSTEM='1', GIT_TERMINAL_PROMPT='0')

def git(cwd, *args):
    return subprocess.check_output(['/usr/bin/git', '-C', str(cwd), '-c', 'user.name=Fixture', '-c', 'user.email=fixture@example.invalid', '-c', 'commit.gpgSign=false', '-c', 'protocol.file.allow=always', *args], env=env, text=True, stderr=subprocess.PIPE).strip()

def init(cwd):
    cwd.mkdir(parents=True)
    git(cwd, 'init', '-q', '-b', 'main')
    (cwd / 'tracked').write_bytes(b'fixture data\n')
    git(cwd, 'add', 'tracked')
    git(cwd, 'commit', '-qm', 'fixture')

def contents(cwd):
    records = {}
    for p in sorted(cwd.rglob('*')):
        rel = str(p.relative_to(cwd))
        if p.is_symlink():
            records[rel] = ('link', os.readlink(p))
        elif p.is_file():
            records[rel] = ('file', hashlib.sha256(p.read_bytes()).hexdigest())
        else:
            records[rel] = ('dir',)
    return records

def repo_state(cwd):
    assert cwd.is_dir()
    git(cwd, 'fsck', '--full')
    return {'head': git(cwd,'rev-parse','HEAD'), 'refs': git(cwd,'show-ref'), 'tree': git(cwd, 'cat-file', '-p', 'HEAD^{tree}'), 'bytes': hashlib.sha256(json.dumps(contents(cwd), sort_keys=True).encode()).hexdigest()}

if phase == 'seed':
    source = outside / 'module-origin'
    init(source)
    for rel in module_paths:
        at = workspace / rel
        init(at)
        for i in range(28):
            git(at, 'submodule', 'add', '-q', str(source), f'components/module-{i:02d}')
        git(at, 'commit', '-qm', 'add absorbed Git modules')
        (at / 'tracked').write_bytes(b'dirty tracked bytes\n')
        (at / 'untracked').write_bytes(b'untracked local bytes\n')
        (at / '.git/info/exclude').write_text('/ignored-cache\n')
        (at / 'ignored-cache').write_bytes(b'ignored local bytes\n')
        git(at, 'branch', 'local-only')
    markers = 1
    if extended:
        git(source, 'clone', '-q', '--no-hardlinks', '--bare', str(source), str(workspace / bare_path))
        init(workspace / normal_path)
        inventory = json.loads(Path(optional_inventory[0]).read_text())
        for row in inventory['markers']:
            if row['kind'] != 'symlink':
                continue
            rel = Path(row['marker'])
            target = Path(row['target'])
            for rel_path in (rel, target):
                assert not rel_path.is_absolute() and '..' not in rel_path.parts and rel_path.name == '.git'
            at = workspace / rel
            at.parent.mkdir(parents=True, exist_ok=True)
            if not at.is_symlink():
                at.symlink_to(workspace / target)
                markers += 1
    marker, = [row['marker'] for row in inventory['markers'] if row['kind']=='symlink' and row['target']==normal_path+'/.git']
    execroot = (workspace / marker).parent
    assert execroot.is_dir() and (execroot / '.git').is_symlink()
    for name in ('bazel-bin','bazel-out','bazel-testlogs','bazel-firmware-build-src'):
        target = execroot / name
        target.mkdir()
        (target / 'synthetic-build.txt').write_text('synthetic t23 build output\n')
        (workspace / normal_path / name).symlink_to(target)
    assert markers == 10 and len(paths) == 7
    seeded = {'repositories': paths, 'gitfiles': 112, 'Bazel_links': markers, 't23_build_links': 4, 'outside_registered_consumer': True}
    with (fixture / 'seven-seeded.json').open('x') as out:
        json.dump(seeded, out, indent=2)
        out.write('\n')
    print(json.dumps(seeded))
    sys.exit(0)

assert phase in ('move', 'preserved', 'after')
seeded = json.loads((fixture / 'seven-seeded.json').read_bytes())
assert seeded['repositories'] == paths and seeded['gitfiles'] == 112 and seeded['Bazel_links'] == 10 and seeded['t23_build_links'] == 4
receipt = fixture / 'preservation-receipt.json'
consumer = outside / 'dependent-worktree'
independent = outside / 'independent'
module_origin = outside / 'module-origin'
if phase == 'move':
    backup.mkdir()
    before = {rel: contents(workspace / rel) for rel in paths}
    consumer_before = repo_state(consumer)
    old_pointer = (consumer / '.git').read_bytes()
    untouched = {str(p): repo_state(p) for p in (independent, module_origin)}
    for rel in paths:
        dest = backup / rel
        dest.parent.mkdir(parents=True, exist_ok=True)
        assert (workspace / rel).stat().st_dev == dest.parent.stat().st_dev
        assert not os.path.lexists(dest)
        os.rename(workspace / rel, dest)
        assert not os.path.lexists(workspace / rel)
        assert contents(dest) == before[rel]
    assert subprocess.run(['/usr/bin/git', '-C', str(consumer), 'rev-parse', '--git-dir'], env=env, capture_output=True).returncode != 0
    git(backup / paths[0], 'worktree', 'repair', str(consumer))
    assert (consumer / '.git').read_bytes() != old_pointer
    consumer_after = repo_state(consumer)
    assert all(consumer_before[key] == consumer_after[key] for key in ('head','refs','tree'))
    assert contents(backup / paths[0]) == before[paths[0]]
    assert {str(p): repo_state(p) for p in (independent, module_origin)} == untouched
    states = {rel: repo_state(backup / rel) for rel in paths}
    modules = {}
    for rel in module_paths:
        for i in range(28):
            sub = backup / rel / f'components/module-{i:02d}'
            assert Path(git(sub,'rev-parse','--absolute-git-dir')).is_relative_to(backup / rel / '.git/modules')
            modules[str(sub)] = repo_state(sub)
    result = {'relocated': states, 'preserved_consumers': {str(consumer): consumer_after, **untouched}, 'preserved_modules': modules,
              'all_moved_bytes_identical': True, 'checked_before_after_repair': True}
    receipt.write_text(json.dumps(result, indent=2) + '\n')
else:
    result = json.loads(receipt.read_text())
    assert not any(path.name.startswith(('.deleting-', '.yesman-deleting-')) for path in (fixture / 'yesmen').iterdir())
    assert {rel: repo_state(backup / rel) for rel in paths} == result['relocated']
    for key in ('preserved_consumers','preserved_modules'):
        assert {p: repo_state(Path(p)) for p in result[key]} == result[key]
assert len(result['relocated']) == 7 and len(result['preserved_modules']) == 112
inventory = json.loads(Path(optional_inventory[0]).read_text())
links = [entry for entry in inventory['markers'] if entry['kind'] == 'symlink']
assert len(links) == 10
if phase != 'after':
    for entry in links:
        path = workspace / entry['marker']
        assert path.is_symlink() and not path.exists() and os.readlink(path) == str(workspace / entry['target'])
for name in ('bazel-bin','bazel-out','bazel-testlogs','bazel-firmware-build-src'):
    saved_link = backup / normal_path / name
    assert saved_link.is_symlink() and Path(os.readlink(saved_link)).is_relative_to(workspace)
    assert saved_link.exists() == (phase != 'after')
print(json.dumps({'phase': phase, 'workspace_absent': not os.path.lexists(workspace), 'repositories': len(result['relocated']), 'module_worktrees': len(result['preserved_modules']), 'outside_controls': len(result['preserved_consumers']), 'all_moved_bytes_identical': result['all_moved_bytes_identical'], 'checked_before_after_repair': result['checked_before_after_repair']}))
