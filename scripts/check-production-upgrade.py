#!/usr/bin/env python3
"""Real production-main multi-hop upgrades on disposable hosted profiles only.

Controlled plans explicitly describe production-source rebuilds, not releases.
No recorder or updater implementation is loaded into an application. Drive only
existing public Yesman CheckForUpdates/Update/RestartToUpdate APIs and WEB_DIR.
"""
import contextlib
import ctypes
import hashlib
import http.server
import importlib.util
import json
import os
import pathlib
import shutil
import signal
import subprocess
import sys
import tarfile
import tempfile
import threading
import time
import urllib.parse
import uuid

spec=importlib.util.spec_from_file_location('naming',pathlib.Path(__file__).with_name('check-process-name.py'))
naming=importlib.util.module_from_spec(spec);spec.loader.exec_module(naming)

def sha(path):return hashlib.sha256(pathlib.Path(path).read_bytes()).hexdigest()
def wait(fn,message,timeout=90):
    end=time.monotonic()+timeout
    while time.monotonic()<end:
        value=fn()
        if value:return value
        time.sleep(.15)
    raise AssertionError(message)

def find(exe):
    if sys.platform=='win32':return naming.windows_pids(exe)
    if sys.platform=='linux':
        result=[]
        for p in pathlib.Path('/proc').iterdir():
            if p.name.isdigit():
                try:
                    if (p/'exe').resolve()==exe.resolve():result.append(int(p.name))
                except (OSError,RuntimeError):pass
        return result
    lines=subprocess.check_output(['ps','-axo','pid=,comm='],text=True).splitlines()
    return [int(x.split(None,1)[0]) for x in lines if len(x.split(None,1))==2 and pathlib.Path(x.split(None,1)[1]).resolve()==exe.resolve()]

class Existing:
    def __init__(self,pid):self.pid=pid;self.returncode=None
    def poll(self):
        try:os.kill(self.pid,0);return None
        except ProcessLookupError:self.returncode=0;return 0
    def wait(self,timeout):
        wait(lambda:self.poll() is not None,'process did not exit',timeout)
        return None # Launch Services/non-child PID: no OS exit status available
    def send_signal(self,s):os.kill(self.pid,s)
    def terminate(self):self.send_signal(signal.SIGTERM)
    def kill(self):self.send_signal(signal.SIGKILL)

class Feed:
    def __init__(self,plan,mode):
        self.plan,self.mode=plan,mode;self.target=0;self.command=None;self.serial=0;self.latest=None;self.lock=threading.Lock();self.requests=[]
        self.files={}
        for s in plan['Stages']:
            m=json.loads(pathlib.Path(s['Manifest']).read_text(encoding='utf-8'))
            self.files[pathlib.Path(s['Archive']).name]=pathlib.Path(s['Archive'])
            for d in m.get('deltas',[]):self.files[pathlib.Path(urllib.parse.urlparse(d['url']).path).name]=pathlib.Path(s['Manifest']).parent.parent.parent/pathlib.Path(urllib.parse.urlparse(d['url']).path).name
        # Deltas are written beside plan.json, not under each build directory.
        base=pathlib.Path(plan['_path']).parent
        for p in base.glob('*.delta'):self.files[p.name]=p
    def send(self,op):
        with self.lock:
            self.serial+=1;self.command={'id':self.serial,'op':op,'page':self.latest['page'] if self.latest else None}
    def serve(self,host,port,tls_context=None):
        feed=self
        class Handler(http.server.BaseHTTPRequestHandler):
            def log_message(self,*args):pass
            def do_CONNECT(self):
                host,separator,port=self.path.partition(':')
                allowed={'github.com','api.github.com','objects.githubusercontent.com','release-assets.githubusercontent.com'}
                if tls_context is None or not separator or port!='443' or host not in allowed:
                    self.send_error(403,'Network outside upgrade inputs disabled');return
                self.send_response(200);self.end_headers()
                connection=tls_context.wrap_socket(self.connection,server_side=True)
                try:Handler(connection,self.client_address,self.server)
                finally:connection.close();self.close_connection=True
            def respond(self,code,typ='application/json',length=None):
                self.send_response(code);self.send_header('Content-Type',typ);self.send_header('Access-Control-Allow-Origin','*')
                if length is not None:self.send_header('Content-Length',str(length))
                self.send_header('Connection','close');self.end_headers()
            def do_OPTIONS(self):
                self.send_response(204);self.send_header('Access-Control-Allow-Origin','*');self.send_header('Access-Control-Allow-Methods','GET,POST,OPTIONS');self.send_header('Access-Control-Allow-Headers','Content-Type');self.send_header('Access-Control-Allow-Private-Network','true');self.end_headers()
            def do_POST(self):
                if self.path!='/report':self.send_error(404);return
                size=int(self.headers.get('Content-Length','0'));assert size<131072
                value=json.loads(self.rfile.read(size))
                with feed.lock:feed.latest=value
                self.respond(200,length=2);self.wfile.write(b'{}')
            def do_GET(self):
                path=urllib.parse.urlparse(self.path).path
                if path=='/command':
                    with feed.lock:value=feed.command
                    body=json.dumps(value).encode();self.respond(200,length=len(body));self.wfile.write(body);return
                name=urllib.parse.unquote(path.rsplit('/',1)[-1])
                with feed.lock:
                    target=feed.target
                    feed.requests.append({'stage':target,'file':name})
                if name=='update-'+feed.plan['Target']+'.json':
                    m=json.loads(pathlib.Path(feed.plan['Stages'][target]['Manifest']).read_text(encoding='utf-8'))
                    if feed.mode=='archive':m['deltas']=[]
                    body=json.dumps(m).encode();self.respond(200,length=len(body));self.wfile.write(body);return
                file=feed.files.get(name)
                if file is None:self.send_error(404);return
                self.respond(200,'application/octet-stream',file.stat().st_size)
                with file.open('rb') as body:shutil.copyfileobj(body,self.wfile)
        server=http.server.ThreadingHTTPServer((host,port),Handler)
        threading.Thread(target=server.serve_forever,daemon=True).start();return server

DRIVER=r'''<script>
window.addEventListener('DOMContentLoaded',async()=>{
 const id=String(Math.random()),seen=new Set(),errors=[];
 window.addEventListener('error',e=>errors.push(e.message));
 const call=(method,...args)=>window.mygo.call('Yesman.'+method,...args);
 const sleep=ms=>new Promise(r=>setTimeout(r,ms));
 try {
  const end=Date.now()+45000;while(!document.querySelector('#settings-button')){if(Date.now()>end)throw Error('page not ready');await sleep(50)}
  while(true){
   const command=await fetch(ENDPOINT+'/command').then(r=>r.json());
   if(command&&command.page===id&&!seen.has(command.id)){
    seen.add(command.id);
    if(command.op==='initialize'){localStorage.setItem('yesman.upgrade-sentinel',TOKEN);await call('SetAppearance','dark');await call('SetLanguage','en')}
    if(command.op==='check')await call('CheckForUpdates');
    if(command.op==='restart')await call('RestartToUpdate');
   }
   const value={page:id,about:await call('About'),state:await call('State'),settings:await call('Settings'),update:await call('Update'),localStorage:localStorage.getItem('yesman.upgrade-sentinel'),visibility:document.visibilityState,errors};
   await fetch(ENDPOINT+'/report',{method:'POST',body:JSON.stringify(value)});await sleep(250);
  }
 }catch(e){await fetch(ENDPOINT+'/report',{method:'POST',body:JSON.stringify({error:String(e),errors})})}
});</script>'''

def main():
    with contextlib.ExitStack() as stack:
        run(stack)


def run(stack):
    naming.hosted()
    path=pathlib.Path(sys.argv[1]).resolve();plan=json.loads(path.read_text(encoding='utf-8'));plan['_path']=str(path)
    evidence=pathlib.Path(sys.argv[2]).resolve();evidence.mkdir(parents=True,exist_ok=True)
    assert plan['kind'] in ('controlled-production-source-rebuild','released-candidate-plus-private-QA')
    for s in plan['Stages']:
        for field in ('Archive','Manifest','Web'):
            if not pathlib.Path(s[field]).is_absolute():s[field]=str(path.parent/s[field])
    assert len(plan['Stages'])==4
    actual_target=subprocess.check_output(['go','env','GOOS'],text=True).strip()+'-'+subprocess.check_output(['go','env','GOARCH'],text=True).strip()
    assert plan['Target']==actual_target,'Plan target does not match native host'
    subprocess.run(['go','run','./tools/package','-verify-upgrade-assets',str(path)],check=True)
    proof={'kind':plan['kind'],'limits':plan['limits'],'sourceSHA':plan['sourceSHA'],'runID':os.environ['GITHUB_RUN_ID'],'scenarios':[]}
    helper=None
    if sys.platform=='darwin':
        helper=evidence/'native-info';subprocess.run(['xcrun','swiftc',str(pathlib.Path(__file__).with_name('name-process-macos.swift')),'-o',str(helper)],check=True)
    tls_context=None;ca_file=None
    if plan['kind']=='released-candidate-plus-private-QA' or plan.get('useTLS'):
        module_spec=importlib.util.spec_from_file_location('transport',pathlib.Path(__file__).with_name('upgrade-transport.py'))
        transport=importlib.util.module_from_spec(module_spec);module_spec.loader.exec_module(transport)
        tls_context,ca_file,fingerprint=stack.enter_context(transport.trusted_transport())
        proof['controlledTransportCAFingerprint']=fingerprint
    for mode in ('archive','delta'):
        feed=Feed(plan,mode);url=urllib.parse.urlparse(plan['Feed']);server=feed.serve(url.hostname,url.port,tls_context)
        process=None;guard=None
        try:
            with tempfile.TemporaryDirectory(prefix='Real upgrade '+mode+' ',dir=os.environ['RUNNER_TEMP'],ignore_cleanup_errors=True) as directory:
                root=pathlib.Path(directory);install=root/'Application install with spaces';install.mkdir()
                data=root/'user data';data.mkdir();active=root/'active web';active.mkdir();token=uuid.uuid4().hex
                sentinels={data/'retained/workspace/.yesman/sessions/lead/session.jsonl':b'synthetic session sentinel',data/'secrets/vault-sentinel.keep':b'synthetic vault sentinel',data/'retained/workspace/source.txt':b'synthetic workspace sentinel'}
                for p,b in sentinels.items():p.parent.mkdir(parents=True,exist_ok=True);p.write_bytes(b)
                def switch_web(index):
                    web=pathlib.Path(plan['Stages'][index]['Web'])
                    for child in list(active.iterdir()):
                        if child.is_dir():shutil.rmtree(child)
                        else:child.unlink()
                    shutil.copytree(web,active,dirs_exist_ok=True)
                    file=active/'index.html';original=file.read_text(encoding='utf-8');driver=DRIVER.replace('ENDPOINT',json.dumps(plan['Feed'])).replace('TOKEN',json.dumps(token));file.write_text(original.replace('<head>','<head>'+driver,1),encoding='utf-8');assert file.read_text(encoding='utf-8').replace(driver,'',1)==original
                def executable(index):
                    name=plan['Stages'][index]['mainName']
                    return install/('yesman.app/Contents/MacOS/'+name if sys.platform=='darwin' else name+('.exe' if sys.platform=='win32' else ''))
                def progress(message):
                    print(mode+': '+message,flush=True)
                    proof['progress']=mode+': '+message
                    (evidence/'production-upgrade.json').write_text(json.dumps(proof,indent=2))
                def snapshot(index):
                    with feed.lock:value=feed.latest
                    if value and value.get('about',{}).get('version')==plan['Stages'][index]['Version']:
                        assert not value.get('error') and not value['errors'],value
                        assert value['state']['home']==str(data) and not value['state']['agents'],value
                        if plan['Stages'][index].get('official'):assert value['about'].get('revision')==plan['Stages'][index]['Source'],value
                        return value
                    return None
                def inspect(index):
                    value=wait(lambda:snapshot(index),'No real webview/Go state for '+str(index))
                    assert value['localStorage']==token and value['settings']['appearance']=='dark' and value['settings']['language']=='en',value
                    ids=wait(lambda:find(executable(index)),'Cannot find actual upgraded process')
                    assert len(ids)==1,ids
                    info=naming.native(ids[0],helper);assert info['processName']==plan['Stages'][index]['mainName'],info
                    assert value['visibility']=='visible',value
                    if sys.platform=='darwin':assert any(w['onScreen'] and w['bounds'].get('Width',0)>100 for w in info['windows']),info
                    elif sys.platform=='win32':assert any(w['visible'] and w['title']==plan['Stages'][index]['mainName'] for w in info['windows']),info
                    else:assert '"'+plan['Stages'][index]['mainName']+'"' in info['windows'],info
                    assert sha(executable(index))==plan['Stages'][index]['binarySHA256']
                    for p,b in sentinels.items():assert p.read_bytes()==b,p
                    return {'stage':index,'version':value['about']['version'],'pid':ids[0],'native':info,'bridge':value,'binarySHA256':sha(executable(index))}
                def update(index):
                    progress('requesting update '+str(index))
                    feed.target=index;feed.send('check')
                    return wait(lambda:(feed.latest if feed.latest and feed.latest.get('update',{}).get('state') in ('ready','failed') and feed.latest['update'].get('version')==plan['Stages'][index]['Version'] else None),'Upgrade never reached a result',120)
                def restart(index):
                    nonlocal process
                    oldpage=feed.latest['page'];switch_web(index);feed.send('restart');progress('restart requested for '+str(index))
                    if process is not None:
                        result=process.wait(timeout=30)
                        if not isinstance(process,Existing):assert result==0
                    wait(lambda:feed.latest and feed.latest.get('page')!=oldpage and snapshot(index),'Restart did not load the new production app')
                    ids=find(executable(index));assert len(ids)==1
                    process=naming.WindowsChild(ids[0]) if sys.platform=='win32' else Existing(ids[0])
                with tarfile.open(plan['Stages'][0]['Archive']) as tar:tar.extractall(install,filter='data')
                switch_web(0)
                env={k:v for k,v in os.environ.items() if k in ('HOME','USER','LOGNAME','PATH','TMPDIR','LANG','LC_ALL','SHELL','SystemRoot','SYSTEMROOT','WINDIR','USERPROFILE','APPDATA','LOCALAPPDATA','TEMP','TMP','DISPLAY','XAUTHORITY','XDG_RUNTIME_DIR','DBUS_SESSION_BUS_ADDRESS')}
                assert not any('PRIVATE_KEY' in k or k in ('GH_TOKEN','GITHUB_TOKEN') for k in env)
                env.update(YESMAN_HOME=str(data),YESMAN_PI=str(root/'missing-pi'),PI_CODING_AGENT_DIR=str(root/'pi'),YESMAN_WEB_DIR=str(active),HTTP_PROXY=plan['Feed'],HTTPS_PROXY=plan['Feed'],NO_PROXY='127.0.0.1,localhost,::1')
                if ca_file and sys.platform=='linux':env['SSL_CERT_FILE']=ca_file
                log=(evidence/(mode+'-initial.log')).open('w')
                process=subprocess.Popen([str(executable(0))],cwd=root,env=env,stdin=subprocess.DEVNULL,stdout=log,stderr=subprocess.STDOUT)
                progress('old main launched')
                wait(lambda:snapshot(0),'Old production source did not start')
                feed.send('initialize');wait(lambda:snapshot(0) and snapshot(0)['localStorage']==token,'Synthetic settings initialization failed')
                steps=[inspect(0)]
                for index in (1,2):
                    status=update(index);assert status['update']['state']=='ready',status
                    restart(index);steps.append(inspect(index));progress('real main/bridge/hash verified at '+str(index))
                # Force failure in the actual production updater at its own
                # installation. Mac fails the deferred new-bundle mv; Windows
                # blocks a later resource rename (after earlier placements).
                # Linux removes directory write permission (pre-placement).
                if sys.platform=='win32':
                    from ctypes import wintypes
                    kernel=ctypes.WinDLL('kernel32',use_last_error=True);kernel.CreateFileW.restype=wintypes.HANDLE
                    kernel.CreateFileW.argtypes=[wintypes.LPCWSTR,wintypes.DWORD,wintypes.DWORD,wintypes.LPVOID,wintypes.DWORD,wintypes.DWORD,wintypes.HANDLE]
                    kernel.CloseHandle.argtypes=[wintypes.HANDLE]
                    guard=kernel.CreateFileW(str(install/'tray.svg'),0x80000000,3,None,3,0,None);assert guard not in (None,wintypes.HANDLE(-1).value)
                elif sys.platform!='darwin':install.chmod(0o555)
                status=update(3)
                if sys.platform=='darwin':
                    assert status['update']['state']=='ready',status
                    target=install/'yesman.app'
                    staged=list(install.glob('.yesman.app.update-*/app/*.app'));assert len(staged)==1,staged
                    assert staged[0].is_relative_to(install)
                    shutil.rmtree(staged[0])
                    feed.target=2;restart(2)
                else:
                    assert status['update']['state']=='failed',status
                    if sys.platform=='win32':kernel.CloseHandle(guard);guard=None
                    else:install.chmod(0o755)
                    feed.target=2
                rollback=inspect(2);progress('failure recovery image/bridge verified')
                if isinstance(process,Existing):
                    process.send_signal(signal.SIGTERM);process.wait(timeout=20)
                else:naming.quit_process(process,rollback['native'])
                process=None
                legacy=executable(2).with_name('yesman.exe' if sys.platform=='win32' else 'yesman')
                cli_env=dict(env,MYGO_GENERATE=str(root/'client.ts'))
                cli=subprocess.run([str(legacy)],cwd=root,env=cli_env,input=b'',capture_output=True,timeout=30)
                assert cli.returncode==0 and 'Yesman.State' in (root/'client.ts').read_text(encoding='utf-8'),cli.stderr
                for p,b in sentinels.items():assert p.read_bytes()==b,p
                # Each positive hop must use the selected actual download path.
                for index in (1,2):
                    requests=[x['file'] for x in feed.requests if x['stage']==index]
                    assert any(x.endswith('.delta' if mode=='delta' else '.tar.gz') for x in requests),requests
                    if mode=='delta':assert not any(x.endswith('.tar.gz') for x in requests),requests
                proof['scenarios'].append({'mode':mode,'stages':steps,'failedInstall':status['update'],'rollback':rollback,'legacyCLIExit':cli.returncode,'requests':feed.requests,'sentinels':True,'failurePoint':'deferred bundle swap' if sys.platform=='darwin' else 'late resource rename' if sys.platform=='win32' else 'directory write permission before placement'})
                log.close()
        finally:
            if guard is not None:kernel.CloseHandle(guard)
            if process is not None and process.poll() is None:
                process.terminate()
                try:process.wait(timeout=10)
                except subprocess.TimeoutExpired:process.kill();process.wait(timeout=10)
            if 'install' in locals() and install.exists():install.chmod(0o755)
            if 'root' in locals():shutil.rmtree(root,ignore_errors=True)
            server.shutdown();server.server_close()
            (evidence/'production-upgrade.json').write_text(json.dumps(proof,indent=2))
    proof['passed']=True;(evidence/'production-upgrade.json').write_text(json.dumps(proof,indent=2))
    print('PASS: real production-source old -> new -> next update -> real install failure/rollback -> working legacy CLI; controlled provenance only')

if __name__=='__main__':main()
