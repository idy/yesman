"""Public identity and data validation shared by the read-only native consumer."""
import hashlib,json,os,pathlib,posixpath,re,stat,tarfile
KEY='yrQPn0gg1/gASLxIQiUCIiUQ/csWENI0+up1cs3KWO0='
OLD_SOURCE='87c3bc4faa0a55313cd68be77058fea166beca82'
OLD_TREE='111aeca1331a304acbc90866831e634eb805d981'
NEW_TREE='c970394849af0fca550f0150ef14bc1661cb0a60'
BASELINE_CSV_SHA='5e9c21eb8f04a75dda06fe5a4f4323aadd99cef6c7cfb8595707bb8576049ed1'
OLD={'source':OLD_SOURCE,'releaseID':450776814,'binarySHA256':'fd588ec89b9e8f05951bdc3fb32cfa249ab6c85f34aac4d55dcb094492a74e2e',
     'archive':{'id':765802737,'name':'yesman-0.1.50-darwin-arm64.tar.gz','size':7829476,'digest':'sha256:c56d0cd18f86cd521197b32a1937c80303861e4ecd511b2f773c5b6b0bb433a1'},
     'manifest':{'id':765802864,'name':'update-darwin-arm64.json','size':1948,'digest':'sha256:8c616ad3ba2aaeae3c36bf380d25a46369b08313b980dedd3ad4776094864855'}}
def require(ok,why):
 if not ok:raise ValueError(why)
def sha(body):return hashlib.sha256(body).hexdigest()
def unique(pairs):
 d={}
 for k,v in pairs:
  require(k not in d,'duplicate JSON key');d[k]=v
 return d
def parse(raw):return json.loads(raw,object_pairs_hook=unique)
def validate_binding(raw):
 require(isinstance(raw,str) and len(raw.encode())<=8192,'binding size/type')
 b=parse(raw)
 require(isinstance(b,dict) and set(b)=={'qaSource','candidate'},'binding schema')
 require(isinstance(b['qaSource'],str) and re.fullmatch('[0-9a-f]{40}',b['qaSource']),'QA source')
 c=b['candidate']
 require(isinstance(c,dict) and set(c)=={'source','releaseID','archive','manifest','binarySHA256','assetSetSHA256'},'candidate schema')
 require(isinstance(c['source'],str) and re.fullmatch('[0-9a-f]{40}',c['source']) and c['source'] not in [OLD_SOURCE,b['qaSource']],'candidate source')
 require(type(c['releaseID']) is int and c['releaseID']>0 and c['releaseID']!=OLD['releaseID'],'candidate release ID')
 for key in ['binarySHA256','assetSetSHA256']:
  require(isinstance(c[key],str) and re.fullmatch('[0-9a-f]{64}',c[key]),key)
 require(c['binarySHA256']!=OLD['binarySHA256'],'candidate reuses old binary')
 for key,name in [('archive','yesman-0.1.51-darwin-arm64.tar.gz'),('manifest','update-darwin-arm64.json')]:
  a=c[key];require(isinstance(a,dict) and set(a)=={'id','name','size','digest'},'asset schema')
  require(type(a['id']) is int and a['id']>0 and a['id']!=OLD[key]['id'],'asset ID')
  require(a['name']==name and type(a['size']) is int and 0<a['size']<100*1024*1024,'asset name/size')
  require(isinstance(a['digest'],str) and re.fullmatch('sha256:[0-9a-f]{64}',a['digest']),'asset digest')
 require(c['archive']['id']!=c['manifest']['id'],'distinct assets')
 return b
def asset_identity(a):return {k:a[k] for k in ['id','name','size','digest']}
def asset_set_hash(assets):
 require(len({a['id'] for a in assets})==len(assets) and len({a['name'] for a in assets})==len(assets),'duplicate assets')
 return sha(json.dumps(sorted([asset_identity(a) for a in assets],key=lambda x:x['name']),sort_keys=True,separators=(',',':')).encode())
def verify_file(p,a):
 require(p.is_file() and not p.is_symlink() and p.stat().st_nlink==1,'nonprivate regular input')
 body=p.read_bytes();require(len(body)==a['size'] and 'sha256:'+sha(body)==a['digest'],'asset size/digest');return body
def validate_transfer(folder,b,run_id):
 meta=parse((folder/'transfer.json').read_text())
 require(meta['qaSource']==b['qaSource'] and meta['runID']==run_id and meta['attempt']=='1','same-run transfer')
 require(meta['binding']==b and set(meta['stages'])=={'old','new'},'transfer binding')
 for stage,spec in [('old',OLD),('new',b['candidate'])]:
  p=folder/stage
  require(meta['stages'][stage]['source']==spec['source'],'source transfer')
  for k in ['archive','manifest']:verify_file(p/spec[k]['name'],spec[k])
  manifest=parse((p/spec['manifest']['name']).read_text())
  tag='v0.1.50' if stage=='old' else 'v0.1.51'
  require(manifest['version']==tag[1:] and manifest['size']==spec['archive']['size'],'manifest version/size')
  require(manifest['url']=='https://github.com/idy/yesman/releases/download/'+tag+'/'+spec['archive']['name'],'manifest URL')
  require(isinstance(manifest['signature'],str),'manifest signature')
  web=meta['stages'][stage]['webSHA256']
  require(0<len(web)<=1000 and 'index.html' in web and 'crew.js' in web,'web inventory')
  require(inventory(p/'web',files_only=True)==web,'exact web inventory')
  require(sha((p/'embedded.csv').read_bytes())==BASELINE_CSV_SHA,'embedded CSV')
 return meta
def relative(name):
 p=pathlib.PurePosixPath(name)
 require(name and not name.startswith('/') and '\\' not in name and '..' not in p.parts and '\x00' not in name,'unsafe path')
 return name.removeprefix('./').rstrip('/')
def extract_bundle(archive,dest):
 require(not dest.exists() and not dest.is_symlink(),'extraction destination exists')
 with tarfile.open(archive) as t:
  members=t.getmembers();require(0<len(members)<=1000,'archive member budget')
  names={};total=0
  for m in members:
   n=relative(m.name);require(n not in names and (n=='yesman.app' or n.startswith('yesman.app/')),'archive root/duplicate');names[n]=m
   require(m.isdir() or m.isfile() or m.issym(),'archive member type')
   require(not m.mode&0o7000,'special file modes')
   if m.isfile():total+=m.size;require(m.size>=0 and total<=300*1024*1024,'archive bytes budget')
   if m.issym():
    require(m.linkname and not m.linkname.startswith('/') and '\\' not in m.linkname,'unsafe link')
    resolved=posixpath.normpath(posixpath.join(posixpath.dirname(n),m.linkname))
    require(resolved.startswith('yesman.app/') and '..' not in pathlib.PurePosixPath(resolved).parts,'escaping link')
  for n in names:
   for parent in pathlib.PurePosixPath(n).parents:
    if str(parent) in names:require(names[str(parent)].isdir(),'archive writes through non-directory')
  require('yesman.app/Contents/MacOS/Yes Man' in names and names['yesman.app/Contents/MacOS/Yes Man'].isfile(),'main image')
  alias=names.get('yesman.app/Contents/MacOS/yesman')
  require(alias is not None and alias.issym() and alias.linkname=='Yes Man','CLI symlink')
  dest.mkdir(mode=0o700)
  t.extractall(dest,members=members,filter='data')
 return dest/'yesman.app'
def inventory(root,files_only=False):
 require(root.is_dir() and not root.is_symlink(),'inventory root')
 result={}
 for p in sorted(root.rglob('*')):
  n=p.relative_to(root).as_posix();st=p.lstat();mode=stat.S_IMODE(st.st_mode)
  if stat.S_ISLNK(st.st_mode):
   require(not files_only,'linked web file');result[n]={'type':'link','mode':mode,'target':os.readlink(p)}
  elif stat.S_ISREG(st.st_mode):
   require(st.st_nlink==1,'hard-linked file');h=sha(p.read_bytes());result[n]=h if files_only else {'type':'file','mode':mode,'sha256':h}
  elif stat.S_ISDIR(st.st_mode):
   if not files_only:result[n]={'type':'directory','mode':mode}
  else:raise ValueError('unexpected filesystem entry')
 return result
