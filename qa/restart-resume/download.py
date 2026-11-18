"""GET-only .48/public and .49/draft acquisition. Executes no downloaded code."""
import hashlib,io,json,os,re,subprocess,sys,tarfile
from pathlib import Path,PurePosixPath
from urllib.parse import urlsplit,unquote
from provenance import CANDIDATE,TREE,INTEGRATION,RELEASE,RELEASE_TREE,PREDECESSOR,first_attempt

def need(ok,why):
    if not ok:raise ValueError(why)
def sha(b):return hashlib.sha256(b).hexdigest()
def get(endpoint,raw=False):
    args=['gh','api','--method','GET',endpoint]
    if raw:args+=['-H','Accept: application/octet-stream']
    p=subprocess.run(args,capture_output=True,timeout=60);need(p.returncode==0,'GET failed: '+endpoint)
    return p.stdout if raw else json.loads(p.stdout)
def pages(endpoint):
    result=[]
    for page in range(1,11):
        rows=get(endpoint+('&'if'?'in endpoint else'?')+'per_page=100&page='+str(page));result+=rows
        if len(rows)<100:return result
    raise ValueError('pagination budget')
def source_mapping():
    integration=get('repos/idy/yesman/git/commits/'+INTEGRATION);released=get('repos/idy/yesman/git/commits/'+RELEASE)
    need(integration['tree']['sha']==TREE and released['tree']['sha']==RELEASE_TREE and [p['sha']for p in released['parents']]==[INTEGRATION],'immutable source parents/tree differ')
    rows=[]
    for tree in (TREE,RELEASE_TREE):
        v=get('repos/idy/yesman/git/trees/'+tree+'?recursive=1');need(not v['truncated'],'source tree truncated');rows.append({x['path']:(x['mode'],x['type'],x['sha'])for x in v['tree']if x['type']!='tree'})
    changed=sorted(k for k in rows[0].keys()|rows[1].keys()if rows[0].get(k)!=rows[1].get(k));need(changed==['CHANGELOG.md','mygo.json'],'release product differs')
    return {'passed':True,'candidate':CANDIDATE,'candidateTree':TREE,'integration':INTEGRATION,'integrationTree':TREE,'releaseSourceCommit':RELEASE,'releaseTree':RELEASE_TREE,'releaseParents':[INTEGRATION],'changedPaths':changed,'nonMetadataEntriesIdentical':True,'restTrees':rows}
def main():
    first_attempt()
    need(os.environ['GITHUB_REPOSITORY']=='idy/yesman','repository differs')
    out=Path(sys.argv[1]).absolute();need(not out.exists(),'new download destination required');out.mkdir(parents=True)
    releases=pages('repos/idy/yesman/releases');meta={'kind':'downloaded-release-and-draft','refs':{'v0.1.48':PREDECESSOR,'v0.1.49':RELEASE},'releases':[],'stages':{},'restartAcceptance':{'candidate':CANDIDATE,'tree':TREE,'productIntegration':INTEGRATION}}
    mapping=source_mapping();(out/'source-mapping.json').write_text(json.dumps(mapping,indent=2)+'\n')
    for tag,source,draft in [('v0.1.48',PREDECESSOR,False),('v0.1.49',RELEASE,True)]:
        ref=get('repos/idy/yesman/git/ref/tags/'+tag);need(ref['object']=={'sha':source,'type':'commit','url':'https://api.github.com/repos/idy/yesman/git/commits/'+source},'tag differs')
        found=[r for r in releases if r['tag_name']==tag];need(len(found)==1 and found[0]['draft']==draft and not found[0]['prerelease'],'release state differs');r=found[0]
        assets=pages('repos/idy/yesman/releases/'+str(r['id'])+'/assets');folder=out/tag;folder.mkdir();records=[]
        def download(name):
            need(re.fullmatch('[A-Za-z0-9._-]+',name),'asset name');matches=[a for a in assets if a['name']==name];need(len(matches)==1,'unique asset missing');a=matches[0];need(a['state']=='uploaded'and a['size']>0 and re.fullmatch('sha256:[0-9a-f]{64}',a['digest']),'API digest missing');raw=get('repos/idy/yesman/releases/assets/'+str(a['id']),True);need(len(raw)==a['size']and'sha256:'+sha(raw)==a['digest'],'download API digest differs');p=folder/name;p.write_bytes(raw);records.append({k:a[k]for k in('id','name','size','digest')});return p
        manifest=download('update-darwin-arm64.json');m=json.loads(manifest.read_bytes());need(m['version']==tag[1:],'manifest version')
        archive=download(unquote(urlsplit(m['url']).path.rsplit('/',1)[-1]))
        for delta in m.get('deltas',[]):download(unquote(urlsplit(delta['url']).path.rsplit('/',1)[-1]))
        with tarfile.open(archive)as t:binary=t.extractfile('yesman.app/Contents/MacOS/Yes Man').read()
        # Exact source export is data only: preserve the original web files.
        raw=get('repos/idy/yesman/tarball/'+source,True)
        with tarfile.open(fileobj=io.BytesIO(raw))as t:
            members=t.getmembers();roots={m.name.split('/')[0]for m in members};need(len(roots)==1,'source roots');root=roots.pop();cfg=json.load(t.extractfile(root+'/mygo.json'));need(cfg['version']==tag[1:]and cfg['name']=='Yes Man'and cfg['identifier']=='dev.idy.yesman','source package config')
            source_hashes={str(PurePosixPath(member.name).relative_to(root)):sha(t.extractfile(member).read()) for member in members if member.isfile()}
            if tag=='v0.1.49':meta['sourceFileHashes']=source_hashes
            for member in members:
                relative=PurePosixPath(member.name).relative_to(root);need('..'not in relative.parts and not relative.is_absolute(),'source traversal')
                if not relative.parts or relative.parts[0]!='web':continue
                need(member.isdir()or member.isfile(),'source web links refused')
                if member.isfile():dest=folder.joinpath(*relative.parts);dest.parent.mkdir(parents=True,exist_ok=True);dest.write_bytes(t.extractfile(member).read())
        if 'PublicKey'in meta:need(meta['PublicKey']==cfg['updates']['publicKey'],'updater key changed')
        meta['PublicKey']=cfg['updates']['publicKey'];meta['stages'][tag+'/darwin-arm64']={'Version':tag[1:],'Source':source,'Archive':str(archive.relative_to(out)),'Manifest':str(manifest.relative_to(out)),'Web':tag+'/web','mainName':'Yes Man','binarySHA256':sha(binary),'official':True}
        meta['releases'].append({'tag':tag,'id':r['id'],'draft':draft,'assets':records})
        need(get('repos/idy/yesman/git/ref/tags/'+tag)['object']['sha']==source,'tag moved')
    (out/'release-inputs.json').write_text(json.dumps(meta,indent=2)+'\n')
    print('PASS: untouched official .48/.49 archives, exact manifests, API asset digests, source mapping; GET only')
if __name__=='__main__':main()
