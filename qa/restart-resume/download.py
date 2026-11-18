"""GET-only .48/public and .49/draft acquisition. Executes no downloaded code."""
import hashlib,io,json,os,re,stat,subprocess,sys,tarfile
from pathlib import Path,PurePosixPath
from urllib.parse import urlsplit,unquote
import urllib.request
from acquisition_diagnostics import Diagnostic
from provenance import CANDIDATE,TREE,INTEGRATION,RELEASE,RELEASE_TREE,PREDECESSOR,first_attempt

SOURCE_TREES={PREDECESSOR:'0126ef4a6e19a665f0f8482ba31d9a2bffb49d41',RELEASE:RELEASE_TREE}
PREDECESSOR_DATA_SHA256='4a44fdde0a538f474afa55ca9ad07e4ee9d7c4ceb1ed8ef70bf4fe6f71c0540a'
DIAGNOSTIC=None

def need(ok,why):
    if not ok:raise ValueError(why)
def sha(b):return hashlib.sha256(b).hexdigest()
def get(endpoint,raw=False):
    if DIAGNOSTIC:
        DIAGNOSTIC.phase('asset' if raw else 'metadata');DIAGNOSTIC.api(endpoint)
    args=['gh','api','--method','GET',endpoint]
    if raw:args+=['-H','Accept: application/octet-stream']
    p=subprocess.run(args,capture_output=True,timeout=60)
    if DIAGNOSTIC:DIAGNOSTIC.command(p)
    need(p.returncode==0,'release GET failed')
    return p.stdout if raw else json.loads(p.stdout)
def pages(endpoint):
    result=[]
    for page in range(1,11):
        rows=get(endpoint+('&'if'?'in endpoint else'?')+'per_page=100&page='+str(page));result+=rows
        if len(rows)<100:return result
    raise ValueError('pagination budget')
def source_mapping():
    integration=get('repos/idy/yesman/git/commits/'+INTEGRATION);released=get('repos/idy/yesman/git/commits/'+RELEASE)
    need(integration['sha']==INTEGRATION and released['sha']==RELEASE and integration['tree']['sha']==TREE and released['tree']['sha']==RELEASE_TREE and [p['sha']for p in released['parents']]==[INTEGRATION],'immutable source parents/tree differ')
    rows=[]
    for tree in (TREE,RELEASE_TREE):
        v=get('repos/idy/yesman/git/trees/'+tree+'?recursive=1');need(not v['truncated'],'source tree truncated');rows.append({x['path']:(x['mode'],x['type'],x['sha'])for x in v['tree']if x['type']!='tree'})
    changed=sorted(k for k in rows[0].keys()|rows[1].keys()if rows[0].get(k)!=rows[1].get(k));need(changed==['CHANGELOG.md','mygo.json'],'release product differs')
    return {'passed':True,'candidate':CANDIDATE,'candidateTree':TREE,'integration':INTEGRATION,'integrationTree':TREE,'releaseSourceCommit':RELEASE,'releaseTree':RELEASE_TREE,'releaseParents':[INTEGRATION],'changedPaths':changed,'nonMetadataEntriesIdentical':True,'restTrees':rows}

def predecessor_data(base=None):
    """Sealed complete P web/config data; no network, checkout or execution."""
    if DIAGNOSTIC:DIAGNOSTIC.phase('verify-source-P')
    base=Path(base)if base is not None else Path(__file__).parent/'predecessor-data'
    need(all(not parent.is_symlink()for parent in [base,*base.parents]),'ordinary predecessor data root required')
    raw=(base/'inventory.json').read_bytes();need(sha(raw)==PREDECESSOR_DATA_SHA256,'sealed predecessor inventory changed');inventory=json.loads(raw)
    need(inventory['sourceCommit']==PREDECESSOR and inventory['gitTree']==SOURCE_TREES[PREDECESSOR]and inventory['scope']=='complete-web-and-config'and inventory['completeWebFiles']==107,'predecessor source identity differs')
    rows=inventory['fileObjects'];need(len(rows)==108,'complete predecessor data inventory required');files={}
    for name,row in rows.items():
        path=PurePosixPath(name);need(not path.is_absolute()and'..'not in path.parts and'\\'not in name and path.as_posix()==name and(name=='mygo.json'or name.startswith('web/')),'predecessor data path')
        target=base.joinpath(*path.parts);need(all(not parent.is_symlink()for parent in target.parents),'ordinary predecessor data parents required')
        fd=os.open(target,os.O_RDONLY|os.O_NOFOLLOW)
        with os.fdopen(fd,'rb')as handle:
            st=os.fstat(handle.fileno());need(stat.S_ISREG(st.st_mode)and st.st_nlink==1 and st.st_size==row['bytes']and st.st_size<=16<<20,'ordinary predecessor data file/size required');content=handle.read(st.st_size+1)
        need(row['mode']in('100644','100755')and bool(st.st_mode&0o111)==(row['mode']=='100755'),'predecessor regular mode differs')
        need(len(content)==row['bytes']and sha(content)==row['sha256']and hashlib.sha1(b'blob '+str(len(content)).encode()+b'\0'+content).hexdigest()==row['gitBlob'],'predecessor data differs from pinned Git blob');files[name]=content
    need({x.relative_to(base).as_posix()for x in base.rglob('*')if x.is_file()}==set(rows)|{'inventory.json'},'predecessor export coverage differs')
    hashes={name:sha(raw)for name,raw in files.items()};web={name:raw for name,raw in files.items()if name.startswith('web/')}
    need(len(web)==107 and sha(json.dumps({name:hashes[name]for name in web},sort_keys=True).encode())==inventory['webSHA256'],'predecessor complete web differs')
    cfg=json.loads(files['mygo.json']);need(cfg['version']=='0.1.48'and cfg['name']=='Yes Man'and cfg['identifier']=='dev.idy.yesman'and cfg['updates']['publicKey']=='yrQPn0gg1/gASLxIQiUCIiUQ/csWENI0+up1cs3KWO0=','predecessor package config differs')
    proof={'sourceCommit':PREDECESSOR,'gitTree':SOURCE_TREES[PREDECESSOR],'scope':'complete-web-and-config','acquisition':'sealed-predecessor-data-only','inventorySHA256':PREDECESSOR_DATA_SHA256,'gitBlobObjectsVerified':True,'completeWebInventory':True,'regularFiles':len(rows),'webSHA256':inventory['webSHA256'],'fileObjects':rows,'packageBinding':inventory['packageBinding']}
    return cfg,hashes,web,proof

class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self,*args,**kwargs):return None

def public_source(source):
    """Fixed keyless S codeload bytes, no API media override/credentials/redirects.
    This is the sole full-S source route, never an online fallback for P.
    """
    need(source==RELEASE,'only fixed S source allowed')
    if DIAGNOSTIC:DIAGNOSTIC.phase('source-S')
    url='https://codeload.github.com/idy/yesman/legacy.tar.gz/'+source
    opener=urllib.request.build_opener(urllib.request.ProxyHandler({}),NoRedirect())
    request=urllib.request.Request(url,headers={'User-Agent':'t199-fixed-public-source'})
    with opener.open(request,timeout=60)as response:
        need(response.status==200 and response.geturl()==url,'fixed source response differs')
        raw=response.read((32<<20)+1)
    need(len(raw)<=32<<20 and raw[:2]==b'\x1f\x8b','source archive size/format')
    return raw

def source_contents(raw,source,entries):
    """Verify full source coverage and every regular Git blob before copying web."""
    need(source in SOURCE_TREES and len(raw)<=32<<20,'fixed source/budget required')
    role='P' if source==PREDECESSOR else 'S'
    if DIAGNOSTIC:DIAGNOSTIC.phase('verify-source-'+role)
    need(entries and len(entries)<=5000,'source object budget')
    for name,(mode,kind,oid)in entries.items():
        path=PurePosixPath(name)
        need(not path.is_absolute()and'..'not in path.parts and'\\'not in name and path.as_posix()==name,'source object path')
        need(kind=='blob'and mode in ('100644','100755')and re.fullmatch('[0-9a-f]{40}',oid),'source regular Git blobs required')
    files={};objects={};total=0
    with tarfile.open(fileobj=io.BytesIO(raw))as t:
        members=t.getmembers();need(len(members)<=10000,'source member budget')
        roots={m.name.split('/')[0]for m in members};need(len(roots)==1,'source roots');root=roots.pop()
        for member in members:
            path=PurePosixPath(member.name)
            need(not path.is_absolute()and'..'not in path.parts and'\\'not in member.name,'source traversal')
            relative=path.relative_to(root)
            if member.isdir():continue
            name=relative.as_posix();need(member.isfile()and name in entries and name not in files,'source member differs from Git tree')
            need(0<=member.size<=16<<20,'source file budget');total+=member.size;need(total<=128<<20,'expanded source budget')
            content=t.extractfile(member).read();mode,kind,oid=entries[name]
            need(len(content)==member.size and bool(member.mode&0o111)==(mode=='100755'),'source size/mode differs')
            actual=hashlib.sha1(b'blob '+str(len(content)).encode()+b'\0'+content).hexdigest();need(actual==oid,'source bytes differ from pinned Git blob')
            files[name]=content;objects[name]={'gitBlob':oid,'sha256':sha(content),'bytes':len(content),'mode':mode}
    need(set(files)==set(entries),'source Git tree coverage differs')
    cfg=json.loads(files['mygo.json']);need(cfg['version']==('0.1.48' if source==PREDECESSOR else '0.1.49')and cfg['name']=='Yes Man'and cfg['identifier']=='dev.idy.yesman','source package config')
    proof={'sourceCommit':source,'gitTree':SOURCE_TREES[source],'acquisition':'keyless-fixed-codeload-no-redirect','archiveSHA256':sha(raw),'archiveBytes':len(raw),'gitBlobObjectsVerified':True,'regularFiles':len(objects),'fileObjects':objects}
    return cfg,{name:row['sha256']for name,row in objects.items()},{name:content for name,content in files.items()if name.startswith('web/')},proof

def acquire():
    need(os.environ['GITHUB_REPOSITORY']=='idy/yesman','repository differs')
    out=Path(sys.argv[1]).absolute();need(not out.exists(),'new download destination required');out.mkdir(parents=True)
    releases=pages('repos/idy/yesman/releases');meta={'kind':'downloaded-release-and-draft','refs':{'v0.1.48':PREDECESSOR,'v0.1.49':RELEASE},'releases':[],'stages':{},'restartAcceptance':{'candidate':CANDIDATE,'tree':TREE,'productIntegration':INTEGRATION}}
    mapping=source_mapping();mapping['sourceExports']={}
    old=get('repos/idy/yesman/git/commits/'+PREDECESSOR);need(old['sha']==PREDECESSOR and old['tree']['sha']==SOURCE_TREES[PREDECESSOR],'fixed P commit/tree differs')
    pcfg,phashes,pweb,pproof=predecessor_data()
    for tag,source,draft in [('v0.1.48',PREDECESSOR,False),('v0.1.49',RELEASE,True)]:
        ref=get('repos/idy/yesman/git/ref/tags/'+tag);need(ref['object']=={'sha':source,'type':'commit','url':'https://api.github.com/repos/idy/yesman/git/commits/'+source},'tag differs')
        found=[r for r in releases if r['tag_name']==tag];need(len(found)==1 and found[0]['draft']==draft and not found[0]['prerelease'],'release state differs');r=found[0]
        assets=pages('repos/idy/yesman/releases/'+str(r['id'])+'/assets');folder=out/tag;folder.mkdir();records=[]
        if source==PREDECESSOR:
            binding=pproof['packageBinding'];need(r['id']==binding['releaseID'],'selected P release differs')
            current={a['name']:a for a in assets}
            for name,expected in binding['assets'].items():need(name in current and {key:current[name][key]for key in expected}==expected,'selected P asset identity differs')
        def download(name):
            need(re.fullmatch('[A-Za-z0-9._-]+',name),'asset name');matches=[a for a in assets if a['name']==name];need(len(matches)==1,'unique asset missing');a=matches[0];need(a['state']=='uploaded'and a['size']>0 and re.fullmatch('sha256:[0-9a-f]{64}',a['digest']),'API digest missing');raw=get('repos/idy/yesman/releases/assets/'+str(a['id']),True);need(len(raw)==a['size']and'sha256:'+sha(raw)==a['digest'],'download API digest differs');p=folder/name;p.write_bytes(raw);records.append({k:a[k]for k in('id','name','size','digest')});return p
        manifest=download('update-darwin-arm64.json');m=json.loads(manifest.read_bytes());need(m['version']==tag[1:],'manifest version')
        archive=download(unquote(urlsplit(m['url']).path.rsplit('/',1)[-1]))
        for delta in m.get('deltas',[]):download(unquote(urlsplit(delta['url']).path.rsplit('/',1)[-1]))
        with tarfile.open(archive)as t:binary=t.extractfile('yesman.app/Contents/MacOS/Yes Man').read()
        # Treat exact source as data. Every selected P blob and every full-S
        # blob is verified; no fetched source is checked out or executed.
        if source==PREDECESSOR:
            need(sha(binary)==pproof['packageBinding']['mainSHA256'],'selected P main differs')
            cfg,hashes,web,proof=pcfg,phashes,pweb,pproof
        else:cfg,hashes,web,proof=source_contents(public_source(source),source,mapping['restTrees'][1])
        mapping['sourceExports'][source]=proof
        if tag=='v0.1.49':meta['sourceFileHashes']=hashes
        for name,content in web.items():
            dest=folder.joinpath(*PurePosixPath(name).parts);dest.parent.mkdir(parents=True,exist_ok=True);dest.write_bytes(content)
        if 'PublicKey'in meta:need(meta['PublicKey']==cfg['updates']['publicKey'],'updater key changed')
        meta['PublicKey']=cfg['updates']['publicKey'];meta['stages'][tag+'/darwin-arm64']={'Version':tag[1:],'Source':source,'Archive':str(archive.relative_to(out)),'Manifest':str(manifest.relative_to(out)),'Web':tag+'/web','mainName':'Yes Man','binarySHA256':sha(binary),'official':True}
        meta['releases'].append({'tag':tag,'id':r['id'],'draft':draft,'assets':records})
        need(get('repos/idy/yesman/git/ref/tags/'+tag)['object']['sha']==source,'tag moved')
    (out/'source-mapping.json').write_text(json.dumps(mapping,indent=2)+'\n')
    (out/'release-inputs.json').write_text(json.dumps(meta,indent=2)+'\n')
    print('PASS: untouched official .48/.49 archives, exact manifests/API digests, sealed complete P web/config, full S Git objects and source mapping; GET only')

def main():
    first_attempt()
    global DIAGNOSTIC
    try:
        with Diagnostic('download')as diagnostic:
            DIAGNOSTIC=diagnostic
            diagnostic.phase('tool-version');version=subprocess.run(['gh','--version'],capture_output=True,timeout=10)
            diagnostic.command(version);need(version.returncode==0,'installed gh version unavailable');diagnostic.version('gh',version.stdout)
            acquire()
    except Exception:
        raise SystemExit('t199 release acquisition failed; see sanitized T199_ACQUISITION_STATUS and diagnostics artifact')from None
    finally:DIAGNOSTIC=None
if __name__=='__main__':main()
