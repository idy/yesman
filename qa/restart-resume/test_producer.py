"""Exact two-plan producer composition with benign in-memory build substitutes."""
import base64,hashlib,importlib.util,io,json,os,sys,tarfile,tempfile,unittest
from pathlib import Path
from unittest.mock import patch
ROOT=Path(__file__).resolve().parents[2]
spec=importlib.util.spec_from_file_location('producer',ROOT/'scripts/prepare-upgrade-qa.py');q=importlib.util.module_from_spec(spec);spec.loader.exec_module(q)
from provenance import CANDIDATE,TREE,INTEGRATION,RELEASE
class Composition(unittest.TestCase):
    def test_one_private_Q_two_exact_plans_and_keyless_verifier(self):
        with tempfile.TemporaryDirectory()as directory:
            root=Path(directory);inputs=root/'inputs';out=root/'out';inputs.mkdir();out.mkdir();public=base64.b64encode(bytes(32)).decode();key=base64.b64encode(bytes(64)).decode();files={'mygo.json':json.dumps({'version':'0.1.49','updates':{'publicKey':public,'deltas':3}}).encode(),'main.go':b'package main\n','web/index.html':b'synthetic'}
            buf=io.BytesIO()
            with tarfile.open(fileobj=buf,mode='w')as t:
                for name,raw in files.items():m=tarfile.TarInfo(name);m.size=len(raw);t.addfile(m,io.BytesIO(raw))
            meta={'refs':{'v0.1.48':'b'*40,'v0.1.49':RELEASE},'PublicKey':public,'releases':[],'stages':{},'restartAcceptance':{'candidate':CANDIDATE,'tree':TREE,'productIntegration':INTEGRATION},'sourceFileHashes':{n:hashlib.sha256(b).hexdigest()for n,b in files.items()}}
            for tag in('v0.1.48','v0.1.49'):
                d=inputs/tag;d.mkdir();(d/'web').mkdir();(d/'web/index.html').write_bytes(b'synthetic');(d/'archive.tar.gz').write_bytes(tag.encode());(d/'update.json').write_text(json.dumps({'version':tag[1:],'deltas':[]}));meta['stages'][tag+'/darwin-arm64']={'Version':tag[1:],'Source':meta['refs'][tag],'Archive':tag+'/archive.tar.gz','Manifest':tag+'/update.json','Web':tag+'/web','official':True}
            (inputs/'release-inputs.json').write_text(json.dumps(meta));(inputs/'source-mapping.json').write_text('{}');calls=[]
            class Group:
                ident='synthetic'
                retained=[]
                def settle(self,aborting):pass
                def run(owner,args,**kw):
                    args=list(map(str,args));env=kw.get('env',os.environ);calls.append((args,'MYGO_UPDATER_PRIVATE_KEY'in env))
                    if args[:2]==['go','env']:return {'GOOS':b'darwin','GOARCH':b'arm64'}[args[2]]
                    if args[:2]==['git','rev-parse']:return RELEASE.encode()
                    if args[:2]==['git','show']:return INTEGRATION.encode()
                    if args[:2]==['git','diff']:return b'CHANGELOG.md\nmygo.json\n'
                    if args[:2]==['git','archive']:return buf.getvalue()
                    if args[:4]==['go','tool','mygo','build']:
                        cfg=json.loads((Path(kw['cwd'])/'mygo.json').read_bytes());v=cfg['version'];package=Path(args[args.index('-o')+1])/'darwin-arm64';(package/'yesman.app/Contents/MacOS').mkdir(parents=True);(package/'yesman.app/Contents/MacOS/Yes Man').write_bytes(b'private Q');(package/('yesman-'+v+'-darwin-arm64.tar.gz')).write_bytes(b'archive');(package/'update-darwin-arm64.json').write_text(json.dumps({'version':v,'size':7,'signature':'synthetic','deltas':[]}));return
                    if args[:4]==['go','run','./tools/package','-build-upgrade-assets']:Path(args[4]).write_bytes(b'public-key helper');return
                    if len(args)in(2,3):return
                    raise AssertionError(args)
            env={'RUNNER_TEMP':str(root),'GITHUB_RUN_ID':'123','GITHUB_RUN_ATTEMPT':'1'}
            with patch.dict(os.environ,env),patch.object(q,'helper_blobs',return_value=('a'*40,'a'*40)):
                q._successors(Group(),key,inputs,out,meta)
            plan=json.loads((out/'plan.json').read_bytes());compat=json.loads((out/'compat-plan.json').read_bytes());self.assertEqual([s['Version']for s in plan['Stages']],['0.1.49','0.1.900001']);self.assertEqual([s['Version']for s in compat['Stages']],['0.1.48','0.1.49']);self.assertEqual((out/'v0.1.49/update.json').read_bytes(),(inputs/'v0.1.49/update.json').read_bytes());self.assertEqual((out/'public-upgrade-verifier').read_bytes(),b'public-key helper');self.assertEqual(sum(1 for a,keyed in calls if a[:4]==['go','tool','mygo','build']),1);self.assertTrue(all(not keyed for a,keyed in calls if a[0]in('go','git')))
if __name__=='__main__':unittest.main()
