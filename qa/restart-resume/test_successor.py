"""Focused benign controls. No sockets, app/pi, npm, compiler or signalling."""
import copy,hashlib,importlib.util,json,os,tempfile,unittest
from pathlib import Path
from unittest.mock import patch
import guards,provenance

class Provider(unittest.TestCase):
    def complete(self):
        x=guards.ProviderLedger()
        for row in [dict(seq=1,kind='provider-request',agent='paused/lead',action='hold'),dict(seq=2,kind='provider-disconnected',agent='paused/lead',action='hold'),dict(seq=3,kind='provider-request',agent='active/lead',action='hold'),dict(seq=4,kind='provider-disconnected',agent='active/lead',action='hold'),dict(seq=5,kind='provider-request',agent='active/lead',action='continue'),dict(seq=6,kind='provider-continued',agent='active/lead')]:x.record(row)
        return x
    def test_complete_and_extra_original_request(self):
        x=self.complete();self.assertEqual(len(x.assert_complete()['requests']),3)
        with self.assertRaisesRegex(ValueError,'additional provider'):x.record(dict(seq=7,kind='provider-request',agent='paused/lead',action='hold'))
    def test_cancellation_required(self):
        x=self.complete();x.terminals.pop(3)
        with self.assertRaisesRegex(ValueError,'streams must cancel'):x.assert_complete()
    def test_birth_change_refuses_signal(self):
        first=dict(pid=1,uid=os.getuid(),executable='/synthetic/main',startSeconds=1,startMicroseconds=0)
        with patch('guards.os.kill')as kill:
            x=guards.Owned(1,'/synthetic/main',observe=lambda _:first)
            x.observe=lambda _:{**first,'startSeconds':2}
            with self.assertRaisesRegex(ValueError,'incarnation changed'):x.terminate()
            kill.assert_not_called()

class Provenance(unittest.TestCase):
    def setUp(self):
        self.tmp=tempfile.TemporaryDirectory();self.addCleanup(self.tmp.cleanup);self.root=Path(self.tmp.name);self.counter=0
        self.env=patch.dict(os.environ,{'GITHUB_SHA':'a'*40,'GITHUB_RUN_ID':'123','GITHUB_RUN_ATTEMPT':'2'});self.env.start();self.addCleanup(self.env.stop)
    def bind(self,value):
        path=self.root/(str(self.counter)+'.json');self.counter+=1;path.write_text(json.dumps(value));return {'path':str(path),'sha256':hashlib.sha256(path.read_bytes()).hexdigest()}
    def fixture(self):
        plan={'sourceSHA':provenance.RELEASE,'Stages':[]};c={'releaseSourceCommit':provenance.RELEASE,'artifacts':{}}
        for key in('S','Q'):
            manifest=self.root/(key+'-manifest');manifest.write_text(key);digest=hashlib.sha256(manifest.read_bytes()).hexdigest();plan['Stages'].append({'Source':provenance.RELEASE,'Manifest':str(manifest)})
            v={'sourceCommit':provenance.RELEASE,'sourceFilesVerified':True,'manifestSHA256':digest}
            if key=='Q':v.update(producerRunID=123,producerRunAttempt=2,producerWorkflowCommit='a'*40,producerWorkflow=provenance.WORKFLOW,producerRepository=provenance.REPOSITORY,transferArtifactID=7,transferArtifactDigest='sha256:'+'b'*64)
            c['artifacts'][key]={**v,'verificationReceipt':self.bind(v)}
        path=self.root/'plan.json';path.write_text(json.dumps(plan));c['packagePlanSHA256']=hashlib.sha256(path.read_bytes()).hexdigest()
        mapping={'passed':True,'candidate':provenance.CANDIDATE,'candidateTree':provenance.TREE,'integration':provenance.INTEGRATION,'integrationTree':provenance.TREE,'releaseSourceCommit':provenance.RELEASE,'releaseTree':provenance.RELEASE_TREE,'releaseParents':[provenance.INTEGRATION],'changedPaths':['CHANGELOG.md','mygo.json'],'nonMetadataEntriesIdentical':True};c['sourceMapping']=self.bind(mapping)
        identity={'repository':provenance.REPOSITORY,'workflow':provenance.WORKFLOW,'workflowCommit':'a'*40,'runID':'123','runAttempt':2}
        transfer={**identity,'artifactID':7,'artifactDigest':'sha256:'+'b'*64,'zipDigestVerified':True,'sourceCommit':provenance.RELEASE};c['transferReceipt']=self.bind(transfer)
        npm={**identity,'integritiesVerified':True,'scriptsExecuted':False,'credentialsUsed':False};c['publicNpmReceipt']=self.bind(npm)
        authority={**identity,'releaseSourceCommit':provenance.RELEASE,'packagePlanSHA256':c['packagePlanSHA256'],'sourceMappingSHA256':c['sourceMapping']['sha256'],'transferReceiptSHA256':c['transferReceipt']['sha256'],'transferArtifactID':7,'transferArtifactDigest':transfer['artifactDigest'],'artifactReceiptsSHA256':{k:c['artifacts'][k]['verificationReceipt']['sha256']for k in('S','Q')},'manifestSHA256':{k:c['artifacts'][k]['manifestSHA256']for k in('S','Q')},'publicNpmReceiptSHA256':c['publicNpmReceipt']['sha256']};        verifier=self.root/'verifier';verifier.write_bytes(b'synthetic keyless verifier');c['publicVerifier']=str(verifier);c['publicVerifierSHA256']=hashlib.sha256(verifier.read_bytes()).hexdigest();authority['publicVerifierSHA256']=c['publicVerifierSHA256']
        c['hostedHandoff']=self.bind(authority)
        return c,plan,path
    def test_expected_selected_pair(self):
        c,p,path=self.fixture();provenance.verify_contract(c,p,path,'123')
    def test_accidental_wrong_source_attempt_manifest(self):
        for variant in('source','run','attempt','manifest'):
            with self.subTest(variant=variant):
                c,p,path=self.fixture();run_id='123'
                if variant=='source':p['Stages'][1]['Source']='c'*40
                elif variant=='run':run_id='124'
                elif variant=='attempt':os.environ['GITHUB_RUN_ATTEMPT']='3'
                else:Path(p['Stages'][0]['Manifest']).write_text('changed')
                with self.assertRaises(ValueError):provenance.verify_contract(c,p,path,run_id)
                os.environ['GITHUB_RUN_ATTEMPT']='2'

class Structure(unittest.TestCase):
    def test_early_helper_and_actual_two_invocations(self):
        root=Path(__file__).parent;source=(root/'runner-attachment-f48/runner_attachment.py').read_text();import ast
        module=ast.parse(source);sequence=next(n for n in module.body if isinstance(n,ast.FunctionDef)and n.name=='sequence');calls=[n.value.func.attr for n in sequence.body if isinstance(n,ast.Expr)and isinstance(n.value,ast.Call)];self.assertEqual(calls[:4],['start','prepare_update','seed','capture_before']);prepare=next(n for n in next(n for n in module.body if isinstance(n,ast.ClassDef)and n.name=='Host').body if isinstance(n,ast.FunctionDef)and n.name=='prepare_update');self.assertIn('self.watch_helper()',ast.unparse(prepare));native=(root/'run_native.py').read_text();self.assertIn("('compat-plan.json','compat.py','compat')",native);self.assertIn("('plan.json','runner-attachment-f48/runner_attachment.py','stateful')",native)
    def test_npm_pinned_selection_and_feed_unchanged(self):
        import public_npm_cache
        root=Path(__file__).resolve().parents[2];lock=(root/'internal/piruntime/assets/package-lock.json').read_bytes();self.assertEqual(len(public_npm_cache.selected(lock)),122);text=(root/'scripts/check-production-upgrade.py').read_text();self.assertIn("allowed={'github.com','api.github.com','objects.githubusercontent.com','release-assets.githubusercontent.com'}",text)
if __name__=='__main__':unittest.main()
