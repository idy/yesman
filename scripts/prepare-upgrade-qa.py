#!/usr/bin/env python3
"""Trusted packaging step only: private QA successors, never formal releases.
Run from the workflow's own checkout with the candidate checkout as cwd.
MYGO_UPDATER_PRIVATE_KEY leaves the environment before the first subprocess.
Only the two signers get it: each MyGo build through a short-lived private
key file, and the precompiled upgrade-assets helper's own execution. go env,
git, Go/cgo compilers and the helper's compilation never see it.
The native/runtime jobs receive only the resulting public artifacts/plan.
"""
import base64,contextlib,hashlib,io,json,os,pathlib,shutil,signal,subprocess,sys,tarfile,tempfile

KEY='MYGO_UPDATER_PRIVATE_KEY'
PRODUCER=pathlib.Path(__file__).resolve().parents[1]

def stopped(signum,frame):raise SystemExit(128+signum)

def check_key(key,public):
    # A Go Ed25519 private key is seed||public; MyGo and the helper also check it.
    raw=base64.b64decode(key.strip(),validate=True)
    assert len(raw)==64 and raw[32:]==base64.b64decode(public.strip(),validate=True),'updater key does not match the released public key'

@contextlib.contextmanager
def key_file(key):
    """The key exists on disk only while one MyGo build signs, outside tree/output."""
    directory=tempfile.mkdtemp(prefix='qa-updater-key-',dir=os.environ['RUNNER_TEMP']);path=os.path.join(directory,'mygo-update.key')
    try:
        fd=os.open(path,os.O_WRONLY|os.O_CREAT|os.O_EXCL|getattr(os,'O_NOFOLLOW',0),0o600)
        with os.fdopen(fd,'w',encoding='ascii') as f:f.write(key.strip()+'\n')
        yield path
    finally:
        with contextlib.suppress(FileNotFoundError):os.unlink(path)
        os.rmdir(directory)

def main():
    # First, before any subprocess, assertion or exception path: remove the key.
    key=os.environ.pop(KEY,'')
    signal.signal(signal.SIGTERM,stopped)
    assert key.strip(),'signing-only job requires the existing configured key'
    assert not any(key.strip() in v for v in os.environ.values()),'updater key duplicated in another variable'
    assert os.environ.get('GITHUB_ACTIONS')=='true' and os.environ.get('RUNNER_ENVIRONMENT')=='github-hosted'
    inputs=pathlib.Path(sys.argv[1]).resolve();out=pathlib.Path(sys.argv[2]).resolve();out.mkdir(parents=True,exist_ok=True)
    meta=json.loads((inputs/'release-inputs.json').read_text(encoding='utf-8'));check_key(key,meta['PublicKey'])
    target=subprocess.check_output(['go','env','GOOS'],text=True).strip()+'-'+subprocess.check_output(['go','env','GOARCH'],text=True).strip()
    source=subprocess.check_output(['git','rev-parse','HEAD'],text=True).strip();assert source==meta['refs']['v0.1.39']
    # The helper is compiled by this producer; it must be the candidate's helper.
    helper_source=subprocess.check_output(['git','show',source+':tools/package/upgrade_assets.go.txt'])
    assert helper_source==(PRODUCER/'tools/package/upgrade_assets.go.txt').read_bytes(),'producer and candidate upgrade-assets helpers differ'
    stages=[]
    for tag in ('v0.1.38','v0.1.39'):
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
            env=dict(os.environ,CGO_ENABLED='1' if target.startswith('darwin-') else '0',MYGO_LDFLAGS='-X github.com/egoist/mygo.packageUpdateFeed=http://127.0.0.1:37059/update-'+target+'.json',MACOSX_DEPLOYMENT_TARGET='13.0',CGO_CFLAGS='-O2 -g -mmacosx-version-min=13.0')
            assert KEY not in env
            with key_file(key) as signer:
                cfg['updates']['privateKey']=signer  # read only by MyGo's signer, never by compilers
                (tree/'mygo.json').write_text(json.dumps(cfg,indent=2),encoding='utf-8')
                with (out/('qa-build-'+version+'.log')).open('w') as log:subprocess.run(['go','tool','mygo','build','-platform',target.replace('-','/'),'-skip-dmg','-skip-installer','-o',str(out/version)],cwd=tree,env=env,stdout=log,stderr=subprocess.STDOUT,check=True)
            assert before=={str(p.relative_to(tree)):hashlib.sha256(p.read_bytes()).hexdigest() for p in tree.rglob('*.go')}
            package=out/version/target;binary=package/('yesman.app/Contents/MacOS/Yes Man' if target.startswith('darwin-') else 'Yes Man'+('.exe' if target.startswith('windows-') else ''))
            # MyGo skips signing without a key; never continue with an unsigned successor.
            archive=package/('yesman-'+version+'-'+target+'.tar.gz');manifest=json.loads((package/('update-'+target+'.json')).read_text(encoding='utf-8'))
            assert manifest['version']==version and manifest['size']==archive.stat().st_size and manifest['signature'],'unsigned QA successor'
            web=out/version/'web';shutil.copytree(tree/'web',web)
            stages.append({'Version':version,'Source':source,'Archive':str((package/('yesman-'+version+'-'+target+'.tar.gz')).relative_to(out)),'Manifest':str((package/('update-'+target+'.json')).relative_to(out)),'Web':str(web.relative_to(out)),'mainName':'Yes Man','binarySHA256':hashlib.sha256(binary.read_bytes()).hexdigest(),'sourceGoHashes':before,'official':False,'limits':['private QA version, normal production build from exact candidate source','temporary packaging version/notes/linked loopback feed; no source change or formal tag','not a future released version']})
    plan={'kind':'released-candidate-plus-private-QA','PublicKey':meta['PublicKey'],'Feed':'http://127.0.0.1:37059','Target':target,'Stages':stages,'releases':meta['releases'],'refs':meta['refs'],'sourceSHA':source,'limits':['first edge uses untouched published .38 and downloaded draft .39 bytes','subsequent edges are normal production-source private QA successors, not .40 releases','released feed discovery is supplied by a constrained VM TLS transport; original Ed25519 verification remains active']}
    path=out/'plan.json';path.write_text(json.dumps(plan,indent=2),encoding='utf-8')
    helper_dir=tempfile.mkdtemp(prefix='qa-upgrade-assets-',dir=os.environ['RUNNER_TEMP'])
    try:
        helper=os.path.join(helper_dir,'yesman-upgrade-assets'+('.exe' if target.startswith('windows-') else ''))
        assert KEY not in os.environ
        subprocess.run(['go','run','./tools/package','-build-upgrade-assets',helper],cwd=PRODUCER,check=True)
        subprocess.run([helper,str(path)],env=dict(os.environ,**{KEY:key.strip()}),check=True)
        subprocess.run([helper,str(path),'verify'],env=dict(os.environ),check=True)
    finally:shutil.rmtree(helper_dir)
    print('PASS: immutable official pair preserved; private production-source QA successors signed separately; no private key in runtime artifacts')
if __name__=='__main__':main()
