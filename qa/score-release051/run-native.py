#!/usr/bin/env python3
"""Hosted-only .50 -> .51 full archive replacement and .51 cold-open proof."""
import http.server,json,os,pathlib,platform,shlex,shutil,signal,subprocess,sys,threading,time,uuid
from validation import OLD,validate_binding,validate_transfer,extract_bundle,inventory,image_identity,observe_install,require,sha
from contract import seed,files,verify_page,verify_cold,catalog_read_at
KIT=pathlib.Path(__file__).resolve().parent
REPO=KIT.parents[1]
sys.path.insert(0,str(REPO/'scripts'))
from check_macos_startup import require_hosted_runner
from check_macos_bundle import inspect

def save(path,value):
 path.write_text(json.dumps(value,indent=2)+'\n')
def build_environment(root):
 env={k:os.environ[k] for k in ['HOME','USER','LOGNAME','PATH','LANG','LC_ALL','SHELL'] if k in os.environ}
 env.update(TMPDIR=str(root/'temp'),TMP=str(root/'temp'),TEMP=str(root/'temp'),GOTMPDIR=str(root/'temp'),
  GOCACHE=str(root/'cache/go'),GOMODCACHE=str(root/'cache/modules'),GOPATH=str(root/'cache/gopath'),GOENV='off',GOTOOLCHAIN='local',GOPROXY='off',GOSUMDB='off',GOWORK='off',CGO_ENABLED='0',
  CLANG_MODULE_CACHE_PATH=str(root/'cache/clang'),SWIFT_MODULECACHE_PATH=str(root/'cache/swift'))
 return env

def app_environment(root,data,web,catalog,proxy):
 env={k:os.environ[k] for k in ['HOME','USER','LOGNAME','PATH','SHELL'] if k in os.environ}
 env.update(LANG='en_US.UTF-8',LC_ALL='en_US.UTF-8',TMPDIR=str(root/'temp'),TMP=str(root/'temp'),TEMP=str(root/'temp'),
  YESMAN_HOME=str(data),YESMAN_PI=str(catalog),PI_CODING_AGENT_DIR=str(root/'pi-config'),YESMAN_WEB_DIR=str(web),
  SCORE_CATALOG_LOG=str(root/'evidence/catalog.jsonl'),HTTP_PROXY=proxy,HTTPS_PROXY=proxy,ALL_PROXY=proxy,
  http_proxy=proxy,https_proxy=proxy,all_proxy=proxy,NO_PROXY='127.0.0.1,localhost,::1',no_proxy='127.0.0.1,localhost,::1')
 return env

class Collector(http.server.BaseHTTPRequestHandler):
 def log_message(self,*args):pass
 def do_OPTIONS(self):
  if self.path!='/'+self.server.token:self.send_error(404);return
  self.send_response(204)
  for k,v in [('Access-Control-Allow-Origin','*'),('Access-Control-Allow-Methods','POST, OPTIONS'),('Access-Control-Allow-Headers','Content-Type'),('Access-Control-Allow-Private-Network','true')]:self.send_header(k,v)
  self.end_headers()
 def do_CONNECT(self):
  self.server.blocked.append({'method':'CONNECT','target':self.path});self.send_error(403,'Network disabled in score acceptance')
 def do_GET(self):
  self.server.blocked.append({'method':'GET','target':self.path});self.send_error(403,'Network disabled in score acceptance')
 def do_POST(self):
  if self.path!='/'+self.server.token:self.send_error(404);return
  try:
   length=int(self.headers.get('Content-Length','0'));require(0<length<=131072,'report size')
   value=json.loads(self.rfile.read(length));require(isinstance(value,dict),'report type')
   with self.server.report_lock:
    require(not self.server.received.is_set(),'duplicate report');self.server.payload.update(value)
    self.send_response(200);self.send_header('Access-Control-Allow-Origin','*');self.end_headers();self.server.received.set()
  except (ValueError,UnicodeError):self.send_error(400)

def run_phase(stage,spec,target,web,root,data,helper,catalog,expected_inventory,anchor,shim_required,proof):
 e=root/'evidence';executable=target/'Contents/MacOS/Yes Man'
 if stage=='cold':save(e/'shim-before-cold.json',observe_install(target,data,expected_inventory,anchor,shim_required))
 else:require(inventory(target)==expected_inventory and image_identity(executable)==anchor,'bundle changed before launch')
 before=files(data);server=http.server.ThreadingHTTPServer(('127.0.0.1',0),Collector)
 server.token=uuid.uuid4().hex;server.received=threading.Event();server.payload={};server.blocked=[];server.report_lock=threading.Lock()
 thread=threading.Thread(target=server.serve_forever,daemon=True);thread.start()
 proxy='http://127.0.0.1:'+str(server.server_port)
 observed=root/('web-'+stage);shutil.copytree(web,observed)
 original=(observed/'index.html').read_text()
 driver='<script>'+(KIT/'observer.js').read_text().replace('SCORE_ENDPOINT',json.dumps(proxy+'/'+server.token))+'</script>'
 require(original.count('<head>')==1,'index head')
 (observed/'index.html').write_text(original.replace('<head>','<head>'+driver,1))
 require((observed/'index.html').read_text().replace(driver,'',1)==original,'observer injection changed production')
 for path in web.rglob('*'):
  if path.is_file() and path.relative_to(web).as_posix()!='index.html':require((observed/path.relative_to(web)).read_bytes()==path.read_bytes(),'production web changed')
 env=app_environment(root,data,observed,catalog,proxy);process=None
 phase={'stage':stage,'source':spec['source'],'version':'0.1.50' if stage=='old' else '0.1.51','binarySHA256':spec['binarySHA256'],'before':before,'passed':False}
 try:
  with (e/('app-'+stage+'.log')).open('w') as log:
   process=subprocess.Popen([str(executable)],cwd=root,env=env,stdout=log,stderr=subprocess.STDOUT)
   phase['pid']=process.pid;save(e/('phase-'+stage+'.json'),phase)
   received=server.received.wait(45)
   if received:
    report=dict(server.payload);save(e/('page-'+stage+'.json'),report)
   require(received,'No real WebKit/Go observer report')
   require(process.poll() is None,'App exited or handed off to another instance')
   verify_page(report,stage,data,spec['source'])
   phase['catalogReadAt']=catalog_read_at(report['crew'])
   raw=subprocess.check_output([str(helper),str(process.pid)],env=env,text=True,timeout=10)
   (e/('process-'+stage+'.json')).write_text(raw)
   actual=json.loads(raw);phase['actualProcess']=actual;save(e/('phase-'+stage+'.json'),phase)
   require(actual['bundleIdentifier']=='dev.idy.yesman' and actual['localizedName']=='Yes Man','native App identity')
   require(pathlib.Path(actual['executable']).resolve()==executable.resolve(),'native executable path')
   require(any(w['onScreen'] and w['bounds'].get('Width',0)>100 and w['bounds'].get('Height',0)>100 for w in actual['windows']),'native visible window')
   require(sha(pathlib.Path(actual['executable']).read_bytes())==spec['binarySHA256'],'actual native image')
   files(data);process.send_signal(signal.SIGTERM);code=process.wait(timeout=15)
   phase['exitCode']=code;save(e/('phase-'+stage+'.json'),phase);require(code==0,'normal shutdown required')
   raw=subprocess.check_output([str(helper),str(process.pid)],env=env,text=True,timeout=10)
   (e/('postexit-process-'+stage+'.json')).write_text(raw)
   require(not json.loads(raw)['windows'],'window remains after exit')
   process=None
   phase['shim']=observe_install(target,data,expected_inventory,anchor,shim_required)
   save(e/('shim-after-'+stage+'.json'),phase['shim'])
   subprocess.run(['codesign','--verify','--deep','--strict',str(target)],env=env,check=True,timeout=20)
   phase.update(actualProcess=actual,after=files(data),passed=True)
 except BaseException as ex:
  phase['error']=type(ex).__name__+': '+str(ex)
  save(e/('phase-'+stage+'.json'),phase)
  raise
 finally:
  cleanup_errors=[]
  try:
   if process is not None and process.poll() is None:
    process.send_signal(signal.SIGTERM)
    try:phase['cleanupExitCode']=process.wait(timeout=5)
    except subprocess.TimeoutExpired:process.kill();phase['cleanupExitCode']=process.wait(timeout=5);phase['forcedCleanup']=True
  except BaseException as ex:cleanup_errors.append('process: '+type(ex).__name__+': '+str(ex))
  for label,action in [('server shutdown',server.shutdown),('server close',server.server_close),('thread join',lambda:thread.join(timeout=3))]:
   try:action()
   except BaseException as ex:cleanup_errors.append(label+': '+type(ex).__name__+': '+str(ex))
  if thread.is_alive():cleanup_errors.append('observer thread did not drain')
  phase['cleanupErrors']=cleanup_errors
  if cleanup_errors:phase['passed']=False
  phase['blockedNetwork']=server.blocked;save(e/('phase-'+stage+'.json'),phase)
  proof['phases'].append(phase);save(e/'proof.json',proof)
  if cleanup_errors and 'error' not in phase:raise RuntimeError('phase cleanup failed: '+str(cleanup_errors))
 return report

def main():
 # Admission precedes all runner-owned writes, extraction, helper builds or launch.
 require_hosted_runner()
 require(platform.machine()=='arm64','ARM64 required')
 require(subprocess.run(['pgrep','-x','Yes Man'],stdout=subprocess.DEVNULL).returncode==1,'existing Yes Man process')
 require(not any(os.environ.get(k) for k in ['GH_TOKEN','GITHUB_TOKEN','MYGO_UPDATER_PRIVATE_KEY','OPENAI_API_KEY']),'credential mapped into execution job')
 b=validate_binding(os.environ['EXPECTED_BINDING'])
 require(os.environ.get('GITHUB_SHA')==b['qaSource'] and os.environ.get('GITHUB_REF')=='refs/heads/qa/model-score-release-051' and os.environ.get('GITHUB_REPOSITORY')=='idy/yesman','QA run identity')
 require(os.environ.get('GITHUB_RUN_ATTEMPT')=='1','one attempt only')
 require(subprocess.check_output(['git','-C',str(REPO),'rev-parse','HEAD'],text=True).strip()==b['qaSource'],'QA checkout identity')
 run=os.environ.get('GITHUB_RUN_ID','');require(run.isdigit(),'run ID')
 temp=pathlib.Path(os.environ['RUNNER_TEMP']).resolve();folder=temp/'score-pair-inputs';root=temp/'score-pair-run'
 require(not root.exists() and not root.is_symlink(),'fresh run directory')
 transfer=validate_transfer(folder,b,run)
 # Remove all unlisted ambient credentials before helper/inspector/App code.
 clean={k:os.environ[k] for k in ['HOME','USER','LOGNAME','PATH','LANG','LC_ALL','SHELL'] if k in os.environ}
 os.environ.clear();os.environ.update(clean)
 root.mkdir(mode=0o700)
 for p in ['temp','cache/go','cache/modules','cache/gopath','cache/swift','cache/clang','pi-config','install','evidence']:(root/p).mkdir(parents=True,exist_ok=True)
 buildenv=build_environment(root);os.environ.update(buildenv)
 e=root/'evidence';save(e/'transfer.json',transfer)
 proof={'passed':False,'route':'manual full archive replacement at a private install path; not automatic updater/delta','qaSource':b['qaSource'],'runID':run,'runAttempt':1,'runnerEnvironment':'github-hosted','platform':'darwin/arm64','installation':str(root/'install/yesman.app'),'data':str(root/'data'),'phases':[],'binding':b}
 save(e/'proof.json',proof)
 try:
  verifier=root/'verify-signature'
  subprocess.run(['go','build','-trimpath','-o',str(verifier),str(KIT/'verify-signature.go')],env=buildenv,check=True,timeout=90)
  helper=root/'process-info'
  subprocess.run(['xcrun','swiftc','-module-cache-path',str(root/'cache/swift'),str(REPO/'scripts/name-process-macos.swift'),'-o',str(helper)],env=buildenv,check=True,timeout=60)
  catalog=root/'catalog';catalog.write_text('#!/bin/sh\nexec '+shlex.quote(sys.executable)+' '+shlex.quote(str(KIT/'catalog.py'))+' "$@"\n');catalog.chmod(0o700)
  specs={'old':OLD,'new':b['candidate']};bundles={};inventories={}
  for stage,spec in specs.items():
   p=folder/stage
   subprocess.run([str(verifier),str(p/spec['manifest']['name']),str(p/spec['archive']['name'])],env=buildenv,check=True,timeout=20)
   app=extract_bundle(p/spec['archive']['name'],root/('extracted-'+stage))
   inspected=inspect(app,'v0.1.50' if stage=='old' else 'v0.1.51','arm64',spec['source'])
   require(inspected['binarySHA256']==spec['binarySHA256'],'publisher image hash')
   binary=(app/'Contents/MacOS/Yes Man').read_bytes()
   for path in (p/'web').rglob('*'):
    if path.is_file():require(path.read_bytes() in binary,'package web differs from immutable source: '+path.name)
   require((p/'embedded.csv').read_bytes() in binary,'package embedded CSV')
   save(e/('bundle-'+stage+'.json'),inspected)
   inventories[stage]=inventory(app);save(e/('bundle-inventory-'+stage+'.json'),inventories[stage]);bundles[stage]=app
  data=root/'data';seed(data);proof['before']=files(data);save(e/'proof.json',proof)
  target=root/'install/yesman.app';shutil.copytree(bundles['old'],target,symlinks=True)
  old_anchor=image_identity(target/'Contents/MacOS/Yes Man');shim_required=shutil.which('gh') is not None
  proof['ghAvailable']=shim_required;save(e/'proof.json',proof)
  subprocess.run(['codesign','--verify','--deep','--strict',str(target)],env=buildenv,check=True,timeout=20)
  old=run_phase('old',OLD,target,folder/'old/web',root,data,helper,catalog,inventories['old'],old_anchor,shim_required,proof)
  # Only this run's normally exited, verified private image is renamed.
  save(e/'shim-before-replacement.json',observe_install(target,data,inventories['old'],old_anchor,shim_required))
  require(files(data)==proof['before'],'pre-replacement profile changed')
  backup=root/'install/previous-0.1.50.app';target.rename(backup)
  shutil.copytree(bundles['new'],target,symlinks=True)
  require(inventory(target)==inventories['new'],'full package copy identity')
  new_anchor=image_identity(target/'Contents/MacOS/Yes Man')
  save(e/'old-shim-before-new-start.json',observe_install(backup,data,inventories['old'],old_anchor,shim_required))
  subprocess.run(['codesign','--verify','--deep','--strict',str(target)],env=buildenv,check=True,timeout=20)
  new=run_phase('new',b['candidate'],target,folder/'new/web',root,data,helper,catalog,inventories['new'],new_anchor,shim_required,proof)
  # Replacing the shim must release the retained old image's only runtime link.
  require(inventory(backup)==inventories['old'] and image_identity(backup/'Contents/MacOS/Yes Man')==old_anchor,'old image changed or retained unknown link')
  cold=run_phase('cold',b['candidate'],target,folder/'new/web',root,data,helper,catalog,inventories['new'],new_anchor,shim_required,proof)
  proof['coldCatalogReadAt']=verify_cold(new,cold)
  require(len({p['pid'] for p in proof['phases']})==3 and all(p['passed'] and p['exitCode']==0 for p in proof['phases']),'three real processes/normal exits')
  catalog_calls=[json.loads(l) for l in (e/'catalog.jsonl').read_text().splitlines()]
  require(catalog_calls and all(r['method']=='get_available_models' for r in catalog_calls),'catalog-only process')
  proof['after']=files(data);require(proof['after']==proof['before'],'external profile bytes changed')
  proof['passed']=True;proof['limits']=['No actual automatic updater or delta applied','No native file dialog/import exercised; .50 compatible CSV seeded externally','No provider/model request or Lead agent started','Same real AA snapshot in .50/.51; inherited changed-snapshot evidence remains the accepted R2 controlled test','No Apple signing/TCC continuity test','No local production App/profile use']
 except BaseException as ex:
  proof['error']=type(ex).__name__+': '+str(ex);raise
 finally:save(e/'proof.json',proof)
 print('PASS: official .50 -> final .51 manual full-package replacement; exact CSV/settings and .51 effective scores survive cold reopen')

if __name__=='__main__':main()
