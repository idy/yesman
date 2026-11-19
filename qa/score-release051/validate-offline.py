#!/usr/bin/env python3
"""Bounded offline tests. Never runs the native runner's main or a real App."""
import ast,copy,importlib.util,io,json,os,pathlib,shutil,subprocess,sys,tarfile
from validation import *
from contract import *
KIT=pathlib.Path(__file__).resolve().parent

def main():
 root=pathlib.Path(sys.argv[1]).resolve();require(root.is_dir() and 'model-score-release' in str(root),'test root')
 out=root/'checks';out.mkdir();results=[]
 def test(name,fn):
  fn();results.append({'name':name,'result':'PASS'})
 def rejects(fn):
  try:fn()
  except (ValueError,AssertionError,KeyError,TypeError):return
  raise AssertionError('negative control unexpectedly accepted')
 sample={'qaSource':'1'*40,'candidate':{'source':'2'*40,'releaseID':900000001,'archive':{'id':900000002,'name':'yesman-0.1.51-darwin-arm64.tar.gz','size':100,'digest':'sha256:'+'a'*64},'manifest':{'id':900000003,'name':'update-darwin-arm64.json','size':100,'digest':'sha256:'+'b'*64},'binarySHA256':'c'*64,'assetSetSHA256':'d'*64}}
 test('strict binding positive',lambda:require(validate_binding(json.dumps(sample))==sample,'binding'))
 mutations=[('wrong QA SHA',lambda b:b.update(qaSource='main')),('missing final source',lambda b:b['candidate'].update(source=None)),('old candidate source',lambda b:b['candidate'].update(source=OLD_SOURCE)),('self candidate source',lambda b:b['candidate'].update(source=b['qaSource'])),('reused old release',lambda b:b['candidate'].update(releaseID=450776814)),('wrong archive name',lambda b:b['candidate']['archive'].update(name='../bad')),('wrong manifest name',lambda b:b['candidate']['manifest'].update(name='update-darwin-amd64.json')),('boolean size',lambda b:b['candidate']['archive'].update(size=True)),('invalid digest',lambda b:b['candidate']['archive'].update(digest='sha256:bad')),('unexpected input field',lambda b:b.update(command='id')),('same asset ID',lambda b:b['candidate']['archive'].update(id=b['candidate']['manifest']['id']))]
 for name,mut in mutations:
  b=copy.deepcopy(sample);mut(b);test(name,lambda b=b:rejects(lambda:validate_binding(json.dumps(b))))
 test('duplicate binding key',lambda:rejects(lambda:validate_binding('{"qaSource":"'+('1'*40)+'","qaSource":"'+('1'*40)+'","candidate":{}}')))
 test('binding size bound',lambda:rejects(lambda:validate_binding(' '*8193)))
 # Execute exactly the inlined downloader definitions with test functions only.
 ns={'__name__':'offline_validation'};exec(compile((KIT/'validation.py').read_text()+'\n'+(KIT/'acquire.py').read_text(),'inline-downloader','exec'),ns)
 effects=[]
 ns['gh']=lambda p:effects.append(p)
 test('incomplete binding rejects before GET or write',lambda:rejects(lambda:ns['acquire']('{}',out/'refused-acquire')))
 require(not effects and not (out/'refused-acquire').exists(),'invalid acquisition side effect')
 # Model source, release status and asset identity responses without any network.
 candidate=sample['candidate'];aa=[dict(candidate[k],state='uploaded') for k in ['archive','manifest']]
 candidate['assetSetSHA256']=asset_set_hash(aa)
 def fake_get(p):
  if '/git/ref/' in p:return {'object':{'type':'commit','sha':candidate['source']}}
  if '/git/commits/' in p:return {'sha':candidate['source'],'tree':{'sha':NEW_TREE}}
  if '/assets?' in p:return copy.deepcopy(aa)
  return {'id':candidate['releaseID'],'tag_name':'v0.1.51','draft':True,'prerelease':False}
 ns['gh']=fake_get
 test('fixed remote identity positive',lambda:ns['check_remote']('new',candidate))
 def wrong_reply(fragment,change):
  def f(p):
   d=fake_get(p)
   if fragment in p:change(d)
   return d
  ns['gh']=f;rejects(lambda:ns['check_remote']('new',candidate));ns['gh']=fake_get
 for name,fragment,change in [('moved tag','/git/ref/',lambda d:d['object'].update(sha='3'*40)),('wrong whole tree','/git/commits/',lambda d:d['tree'].update(sha='4'*40)),('published candidate','/releases/900000001',lambda d:d.update(draft=False) if isinstance(d,dict) else None),('replaced exact asset','/assets?',lambda d:d[0].update(id=999999)),('late extra asset','/assets?',lambda d:d.append({'id':7,'name':'late.bin','size':1,'digest':'sha256:'+'7'*64,'state':'uploaded'}))]:
  test(name,lambda fragment=fragment,change=change:wrong_reply(fragment,change))
 # Path refusal must precede extraction writes.
 def tarcase(name,mut):
  data=io.BytesIO()
  with tarfile.open(fileobj=data,mode='w:gz') as t:
   for path,typ,link in [('yesman.app',tarfile.DIRTYPE,''),('yesman.app/Contents',tarfile.DIRTYPE,''),('yesman.app/Contents/MacOS',tarfile.DIRTYPE,''),('yesman.app/Contents/MacOS/Yes Man',tarfile.REGTYPE,''),('yesman.app/Contents/MacOS/yesman',tarfile.SYMTYPE,'Yes Man')]+mut:
    x=tarfile.TarInfo(path);x.type=typ;x.linkname=link;x.mode=0o755;x.size=1 if typ==tarfile.REGTYPE else 0;t.addfile(x,io.BytesIO(b'x') if x.size else None)
  p=out/(name+'.tar.gz');p.write_bytes(data.getvalue());dest=out/('extract-'+name)
  rejects(lambda:extract_bundle(p,dest));require(not dest.exists(),'failed extraction wrote destination')
 for name,mut in [('traversal',[('../outside',tarfile.REGTYPE,'')]),('absolute',[('/outside',tarfile.REGTYPE,'')]),('hardlink',[('yesman.app/hard',tarfile.LNKTYPE,'yesman.app/Contents/MacOS/Yes Man')]),('escaping-symlink',[('yesman.app/escape',tarfile.SYMTYPE,'../../outside')]),('link-parent',[('yesman.app/linked',tarfile.SYMTYPE,'Contents'),('yesman.app/linked/new',tarfile.REGTYPE,'')]),('duplicate',[('yesman.app/Contents/MacOS/Yes Man',tarfile.REGTYPE,'')])]:
  test(name,lambda name=name,mut=mut:tarcase(name,mut))
 # The actual official predecessor package is read, extracted and copied, never launched.
 archive=root/'official050'/OLD['archive']['name'];verify_file(archive,OLD['archive'])
 manifest=root/'official050'/OLD['manifest']['name'];verify_file(manifest,OLD['manifest'])
 bundle=extract_bundle(archive,out/'official-unpacked');copied=out/'copied-050.app';shutil.copytree(bundle,copied,symlinks=True)
 expected=inventory(bundle)
 test('actual full package copied with exact modes/types/symlinks/bytes',lambda:require(inventory(copied)==expected,'copy inventory'))
 test('actual official image matches publisher',lambda:require(sha((copied/'Contents/MacOS/Yes Man').read_bytes())==OLD['binarySHA256'],'image'))
 test('actual official copy strict codesign',lambda:subprocess.run(['codesign','--verify','--deep','--strict',str(copied)],check=True,capture_output=True,timeout=20))
 broken=out/'dereferenced-050.app';shutil.copytree(bundle,broken,symlinks=False)
 test('dereferenced alias detected',lambda:rejects(lambda:require(inventory(broken)==expected,'copy symlink mismatch')))
 badsignature=subprocess.run(['codesign','--verify','--deep','--strict',str(broken)],capture_output=True,text=True,timeout=20)
 test('dereferenced alias rejected by actual codesign',lambda:require(badsignature.returncode!=0,'expected signature failure'))
 # Use real saved independent DTO reports only as structure references; these
 # synthetic controls prove rejection logic, never real native execution.
 data=out/'data';seed(data)
 def fixture(stage):
  members=[]
  for i,w in enumerate(BASE_VALUES if stage=='old' else FINAL_VALUES):
   patch=stage!='old' and i in [0,2];m={'key':KEYS[i],'source':'patch' if patch else 'aa','score':dict(zip(FIELDS,w),effort=OPTIONS[i]['effort'])}
   if patch:m.update(fallbackSource='aa',fieldSources={f:('patch' if (i==0 and f in ['intelligence','costPerTask']) or (i==2 and f in ['speed','minutesPerTask']) else 'aa') for f in FIELDS})
   members.append(m)
  members.append({'key':KEYS[4],'source':'patch','score':{'intelligenceMissing':True,'speed':37.125,'minutesPerTask':0},'fieldSources':{'speed':'patch','minutesPerTask':'patch'}} if stage!='old' else {'key':KEYS[4]})
  crew={'date':'2026-11-18','readAt':'2026-11-19T03:00:00.123456Z','yesman':KEYS[0],'models':[{}, {}, {}],'members':members}
  if stage!='old':crew['patch']={'path':str(data/'model-score-patch.csv'),'rows':3,'enabled':True}
  rows=[{'key':k,'text':'Scored row','metrics':['value']*5} for k in KEYS]
  if stage=='old':rows[4].update(text='Patch only · No scores: give yours',metrics=[])
  if stage!='old':
   rows[0]['metrics'][0]='0 Your CSV';rows[0]['metrics'][1]='60.7 tok/s Inherited from AA';rows[2]['metrics'][1]='123.5 tok/s Your CSV';rows[2]['metrics'][4]='0 min Your CSV';rows[4]['metrics'][0]='—';rows[4]['metrics'][1]='37.13 tok/s'
  return {'ok':True,'errors':[],'visibility':'visible','userAgent':'AppleWebKit/offline-synthetic','calls':['Yesman.'+m for m in ['State','Settings','About','Update','Crew']],'state':{'home':str(data),'agents':[],'yesmen':[]},'settings':{'settingsFile':str(data/'settings.json'),'appearance':'dark','autoFetch':0},'about':{'version':'0.1.50' if stage=='old' else '0.1.51','revision':OLD_SOURCE if stage=='old' else candidate['source']},'crew':crew,'rows':rows}
 for stage in ['old','new','cold']:test('synthetic '+stage+' contract positive',lambda stage=stage:verify_page(fixture(stage),stage,data,OLD_SOURCE if stage=='old' else candidate['source']))
 for name,change in [('wrong App version',lambda p:p['about'].update(version='0.1.50')),('wrong actual source',lambda p:p['about'].update(revision=OLD_SOURCE)),('old unpatched metric',lambda p:p['crew']['members'][0]['score'].update(intelligence=60.1)),('false inherited provenance',lambda p:p['crew']['members'][0]['fieldSources'].update(speed='patch')),('zero lost',lambda p:p['crew']['members'][2]['score'].update(minutesPerTask=None)),('patch disabled',lambda p:p['crew']['patch'].update(enabled=False)),('wrong profile',lambda p:p['state'].update(home='/not-this-profile')),('no visible native page',lambda p:p.update(visibility='hidden')),('non-readonly observer',lambda p:p['calls'].append('Yesman.Submit'))]:
  p=fixture('new');change(p);test(name,lambda p=p:rejects(lambda:verify_page(p,'new',data,candidate['source'])))
 (data/'model-score-patch.csv').write_bytes(CSV+b'\n')
 test('changed persisted CSV hash rejected',lambda:rejects(lambda:files(data)))
 (data/'model-score-patch.csv').write_bytes(CSV)
 # Test only environment constructors; never call main/require_hosted with fake flags.
 modspec=importlib.util.spec_from_file_location('native_runner',KIT/'run-native.py');native=importlib.util.module_from_spec(modspec);modspec.loader.exec_module(native)
 os.environ['GH_TOKEN']='SYNTHETIC_SENTINEL';os.environ['MYGO_UPDATER_PRIVATE_KEY']='SYNTHETIC_SENTINEL';os.environ['OPENAI_API_KEY']='SYNTHETIC_SENTINEL'
 appenv=native.app_environment(out,data,out/'web',out/'catalog','http://127.0.0.1:1');buildenv=native.build_environment(out)
 test('App environment excludes acquisition/signing/model credentials',lambda:require(not any(k in appenv for k in ['GH_TOKEN','MYGO_UPDATER_PRIVATE_KEY','OPENAI_API_KEY','GITHUB_TOKEN']),'App credential inheritance'))
 test('helper environment excludes acquisition/signing/model credentials',lambda:require(not any(k in buildenv for k in ['GH_TOKEN','MYGO_UPDATER_PRIVATE_KEY','OPENAI_API_KEY','GITHUB_TOKEN']),'build credential inheritance'))
 for k in ['GH_TOKEN','MYGO_UPDATER_PRIVATE_KEY','OPENAI_API_KEY']:os.environ.pop(k)
 # Actual entrypoint on workstation must reject, with no fake hosted flags.
 denied=subprocess.run([sys.executable,'-B',str(KIT/'run-native.py')],env={'PATH':os.environ['PATH'],'HOME':str(out/'private-home')},capture_output=True,text=True,timeout=10)
 test('actual native entry refuses workstation',lambda:require(denied.returncode!=0 and 'Refusing to launch outside a disposable GitHub-hosted runner' in denied.stderr,'host refusal'))
 (out/'native-refusal.stderr').write_text(denied.stderr)
 report={'passed':True,'scope':'offline fixture validation and real .50 static signature/copy checks only; no App launch, no .51 package','tests':results,'oldBinarySHA256':OLD['binarySHA256'],'inventoryEntries':len(expected),'copySymlink':os.readlink(copied/'Contents/MacOS/yesman'),'brokenCopyCodesign':{'exitCode':badsignature.returncode,'stderr':badsignature.stderr},'productionUnchanged':True}
 (out/'results.json').write_text(json.dumps(report,indent=2)+'\n');print('PASS',len(results),'bounded controls; actual .50 copy/signature; native launch refused')
if __name__=='__main__':main()
