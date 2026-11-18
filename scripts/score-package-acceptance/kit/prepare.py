#!/usr/bin/env python3
"""Export fixed R2 and optionally build two private macOS ARM64 packages; never launch them."""
import argparse
import io
import json
import os
from pathlib import Path
import platform
import subprocess
import tarfile
from contract import SOURCE, TREE, STAGES, CSV, SETTINGS, sha

KIT = Path(__file__).resolve().parent

def output(*args, cwd=None, env=None):
    return subprocess.check_output(args, cwd=cwd, env=env, text=True).strip()

def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--repo', required=True, type=Path)
    parser.add_argument('--output', required=True, type=Path)
    parser.add_argument('--build', action='store_true')
    args = parser.parse_args()
    repo, root = args.repo.resolve(), args.output.resolve()
    assert output('git','rev-parse',SOURCE+'^{tree}',cwd=repo) == TREE
    assert not root.exists(), 'Use a new task-private output directory'
    root.mkdir(parents=True, mode=0o700)
    archive = subprocess.check_output(['git','archive',SOURCE], cwd=repo)
    modcache = output('go','env','GOMODCACHE')
    for name in ['home','cache','temp','gopath','config','evidence']:
        (root/name).mkdir(mode=0o700)
    env = {key:os.environ[key] for key in ('PATH','LANG') if key in os.environ}
    env.update(HOME=str(root/'home'),XDG_CONFIG_HOME=str(root/'config'),TMPDIR=str(root/'temp'),
               GOCACHE=str(root/'cache'),GOMODCACHE=modcache,GOPATH=str(root/'gopath'),
               GOENV='off',GOTOOLCHAIN='local',GOPROXY='off',GOSUMDB='off',
               CGO_ENABLED='1',CGO_CFLAGS='-O2 -g -mmacosx-version-min=13.0',MACOSX_DEPLOYMENT_TARGET='13.0')
    plan={'source':SOURCE,'tree':TREE,'platform':'darwin/arm64','packageVersion':'0.1.45',
          'archiveSHA256':sha(archive),'patchSHA256':sha(CSV),'settingsSHA256':sha(SETTINGS),
          'fixtureChanges':['one newest synthetic score CSV per stage','mygo.json updates removed; native updater acceptance reused separately'],
          'kitSHA256':{p.name:sha(p.read_bytes()) for p in KIT.iterdir() if p.is_file() and p.suffix in ['.py','.js']},
          'built':False,'stages':{}}
    for stage, spec in STAGES.items():
        src=root/('source-'+stage);src.mkdir()
        with tarfile.open(fileobj=io.BytesIO(archive)) as tar:
            members=tar.getmembers()
            assert all((m.isfile() or m.isdir()) and not Path(m.name).is_absolute() and '..' not in Path(m.name).parts for m in members)
            tar.extractall(src) # Fixed Git archive, regular files/directories only; Python 3.9 compatible.
        original={str(p.relative_to(src)):sha(p.read_bytes()) for p in src.rglob('*') if p.is_file()}
        cfg=json.loads((src/'mygo.json').read_text())
        assert cfg['identifier']=='dev.idy.yesman' and cfg['version']=='0.1.45'
        cfg.pop('updates') # No feed, test signing key, updater invocation or network on launch.
        (src/'mygo.json').write_text(json.dumps(cfg,indent=2)+'\n')
        text=('model,intelligence_index,tok_per_s,coding_mean_pct,cost_per_task_usd,time_min_per_task\n'
              f"QA CSV Fixture,{spec['baseIntelligence']},{spec['speed']},{spec['coding']},4.5,{spec['minutes']}\n")
        csvpath=src/'internal/crew/scores'/('qa-csv-fixture-'+spec['date']+'.csv')
        csvpath.write_text(text)
        dest=root/('package-'+stage)
        item={**spec,'sourceDirectory':str(src),'outputDirectory':str(dest),'csv':str(csvpath),
              'csvSHA256':sha(text.encode()),'fixtureConfigSHA256':sha((src/'mygo.json').read_bytes()),
              'goSources':{p:h for p,h in original.items() if p.endswith('.go')},
              'webSources':{p:h for p,h in original.items() if p.startswith('web/')}}
        command=['go','tool','mygo','build','-platform','darwin/arm64','-skip-dmg','-skip-notarize','-sign','-','-o',str(dest)]
        item['buildCommand']=command
        if args.build:
            assert platform.system()=='Darwin' and platform.machine()=='arm64', 'Build on native macOS ARM64'
            with (root/'evidence'/('build-'+stage+'.log')).open('w') as log:
                subprocess.run(command,cwd=src,env=env,stdout=log,stderr=subprocess.STDOUT,check=True,timeout=600)
            apps=list(dest.glob('darwin-arm64/*.app'));assert len(apps)==1,apps
            app=apps[0]
            # Existing read-only inspector: no launch, registration or permission calls.
            inspect_path=root/'evidence'/('bundle-'+stage+'.json')
            subprocess.run(['python3',str(src/'scripts/check_macos_bundle.py'),str(app),str(inspect_path)],env=env,check=True,timeout=45)
            info=json.loads(inspect_path.read_text())
            assert info['goBuildInfo']['Settings'] and info['machOArchitecture']=='arm64'
            binary=app/'Contents/MacOS'/info['info']['CFBundleExecutable']
            assert csvpath.read_bytes() in binary.read_bytes(), 'fixture is not embedded'
            assert all((src/p).read_bytes() in binary.read_bytes() for p in ['web/crew.js','web/app.js','web/index.html'])
            item.update(app=str(app),binary=str(binary),binarySHA256=sha(binary.read_bytes()),
                        bundleFiles={str(p.relative_to(app)):sha(p.read_bytes()) for p in app.rglob('*') if p.is_file()})
        for path,digest in original.items():
            if path!='mygo.json':assert sha((src/path).read_bytes())==digest, 'Build modified source '+path
        item['originalFilesUnchangedExceptConfig']=True
        plan['stages'][stage]=item
        (root/'plan.json').write_text(json.dumps(plan,indent=2)+'\n')
    plan['built']=args.build
    if args.build:assert plan['stages']['A']['binarySHA256']!=plan['stages']['B']['binarySHA256']
    (root/'plan.json').write_text(json.dumps(plan,indent=2)+'\n')
    print(root/'plan.json')

if __name__=='__main__':main()
