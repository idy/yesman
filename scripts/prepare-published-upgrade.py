#!/usr/bin/env python3
"""Download immutable predecessor + draft inputs for the .35 -> .36 hard gate.
Run only in the privileged download step; no application or signing key here.
"""
import hashlib,io,json,os,pathlib,re,subprocess,sys,tarfile,urllib.parse,urllib.request

def gh(path):return json.loads(subprocess.check_output(['gh','api',path]))
def commit(tag):
    obj=gh('repos/idy/yesman/git/ref/tags/'+tag)['object']
    for _ in range(8):
        if obj['type']=='commit':assert re.fullmatch('[0-9a-f]{40}',obj['sha']);return obj['sha']
        assert obj['type']=='tag';obj=gh('repos/idy/yesman/git/tags/'+obj['sha'])['object']
    raise AssertionError('tag cycle/depth')
def release(tag):
    for page in range(1,11):
        values=gh('repos/idy/yesman/releases?per_page=100&page='+str(page))
        matches=[r for r in values if r['tag_name']==tag]
        if matches:assert len(matches)==1;return matches[0]
        if len(values)<100:break
    raise AssertionError('release not visible: '+tag)
def main():
    oldtag,newtag,output=sys.argv[1:4]
    assert oldtag=='v0.1.35' and newtag=='v0.1.36','This gate is only the allocated naming transition'
    out=pathlib.Path(output).resolve();out.mkdir(parents=True,exist_ok=True)
    refs={t:commit(t) for t in (oldtag,newtag)}
    old,new=release(oldtag),release(newtag)
    assert not old['draft'] and not old['prerelease'] and new['draft'] and not new['prerelease']
    for target in ('darwin-amd64','darwin-arm64','windows-amd64','windows-arm64','linux-amd64','linux-arm64'):
        with urllib.request.urlopen('https://github.com/idy/yesman/releases/latest/download/update-'+target+'.json') as r:assert json.load(r)['version']=='0.1.35','ordinary predecessor feed not propagated'
    metadata={'kind':'downloaded-release-and-draft','refs':refs,'releases':[],'stages':{}}
    for item,tag in ((old,oldtag),(new,newtag)):
        folder=out/tag;folder.mkdir(exist_ok=True)
        assets={}
        for a in item['assets']:assets.setdefault(a['name'],[]).append(a)
        def download(name):
            assert re.fullmatch('[A-Za-z0-9._-]+',name),name
            matches=assets.get(name,[]);assert len(matches)==1,name
            asset=matches[0];assert asset['size']>0 and re.fullmatch('sha256:[0-9a-f]{64}',asset.get('digest',''))
            subprocess.run(['gh','release','download',tag,'-R','idy/yesman','-p',name,'-D',str(folder),'--clobber'],check=True)
            path=folder/name;assert path.stat().st_size==asset['size'] and hashlib.sha256(path.read_bytes()).hexdigest()==asset['digest'][7:]
            return path,{'id':asset['id'],'name':name,'size':asset['size'],'digest':asset['digest']}
        records=[]
        for target in ('darwin-amd64','darwin-arm64','windows-amd64','linux-amd64'):
            manifest,record=download('update-'+target+'.json');records.append(record);m=json.loads(manifest.read_text(encoding='utf-8'));assert m['version']==tag[1:]
            artifact,record=download(urllib.parse.unquote(urllib.parse.urlparse(m['url']).path.rsplit('/',1)[-1]));records.append(record)
            for d in m.get('deltas',[]):_,record=download(urllib.parse.unquote(urllib.parse.urlparse(d['url']).path.rsplit('/',1)[-1]));records.append(record)
            name='yesman' if tag==oldtag else 'Yes Man';entry='yesman.app/Contents/MacOS/'+name if target.startswith('darwin-') else name+('.exe' if target.startswith('windows-') else '')
            with tarfile.open(artifact) as archive:binary=archive.extractfile(entry).read()
            metadata['stages'][tag+'/'+target]={'Version':tag[1:],'Source':refs[tag],'Archive':str(artifact.relative_to(out)),'Manifest':str(manifest.relative_to(out)),'Web':tag+'/web','mainName':name,'binarySHA256':hashlib.sha256(binary).hexdigest(),'official':True}
        metadata['releases'].append({'tag':tag,'id':item['id'],'draft':item['draft'],'assets':records})
        subprocess.run(['git','fetch','--no-tags','origin',refs[tag]],check=True,stdout=subprocess.DEVNULL)
        web=subprocess.check_output(['git','archive',refs[tag],'web'])
        with tarfile.open(fileobj=io.BytesIO(web)) as archive:archive.extractall(folder,filter='data')
        cfg=json.loads(subprocess.check_output(['git','show',refs[tag]+':mygo.json']))
        assert cfg['identifier']=='dev.idy.yesman' and cfg['name']==('yesman' if tag==oldtag else 'Yes Man')
        if 'PublicKey' in metadata:assert metadata['PublicKey']==cfg['updates']['publicKey']
        metadata['PublicKey']=cfg['updates']['publicKey']
    assert refs=={t:commit(t) for t in refs},'immutable tag changed during download'
    (out/'release-inputs.json').write_text(json.dumps(metadata,indent=2),encoding='utf-8')
    print('PASS: exact public .35 + private .36 draft assets/API digests/tag sources and six ordinary predecessor manifests')
if __name__=='__main__':main()
