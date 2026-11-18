"""Keyless, lock-pinned public npm cache. No app/pi or package scripts run.
The production installer still performs its own npm ci and patch verification.
"""
import argparse
import base64
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
from urllib.parse import urlsplit
from provenance import first_attempt
from acquisition_diagnostics import Diagnostic

LOCK='805dadc8e1468ae4f0d72fac3936e7a1fc9a97ac3e7b23ab1e3fc18d1e1f5569'
PACKAGE='ebe3b8eab26c2007d69658c6c42b53ddd29351b805c326777eaf4692d3369626'


def sha(b):return hashlib.sha256(b).hexdigest()
def need(ok,why):
    if not ok:raise ValueError(why)
def selected(lock):
    need(sha(lock)==LOCK,'pinned lock changed')
    rows={};resolved=0
    for package,value in json.loads(lock)['packages'].items():
        if not value.get('resolved'):continue
        resolved+=1
        u=urlsplit(value['resolved']);need(u.scheme=='https' and u.hostname=='registry.npmjs.org' and not u.username and not u.password and not u.query and not u.fragment and u.path.endswith('.tgz'),'public pinned registry tarball only')
        algorithm,encoded=value['integrity'].split('-',1);need(algorithm=='sha512','expected lock SHA512 integrity')
        raw=base64.b64decode(encoded,validate=True);need(len(raw)==64,'invalid SRI')
        digest=raw.hex();relative='_cacache/content-v2/sha512/'+digest[:2]+'/'+digest[2:4]+'/'+digest[4:]
        rows[relative]={'url':value['resolved'],'integrity':value['integrity'],'sha512':digest}
    need(resolved==123 and len(rows)==122,'pinned dependency set changed')
    return rows


def verify(cache,lock):
    cache=Path(cache).absolute();rows=selected(lock);total=0
    for relative,row in rows.items():
        path=cache/relative
        for parent in [cache,*path.relative_to(cache).parents]:
            if parent!=Path('.'):need(not (cache/parent if not parent.is_absolute() else parent).is_symlink(),'linked cache refused')
        need(path.is_file() and not path.is_symlink(),'pinned public cache object missing')
        st=path.stat();need(st.st_nlink==1 and st.st_size<=64<<20,'cache object budget/identity')
        raw=path.read_bytes();need(len(raw)==st.st_size and hashlib.sha512(raw).hexdigest()==row['sha512'],'cache object integrity differs')
        total+=len(raw);need(total<=512<<20,'cache budget')
    return {'schema':1,'lockSHA256':LOCK,'packageSHA256':PACKAGE,'objects':rows,'bytes':total,'integritiesVerified':True,'scriptsExecuted':False,'credentialsUsed':False}


def bootstrap(cache,data,lock):
    """Called only after the guarded fresh seed, before first production launch.
    Copies exactly lock-selected SRI content, never npmrc/index/global settings."""
    cache,data=Path(cache).absolute(),Path(data).absolute();receipt=verify(cache,lock)
    target=data/'.runtime/pi-managed/npm-cache';need(data.is_dir() and not target.exists(),'new seeded profile/cache required')
    for p in [*reversed(data.parents),data]:need(not p.is_symlink(),'linked data root')
    for relative in receipt['objects']:
        source=cache/relative;dest=target/relative;dest.parent.mkdir(parents=True,exist_ok=True);shutil.copyfile(source,dest)
    verify(target,lock)
    config=target.parent/'npm-public-cache.ini'
    need(not config.exists(), 'new npm fixture config required')
    with config.open('x') as output:
        os.chmod(config, 0o600)
        output.write('offline=true\nignore-scripts=true\naudit=false\nfund=false\nupdate-notifier=false\nfetch-retries=0\n')
    return {**receipt,'target':str(target),'productionInstallerStillRequired':True,'npmUserConfig':str(config)}


def acquisition_environment(work):
    """Distinct empty config files avoid npm double-loading one /dev/null path."""
    work=Path(work)
    user=work/'user-empty.ini';global_config=work/'global-empty.ini'
    for path in (user,global_config):
        with path.open('xb')as f:os.chmod(path,0o600)
    env={k:os.environ[k]for k in ('PATH','HOME','USER','LOGNAME','LANG','LC_ALL','TMPDIR')if k in os.environ}
    env.update(NPM_CONFIG_USERCONFIG=str(user),NPM_CONFIG_GLOBALCONFIG=str(global_config),
               NPM_CONFIG_REGISTRY='https://registry.npmjs.org/',NPM_CONFIG_FETCH_RETRIES='0')
    return env


def _acquire(source,out,diagnostic):
    diagnostic.phase('npm-config')
    source,out=Path(source).resolve(),Path(out).absolute();need(not out.exists(),'new public cache output required')
    lock=(source/'internal/piruntime/assets/package-lock.json').read_bytes();package=(source/'internal/piruntime/assets/package.json').read_bytes();need(sha(package)==PACKAGE,'pinned package changed');selected(lock)
    need(os.environ.get('GITHUB_ACTIONS')=='true' and os.environ.get('RUNNER_ENVIRONMENT')=='github-hosted','keyless hosted acquisition only')
    need(not os.environ.get('MYGO_UPDATER_PRIVATE_KEY') and not os.environ.get('GH_TOKEN') and not os.environ.get('GITHUB_TOKEN'),'acquisition must have no credentials')
    work=Path(os.environ['RUNNER_TEMP'])/'t199-public-npm-acquisition';need(not work.exists(),'acquisition must not retry in place');work.mkdir();(work/'package.json').write_bytes(package);(work/'package-lock.json').write_bytes(lock)
    cache=work/'cache';env=acquisition_environment(work)
    command=['npm','ci','--ignore-scripts','--no-audit','--no-fund','--cache',str(cache)]
    diagnostic.phase('tool-version');version=subprocess.run(['npm','--version'],cwd=work,env=env,capture_output=True,timeout=10)
    diagnostic.command(version);need(version.returncode==0,'installed npm version unavailable');diagnostic.version('npm',version.stdout)
    diagnostic.phase('npm-ci')
    p=subprocess.run(command,cwd=work,env=env,capture_output=True,timeout=300)
    diagnostic.command(p);need(p.returncode==0,'keyless public acquisition failed; no retry')
    diagnostic.phase('npm-integrity');receipt=verify(cache,lock)
    out.mkdir(parents=True)
    for relative in receipt['objects']:
        dest=out/'cache'/relative;dest.parent.mkdir(parents=True,exist_ok=True);shutil.copyfile(cache/relative,dest)
    receipt['command']=command;receipt['repository']=os.environ['GITHUB_REPOSITORY'];receipt['workflowCommit']=os.environ['GITHUB_SHA'];receipt['runID']=os.environ['GITHUB_RUN_ID'];receipt['runAttempt']=int(os.environ['GITHUB_RUN_ATTEMPT'])
    (out/'cache-receipt.json').write_text(json.dumps(receipt,indent=2)+'\n')


def acquire(source,out):
    first_attempt()
    try:
        with Diagnostic('public-npm')as diagnostic:_acquire(source,out,diagnostic)
    except Exception:
        raise SystemExit('t199 public npm acquisition failed; see sanitized T199_ACQUISITION_STATUS and diagnostics artifact')from None


if __name__=='__main__':
    p=argparse.ArgumentParser();p.add_argument('source');p.add_argument('output');a=p.parse_args();acquire(a.source,a.output)
