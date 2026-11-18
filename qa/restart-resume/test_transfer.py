"""Actual binder main + harmless local gh child; no network/credentials/app/signing.
Requires the two already verified R3 ZIPs and REST snapshot as explicit data inputs.
The resulting local receipts are synthetic controls, never hosted acceptance proof.
"""
import contextlib,hashlib,io,json,os,stat,subprocess,tempfile,unittest
from pathlib import Path
from unittest.mock import patch
import bind
import provenance as p

HEAD='8d9acbe666f34d7fa27ec156b72b48f57816949c'
RUN=44380011126
SIGNED=14463076864
NPM=14461884555
ADAPTER='''#!/usr/bin/env python3
import json,os,pathlib,sys,time
root=pathlib.Path(os.environ['T199_TRANSFER_CONTROL_ROOT']);cfg=json.loads((root/'fixture.json').read_text());args=sys.argv[1:]
assert args[:3]==['api','--method','GET']
endpoint=args[3];raw=endpoint.endswith('/zip')
assert args[4:]==(['--allow-escape-sequences']if raw else[])
assert not any(k in os.environ for k in ('GH_TOKEN','GITHUB_TOKEN','MYGO_UPDATER_PRIVATE_KEY'))
path=root/'calls.json';calls=json.loads(path.read_text())if path.exists()else[]
occurrence=1+sum(x['endpoint']==endpoint for x in calls);calls.append({'endpoint':endpoint,'occurrence':occurrence,'args':args});path.write_text(json.dumps(calls))
if endpoint in cfg['metadata']:sys.stdout.write(json.dumps(cfg['metadata'][endpoint]));sys.exit(0)
assert raw and endpoint in cfg['zipFiles'];artifact=int(endpoint.split('/')[-2]);assert not(root/'bound'/('inputs'if artifact==14463076864 else'npm')).exists(),'ZIP consumed before acquisition finished'
scenario=cfg['scenario']if artifact==14463076864 else'success'
if scenario=='timeout':
 sys.stdout.buffer.write(b'innocuous partial ZIP bytes');sys.stdout.buffer.flush();sys.stderr.write('innocuous timeout marker');sys.stderr.flush();time.sleep(2)
if scenario in ('http503_twice','http415','http403','http429','unknown')or scenario in('http503_once','eof_once')and occurrence==1:
 code={'http503_twice':503,'http503_once':503,'http415':415,'http403':403,'http429':429}.get(scenario)
 text='Get https://fixture.invalid/?innocuous-query-sentinel: EOF'if scenario=='eof_once'else'gh: harmless fixture /innocuous/path-sentinel ?innocuous-query-sentinel'+(' (HTTP '+str(code)+')'if code else' unknown')
 sys.stdout.buffer.write(b'innocuous partial response');sys.stderr.write(text+'\\n');sys.exit(1)
raw=pathlib.Path(cfg['zipFiles'][endpoint]).read_bytes()
if scenario=='integrity':raw=b'innocuous changed ZIP bytes'
sys.stdout.buffer.write(raw)
'''

class ProductionTransfer(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.signed=Path(os.environ['T199_R3_SIGNED_ZIP']).resolve();cls.npm=Path(os.environ['T199_R3_NPM_ZIP']).resolve()
        api=json.loads(Path(os.environ['T199_R3_ARTIFACT_API']).read_bytes());run=json.loads(Path(os.environ['T199_R3_RUN_API']).read_bytes())
        cls.artifacts={a['id']:a for a in api['artifacts']}
        for ident,path in[(SIGNED,cls.signed),(NPM,cls.npm)]:
            a=cls.artifacts[ident];raw=path.read_bytes()
            assert a['workflow_run']['id']==RUN and a['workflow_run']['head_sha']==HEAD and a['digest']=='sha256:'+hashlib.sha256(raw).hexdigest()
        cls.run_api=run

    def execute(self,scenario,wrong_action=False):
        with tempfile.TemporaryDirectory()as directory:
            root=Path(directory);(root/'bin').mkdir();(root/'home').mkdir();(root/'tmp').mkdir()
            executable=root/'bin/gh';executable.write_text(ADAPTER);executable.chmod(0o700)
            metadata={f'repos/idy/yesman/actions/artifacts/{k}':self.artifacts[k]for k in(SIGNED,NPM)}
            metadata[f'repos/idy/yesman/actions/runs/{RUN}']=self.run_api
            cfg={'scenario':scenario,'metadata':metadata,'zipFiles':{f'repos/idy/yesman/actions/artifacts/{SIGNED}/zip':str(self.signed),f'repos/idy/yesman/actions/artifacts/{NPM}/zip':str(self.npm)}}
            (root/'fixture.json').write_text(json.dumps(cfg))
            env={'PATH':str(root/'bin')+os.pathsep+os.environ['PATH'],'HOME':str(root/'home'),'TMPDIR':str(root/'tmp'),'RUNNER_TEMP':str(root),
                 'GITHUB_RUN_ATTEMPT':'1','GITHUB_RUN_ID':str(RUN),'GITHUB_SHA':HEAD,'GITHUB_REPOSITORY':p.REPOSITORY,
                 'APPROVED_QA_COMMIT':HEAD,'QA_ARTIFACT_ID':str(SIGNED),'QA_ARTIFACT_DIGEST':('c'*64 if wrong_action else self.artifacts[SIGNED]['digest'][7:]),
                 'NPM_ARTIFACT_ID':str(NPM),'NPM_ARTIFACT_DIGEST':self.artifacts[NPM]['digest'][7:],'T199_TRANSFER_CONTROL_ROOT':str(root)}
            capture=io.StringIO();error=None;run_child=subprocess.run
            def bounded_run(args,**kwargs):
                if scenario=='timeout'and args[4].endswith('/zip'):kwargs['timeout']=0.15
                return run_child(args,**kwargs)
            with patch.dict(os.environ,env,clear=True),patch.object(bind.sys,'argv',['bind.py',str(root/'bound')]),patch.object(bind.subprocess,'run',side_effect=bounded_run),contextlib.redirect_stdout(capture):
                try:bind.main()
                except SystemExit as exc:error=exc
            path=root/'t199-bind-diagnostics.json';raw=path.read_bytes();diagnostic=json.loads(raw);text=capture.getvalue()
            self.assertLessEqual(len(raw),2048);self.assertEqual(stat.S_IMODE(path.stat().st_mode),0o600)
            self.assertNotIn('innocuous-query-sentinel',text);self.assertNotIn('path-sentinel',text);self.assertNotIn('fixture.invalid',text)
            self.assertNotIn(b'innocuous',raw);self.assertIsNone(bind.DIAGNOSTIC)
            calls=json.loads((root/'calls.json').read_text());zip_calls=[x for x in calls if x['endpoint'].endswith('/zip')]
            success=(root/'bound/inputs/contract.json').exists()
            if success:
                self.assertTrue((root/'bound/transfer-files.json').is_file())
                transfer=json.loads((root/'bound/inputs/receipts/transfer.json').read_bytes())
                self.assertEqual(transfer['artifactID'],SIGNED);self.assertEqual(transfer['artifactDigest'],self.artifacts[SIGNED]['digest'])
                self.assertEqual(transfer['uploadActionSHA256'],transfer['zipSHA256']);self.assertEqual(transfer['runAttempt'],1)
                self.assertEqual(transfer['runID'],str(RUN));self.assertEqual(transfer['workflowCommit'],HEAD)
                transfer_files=json.loads((root/'bound/transfer-files.json').read_text())
                self.assertTrue(all('diagnostics'not in x for x in transfer_files))
            else:
                self.assertFalse((root/'bound/transfer-files.json').exists())
                self.assertFalse((root/'bound/inputs').exists())
            print(json.dumps({'scenario':scenario,'wrongAction':wrong_action,'productionMainSucceeded':success,'zipGETs':len(zip_calls),'diagnosticState':diagnostic['state'],'attempts':diagnostic.get('transferAttempts',[]),'diagnosticBytes':len(raw),'syntheticLocalTransport':True}))
            return error,diagnostic,calls,success

    def test_normal_actual_P_S_Q_data_through_production_binder_main(self):
        error,d,calls,success=self.execute('success')
        self.assertIsNone(error);self.assertTrue(success);self.assertEqual(d['state'],'success')
        self.assertEqual(len(d['transferAttempts']),2);self.assertTrue(all(x['httpStatus']is None and x['exitCode']==0 for x in d['transferAttempts']))

    def test_transient_503_then_success_records_first_failure_same_ID(self):
        error,d,calls,success=self.execute('http503_once')
        self.assertIsNone(error);self.assertTrue(success)
        self.assertEqual([(x['artifactID'],x['attempt'],x['httpStatus'])for x in d['transferAttempts']],[(SIGNED,1,503),(SIGNED,2,None),(NPM,1,None)])

    def test_transient_EOF_then_success_records_first_failure(self):
        error,d,calls,success=self.execute('eof_once')
        self.assertIsNone(error);self.assertTrue(success)
        self.assertEqual(d['transferAttempts'][0]['detailCode'],'transport-failed')
        self.assertEqual(d['transferAttempts'][1]['artifactID'],SIGNED)

    def test_permanent_and_unclassified_results_stop_without_extraction(self):
        for scenario,status in[('http415',415),('http403',403),('http429',429),('unknown',None)]:
            with self.subTest(scenario=scenario):
                error,d,calls,success=self.execute(scenario)
                self.assertIsNotNone(error);self.assertFalse(success);self.assertEqual(d['state'],'failed')
                self.assertEqual(len(d['transferAttempts']),1);self.assertEqual(d['httpStatus'],status)

    def test_second_transient_failure_exhausts_budget(self):
        error,d,calls,success=self.execute('http503_twice')
        self.assertIsNotNone(error);self.assertFalse(success);self.assertEqual(d['state'],'failed')
        self.assertEqual([x['attempt']for x in d['transferAttempts']],[1,2]);self.assertEqual(d['httpStatus'],503)

    def test_checksum_mismatch_does_not_retry_or_extract(self):
        error,d,calls,success=self.execute('integrity')
        self.assertIsNotNone(error);self.assertFalse(success);self.assertEqual(d['detailCode'],'integrity-failed')
        self.assertEqual(len(d['transferAttempts']),1)

    def test_upload_API_digest_mismatch_stops_before_ZIP_GET(self):
        error,d,calls,success=self.execute('success',wrong_action=True)
        self.assertIsNotNone(error);self.assertFalse(success)
        self.assertFalse(any(x['endpoint'].endswith('/zip')for x in calls))

    def test_process_timeout_records_partial_counts_and_stops(self):
        error,d,calls,success=self.execute('timeout')
        self.assertIsNotNone(error);self.assertFalse(success);self.assertEqual(d['detailCode'],'command-timeout')
        self.assertEqual(len(d['transferAttempts']),1);self.assertGreater(d['stdoutBytes'],0);self.assertGreater(d['stderrBytes'],0)

if __name__=='__main__':unittest.main()
