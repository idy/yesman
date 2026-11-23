"""Offline checks for the exact workflow download script and fixed-source admission."""
import contextlib
import copy
from hashlib import sha256
import io
import json
import os
from pathlib import Path
import subprocess
import sys
import unittest
from unittest.mock import patch

from binding import BASE, HISTORICAL, HOLD_IDS, HOLD_QA, HOLD_TAG, QA_REF, TAG, check_contract, check_inputs

ROOT = Path(__file__).resolve().parents[2]
SCRIPT = ROOT/'.github/workflows/macos-draft-smoke.yml'
CODE = SCRIPT.read_text().split("          python3 - <<'PY'\n", 1)[1].split('\n          PY', 1)[0]
CODE = '\n'.join(line[10:] for line in CODE.splitlines())


def contract():
    return {'schema':4,'repository':'idy/yesman','tag':TAG,'arch':'arm64','mode':'candidate','qaRef':QA_REF,
            'base':BASE,'history':HISTORICAL,'priorPackageAcceptanceReused':False,
            'releaseID':25,'assetID':26,'manifestID':27,'archiveBytes':10,'manifestBytes':13,
            'source':'1'*40,'tree':'2'*40,'bulkMerge':'3'*40,'bulkTree':'4'*40,
            **{k:'a'*64 for k in ('manifestSHA256','archiveSHA256','binarySHA256','assetSetSHA256','bulkAdoptionSHA256','releaseReviewSHA256')}}


class Offline(unittest.TestCase):
    def test_scope_inputs(self):
        for episode in ('cleanup-only','deletion-only'):
            check_inputs(episode,QA_REF,'1','5'*40)
        for args in [('ordinary',QA_REF,'1','5'*40),('cleanup-only','refs/heads/main','1','5'*40),
                     ('cleanup-only',QA_REF,'2','5'*40),('cleanup-only',QA_REF,'1',next(iter(HOLD_QA)))]:
            with self.subTest(args=args),self.assertRaises(AssertionError):
                check_inputs(*args)

    def test_contract_rejects_historic_or_unbound_operands(self):
        check_contract(contract())
        for key,value in [('tag','v0.1.64'),('releaseID',455729493),('assetID',783812678),('manifestID',783812889),
                          ('source',HOLD_TAG),('source',BASE),('schema',3),('priorPackageAcceptanceReused',True),
                          ('history',{'failedBulk':[]}),('bulkAdoptionSHA256',None),('archiveBytes',True)]:
            with self.subTest(key=key),self.assertRaises(AssertionError):
                check_contract({**contract(),key:value})

    def run_download(self, root, episode, **change):
        c=contract()
        q='5'*40
        asset_data={'yesman-0.1.65-darwin-arm64.tar.gz':b'test archive','update-darwin-arm64.json':b'test manifest'}
        rows=[{'id':c[k],'node_id':f'test-{i}','name':name,'url':f'https://api.github.com/repos/idy/yesman/releases/assets/{c[k]}',
               'size':len(data),'state':'uploaded','digest':'sha256:'+sha256(data).hexdigest()}
              for i,((name,data),k) in enumerate(zip(asset_data.items(),('assetID','manifestID')))]
        for row,key in zip(rows,('archive','manifest')):
            c[key+'Bytes']=row['size']
            c[key+'SHA256']=row['digest'][7:]
        c['assetSetSHA256']=sha256(json.dumps(sorted(rows,key=lambda r:r['name']),sort_keys=True,separators=(',',':')).encode()).hexdigest()
        c.update(change.pop('contract',{}))
        data=json.dumps(c).encode()
        env={'GITHUB_REPOSITORY':'idy/yesman','GITHUB_REF':QA_REF,'GITHUB_RUN_ATTEMPT':'1',
             'GITHUB_SHA':q,'EXPECTED_TAG':TAG,'EPISODE':episode,'CONTRACT_SHA256':sha256(data).hexdigest(),
             'RUNNER_TEMP':str(root),'GITHUB_OUTPUT':str(root/'output'),'GITHUB_RUN_ID':'99'}
        env.update(change.pop('env',{}))
        draft={'id':c['releaseID'],'draft':True,'prerelease':False,'tag_name':TAG,'published_at':None,'assets':rows}
        draft.update(change.pop('draft',{}))
        parents=change.pop('parents',[{'sha':c['source']}])
        assert not change
        called=[]
        def fake(argv, **kwargs):
            assert argv[:4]==['gh','api','--method','GET']
            leaf=argv[4].removeprefix('repos/idy/yesman/')
            called.append(leaf)
            replies={f'git/commits/{q}':{'sha':q,'parents':parents},'git/ref/tags/'+TAG:{'object':{'type':'commit','sha':c['source']}},
                      f'git/commits/{c["source"]}':{'tree':{'sha':c['tree']}},f'releases/{c["releaseID"]}':draft}
            if leaf.startswith('contents/'):
                return data
            if leaf.startswith('releases/assets/'):
                name=next(r['name'] for r in rows if leaf.endswith('/'+str(r['id'])))
                return asset_data[name]
            return json.dumps(replies[leaf]).encode()
        with patch.dict(os.environ,env,clear=True),patch.object(subprocess,'check_output',fake):
            try:
                exec(compile(CODE,str(SCRIPT),'exec'),{})
            except AssertionError:
                return False,called
        return True,called

    def test_privileged_download_admission_with_synthetic_replies(self):
        scratch=Path(sys.argv[1]).resolve()
        cases=[('cleanup-only',{},True,8),('deletion-only',{},True,8),
               ('cleanup-only',{'env':{'EXPECTED_TAG':'v0.1.64'}},False,0),
               ('cleanup-only',{'env':{'CONTRACT_SHA256':'a'*63}},False,0),
               ('cleanup-only',{'env':{'CONTRACT_SHA256':'a'*64}},False,1),
               ('cleanup-only',{'contract':{'releaseID':455729493}},False,1),
               ('cleanup-only',{'parents':[{'sha':'1'*40},{'sha':BASE}]},False,2),
               ('cleanup-only',{'draft':{'draft':False}},False,5),
               ('cleanup-only',{'draft':{'assets':[]}},False,5)]
        for n,(episode,updates,expected,count) in enumerate(cases):
            root=scratch/f'case-{n}'
            root.mkdir()
            with self.subTest(case=n):
                ok,calls=self.run_download(root,episode,**updates)
                self.assertEqual((ok,len(calls)),(expected,count))
                if ok:
                    receipt=json.loads((root/'input/receipt.json').read_bytes())
                    self.assertEqual((receipt['episode'],receipt['runAttempt'],receipt['runID']),(episode,'1','99'))
                else:
                    self.assertFalse(any(p.startswith('releases/assets/') for p in calls))


if __name__=='__main__':
    unittest.main(argv=[sys.argv[0]],verbosity=2)
