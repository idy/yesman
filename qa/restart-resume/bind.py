"""Trusted GET-only transfer validator, before the credential-free native job."""
import hashlib,io,json,os,re,shutil,stat,subprocess,sys,tarfile,zipfile
from pathlib import Path,PurePosixPath
import provenance as p

def need(ok,why):
    if not ok:raise ValueError(why)
def sha(raw):return hashlib.sha256(raw).hexdigest()
def save(path,value):
    path.parent.mkdir(parents=True,exist_ok=True);path.write_text(json.dumps(value,indent=2)+'\n')
def get(endpoint,raw=False):
    args=['gh','api','--method','GET',endpoint]
    if raw:args+=['-H','Accept: application/octet-stream']
    q=subprocess.run(args,capture_output=True,timeout=120);need(q.returncode==0,'GET failed: '+endpoint)
    return q.stdout if raw else json.loads(q.stdout)
def transfer(artifact_id,digest,out):
    p.first_attempt()
    # upload-artifact emits bare hex; REST and durable receipts use sha256:hex.
    need(isinstance(digest,str) and re.fullmatch('[0-9a-fA-F]{64}',digest),'upload action must supply a bare SHA256 digest')
    upload_sha256=digest.lower();digest='sha256:'+upload_sha256
    a=get('repos/idy/yesman/actions/artifacts/'+str(artifact_id));run=get('repos/idy/yesman/actions/runs/'+os.environ['GITHUB_RUN_ID'])
    need(isinstance(a.get('digest'),str) and re.fullmatch('sha256:[0-9a-f]{64}',a['digest']),'artifact REST digest must be canonical SHA256')
    need(a['id']==artifact_id and not a['expired']and a['digest']==digest and a['workflow_run']['id']==int(os.environ['GITHUB_RUN_ID']),'selected artifact API identity differs')
    need(run['head_sha']==os.environ['GITHUB_SHA'] and run['run_attempt']==int(os.environ['GITHUB_RUN_ATTEMPT']) and run['path']==p.WORKFLOW,'selected workflow run differs')
    raw=get('repos/idy/yesman/actions/artifacts/'+str(artifact_id)+'/zip',True);zip_sha256=sha(raw);need('sha256:'+zip_sha256==digest==a['digest'],'actual transfer ZIP digest differs')
    out.mkdir(parents=True);total=0
    with zipfile.ZipFile(io.BytesIO(raw))as z:
        need(len(z.infolist())<=10000,'transfer member budget')
        for member in z.infolist():
            rel=PurePosixPath(member.filename);need(not rel.is_absolute()and'..'not in rel.parts and '\\'not in member.filename and not stat.S_ISLNK(member.external_attr>>16),'transfer path/link refused')
            if member.is_dir():continue
            total+=member.file_size;need(total<=2<<30,'transfer byte budget');dest=out.joinpath(*rel.parts);need(not dest.exists(),'duplicate transfer member');dest.parent.mkdir(parents=True,exist_ok=True);dest.write_bytes(z.read(member))
    return {'repository':p.REPOSITORY,'workflow':p.WORKFLOW,'workflowCommit':os.environ['GITHUB_SHA'],'runID':os.environ['GITHUB_RUN_ID'],'runAttempt':int(os.environ['GITHUB_RUN_ATTEMPT']),'sourceCommit':p.RELEASE,'artifactID':artifact_id,'artifactDigest':digest,'uploadActionSHA256':upload_sha256,'zipSHA256':zip_sha256,'firstAttemptOnly':True,'zipDigestVerified':True,'artifactAPI':a,'runAPI':run}
def inventory(archive):
    rows={}
    with tarfile.open(archive)as t:
        for m in t.getmembers():
            rel=PurePosixPath(m.name);need(rel.parts and rel.parts[0]=='yesman.app'and'..'not in rel.parts and not rel.is_absolute(),'bundle archive path')
            if m.isdir():continue
            name='/'.join(rel.parts[1:]);need(name not in rows,'duplicate archive entry')
            if m.issym():need(not Path(m.linkname).is_absolute()and'..'not in Path(m.linkname).parts,'bundle external link');rows[name]={'link':m.linkname}
            else:need(m.isfile(),'archive special member');raw=t.extractfile(m).read();rows[name]={'sha256':sha(raw),'bytes':len(raw),'mode':m.mode&0o7777}
    need('Contents/MacOS/Yes Man'in rows,'archive main missing');return sha(json.dumps(rows,sort_keys=True).encode())
def main():
    p.first_attempt()
    out=Path(sys.argv[1]).absolute();need(not out.exists(),'new bound output');out.mkdir(parents=True)
    transfer_receipt=transfer(int(os.environ['QA_ARTIFACT_ID']),os.environ['QA_ARTIFACT_DIGEST'],out/'inputs')
    cache_receipt=transfer(int(os.environ['NPM_ARTIFACT_ID']),os.environ['NPM_ARTIFACT_DIGEST'],out/'npm')
    # The artifact ZIPs are verified before any payload can be imported/run.
    base=out/'inputs';plan=json.loads((base/'plan.json').read_bytes());meta=json.loads((base/'release-inputs.json').read_bytes());validation=json.loads((base/'producer-validation.json').read_bytes())
    need(validation['sourceCommit']==p.RELEASE and validation['sourceFilesVerified']is True and validation['archiveAndDeltaSignaturesVerified']is True and validation['signingCredentialIncluded']is False,'producer validation differs')
    need(validation['planSHA256']==sha((base/'plan.json').read_bytes())and validation['compatPlanSHA256']==sha((base/'compat-plan.json').read_bytes()),'producer plan bytes differ')
    mapping=base/'source-mapping.json';need(json.loads(mapping.read_bytes())['nonMetadataEntriesIdentical']is True,'source mapping missing')
    runner=Path(__file__).parent/'runner-attachment-f48';bindings=json.loads((runner/'source-binding.json').read_bytes());gates=Path(__file__).parent/'source-gates.json';contract=json.loads((runner/'contract.template.json').read_bytes());contract.update(releaseSourceCommit=p.RELEASE,packagePlanSHA256=sha((base/'plan.json').read_bytes()),publicNpmCache='../npm/cache',runtimeSHA256=bindings['runtimeSourceSHA256'],publicVerifier='public-upgrade-verifier',publicVerifierSHA256=validation['publicVerifierSHA256'])
    verifier=base/'public-upgrade-verifier'
    need(sha(verifier.read_bytes())==validation['publicVerifierSHA256'] and validation['publicVerifierCompiledWithoutKey']is True,'keyless public verifier differs')
    helper_raw=(Path(__file__).resolve().parents[2]/'tools/package/upgrade_assets.go.txt').read_bytes()
    need(hashlib.sha1(b'blob '+str(len(helper_raw)).encode()+b'\0'+helper_raw).hexdigest()==validation['publicVerifierSourceBlob'],'public verifier source differs')
    receipts=base/'receipts';receipts.mkdir();shutil.copyfile(gates,receipts/'source-gates.json');shutil.copyfile(mapping,receipts/'source-mapping.json');shutil.copyfile(out/'npm/cache-receipt.json',receipts/'public-npm.json');save(receipts/'transfer.json',transfer_receipt);save(receipts/'cache-transfer.json',cache_receipt)
    def bound(name):
        path=receipts/name;return {'path':'receipts/'+name,'sha256':sha(path.read_bytes())}
    contract['sourceGates']=bound('source-gates.json');contract['sourceMapping']=bound('source-mapping.json');contract['transferReceipt']=bound('transfer.json');contract['publicNpmReceipt']=bound('public-npm.json')
    for key,stage in zip(('S','Q'),plan['Stages']):
        archive=base/stage['Archive'];manifest=base/stage['Manifest'];web=base/stage['Web'];webrows={x.relative_to(web).as_posix():sha(x.read_bytes())for x in sorted(web.rglob('*'))if x.is_file()}
        need(webrows==bindings['webFiles'],'package web differs from accepted production page');need(stage['Source']==p.RELEASE,'selected S/Q source differs')
        row={'passed':True,'candidate':p.CANDIDATE,'candidateTree':p.TREE,'version':stage['Version'],'sourceCommit':stage['Source'],'sourceFilesVerified':True,'archiveSHA256':sha(archive.read_bytes()),'manifestSHA256':sha(manifest.read_bytes()),'mainSHA256':stage['binarySHA256'],'bundleInventorySHA256':inventory(archive),'webSHA256':sha(json.dumps(webrows,sort_keys=True).encode()),'runtimeSHA256':bindings['runtimeSourceSHA256'],'extensionSHA256':bindings['files']['pi/yesman.ts']['sha256'],'updaterPublicKey':plan['PublicKey'],'privateQA':key=='Q'}
        with tarfile.open(archive)as t:binary=t.extractfile('yesman.app/Contents/MacOS/Yes Man').read()
        need(sha(binary)==row['mainSHA256'],'archive/main binding differs')
        source_root=Path(__file__).resolve().parents[2]
        for name in bindings['runtimeAssetNames']:
            path='internal/piruntime/assets/'+name;asset=(source_root/path).read_bytes();need(sha(asset)==bindings['files'][path]['sha256']and asset in binary,'actual package runtime asset missing/differs: '+name)
        extension=(source_root/'pi/yesman.ts').read_bytes();need(sha(extension)==row['extensionSHA256']and extension in binary,'actual package extension missing/differs')
        row['embeddedRuntimeAssetsVerified']=True
        if key=='S':
            official=[r for r in meta['releases']if r['tag']=='v0.1.49'];need(len(official)==1,'selected S release missing');assets=[a for a in official[0]['assets']if a['name']==archive.name];need(len(assets)==1 and assets[0]['digest']=='sha256:'+row['archiveSHA256'],'S API archive differs');manifests=[a for a in official[0]['assets']if a['name']==manifest.name];need(len(manifests)==1 and manifests[0]['digest']=='sha256:'+row['manifestSHA256'],'S original API manifest differs');row.update(releaseID=official[0]['id'],archiveAssetID=assets[0]['id'])
            official_binding=json.loads((Path(__file__).parent/'official-S-binding.json').read_bytes())
            need(official_binding['passed'] is True and official_binding['sourceCommit']==p.RELEASE and official_binding['releaseID']==row['releaseID'] and official_binding['mainSHA256']==row['mainSHA256'] and official_binding['archive']['id']==row['archiveAssetID'] and official_binding['archive']['digest']=='sha256:'+row['archiveSHA256'] and official_binding['manifest']['digest']=='sha256:'+row['manifestSHA256'] and official_binding['buildSettings']['vcs.revision']==p.RELEASE and official_binding['buildSettings']['vcs.modified']=='false','S differs from actually validated release build/binary/source receipt')
        else:row.update(producerRunID=int(os.environ['GITHUB_RUN_ID']),producerRunAttempt=int(os.environ['GITHUB_RUN_ATTEMPT']),producerWorkflowCommit=os.environ['GITHUB_SHA'],producerWorkflow=p.WORKFLOW,producerRepository=p.REPOSITORY,transferArtifactID=transfer_receipt['artifactID'],transferArtifactDigest=transfer_receipt['artifactDigest'])
        save(receipts/(key+'.json'),row);contract['artifacts'][key]={**row,'verificationReceipt':bound(key+'.json')}
    handoff={'task':'t199','candidate':p.CANDIDATE,'tree':p.TREE,'authorized':True,'repository':p.REPOSITORY,'workflow':p.WORKFLOW,'workflowCommit':os.environ['GITHUB_SHA'],'runID':os.environ['GITHUB_RUN_ID'],'runAttempt':int(os.environ['GITHUB_RUN_ATTEMPT']),'scenarios':['update','failed-launch-recovery'],'releaseSourceCommit':p.RELEASE,'packagePlanSHA256':contract['packagePlanSHA256'],'compatPlanSHA256':sha((base/'compat-plan.json').read_bytes()),'sourceMappingSHA256':contract['sourceMapping']['sha256'],'publicNpmReceiptSHA256':contract['publicNpmReceipt']['sha256'],'transferReceiptSHA256':contract['transferReceipt']['sha256'],'transferArtifactID':transfer_receipt['artifactID'],'transferArtifactDigest':transfer_receipt['artifactDigest'],'artifactReceiptsSHA256':{k:contract['artifacts'][k]['verificationReceipt']['sha256']for k in('S','Q')},'manifestSHA256':{k:contract['artifacts'][k]['manifestSHA256']for k in('S','Q')},'dispatchApprovedQACommit':os.environ['APPROVED_QA_COMMIT'],'publicVerifierSHA256':validation['publicVerifierSHA256']}
    need(handoff['dispatchApprovedQACommit']==os.environ['GITHUB_SHA'],'dispatch authority differs');save(receipts/'hosted-handoff.json',handoff);contract['hostedHandoff']=bound('hosted-handoff.json');save(base/'contract.json',contract)
    # Every transported file, including hidden cache content, is verified again
    # in the native job before import; API receipts identify the selected ZIPs.
    rows={x.relative_to(out).as_posix():sha(x.read_bytes())for x in out.rglob('*')if x.is_file()};save(out/'transfer-files.json',rows)
    print('PASS: actual same-run repository/workflow/attempt/artifact ZIP digests joined to original manifests, exact S/Q source, plan and receipts')
if __name__=='__main__':main()
