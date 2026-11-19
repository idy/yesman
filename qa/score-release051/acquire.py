"""Inlined into the privileged no-checkout job. Only GH GET/download and data writes."""
import io,subprocess,sys

def gh(path):
 # Path callers are fixed GitHub metadata endpoints; no POST or shell.
 return parse(subprocess.check_output(['gh','api','--method','GET',path],timeout=30).decode())
def resolve(tag):
 obj=gh('repos/idy/yesman/git/ref/tags/'+tag)['object']
 for _ in range(8):
  require(re.fullmatch('[0-9a-f]{40}',obj['sha']),'tag OID')
  if obj['type']=='commit':return obj['sha']
  require(obj['type']=='tag','tag object type')
  obj=gh('repos/idy/yesman/git/tags/'+obj['sha'])['object']
 raise ValueError('tag depth')
def assets(release_id):
 result=[]
 for page in range(1,5):
  rows=gh('repos/idy/yesman/releases/'+str(release_id)+'/assets?per_page=100&page='+str(page));result.extend(rows)
  if len(rows)<100:return result
 raise ValueError('asset page budget')
def check_remote(stage,spec):
 tag='v0.1.50' if stage=='old' else 'v0.1.51'
 require(resolve(tag)==spec['source'],'tag differs from fixed source')
 commit=gh('repos/idy/yesman/git/commits/'+spec['source'])
 require(commit['sha']==spec['source'] and commit['tree']['sha']==(OLD_TREE if stage=='old' else NEW_TREE),'tag tree differs from reviewed tree')
 r=gh('repos/idy/yesman/releases/'+str(spec['releaseID']))
 require(r['id']==spec['releaseID'] and r['tag_name']==tag and r['draft']==(stage=='new') and not r['prerelease'],'release state/identity')
 aa=assets(spec['releaseID'])
 for k in ['archive','manifest']:
  found=[a for a in aa if a['name']==spec[k]['name']]
  require(len(found)==1 and asset_identity(found[0])==spec[k] and found[0]['state']=='uploaded','exact asset identity')
 if stage=='new':require(asset_set_hash(aa)==spec['assetSetSHA256'],'final asset set changed')
 return {'source':spec['source'],'releaseID':r['id'],'tag':tag,'draft':r['draft'],'assets':[spec['archive'],spec['manifest']]}
def source_data(spec,folder):
 body=subprocess.check_output(['gh','api','--method','GET','repos/idy/yesman/tarball/'+spec['source']],timeout=60)
 require(len(body)<30*1024*1024,'source archive budget')
 with tarfile.open(fileobj=io.BytesIO(body)) as t:
  members=t.getmembers();require(len(members)<10000,'source member budget')
  roots={m.name.split('/')[0] for m in members};require(len(roots)==1,'source root');root=roots.pop()
  seen=set();web={};csvbytes=None;config=None;total=0
  for m in members:
   n=relative(m.name);parts=pathlib.PurePosixPath(n).parts;require(parts[0]==root,'source root changed')
   n='/'.join(parts[1:]);require(n not in seen,'duplicate source path');seen.add(n)
   wanted=n=='mygo.json' or n=='internal/crew/scores/aa-scores-2026-11-18.csv' or n.startswith('web/')
   if not wanted or m.isdir():continue
   require(m.isfile() and m.size<10*1024*1024,'source data type/budget');total+=m.size;require(total<30*1024*1024,'source data total')
   data=t.extractfile(m).read()
   if n=='mygo.json':config=parse(data)
   elif n.endswith('aa-scores-2026-11-18.csv'):csvbytes=data
   else:
    target=folder/n;target.parent.mkdir(parents=True,exist_ok=True);target.write_bytes(data);web[n[4:]]=sha(data)
  require(config['version']==('0.1.50' if spec['source']==OLD_SOURCE else '0.1.51') and config['identifier']=='dev.idy.yesman' and config['name']=='Yes Man' and config['updates']['publicKey']==KEY,'source config')
  require(csvbytes is not None and sha(csvbytes)==BASELINE_CSV_SHA,'real embedded CSV')
  require('index.html' in web and 'crew.js' in web and len(web)<=1000,'source web set')
  (folder/'embedded.csv').write_bytes(csvbytes)
 return web

def acquire(raw,out):
 # Input is strictly data and must be fully valid before first network/write.
 b=validate_binding(raw)
 require(os.environ.get('GITHUB_REPOSITORY')=='idy/yesman' and os.environ.get('GITHUB_REF')=='refs/heads/qa/model-score-release-051','workflow repository/ref')
 require(os.environ.get('GITHUB_SHA')==b['qaSource'] and os.environ.get('GITHUB_RUN_ATTEMPT')=='1','workflow source/attempt')
 run=os.environ.get('GITHUB_RUN_ID','');require(run.isdigit(),'workflow run')
 out=pathlib.Path(out).resolve();require(out.parent==pathlib.Path(os.environ['RUNNER_TEMP']).resolve() and not out.exists(),'fresh transfer path')
 specs=[('old',OLD),('new',b['candidate'])]
 remote={stage:check_remote(stage,spec) for stage,spec in specs}
 out.mkdir(mode=0o700)
 for stage,spec in specs:
  folder=out/stage;folder.mkdir()
  for key in ['archive','manifest']:
   a=spec[key]
   subprocess.run(['gh','release','download',remote[stage]['tag'],'--repo','idy/yesman','--pattern',a['name'],'--dir',str(folder)],check=True,timeout=60)
   verify_file(folder/a['name'],a)
  remote[stage]['webSHA256']=source_data(spec,folder)
 for stage,spec in specs:
  observed=check_remote(stage,spec)
  require(all(remote[stage][k]==observed[k] for k in observed),'release changed during acquisition')
 result={'qaSource':b['qaSource'],'runID':run,'attempt':'1','binding':b,'stages':remote}
 (out/'transfer.json').write_text(json.dumps(result,indent=2)+'\n')
 print('PASS: exact official .50 and final draft .51 acquired; application execution is in a separate job')

if __name__=='__main__':
 acquire(os.environ['EXPECTED_BINDING'],pathlib.Path(os.environ['RUNNER_TEMP'])/'score-pair-inputs')
