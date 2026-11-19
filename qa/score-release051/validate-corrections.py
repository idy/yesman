#!/usr/bin/env python3
"""QA051 corrections: actual renderer function, inert filesystem and failure doubles."""
import ast,copy,importlib.util,json,os,pathlib,shutil,subprocess,sys,types
import validation as v
import contract as c
K=pathlib.Path(__file__).resolve().parent;R=K.parents[1]
OLD_QA='3179d0aaad2c2e7d6ee51d4b5eb9c12581143356'
def frozen(path):return subprocess.check_output(['git','-C',str(R),'show',OLD_QA+':qa/score-release051/'+path]).decode()
def refuses(fn):
 try:fn()
 except (AssertionError,ValueError) as ex:return type(ex).__name__+': '+str(ex)
 raise AssertionError('negative accepted')
def main():
 out=pathlib.Path(sys.argv[1]).resolve();out.mkdir();results=[]
 def record(name,**d):results.append({'name':name,**d})
 oldv={'__name__':'old_validation'};exec(compile(frozen('validation.py'),'old_validation','exec'),oldv)
 oldc={'__name__':'old_contract'};exec(compile(frozen('contract.py'),'old_contract','exec'),oldc)
 tree=ast.parse((K/'validate-offline.py').read_text());outer=next(x for x in tree.body if isinstance(x,ast.FunctionDef) and x.name=='main');fixture=next(x for x in outer.body if isinstance(x,ast.FunctionDef) and x.name=='fixture')
 data=out/'data';c.seed(data);ns={k:getattr(c,k) for k in dir(c) if not k.startswith('__')};ns.update(data=data,OLD_SOURCE=v.OLD_SOURCE,candidate={'source':'2'*40})
 exec(compile(ast.Module(body=[fixture],type_ignores=[]),'report-shape','exec'),ns);make=ns['fixture']
 oldjs=subprocess.check_output(['git','-C',str(R),'show',v.OLD_SOURCE+':web/crew.js']).decode();source=oldjs[oldjs.index('  function memberScores(m)'):oldjs.index('\n  function memberRow(',oldjs.index('  function memberScores(m)'))]
 code="let calls=0;const h=()=>{calls++};const f=new Function('h','FIELDS','t','optionName','formatNumber',"+json.dumps(source+';return memberScores;')+")(h,[],x=>x,()=>'',String);const result=f({score:null});if(result!==null||calls!==0)throw Error('renderer');console.log(JSON.stringify({metricCells:0,constructorCalls:calls}));"
 p=subprocess.run(['node','--input-type=module','-e',code],capture_output=True,text=True,check=True,timeout=10);render=json.loads(p.stdout)
 old=make('old');old['rows'][4]['metrics']=[]
 record('QA051-1 frozen old rejects actual renderer shape',refusal=refuses(lambda:oldc['verify_page'](old,'old',data,v.OLD_SOURCE)),renderer=render)
 c.verify_page(old,'old',data,v.OLD_SOURCE);record('QA051-1 repaired accepts actual old zero-cell shape',metricLengths=[len(r['metrics']) for r in old['rows']])
 for name,change in [('old fabricated metrics',lambda p:p['rows'][4].update(metrics=['value']*5)),('old unexpected score',lambda p:p['crew']['members'][4].update(score={'intelligence':0})),('old wrong unscored status',lambda p:p['rows'][4].update(text='Scored by Artificial Analysis'))]:
  p=copy.deepcopy(old);change(p);record(name,refusal=refuses(lambda p=p:c.verify_page(p,'old',data,v.OLD_SOURCE)))
 new,cold=make('new'),make('cold');cold['crew']['readAt']='2026-11-19T03:00:01.123456789Z'
 for name,p in [('new',new),('cold',cold)]:c.verify_page(p,name,data,'2'*40)
 record('QA051-2 frozen whole equality rejects fresh readAt',refusal=refuses(lambda:v.require(new['crew']==cold['crew'] and new['rows']==cold['rows'],'cold reopened effective values changed')))
 times=c.verify_cold(new,cold);record('QA051-2 repaired stable equality retains both valid timestamps',readAt=times)
 for name,change in [('numeric change',lambda p:p['crew']['members'][0]['score'].update(speed=999)),('provenance change',lambda p:p['crew']['members'][0]['fieldSources'].update(speed='patch')),('model identity change',lambda p:p['crew']['models'][0].update(id='different')),('member order change',lambda p:p['crew']['members'].reverse()),('patch state change',lambda p:p['crew']['patch'].update(enabled=False)),('display row change',lambda p:p['rows'][0].update(text='changed')),('malformed timestamp',lambda p:p['crew'].update(readAt='today')),('missing timestamp',lambda p:p['crew'].pop('readAt'))]:
  p=copy.deepcopy(cold);change(p);record(name,refusal=refuses(lambda p=p:c.verify_cold(new,p)))
 p=copy.deepcopy(new);p['rows'][4]['metrics']=[];record('new partial row must retain five fields',refusal=refuses(lambda:c.verify_page(p,'new',data,'2'*40)))
 # Same real link -> rename and explicit copy-fallback operations as InstallShim,
 # using inert bytes only. No gh, App, TCC or credentials are invoked.
 def bundle(path,body):
  main=path/'Contents/MacOS/Yes Man';main.parent.mkdir(parents=True);main.write_bytes(body);main.chmod(0o700);main.with_name('yesman').symlink_to('Yes Man');return main
 for mode in ['hardlink','copy']:
  root=out/mode;install=root/'install/yesman.app';profile=root/'data';profile.mkdir(parents=True)
  image=bundle(install,b'INERT OLD IMAGE\n');expected=v.inventory(install);anchor=v.image_identity(image)
  shim=profile/'.runtime/github-app-gh/bin/gh';shim.parent.mkdir(parents=True,mode=0o700)
  def publish(src):
   temp=shim.with_name('.gh-control')
   if mode=='hardlink':os.link(src,temp)
   else:temp.write_bytes(src.read_bytes());temp.chmod(0o700)
   temp.replace(shim)
  publish(image)
  if mode=='hardlink':record('QA051-3 frozen inventory rejects normal link',refusal=refuses(lambda:oldv['inventory'](install)))
  before=v.observe_install(install,profile,expected,anchor,True);record(mode+' old startup observation',observation=before)
  backup=root/'install/previous.app';install.rename(backup);newimage=bundle(install,b'INERT NEW IMAGE\n');newexpected=v.inventory(install);newanchor=v.image_identity(newimage)
  v.observe_install(backup,profile,expected,anchor,True)
  publish(newimage);after=v.observe_install(install,profile,newexpected,newanchor,True);assert v.inventory(backup)==expected
  # Hardlink path normally returns on SameFile; copy fallback replaces its copy.
  if mode=='copy':publish(newimage)
  reopened=v.observe_install(install,profile,newexpected,newanchor,True)
  record(mode+' old -> replacement -> cold operations',old=before,new=after,cold=reopened,oldMainLinks=(backup/'Contents/MacOS/Yes Man').stat().st_nlink,cliSymlink=os.readlink(newimage.with_name('yesman')))
  if mode=='hardlink':record('strict download/extracted admission remains single-link',refusal=refuses(lambda:v.inventory(install)))
  unknown=root/'unknown-alias';os.link(newimage,unknown)
  record(mode+' unknown extra main link rejected',refusal=refuses(lambda:v.observe_install(install,profile,newexpected,newanchor,True)));unknown.unlink()
  other=install/'other';other.write_bytes(b'x');other_expected=v.inventory(install,linked_main=[newimage.stat().st_dev,newimage.stat().st_ino] if mode=='hardlink' else None)
  os.link(other,root/'unknown-other');record(mode+' unrelated file link rejected',refusal=refuses(lambda:v.observe_install(install,profile,other_expected,newanchor,True)));(root/'unknown-other').unlink();other.unlink()
  shim.unlink();record(mode+' missing expected shim rejected',refusal=refuses(lambda:v.observe_install(install,profile,newexpected,newanchor,True)))
  shim.symlink_to(newimage);record(mode+' symlink shim rejected',refusal=refuses(lambda:v.observe_install(install,profile,newexpected,newanchor,True)));shim.unlink()
  unknown_path=profile/'.runtime/unknown-gh';os.link(newimage,unknown_path)
  record(mode+' wrong-path alias rejected',refusal=refuses(lambda:v.observe_install(install,profile,newexpected,newanchor,False)));unknown_path.unlink()
  publish(newimage)
  real_bin=shim.parent.with_name('bin-original');shim.parent.rename(real_bin);shim.parent.symlink_to(real_bin)
  record(mode+' linked shim parent rejected',refusal=refuses(lambda:v.observe_install(install,profile,newexpected,newanchor,True)));shim.parent.unlink();real_bin.rename(shim.parent)
  wrong_owner=dict(newanchor,owner=newanchor['owner']+1)
  record(mode+' mismatched owner identity rejected',refusal=refuses(lambda:v.observe_install(install,profile,newexpected,wrong_owner,True)))
  shim.unlink()
  shim.write_bytes(b'wrong image');shim.chmod(0o700);record(mode+' changed shim bytes rejected',refusal=refuses(lambda:v.observe_install(install,profile,newexpected,newanchor,True)));shim.unlink();publish(newimage)
  replacement=newimage.with_name('replacement');replacement.write_bytes(newimage.read_bytes());replacement.chmod(0o700);replacement.replace(newimage)
  record(mode+' same-byte main inode replacement rejected',refusal=refuses(lambda:v.observe_install(install,profile,newexpected,newanchor,True)))
 # Actual run_phase, benign child/helper/server doubles: operands must survive failure.
 spec=importlib.util.spec_from_file_location('native_corrections',K/'run-native.py');native=importlib.util.module_from_spec(spec);spec.loader.exec_module(native)
 class Event:
  def wait(self,*a):return True
 class Thread:
  def __init__(self,*a,**kw):pass
  def start(self):pass
  def join(self,*a,**kw):pass
  def is_alive(self):return False
 class Proc:
  pid=900001
  def __init__(self,early):self.code=0 if early else None
  def poll(self):return self.code
  def send_signal(self,*a):self.code=0
  def wait(self,*a,**kw):return self.code
 for name,early,cleanup_fail in [('early-exit',True,False),('bad-process',False,False),('early-exit-cleanup-error',True,True)]:
  work=out/name;(work/'evidence').mkdir(parents=True);web=work/'source-web';web.mkdir();(web/'index.html').write_text('<html><head></head></html>')
  server=types.SimpleNamespace(server_port=1111,serve_forever=lambda:None,shutdown=lambda:None,server_close=lambda:None)
  if cleanup_fail:server.shutdown=lambda:(_ for _ in ()).throw(RuntimeError('synthetic cleanup failure'))
  native.http=types.SimpleNamespace(server=types.SimpleNamespace(ThreadingHTTPServer=lambda *a:server));native.threading=types.SimpleNamespace(Event=Event,Lock=lambda:None,Thread=Thread)
  def popen(*a,early=early,**kw):server.payload.update(make('old'));return Proc(early)
  native.subprocess=types.SimpleNamespace(Popen=popen,STDOUT=subprocess.STDOUT,TimeoutExpired=subprocess.TimeoutExpired,check_output=lambda *a,**kw:'{"bundleIdentifier":"wrong","windows":[]}')
  native.inventory=lambda *a,**kw:{};native.image_identity=lambda p:{};proof={'passed':False,'phases':[]}
  error=refuses(lambda:native.run_phase('old',v.OLD,work/'unused.app',web,work,data,work/'helper',work/'catalog',{}, {},True,proof))
  phase=json.loads((work/'evidence/phase-old.json').read_text());assert (work/'evidence/page-old.json').exists() and not phase['passed'] and not proof['passed']
  if not early:assert (work/'evidence/process-old.json').exists() and phase['actualProcess']['bundleIdentifier']=='wrong'
  if cleanup_fail:assert 'App exited' in phase['error'] and phase['cleanupErrors']
  record('N1 '+name,refusal=error,pageSaved=True,processSaved=(work/'evidence/process-old.json').exists(),phase=phase)
 (out/'results.json').write_text(json.dumps({'scope':'actual pure predecessor renderer; real inert old/replacement/cold filesystem operations; synthetic reports/process doubles; no native App','controls':results,'count':len(results)},indent=2)+'\n');print('PASS',len(results),'correction controls; no App launch')
if __name__=='__main__':main()
