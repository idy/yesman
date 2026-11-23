#!/usr/bin/env python3
"""Bind separately verified final inputs. This script only reads files and Git objects."""
import argparse
import hashlib
import json
from pathlib import Path
import re
import subprocess
import tarfile

from binding import BASE, EPISODES, HISTORICAL, QA_REF, TAG, check_source, oid
from hosted import EXECUTION_FILES, KEY, files

def sha(data):
    return hashlib.sha256(data).hexdigest()


def read(path):
    return json.loads(path.read_bytes())


def git_for(path):
    return lambda *args: subprocess.check_output(['/usr/bin/git','--no-optional-locks','-C',str(path),*args],timeout=25)


def make_contract(args):
    release,adoption,review=(read(p) for p in (args.draft,args.bulk_adoption,args.release_review))
    git=git_for(args.source_dir)
    source=git('rev-parse','HEAD').decode().strip()
    metadata=json.loads(git('show',f'{source}:mygo.json'))
    assert metadata['version']==TAG[1:] and metadata['updates']['publicKey']==KEY
    assert release['tag_name']==TAG and release['draft'] is True and release['prerelease'] is False and release['published_at'] is None
    assert adoption['pr']==146 and adoption['base']==BASE and adoption['adopted'] is True
    assert oid(adoption['reviewedHead']) and oid(adoption['reviewedPreview'])
    assert adoption['reviewedTree']==adoption['tree']
    assert (review['releaseSource'],review['tree'],review['featureParent'],review['approved'])==(source,git('rev-parse','HEAD^{tree}').decode().strip(),adoption['merge'],True)
    assert adoption['merge'] not in (adoption['reviewedHead'],adoption['reviewedPreview'])
    assert git('show','-s','--format=%P',adoption['merge']).decode().split()==[BASE]
    # Same release should be established via the authenticated numeric tag and assets.
    tag_ref=read(args.tag_ref)
    assert (tag_ref['ref'],tag_ref['object']['type'],tag_ref['object']['sha'])==('refs/tags/'+TAG,'commit',source)
    assert release['id']!=455729493
    assets=sorted(({k:a[k] for k in ('id','node_id','name','url','size','digest','state')} for a in release['assets']),key=lambda a:a['name'])
    assert len({a['id'] for a in assets})==len({a['name'] for a in assets})==len(assets)
    assert all(a['state']=='uploaded' and a['size']>0 and re.fullmatch(r'sha256:[0-9a-f]{64}',a['digest']) for a in assets)
    identities={a['name']:a for a in assets}
    targets=[f'{os}-{arch}' for os in ('darwin','linux','windows') for arch in ('amd64','arm64')]
    assert {f'update-{target}.json' for target in targets} <= identities.keys()
    assert 'SHA256SUMS' in identities
    for target in targets:
        matches=[n for n in identities if n in (f'yesman-{TAG[1:]}-{target}.tar.gz',f'yesman-{TAG[1:]}-{target}.zip')]
        assert len(matches)==1,target
    archive,manifest=(identities[p.name] for p in (args.archive,args.manifest))
    assert (args.archive.name,args.manifest.name)==(f'yesman-{TAG[1:]}-darwin-arm64.tar.gz','update-darwin-arm64.json')
    for path,row in ((args.archive,archive),(args.manifest,manifest)):
        assert (path.stat().st_size,'sha256:'+sha(path.read_bytes()))==(row['size'],row['digest'])
    with tarfile.open(args.archive,'r:gz') as package:
        image=package.getmember('yesman.app/Contents/MacOS/Yes Man')
        assert image.isfile()
        with package.extractfile(image) as member:
            binary=sha(member.read())
    c={'schema':4,'mode':'candidate','repository':'idy/yesman','tag':TAG,'arch':'arm64','qaRef':QA_REF,
       'history':HISTORICAL,'priorPackageAcceptanceReused':False,'base':BASE,'source':source,'tree':review['tree'],
       'bulkMerge':adoption['merge'],'bulkTree':adoption['tree'],'bulkAdoptionSHA256':sha(args.bulk_adoption.read_bytes()),
       'releaseReviewSHA256':sha(args.release_review.read_bytes()),'releaseID':release['id'],'assetID':archive['id'],
       'manifestID':manifest['id'],'archiveBytes':archive['size'],'manifestBytes':manifest['size'],
       'archiveSHA256':archive['digest'][7:],'manifestSHA256':manifest['digest'][7:],'binarySHA256':binary,
       'assetSetSHA256':sha(json.dumps(assets,sort_keys=True,separators=(',',':')).encode()),
       'webFiles':files(args.source_dir/'web'),'kitFiles':{n:sha((args.kit/n).read_bytes()) for n in sorted(EXECUTION_FILES)}}
    check_source(c, lambda *operands: git(*operands).decode().strip())
    return c


def main():
    parser=argparse.ArgumentParser(description=__doc__)
    for name in ('draft','tag-ref','bulk-adoption','release-review','source-dir','archive','manifest','kit','output'):
        parser.add_argument('--'+name,type=Path,required=True)
    args=parser.parse_args()
    c=make_contract(args)
    args.output.mkdir()
    body=(json.dumps(c,sort_keys=True,indent=2)+'\n').encode()
    (args.output/'contract.json').write_bytes(body)
    for episode in sorted(EPISODES):
        req={'ref':QA_REF.removeprefix('refs/heads/'),'inputs':{'release_tag':TAG,'episode':episode,'contract_sha256':sha(body)}}
        (args.output/f'{episode}-request.json').write_bytes(json.dumps(req,separators=(',',':')).encode())
    print(sha(body))


if __name__=='__main__':
    main()
