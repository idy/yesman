"""One actual untouched official .48 -> .49 production update; no actors."""
import hashlib,importlib.util,json,os,signal,subprocess,tarfile,time,uuid
from pathlib import Path
import sys
sys.path.insert(0, str(Path(__file__).resolve().parent))
from guards import Owned
import provenance as p

def need(ok,why):
    if not ok:raise ValueError(why)
def sha(raw):return hashlib.sha256(raw).hexdigest()
def module(name,path):
    spec=importlib.util.spec_from_file_location(name,path);value=importlib.util.module_from_spec(spec);spec.loader.exec_module(value);return value

def run(stack,runner,plan_path,evidence_path,contract_path):
    plan_path=Path(plan_path).resolve();plan=json.loads(plan_path.read_bytes());contract=json.loads(Path(contract_path).read_bytes());authority=p.receipt(contract['hostedHandoff'])
    need(authority['workflowCommit']==os.environ['GITHUB_SHA']and authority['runID']==os.environ['GITHUB_RUN_ID']and authority['runAttempt']==int(os.environ['GITHUB_RUN_ATTEMPT']),'compat hosted identity differs')
    need(authority['compatPlanSHA256']==sha(plan_path.read_bytes())and plan['Target']=='darwin-arm64'and len(plan['Stages'])==2,'one exact compatible edge required')
    need([s['Source']for s in plan['Stages']]==[p.PREDECESSOR,p.RELEASE]and[s['Version']for s in plan['Stages']]==['0.1.48','0.1.49']and all(s['official']for s in plan['Stages']),'official source pair differs')
    meta=json.loads((plan_path.parent/'release-inputs.json').read_bytes())
    for s in plan['Stages']:
        for f in('Archive','Manifest','Web'):s[f]=str(plan_path.parent/s[f])
        r=[r for r in meta['releases']if r['tag']=='v'+s['Version']];need(len(r)==1,'release receipt missing')
        for f in('Archive','Manifest'):
            file=Path(s[f]);a=[a for a in r[0]['assets']if a['name']==file.name];need(len(a)==1 and a[0]['size']==file.stat().st_size and a[0]['digest']=='sha256:'+sha(file.read_bytes()),'untouched official bytes differ')
    need(os.environ.get('GITHUB_ACTIONS')=='true'and os.environ.get('RUNNER_ENVIRONMENT')=='github-hosted','fresh hosted VM required');runner.naming.hosted()
    need(not any(os.environ.get(k)for k in('GH_TOKEN','GITHUB_TOKEN','MYGO_UPDATER_PRIVATE_KEY')),'native credentials refused')
    evidence=Path(evidence_path).absolute();need(not evidence.exists()and evidence.is_relative_to(Path(os.environ['RUNNER_TEMP']).resolve()),'new owned evidence');evidence.mkdir()
    root=Path(os.environ['RUNNER_TEMP'])/('t199-compat-'+uuid.uuid4().hex);root.mkdir();install=root/'Application install with spaces';install.mkdir();data=root/'data';data.mkdir();web=root/'web';web.mkdir();token=uuid.uuid4().hex
    safe={k:os.environ[k]for k in('PATH','HOME','USER','LOGNAME','LANG','LC_ALL')if k in os.environ};safe.update(YESMAN_HOME=str(data),PI_CODING_AGENT_DIR=str(root/'pi'),YESMAN_PI=str(root/'missing-pi'),YESMAN_WEB_DIR=str(web),HTTP_PROXY=plan['Feed'],HTTPS_PROXY=plan['Feed'],NO_PROXY='127.0.0.1,localhost,::1',TMPDIR=str(root))
    plan['_path']=str(plan_path);feed=runner.Feed(plan,'archive');feed.target=1
    transport=module('compat_transport',Path(runner.__file__).with_name('upgrade-transport.py'));tls,_,fingerprint=stack.enter_context(transport.trusted_transport())
    from urllib.parse import urlsplit
    url=urlsplit(plan['Feed']);server=feed.serve(url.hostname,url.port,tls);helper=root/'native-info';subprocess.run(['xcrun','swiftc',str(Path(runner.__file__).with_name('name-process-macos.swift')),'-o',str(helper)],env=safe,check=True,timeout=60)
    # Same exact helper parser/kernel observer as the stateful attachment.
    attachment=module('compat_attachment',Path(__file__).parent/'runner-attachment-f48/runner_attachment.py')
    process=watcher=None;staging=False;completed=False;proof={'source':p.RELEASE,'predecessor':p.PREDECESSOR,'runID':os.environ['GITHUB_RUN_ID'],'nativeAcceptance':False,'fingerprint':fingerprint,'stages':[]};main=install/'yesman.app/Contents/MacOS/Yes Man';bundle=install/'yesman.app';log=(evidence/'app.log').open('ab',buffering=0)
    sentinel=data/'retained/workspace/.yesman/synthetic.keep';sentinel.parent.mkdir(parents=True);sentinel.write_bytes(b't199 untouched official compatibility data')
    def switch(index):
        import shutil
        for x in web.iterdir():shutil.rmtree(x)if x.is_dir()else x.unlink()
        shutil.copytree(plan['Stages'][index]['Web'],web,dirs_exist_ok=True);file=web/'index.html';original=file.read_text();driver=runner.DRIVER.replace('ENDPOINT',json.dumps(plan['Feed'])).replace('TOKEN',json.dumps(token));need(original.count('<head>')==1,'normal page');file.write_text(original.replace('<head>','<head>'+driver,1));need(file.read_text().replace(driver,'',1)==original,'normal page changed')
    def observe(index):
        value=feed.latest;s=plan['Stages'][index]
        if not value or value.get('about',{}).get('version')!=s['Version']:return None
        need(not value.get('error')and not value['errors']and value['about']['revision']==s['Source']and value['state']['home']==str(data)and not value['state']['agents'],'normal bridge/source state differs')
        return value
    def inspect(index):
        value=runner.wait(lambda:observe(index),'official normal page missing');need(value['localStorage']==token and value['settings']['appearance']=='dark'and value['settings']['language']=='en','settings not retained');ids=runner.find(main);need(len(ids)==1,'main instance');native=runner.naming.native(ids[0],helper);need(native['processName']=='Yes Man'and value['visibility']=='visible'and any(w['onScreen']and w['bounds'].get('Width',0)>100 for w in native['windows']),'visible main/window missing');need(sha(main.read_bytes())==plan['Stages'][index]['binarySHA256']and sentinel.read_bytes()==b't199 untouched official compatibility data','image/data differs');subprocess.run(['codesign','--verify','--deep','--strict',str(bundle)],env=safe,check=True,timeout=30);proof['stages'].append({'index':index,'native':native,'bridge':value,'pid':ids[0],'mainSHA256':sha(main.read_bytes())});return ids[0]
    def register():
        paths=list(install.glob('.yesman.app.update-*/app/yesman.app'));need(len(paths)==1,'one staged helper required');raw=subprocess.check_output(['/bin/ps','-ww','-axo','pid=,ppid=,command='],timeout=5).decode();return attachment.HelperExit(attachment.helper_rows(raw,process.pid,install,paths[0]))
    try:
        verifier=Path(contract['publicVerifier']).resolve();need(sha(verifier.read_bytes())==contract['publicVerifierSHA256']==authority['publicVerifierSHA256'],'bound public verifier differs');subprocess.run([str(verifier),str(plan_path),'verify'],env=safe,check=True,timeout=180)
        with tarfile.open(plan['Stages'][0]['Archive'])as t:t.extractall(install,filter='data')
        switch(0);child=subprocess.Popen([str(main)],cwd=root,env=safe,stdin=subprocess.DEVNULL,stdout=log,stderr=log);process=Owned(child.pid,main,child);runner.wait(lambda:observe(0),'predecessor page missing');feed.send('initialize');runner.wait(lambda:observe(0)and observe(0)['localStorage']==token,'initialize not completed');inspect(0)
        staging=True;feed.send('check');status=runner.wait(lambda:feed.latest if feed.latest and feed.latest.get('update',{}).get('state')in('ready','failed')else None,'update Ready missing',120);need(status['update']['state']=='ready'and status['update']['version']=='0.1.49','production update failed');watcher=register();old=feed.latest['page'];switch(1);feed.send('restart');attachment.ordinary_exit(process.wait(30));runner.wait(lambda:observe(1)and feed.latest['page']!=old,'official relaunch missing');pid=inspect(1);process=Owned(pid,main);runner.wait(watcher.poll,'helper drain missing',30);exits=[e for e in watcher.events if e['fflags']&attachment.NOTE_EXIT];need(len(exits)==1 and exits[0]['fflags']&attachment.NOTE_EXITSTATUS and os.WIFEXITED(exits[0]['data'])and os.WEXITSTATUS(exits[0]['data'])==0,'official helper exit missing');proof['helper']=watcher.events;need(any(r['stage']==1 and r['file']==Path(plan['Stages'][1]['Archive']).name for r in feed.requests),'actual production archive request missing');proof['requests']=feed.requests;proof['contractPassed']=True;completed=True
    finally:
        errors=[];owners=[]
        if staging and not watcher:
            try:watcher=register()
            except Exception as error:errors.append('helper ownership unresolved: '+str(error))
        try:
            if process:owners.append(process.drain())
            if watcher:runner.wait(watcher.poll,'helper drain unresolved',30)
            for pid in runner.find(main):owners.append(Owned(pid,main).drain())
        except Exception as error:errors.append(str(error))
        if watcher:watcher.close()
        if completed and not errors:
            try:
                retained=root/'retained-native-profile';retained.mkdir()
                for i,path in enumerate((Path.home()/'Library/Application Support/yesman',Path.home()/'Library/Preferences/dev.idy.yesman.plist')):
                    if not path.exists():continue
                    need(not path.is_symlink(),'linked compat native profile refused');args=['lsof','-t','+D',str(path)]if path.is_dir()else['lsof','-t','--',str(path)];scan=subprocess.run(args,capture_output=True,timeout=10);need(scan.returncode==1 and not scan.stdout and not scan.stderr,'compat native profile consumer unresolved');os.rename(path,retained/str(i))
            except Exception as error:errors.append(str(error))
        server.shutdown();server.server_close();log.close();proof.update(rootRetained=str(root),completed=completed,teardown={'owners':owners,'errors':errors,'unresolvedOwnership':bool(errors)});(evidence/'compatibility.json').write_text(json.dumps(proof,indent=2)+'\n');need(not errors,'compat cleanup unresolved')
    need(completed,'compatibility incomplete')
