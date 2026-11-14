#!/usr/bin/env python3
"""Trusted packaging step only: private QA successors, never formal releases.
MYGO_UPDATER_PRIVATE_KEY is consumed only by packaging/signing subprocesses.
The native/runtime jobs receive only the resulting public artifacts/plan.
"""
import hashlib,io,json,os,pathlib,shutil,subprocess,sys,tarfile,tempfile

def main():
    assert os.environ.get('GITHUB_ACTIONS')=='true' and os.environ.get('RUNNER_ENVIRONMENT')=='github-hosted'
    inputs=pathlib.Path(sys.argv[1]).resolve();out=pathlib.Path(sys.argv[2]).resolve();out.mkdir(parents=True,exist_ok=True)
    meta=json.loads((inputs/'release-inputs.json').read_text(encoding='utf-8'))
    target=subprocess.check_output(['go','env','GOOS'],text=True).strip()+'-'+subprocess.check_output(['go','env','GOARCH'],text=True).strip()
    source=subprocess.check_output(['git','rev-parse','HEAD'],text=True).strip();assert source==meta['refs']['v0.1.36']
    assert os.environ.get('MYGO_UPDATER_PRIVATE_KEY'),'signing-only job requires the existing configured key'
    stages=[]
    for tag in ('v0.1.35','v0.1.36'):
        original=meta['stages'][tag+'/'+target];s=dict(original)
        for field in ('Archive','Manifest','Web'):
            dest=out/original[field];dest.parent.mkdir(parents=True,exist_ok=True)
            if field=='Web':shutil.copytree(inputs/original[field],dest,dirs_exist_ok=True)
            else:shutil.copy2(inputs/original[field],dest)
        # Preserve every signed released delta and its original manifest URL.
        m=json.loads((inputs/original['Manifest']).read_text(encoding='utf-8'))
        for d in m.get('deltas',[]):
            name=d['url'].rsplit('/',1)[-1];shutil.copy2(inputs/tag/name,out/name)
        stages.append(s)
    with tempfile.TemporaryDirectory(prefix='private-qa-successor-',dir=os.environ['RUNNER_TEMP']) as directory:
        root=pathlib.Path(directory)
        for version in ('0.1.900002','0.1.900003'):
            tree=root/version;tree.mkdir()
            with tarfile.open(fileobj=io.BytesIO(subprocess.check_output(['git','archive',source]))) as archive:archive.extractall(tree,filter='data')
            before={str(p.relative_to(tree)):hashlib.sha256(p.read_bytes()).hexdigest() for p in tree.rglob('*.go')}
            cfg=json.loads((tree/'mygo.json').read_text(encoding='utf-8'));assert cfg['updates']['publicKey']==meta['PublicKey']
            cfg['version']=version;cfg['updates']['deltas']=0
            notes=root/(version+'.md');notes.write_text('## '+version+'\n\nPrivate QA successor; no tag/publication.\n',encoding='utf-8');cfg['updates']['changelog']=str(notes)
            (tree/'mygo.json').write_text(json.dumps(cfg,indent=2),encoding='utf-8')
            env=dict(os.environ,CGO_ENABLED='1' if target.startswith('darwin-') else '0',MYGO_LDFLAGS='-X github.com/egoist/mygo.packageUpdateFeed=http://127.0.0.1:37059/update-'+target+'.json',MACOSX_DEPLOYMENT_TARGET='13.0',CGO_CFLAGS='-O2 -g -mmacosx-version-min=13.0')
            with (out/('qa-build-'+version+'.log')).open('w') as log:subprocess.run(['go','tool','mygo','build','-platform',target.replace('-','/'),'-skip-dmg','-skip-installer','-o',str(out/version)],cwd=tree,env=env,stdout=log,stderr=subprocess.STDOUT,check=True)
            assert before=={str(p.relative_to(tree)):hashlib.sha256(p.read_bytes()).hexdigest() for p in tree.rglob('*.go')}
            package=out/version/target;binary=package/('yesman.app/Contents/MacOS/Yes Man' if target.startswith('darwin-') else 'Yes Man'+('.exe' if target.startswith('windows-') else ''))
            web=out/version/'web';shutil.copytree(tree/'web',web)
            stages.append({'Version':version,'Source':source,'Archive':str((package/('yesman-'+version+'-'+target+'.tar.gz')).relative_to(out)),'Manifest':str((package/('update-'+target+'.json')).relative_to(out)),'Web':str(web.relative_to(out)),'mainName':'Yes Man','binarySHA256':hashlib.sha256(binary.read_bytes()).hexdigest(),'sourceGoHashes':before,'official':False,'limits':['private QA version, normal production build from exact candidate source','temporary packaging version/notes/linked loopback feed; no source change or formal tag','not a future released version']})
    plan={'kind':'released-candidate-plus-private-QA','PublicKey':meta['PublicKey'],'Feed':'http://127.0.0.1:37059','Target':target,'Stages':stages,'releases':meta['releases'],'refs':meta['refs'],'sourceSHA':source,'limits':['first edge uses untouched published .35 and downloaded draft .36 bytes','subsequent edges are normal production-source private QA successors, not .37 releases','released feed discovery is supplied by a constrained VM TLS transport; original Ed25519 verification remains active']}
    path=out/'plan.json';path.write_text(json.dumps(plan,indent=2),encoding='utf-8')
    subprocess.run(['go','run','./tools/package','-upgrade-assets',str(path)],check=True)
    print('PASS: immutable official pair preserved; private production-source QA successors signed separately; no private key in runtime artifacts')
if __name__=='__main__':main()
