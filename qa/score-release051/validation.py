"""Public identity and data validation shared by the read-only native consumer."""
import hashlib,json,os,pathlib,posixpath,re,stat,tarfile
KEY='yrQPn0gg1/gASLxIQiUCIiUQ/csWENI0+up1cs3KWO0='
OLD_SOURCE='87c3bc4faa0a55313cd68be77058fea166beca82'
OLD_TREE='111aeca1331a304acbc90866831e634eb805d981'
NEW_SOURCE='3b7d9cc50863d27323828a6a75812cdb814cf373'
NEW_TREE='105dcd89100bc7dc81a5a48c1a9cc14697a717b0'
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
 require(c['source']==NEW_SOURCE and c['source']!=b['qaSource'],'candidate source')
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
def inventory(root,files_only=False,linked_main=None):
 require(root.is_dir() and not root.is_symlink(),'inventory root')
 result={}
 for p in sorted(root.rglob('*')):
  n=p.relative_to(root).as_posix();st=p.lstat();mode=stat.S_IMODE(st.st_mode)
  if stat.S_ISLNK(st.st_mode):
   require(not files_only,'linked web file');result[n]={'type':'link','mode':mode,'target':os.readlink(p)}
  elif stat.S_ISREG(st.st_mode):
   require(st.st_nlink==1 or (not files_only and n=='Contents/MacOS/Yes Man' and linked_main==[st.st_dev,st.st_ino] and st.st_nlink==2),'hard-linked file')
   h=sha(p.read_bytes());result[n]=h if files_only else {'type':'file','mode':mode,'sha256':h}
  elif stat.S_ISDIR(st.st_mode):
   if not files_only:result[n]={'type':'directory','mode':mode}
  else:raise ValueError('unexpected filesystem entry')
 return result

def image_identity(path):
 st=path.lstat();require(stat.S_ISREG(st.st_mode) and st.st_uid==os.getuid(),'image type/owner')
 return {'device':st.st_dev,'inode':st.st_ino,'owner':st.st_uid,'mode':stat.S_IMODE(st.st_mode),'sha256':sha(path.read_bytes())}

def observe_install(root,data,expected,anchor,shim_required):
 """Postlaunch exception only for the proven private gh alias of this image."""
 main=root/'Contents/MacOS/Yes Man'
 require(image_identity(main)==anchor,'installed main identity replaced or changed')
 for parent in [data,data/'.runtime',data/'.runtime/github-app-gh',data/'.runtime/github-app-gh/bin']:
  if parent.exists() or parent.is_symlink():
   st=parent.lstat();require(stat.S_ISDIR(st.st_mode) and st.st_uid==os.getuid(),'private shim parent type/owner')
 shim=data/'.runtime/github-app-gh/bin/gh';linked=None;record={'kind':'absent','path':str(shim),'main':anchor,'mainLinks':main.stat().st_nlink}
 if shim.exists() or shim.is_symlink():
  identity=image_identity(shim);st=shim.lstat();ms=main.lstat()
  require(identity['sha256']==anchor['sha256'],'shim image bytes differ')
  if (st.st_dev,st.st_ino)==(ms.st_dev,ms.st_ino):
   require(st.st_nlink==ms.st_nlink==2 and identity==anchor,'unknown main hardlink or changed shim')
   linked=[ms.st_dev,ms.st_ino];record.update(kind='exact-main-hardlink',alias=identity,aliasLinks=st.st_nlink)
  else:
   # InstallShim's explicit cross-volume copy fallback uses mode 0700.
   require(st.st_nlink==ms.st_nlink==1 and identity['mode']==0o700,'unknown shim link or copy mode')
   record.update(kind='separate-copy',alias=identity,aliasLinks=st.st_nlink)
 else:
  require(not shim_required and main.stat().st_nlink==1,'expected private shim missing or unknown main link')
 require(inventory(root,linked_main=linked)==expected,'launched bundle inventory changed')
 return record
