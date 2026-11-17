#!/usr/bin/env python3
"""Build controlled production-source upgrade inputs; never starts an app.

These are NOT released binaries. All tracked source is exported at fixed refs;
only temporary packaging configuration (version/public key/notes) is changed.
The compiled GitHub feed is retained and reached through constrained VM TLS.
The generated private key exists only in packaging subprocess env.
"""
import hashlib
import io
import json
import os
import pathlib
import shutil
import subprocess
import sys
import tarfile
import tempfile

BASE='c1047a2a8e338698e96c31fd5dd41dc569475539'
FEED='http://127.0.0.1:37059'

def main():
    assert os.environ.get('GITHUB_ACTIONS')=='true' and os.environ.get('RUNNER_ENVIRONMENT')=='github-hosted'
    output=pathlib.Path(sys.argv[1]).resolve();output.mkdir(parents=True,exist_ok=True)
    current=subprocess.check_output(['git','rev-parse','HEAD'],text=True).strip()
    goos=subprocess.check_output(['go','env','GOOS'],text=True).strip();arch=subprocess.check_output(['go','env','GOARCH'],text=True).strip();target=goos+'-'+arch
    keys=json.loads(subprocess.check_output(['go','run','./tools/upgrade-key'])) # never printed or persisted
    stages=[]
    with tempfile.TemporaryDirectory(prefix='production-source-upgrade-',dir=os.environ['RUNNER_TEMP']) as directory:
        root=pathlib.Path(directory)
        for i,(ref,version) in enumerate([(BASE,'0.1.29'),(current,'0.1.900001'),(current,'0.1.900002'),(current,'0.1.900003')]):
            source=root/('source-'+str(i));source.mkdir()
            archive=subprocess.check_output(['git','archive','--format=tar',ref])
            with tarfile.open(fileobj=io.BytesIO(archive)) as tar:tar.extractall(source,filter='data')
            before={str(p.relative_to(source)):hashlib.sha256(p.read_bytes()).hexdigest() for p in source.rglob('*.go')}
            cfg=json.loads((source/'mygo.json').read_text(encoding='utf-8'));original=json.loads(json.dumps(cfg))
            cfg['version']=version
            cfg['updates']['publicKey']=keys['public'];cfg['updates']['deltas']=0
            notes=root/('notes-'+str(i)+'.md');notes.write_text('## '+version+'\n\nPrivate production-source upgrade compatibility input. Never publish.\n')
            cfg['updates']['changelog']=str(notes)
            (source/'mygo.json').write_text(json.dumps(cfg,indent=2)+'\n',encoding='utf-8')
            dest=output/version
            env=dict(os.environ)
            env['CGO_ENABLED']='0' if i==0 or goos!='darwin' else '1'
            if goos=='darwin':env.update(MACOSX_DEPLOYMENT_TARGET='13.0',CGO_CFLAGS='-O2 -g -mmacosx-version-min=13.0')
            # Keep the normal compiled GitHub feed. The isolated VM transport
            # supplies controlled signed inputs; no runtime/binary feed patch.
            env.pop('MYGO_LDFLAGS',None)
            env['MYGO_UPDATER_PRIVATE_KEY']=keys['private']
            with (output/('build-'+version+'.log')).open('w') as log:
                subprocess.run(['go','tool','mygo','build','-platform',goos+'/'+arch,'-skip-dmg','-skip-installer','-o',str(dest)],cwd=source,env=env,stdout=log,stderr=subprocess.STDOUT,check=True)
            assert before=={str(p.relative_to(source)):hashlib.sha256(p.read_bytes()).hexdigest() for p in source.rglob('*.go')},'Packaging changed Go source'
            package=dest/target
            binary=package/('yesman.app/Contents/MacOS/'+('yesman' if i==0 else 'Yes Man') if goos=='darwin' else ('yesman' if i==0 else 'Yes Man')+('.exe' if goos=='windows' else ''))
            web=dest/'web';shutil.copytree(source/'web',web)
            manifest=package/('update-'+target+'.json')
            artifact=package/('yesman-'+version+'-'+target+'.tar.gz')
            stages.append({'Version':version,'Source':ref,'Archive':str(artifact),'Manifest':str(manifest),'Web':str(web),'binarySHA256':hashlib.sha256(binary.read_bytes()).hexdigest(),'mainName':'yesman' if i==0 else 'Yes Man','goBuild':subprocess.check_output(['go','version','-m',str(binary)],text=True),'sourceGoHashes':before,'packagingChanges':{'originalConfig':original,'fixtureConfig':cfg,'compiledFeed':'original GitHub feed; constrained VM transport','cgo':env['CGO_ENABLED']}})
        plan={'kind':'controlled-production-source-rebuild','PublicKey':keys['public'],'Feed':FEED,'Target':target,'Stages':stages,'useTLS':True,'runID':os.environ['GITHUB_RUN_ID'],'sourceSHA':os.environ['GITHUB_SHA'],'limits':['Not the published .29 binary; no claimed released-artifact E2E','No .go source changes; fixture config/public key/versions/notes differ; original compiled GitHub feed retained','Export has no Git metadata: source refs and per-Go-file hashes bind provenance','Private versions are local fixtures; no tag/release/upload']}
        path=output/'plan.json';path.write_text(json.dumps(plan,indent=2))
        signenv=dict(os.environ,MYGO_UPDATER_PRIVATE_KEY=keys['private'])
        subprocess.run(['go','run','./tools/package','-upgrade-assets',str(path)],env=signenv,check=True)
    del keys
    print('PASS: four normal production-source inputs built/signed; private key excluded from plan and runtime environment')

if __name__=='__main__':main()
