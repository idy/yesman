"""Final draft admission shared by the publisher's binder and hosted runner."""
import re

TAG = 'v0.1.65'
QA_REF = 'refs/heads/qa/t339-v0.1.65-cleanup-r3'
EPISODES = {'cleanup-only'}
HOLD_TAG = '68811d382ad5eac649d429630c21332c53f5cb59'
HOLD_IDS = {455729493, 783812678, 783812889}
HOLD_QA = {'acea9a4018e2530db9af63a5aead8705ba1fe266', 'a2e15720e8823089dcefba817458a6ffb928a92e'}
BASE = '6222162b926882c41637fe5337731a3b37a46e06'
PR144 = '5d779687b70fa34a073dc5e74d071f6fe359fede'
HISTORICAL = {'ordinaryStartup': 45225972788, 'failedBulk': [45228181690, 45237245464]}


def check_inputs(episode, ref, attempt, qa):
    assert episode in EPISODES and ref == QA_REF and attempt == '1'
    assert oid(qa) and qa not in HOLD_QA | {BASE, HOLD_TAG, 'ca7ecd2a2db9e7f31004745a5476d0cbbeaa1c7e', '75b256191b7f4320f9fe04b162f73453c1424ee4'}


def oid(value):
    return isinstance(value, str) and re.fullmatch('[0-9a-f]{40}', value) is not None


def digest(value):
    return isinstance(value, str) and re.fullmatch('[0-9a-f]{64}', value) is not None


def check_contract(c):
    assert (c['schema'], c['repository'], c['tag'], c['arch'], c['mode'], c['qaRef']) == (4, 'idy/yesman', TAG, 'arm64', 'candidate', QA_REF)
    assert c['history'] == HISTORICAL and c['priorPackageAcceptanceReused'] is False
    assert c['base'] == BASE
    for key in ('releaseID', 'assetID', 'manifestID', 'archiveBytes', 'manifestBytes'):
        assert type(c[key]) is int and c[key] > 0
    ids = {c[k] for k in ('releaseID', 'assetID', 'manifestID')}
    assert len(ids) == 3 and not ids & HOLD_IDS
    for key in ('source', 'tree', 'bulkMerge', 'bulkTree'):
        assert oid(c[key]) and c[key] not in {BASE, HOLD_TAG, PR144} | HOLD_QA
    assert c['source'] != c['bulkMerge']
    for key in ('archiveSHA256', 'manifestSHA256', 'binarySHA256', 'assetSetSHA256', 'bulkAdoptionSHA256', 'releaseReviewSHA256'):
        assert digest(c[key])
    assert c['archiveSHA256'] != '7ccfc2a7e8489eaf648013b3799281e7e1741c13327887c3491d801b45e6d3a5'
    assert c['manifestSHA256'] != 'a74d7d4a4d7b348f0cd03a2ebd72775e89bed9b184d0e7a83530351ab16566db'
    assert c['binarySHA256'] != 'eb08d73b8d70f9907e0bc4a0c3f4c8e93edfa3d7fda3e4226637a18c7ec2f2f8'


def check_source(c, git):
    check_contract(c)
    assert git('rev-parse', '--is-shallow-repository') == 'false'
    assert (git('rev-parse', 'HEAD'), git('rev-parse', 'HEAD^{tree}')) == (c['source'], c['tree'])
    assert not git('status', '--porcelain', '--untracked-files=all')
    assert git('show', '-s', '--format=%P', c['source']) == c['bulkMerge']
    assert git('rev-parse', c['bulkMerge']+'^{tree}') == c['bulkTree']
    assert git('show', '-s', '--format=%P', BASE) == PR144
    assert git('show', '-s', '--format=%P', PR144) == HOLD_TAG
    assert git('rev-parse', BASE+'^{tree}') == '82f09616fe242e8d2a8ec1623720192401bd292d'
    assert git('rev-parse', PR144+'^{tree}') == '54739486440b893a9c5cb06a3be79011891494d4'
    git('merge-base', '--is-ancestor', BASE, c['bulkMerge'])
    assert set(git('diff', '--name-only', c['bulkMerge'], c['source']).splitlines()) == {'mygo.json', 'CHANGELOG.md'}
