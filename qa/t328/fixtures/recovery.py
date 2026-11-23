#!/usr/bin/env python3
"""Create and inspect the scoped on-disk fault for the two-process deletion case."""
import hashlib
import json
import os
from pathlib import Path
import re
import stat
import subprocess
import sys

phase, supplied = sys.argv[1:]
profile = Path(supplied)
scope = Path(os.environ['T304_SANDBOX']).resolve(strict=True)
assert profile == scope / 'fixture.noindex' and profile.resolve(strict=True) == profile
parent = profile / 'yesmen'
assert parent.resolve(strict=True) == parent
live = parent / 'yesman-5'
admin = Path('.yesman/tmp/t12/display-fw/host-jieli-sdk/.git')
first_link = Path('.yesman/tmp/t12/display-fw/bazel-output/execroot/_main/.git')
fixture = profile / 'recovery-fixture.json'
previous = profile / 'recovery-stage.json'


def read(path):
    return json.loads(path.read_bytes())


def save(path, row):
    with path.open('x') as out:
        json.dump(row, out, sort_keys=True)


def git(path, *args):
    env = dict(os.environ, GIT_CONFIG_GLOBAL=os.devnull, GIT_CONFIG_NOSYSTEM='1', GIT_TERMINAL_PROMPT='0')
    return subprocess.check_output(['/usr/bin/git', '-C', str(path), *args], env=env,
                                   stderr=subprocess.PIPE, text=True, timeout=10).strip()


def hash_file(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def outside_state():
    snapshots = {}
    for name in ('independent','module-origin'):
        path = profile / 'outside' / name
        assert path.resolve(strict=True) == path
        git(path,'fsck','--full')
        objects = path / '.git/objects'
        members = sorted(p for p in objects.rglob('*') if p.is_file())
        assert 0 < len(members) <= 100 and all(p.resolve(strict=True) == p for p in members)
        snapshots[name] = {'head':git(path,'rev-parse','HEAD'),
                           'refsSHA256':hashlib.sha256(git(path,'show-ref').encode()).hexdigest(),
                           'objectsSHA256':hashlib.sha256(json.dumps({str(p.relative_to(objects)):hash_file(p) for p in members},sort_keys=True).encode()).hexdigest(),
                           'payloadSHA256':hash_file(path/('tracked' if name=='module-origin' else 'tracked.txt'))}
    return snapshots


def lock(path, flag):
    info = path.lstat()
    assert path.resolve(strict=True) == path and path.is_relative_to(parent)
    assert stat.S_ISREG(info.st_mode) and info.st_nlink == 1 and info.st_uid == os.getuid()
    if flag == 'nouchg' and not info.st_flags & stat.UF_IMMUTABLE:
        return
    subprocess.run(['/usr/bin/chflags',flag,str(path)], check=True, timeout=5)
    assert bool(path.stat().st_flags & stat.UF_IMMUTABLE) == (flag=='uchg')


def stages():
    return sorted(p for p in parent.iterdir() if re.fullmatch(r'\.deleting-v1-[0-9a-f]+\.yesman-5',p.name))


def identity(path):
    info = path.lstat()
    return [info.st_dev,info.st_ino]


def protected_state():
    return {name:hash_file(parent/name/'local.txt') for name in ('plain-delete','bazel-links')}


def fault_flag(path):
    return bool(path.lstat().st_flags & stat.UF_IMMUTABLE)


def entry_state(path, with_hash=False):
    try:
        info = path.lstat()
    except FileNotFoundError:
        return {'present':False}
    row = {'present':True,'mode':stat.S_IFMT(info.st_mode),'identity':[info.st_dev,info.st_ino],
           'flagged':bool(info.st_flags & stat.UF_IMMUTABLE)}
    if with_hash and stat.S_ISREG(info.st_mode) and path.resolve(strict=True)==path and info.st_size <= 8<<20:
        row['sha256'] = hash_file(path)
    return row


def snapshot():
    entries = sorted(parent.iterdir())
    assert len(entries) <= 20
    candidates = [p for p in entries if p==live or p.name.startswith('.deleting-v1-')]
    return {'entries':[p.name for p in entries], 'workspaces':{
        p.name:{'directory':entry_state(p),'earlierMarker':entry_state(p/first_link),
                'config':entry_state(p/admin/'config',True),'HEAD':entry_state(p/admin/'HEAD',True)}
        for p in candidates if p.is_dir() and p.resolve(strict=True)==p},
        'approvals':{p.name:entry_state(p,True) for p in entries if p.name.startswith('.approved-deletion-v1-')},
        'outside':snapshot_control(outside_state), 'protected':snapshot_control(protected_state)}


def snapshot_control(check):
    try:
        return {'observed':check()}
    except Exception as error:
        return {'errorType':type(error).__name__}


def proof_of_stage():
    baseline = read(fixture)
    stage, = stages()
    assert stage.is_dir() and stage.resolve(strict=True)==stage and identity(stage)==baseline['directory']
    assert not os.path.lexists(live)
    suffix = stage.name.removeprefix('.deleting-v1-')
    receipt = parent/('.approved-deletion-v1-'+suffix)
    assert receipt.is_file() and not receipt.is_symlink() and receipt.stat().st_size <= 8<<20
    approval = receipt.read_bytes()
    header_size = int.from_bytes(approval[:4],'little')
    assert 0 < header_size <= 1<<20 and 4+header_size+32 < len(approval)
    header = json.loads(approval[4:4+header_size])
    assert header['Stage']==stage.name and len(set(header['Git'].values()))==119 and len(header['Pointers'])==112
    assert bytes(header['Configs'][str(admin)]).hex()==baseline['configSHA256']
    assert not os.path.lexists(stage/first_link)
    assert hash_file(stage/admin/'config')==baseline['configSHA256']
    assert hash_file(stage/admin/'HEAD')==baseline['headSHA256']
    assert outside_state()==baseline['outside'] and protected_state()==baseline['protected']
    return stage, {'entry':stage.name,'identity':identity(stage),'approvalFile':receipt.name,
                   'approvalSHA256':hashlib.sha256(approval).hexdigest(),'approvalStage':header['Stage'],
                   'earlierMarkerRemoved':True,'flagged':fault_flag(stage/admin/'config'),
                   'configSHA256':baseline['configSHA256'],'headSHA256':baseline['headSHA256'],
                   'outside':baseline['outside'],'protected':baseline['protected']}


if phase=='seed':
    assert not stages() and (live/first_link).is_symlink()
    old = profile/'outside/dependent-worktree'
    assert old.resolve(strict=True)==old and git(old,'rev-parse','--show-toplevel')==str(old)
    assert git(old,'status','--porcelain')==''
    git(live/admin.parent,'worktree','remove',str(old))
    assert not old.exists()
    seed = read(profile/'seven-seeded.json')
    assert len(seed['repositories'])==7 and seed['gitfiles']==112 and seed['Bazel_links']==10
    baseline = {'directory':identity(live),'configSHA256':hash_file(live/admin/'config'),
                'headSHA256':hash_file(live/admin/'HEAD'),'outside':outside_state(),'protected':protected_state(),
                'localStores':seed['repositories'],'absorbedSubmodules':seed['gitfiles'],'gitLinks':seed['Bazel_links']}
    save(fixture,baseline)
    lock(live/admin/'config','uchg')
    print(json.dumps(baseline))
elif phase=='live':
    baseline = read(fixture)
    assert live.is_dir() and identity(live)==baseline['directory'] and not stages()
    assert not any(p.name.startswith('.approved-deletion-v1-') for p in parent.iterdir())
    assert (live/first_link).is_symlink() and fault_flag(live/admin/'config')
    assert hash_file(live/admin/'config')==baseline['configSHA256'] and hash_file(live/admin/'HEAD')==baseline['headSHA256']
    assert outside_state()==baseline['outside'] and protected_state()==baseline['protected']
    print(json.dumps({'live':True,'flagged':True,'directory':identity(live),'approvalPresent':False,'earlierMarkerRemoved':False,
                      **{key:baseline[key] for key in ('configSHA256','headSHA256','outside','protected')}}))
elif phase=='interrupted':
    _, row = proof_of_stage()
    assert row['flagged'] is True
    print(json.dumps(row))
elif phase=='release-fault':
    stage, before = proof_of_stage()
    assert before['flagged'] is True
    lock(stage/admin/'config','nouchg')
    _, after = proof_of_stage()
    assert after=={**before,'flagged':False}
    save(previous,after)
    print(json.dumps({'before':before,'after':after}))
elif phase=='pending':
    _, row = proof_of_stage()
    assert row==read(previous) and row['flagged'] is False
    print(json.dumps(row))
elif phase=='complete':
    baseline = read(fixture)
    approved = read(previous)
    assert not stages() and not os.path.lexists(live) and not os.path.lexists(parent/approved['approvalFile'])
    assert outside_state()==baseline['outside'] and protected_state()==baseline['protected']
    print(json.dumps({'stageRemoved':True,'approvalRemoved':True,'approvedStage':approved,
                      'outside':baseline['outside'],'protected':baseline['protected']}))
elif phase=='snapshot':
    print(json.dumps(snapshot()))
elif phase=='restore-on-error':
    errors = []
    for directory in [live,*stages()]:
        config = directory/admin/'config'
        if not os.path.lexists(config):
            continue
        try:
            baseline = read(fixture)
            assert identity(directory)==baseline['directory'] and config.resolve(strict=True)==config
            assert hash_file(config)==baseline['configSHA256']
            lock(config,'nouchg')
        except Exception as error:
            errors.append({'entry':directory.name,'type':type(error).__name__})
    print(json.dumps({'errors':errors,'disk':snapshot()}))
else:
    raise ValueError(phase)
