"""Bounded normal acquisition/diagnostic controls; no native or credential use."""
import contextlib
import hashlib
import io
import json
import os
from pathlib import Path
import shutil
import subprocess
import tarfile
import tempfile
import unittest
from unittest.mock import patch
from urllib.error import HTTPError

import acquisition_diagnostics as ad
import download
import public_npm_cache as npm
import provenance as p

ROOT=Path(__file__).resolve().parents[2]

class AcquisitionContracts(unittest.TestCase):
    def environment(self,root):
        home=root/'home';home.mkdir();tmp=root/'tmp';tmp.mkdir()
        return {'PATH':os.environ['PATH'],'HOME':str(home),'TMPDIR':str(tmp),'RUNNER_TEMP':str(root),
                'GITHUB_RUN_ATTEMPT':'1','GITHUB_REPOSITORY':'idy/yesman',
                'GITHUB_ACTIONS':'true','RUNNER_ENVIRONMENT':'github-hosted'}

    def test_normal_npm_offline_empty_project_config_old_and_repair(self):
        command=shutil.which('npm');self.assertTrue(command)
        with tempfile.TemporaryDirectory()as directory:
            root=Path(directory);env=self.environment(root);results=[]
            for label in ('old','repaired'):
                work=root/label;work.mkdir()
                (work/'package.json').write_text('{"name":"t199-normal-config","version":"1.0.0","private":true}')
                (work/'package-lock.json').write_text('{"name":"t199-normal-config","version":"1.0.0","lockfileVersion":3,"packages":{"":{"name":"t199-normal-config","version":"1.0.0"}}}')
                with patch.dict(os.environ,env,clear=True):
                    child=npm.acquisition_environment(work) if label=='repaired' else {**env,'NPM_CONFIG_USERCONFIG':os.devnull,'NPM_CONFIG_GLOBALCONFIG':os.devnull}
                if label=='repaired':
                    self.assertNotEqual(child['NPM_CONFIG_USERCONFIG'],child['NPM_CONFIG_GLOBALCONFIG'])
                    self.assertEqual(Path(child['NPM_CONFIG_USERCONFIG']).read_bytes(),b'')
                    self.assertEqual(Path(child['NPM_CONFIG_GLOBALCONFIG']).read_bytes(),b'')
                    self.assertEqual(child['NPM_CONFIG_REGISTRY'],'https://registry.npmjs.org/')
                result=subprocess.run([command,'ci','--ignore-scripts','--no-audit','--no-fund','--offline','--cache',str(work/'cache')],cwd=work,env=child,capture_output=True,timeout=15)
                results.append(result)
            self.assertNotEqual(results[0].returncode,0)
            self.assertIn(b'double-loading config',results[0].stderr)
            self.assertEqual(results[1].returncode,0,results[1].stderr.decode())
            version=subprocess.check_output([command,'--version'],cwd=root/'repaired',env=child,timeout=10).decode().strip()
            self.assertEqual(version,'11.19.1','exact hosted npm version required for this control')
            print('Exact npm11.19.1, offline empty package: identical /dev/null config FAIL; actual repaired distinct-empty configuration PASS')

    def test_download_command_failure_retains_only_sanitized_status(self):
        with tempfile.TemporaryDirectory()as directory:
            root=Path(directory);env=self.environment(root);out=root/'release-inputs';capture=io.StringIO()
            result=subprocess.CompletedProcess(['gh'],1,b'normal innocuous body',b'HTTP 415; Authorization: Bearer SYNTHETIC-INNOCUOUS-NOT-A-KEY; https://fixture.invalid/data?innocuous-hidden-value; /fixture/arbitrary-file')
            def child(args,**kwargs):
                return subprocess.CompletedProcess(args,0,b'gh version 2.103.0\n',b'')if args==['gh','--version']else result
            with patch.dict(os.environ,env,clear=True),patch.object(download.sys,'argv',['download.py',str(out)]),patch.object(download.subprocess,'run',side_effect=child),contextlib.redirect_stdout(capture):
                with self.assertRaises(SystemExit):download.main()
            raw=(root/'t199-download-diagnostics.json').read_bytes();row=json.loads(raw)
            self.assertEqual((row['state'],row['phase'],row['exitCode'],row['httpStatus']),('failed','metadata',1,415))
            self.assertLess(len(raw),2048);self.assertNotIn(b'innocuous-hidden-value',raw);self.assertNotIn('fixture.invalid',capture.getvalue())
            self.assertNotIn(b'arbitrary-file',raw);self.assertNotIn(b'SYNTHETIC-INNOCUOUS',raw)
            self.assertNotIn('SYNTHETIC-INNOCUOUS',capture.getvalue());self.assertNotIn('?',row['resource'])
            self.assertFalse((out/'release-inputs.json').exists())
            self.assertIsNone(download.DIAGNOSTIC)

    def test_npm_command_failure_has_diagnostics_without_success_inputs(self):
        with tempfile.TemporaryDirectory()as directory:
            root=Path(directory);env=self.environment(root);out=root/'public-npm';capture=io.StringIO();commands=[]
            def run(args,**kwargs):
                commands.append((args,kwargs['env']))
                if args==['npm','--version']:return subprocess.CompletedProcess(args,0,b'11.19.1\n',b'')
                return subprocess.CompletedProcess(args,7,b'normal innocuous output',b'npm error code ECONNRESET\nAuthorization: Bearer SYNTHETIC-INNOCUOUS-NOT-A-KEY\nhttps://fixture.invalid/?innocuous-hidden-value /fixture/arbitrary-file')
            with patch.dict(os.environ,env,clear=True),patch.object(npm.subprocess,'run',side_effect=run),contextlib.redirect_stdout(capture):
                with self.assertRaises(SystemExit):npm.acquire(ROOT,out)
            raw=(root/'t199-public-npm-diagnostics.json').read_bytes();row=json.loads(raw)
            self.assertEqual((row['state'],row['phase'],row['exitCode'],row['detailCode']),('failed','npm-ci',7,'npm-network'))
            self.assertNotIn(b'innocuous-hidden-value',raw);self.assertNotIn(b'arbitrary-file',raw)
            self.assertNotIn('fixture.invalid',capture.getvalue());self.assertNotIn(b'SYNTHETIC-INNOCUOUS',raw)
            self.assertNotIn('SYNTHETIC-INNOCUOUS',capture.getvalue());self.assertFalse(out.exists())
            self.assertEqual(len(commands),2);self.assertEqual(row['toolVersions']['npm'],'11.19.1')
            args,child=commands[-1];self.assertIn('--ignore-scripts',args);self.assertIn('--no-audit',args);self.assertIn('--no-fund',args)
            self.assertNotEqual(child['NPM_CONFIG_USERCONFIG'],child['NPM_CONFIG_GLOBALCONFIG'])
            self.assertTrue(all(k not in child for k in ('MYGO_UPDATER_PRIVATE_KEY','GH_TOKEN','GITHUB_TOKEN')))
            self.assertEqual(len(npm.selected((ROOT/'internal/piruntime/assets/package-lock.json').read_bytes())),122)

    def test_source_http_failure_status_never_retains_redirect_or_body(self):
        with tempfile.TemporaryDirectory()as directory:
            root=Path(directory);env=self.environment(root);capture=io.StringIO()
            with patch.dict(os.environ,env,clear=True),contextlib.redirect_stdout(capture):
                with self.assertRaises(HTTPError):
                    with ad.Diagnostic('download')as diagnostic:
                        diagnostic.phase('source-P')
                        raise HTTPError('https://fixture.invalid/?innocuous-hidden-value',415,'normal arbitrary response text',{},None)
            raw=(root/'t199-download-diagnostics.json').read_bytes();row=json.loads(raw)
            self.assertEqual((row['state'],row['phase'],row['httpStatus'],row['detailCode']),('failed','source-P',415,'http-failed'))
            self.assertNotIn(b'innocuous-hidden-value',raw);self.assertNotIn('arbitrary response',capture.getvalue())

    def test_fixed_public_source_request_is_keyless_and_nonredirecting(self):
        expected='https://codeload.github.com/idy/yesman/legacy.tar.gz/'+p.RELEASE
        class Response:
            status=200
            def __enter__(self):return self
            def __exit__(self,*args):pass
            def geturl(self):return expected
            def read(self,budget):return b'\x1f\x8bnormal inert source bytes'
        class Opener:
            def open(owner,request,timeout):
                self.assertEqual(request.full_url,expected)
                self.assertIsNone(request.get_header('Authorization'))
                self.assertNotIn('?',request.full_url);self.assertEqual(timeout,60)
                return Response()
        def build(*handlers):
            self.assertEqual(handlers[0].proxies,{})
            self.assertIsInstance(handlers[1],download.NoRedirect)
            self.assertIsNone(handlers[1].redirect_request(None,None,None,None,None,None))
            return Opener()
        with patch.object(download.urllib.request,'build_opener',side_effect=build):
            self.assertTrue(download.public_source(p.RELEASE).startswith(b'\x1f\x8b'))

    def test_normal_source_archive_matches_every_git_blob_and_source_proof(self):
        files={'mygo.json':json.dumps({'version':'0.1.49','name':'Yes Man','identifier':'dev.idy.yesman'}).encode(),'web/index.html':b'<p>normal public fixture</p>\n'}
        def archive_contents(selected):
            stream=io.BytesIO()
            with tarfile.open(fileobj=stream,mode='w:gz')as archive:
                for name,content in selected.items():
                    item=tarfile.TarInfo('idy-yesman-fixed/'+name);item.size=len(content);item.mode=0o644;archive.addfile(item,io.BytesIO(content))
            return stream.getvalue()
        entries={name:('100644','blob',hashlib.sha1(b'blob '+str(len(content)).encode()+b'\0'+content).hexdigest())for name,content in files.items()}
        raw=archive_contents(files)
        cfg,hashes,web,proof=download.source_contents(raw,p.RELEASE,entries)
        self.assertEqual(cfg['version'],'0.1.49');self.assertEqual(web,{'web/index.html':files['web/index.html']})
        self.assertEqual(hashes,{name:hashlib.sha256(raw).hexdigest()for name,raw in files.items()})
        self.assertEqual(proof['sourceCommit'],p.RELEASE);self.assertEqual(proof['gitTree'],download.SOURCE_TREES[p.RELEASE])
        self.assertTrue(proof['gitBlobObjectsVerified']);self.assertEqual(proof['regularFiles'],2)
        self.assertEqual({name:row['gitBlob']for name,row in proof['fileObjects'].items()},{name:row[2]for name,row in entries.items()})
        with self.assertRaisesRegex(ValueError,'coverage differs'):
            download.source_contents(archive_contents({'mygo.json':files['mygo.json']}),p.RELEASE,entries)
        with self.assertRaisesRegex(ValueError,'pinned Git blob'):
            download.source_contents(archive_contents({**files,'web/index.html':b'<p>different innocuous fixture</p>'}),p.RELEASE,entries)

    def test_normal_both_release_sources_finish_with_bound_proofs(self):
        def objects(files):
            return {name:('100644','blob',hashlib.sha1(b'blob '+str(len(raw)).encode()+b'\0'+raw).hexdigest())for name,raw in files.items()}
        def archive(files,prefix):
            out=io.BytesIO()
            with tarfile.open(fileobj=out,mode='w:gz')as t:
                for name,raw in files.items():
                    member=tarfile.TarInfo(prefix+'/'+name);member.size=len(raw);member.mode=0o644;t.addfile(member,io.BytesIO(raw))
            return out.getvalue()
        sources={};tree_rows={};releases=[];assets={};bodies={}
        for index,(commit,version,draft)in enumerate([(p.PREDECESSOR,'0.1.48',False),(p.RELEASE,'0.1.49',True)],1):
            cfg={'version':version,'name':'Yes Man','identifier':'dev.idy.yesman','updates':{'publicKey':'normal-public-fixture'}}
            files={'mygo.json':json.dumps(cfg).encode(),'CHANGELOG.md':version.encode(),'web/index.html':b'<p>normal public fixture</p>'}
            sources[commit]=archive(files,'idy-yesman-normal');entries=objects(files);tree_rows[download.SOURCE_TREES[commit]]=entries
            package_name='yesman-'+version+'-darwin-arm64.tar.gz';package=archive({'Contents/MacOS/Yes Man':b'normal inert binary'},'yesman.app')
            manifest=json.dumps({'version':version,'url':'https://github.com/idy/yesman/releases/download/v'+version+'/'+package_name,'deltas':[]}).encode()
            rows=[]
            for offset,(name,raw)in enumerate([('update-darwin-arm64.json',manifest),(package_name,package)]):
                ident=index*10+offset;rows.append({'id':ident,'name':name,'state':'uploaded','size':len(raw),'digest':'sha256:'+hashlib.sha256(raw).hexdigest()});bodies[ident]=raw
            releases.append({'id':index,'tag_name':'v'+version,'draft':draft,'prerelease':False});assets[index]=rows
        tree_rows[p.TREE]=tree_rows[download.SOURCE_TREES[p.PREDECESSOR]]
        def get(endpoint,raw=False):
            suffix=endpoint.removeprefix('repos/idy/yesman/')
            if suffix.startswith('releases?'):return releases
            if suffix.startswith('releases/assets/'):return bodies[int(suffix.rsplit('/',1)[1])]
            if suffix.startswith('releases/'):return assets[int(suffix.split('/')[1])]
            if suffix.startswith('git/ref/tags/'):
                commit=p.PREDECESSOR if suffix.endswith('v0.1.48')else p.RELEASE
                return {'object':{'sha':commit,'type':'commit','url':'https://api.github.com/repos/idy/yesman/git/commits/'+commit}}
            if suffix.startswith('git/commits/'):
                commit=suffix.rsplit('/',1)[1];tree=p.TREE if commit==p.INTEGRATION else download.SOURCE_TREES[commit]
                return {'sha':commit,'tree':{'sha':tree},'parents':[{'sha':p.INTEGRATION}]if commit==p.RELEASE else[]}
            if suffix.startswith('git/trees/'):
                tree=suffix.split('/')[2].split('?')[0]
                return {'sha':tree,'truncated':False,'tree':[{'path':name,'mode':row[0],'type':row[1],'sha':row[2]}for name,row in tree_rows[tree].items()]}
            raise AssertionError('unexpected normal fixture GET')
        with tempfile.TemporaryDirectory()as directory:
            root=Path(directory);env=self.environment(root);out=root/'inputs'
            def sealed_P():
                cfg,hashes,web,proof=download.source_contents(sources[p.PREDECESSOR],p.PREDECESSOR,tree_rows[download.SOURCE_TREES[p.PREDECESSOR]])
                proof['packageBinding']={'releaseID':1,'mainSHA256':hashlib.sha256(b'normal inert binary').hexdigest(),'assets':{v['name']:v for v in assets[1]}}
                return cfg,hashes,web,proof
            with patch.dict(os.environ,env,clear=True),patch.object(download.sys,'argv',['download.py',str(out)]),patch.object(download,'get',side_effect=get),patch.object(download,'predecessor_data',side_effect=sealed_P),patch.object(download,'public_source',side_effect=lambda commit:sources[commit]),patch.object(download.subprocess,'run',return_value=subprocess.CompletedProcess(['gh'],0,b'gh version 2.103.0\n',b'')),contextlib.redirect_stdout(io.StringIO()):download.main()
            mapping=json.loads((out/'source-mapping.json').read_bytes());meta=json.loads((out/'release-inputs.json').read_bytes());status=json.loads((root/'t199-download-diagnostics.json').read_bytes())
            self.assertEqual(status['state'],'success');self.assertEqual(set(mapping['sourceExports']),{p.PREDECESSOR,p.RELEASE})
            self.assertEqual([v['Source']for v in meta['stages'].values()],[p.PREDECESSOR,p.RELEASE])
            for commit in (p.PREDECESSOR,p.RELEASE):
                proof=mapping['sourceExports'][commit];self.assertTrue(proof['gitBlobObjectsVerified']);self.assertEqual(proof['regularFiles'],3)
                self.assertEqual(proof['archiveSHA256'],hashlib.sha256(sources[commit]).hexdigest())
            self.assertEqual(meta['sourceFileHashes'],{name:row['sha256']for name,row in mapping['sourceExports'][p.RELEASE]['fileObjects'].items()})

    def test_sealed_P_has_complete_web_config_and_missing_file_refuses(self):
        cfg,hashes,web,proof=download.predecessor_data()
        self.assertEqual(len(web),107);self.assertEqual(len(hashes),108)
        self.assertEqual(cfg['version'],'0.1.48');self.assertEqual(proof['scope'],'complete-web-and-config')
        self.assertEqual(proof['sourceCommit'],p.PREDECESSOR);self.assertEqual(proof['gitTree'],download.SOURCE_TREES[p.PREDECESSOR])
        self.assertTrue(proof['completeWebInventory']and proof['gitBlobObjectsVerified'])
        self.assertEqual(proof['packageBinding']['releaseID'],450165231)
        with tempfile.TemporaryDirectory()as directory:
            copy=Path(directory)/'P';shutil.copytree(ROOT/'qa/restart-resume/predecessor-data',copy)
            (copy/'web/actions.js').unlink()
            with self.assertRaises(FileNotFoundError):download.predecessor_data(copy)

    def test_timeout_retains_bounded_failure_and_fixed_metadata(self):
        with tempfile.TemporaryDirectory()as directory:
            root=Path(directory);env=self.environment(root);capture=io.StringIO()
            with patch.dict(os.environ,env,clear=True),contextlib.redirect_stdout(capture):
                with self.assertRaises(subprocess.TimeoutExpired):
                    with ad.Diagnostic('public-npm')as diagnostic:
                        diagnostic.phase('npm-ci')
                        raise subprocess.TimeoutExpired(['normal-fixture'],1,output=b'innocuous-hidden-output',stderr=b'innocuous-hidden-stderr')
            raw=(root/'t199-public-npm-diagnostics.json').read_bytes();row=json.loads(raw)
            self.assertEqual((row['state'],row['detailCode']),('failed','command-timeout'))
            self.assertTrue(row['startedAt']and row['updatedAt']and row['finishedAt'])
            self.assertEqual(row['resource'],'lock-pinned-public-npm');self.assertIn('python',row['toolVersions'])
            self.assertNotIn(b'innocuous-hidden',raw);self.assertNotIn('normal-fixture',capture.getvalue())
            self.assertLess(len(raw),2048)

    def test_workflow_always_retains_only_fixed_diagnostic_files(self):
        text=(ROOT/p.WORKFLOW).read_text()
        for role,name in [('download','restart-download-diagnostics'),('public-npm','restart-public-npm-diagnostics'),('bind','restart-bind-diagnostics')]:
            marker='          name: '+name+'\n';position=text.index(marker);block=text[:position].rsplit('      - name:',1)[1]+text[position:].split('      - uses:',1)[0]
            self.assertIn('        if: always()\n',block)
            self.assertIn('          path: ${{ runner.temp }}/t199-'+role+'-diagnostics.json\n',block)
            self.assertNotIn('include-hidden-files',block)
        self.assertNotIn("(out/'acquisition.log')",(ROOT/'qa/restart-resume/public_npm_cache.py').read_text())
        self.assertNotIn('diagnostics',(ROOT/'qa/restart-resume/run_native.py').read_text())
        binder=(ROOT/'qa/restart-resume/bind.py').read_text()
        for name in ('restart-download-diagnostics','restart-public-npm-diagnostics','restart-bind-diagnostics'):
            self.assertNotIn(name,binder)  # no diagnostic artifact is a bind input

if __name__=='__main__':unittest.main()
