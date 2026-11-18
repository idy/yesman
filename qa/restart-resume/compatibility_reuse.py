"""Reuse one independently accepted immutable compatibility result as data.
No old runtime, Q/cache artifact or producing identity is admitted into this run.
Missing/mismatched evidence refuses the stateful-only route; there is no fallback.
"""
import ast
import hashlib
import json
from pathlib import Path
import re

import provenance as p

FILES = ('qa/restart-resume/compat.py', 'qa/restart-resume/guards.py',
         'scripts/check-production-upgrade.py', 'scripts/upgrade-transport.py',
         'scripts/check-process-name.py', 'scripts/name-process-macos.swift',
         'qa/restart-resume/download.py', 'scripts/prepare-upgrade-qa.py',
         'qa/restart-resume/bind.py', 'qa/restart-resume/provenance.py')
HELPERS = ('helper_rows', 'ordinary_exit', 'HelperExit')
REVIEW_SEAL = '96ed36d8f7b86561b5380e980256583e306edea12122bcde7bbfa38dae90c376'
ASSESSMENT_SHA256 = '81b8656ee999682efd4e23e6c67d9b06ff2bf973a614e8c2ef5e5b99bef2f9c5'


def need(value):
    if not value:
        raise ValueError('accepted compatibility evidence missing or differs; stateful-only admission refused')


def sha(raw):
    return hashlib.sha256(raw).hexdigest()


def read(path, limit=512 << 10):
    path = Path(path)
    need(not path.is_symlink() and path.is_file() and path.stat().st_size <= limit)
    raw = path.read_bytes()
    need(len(raw) <= limit)
    return raw


def helper_hashes(source):
    text = source.decode()
    tree = ast.parse(text)
    return {name: sha(ast.get_source_segment(text, next(n for n in tree.body if isinstance(n, (ast.FunctionDef, ast.ClassDef)) and n.name == name)).encode()) for name in HELPERS}


def verify(repo, inputs, evidence=None):
    repo, inputs = Path(repo), Path(inputs)
    evidence = Path(evidence) if evidence else repo / 'qa/restart-resume/compatibility-reuse'
    raw = read(evidence / 'acceptance.json')
    accepted = json.loads(raw)
    need(accepted['schema'] == 1 and accepted['scope'] == 'official-arm64-full-archive-compatibility-only')
    need(accepted['independentTask'] == 't214' and accepted['independentReviewer'] == 'restart-package-review' and accepted['compatibilityAccepted'] is True)
    need(accepted['independentReviewSealSHA256'] == REVIEW_SEAL and accepted['executionAuthorized'] is False)
    assessment_raw = read(evidence / 'independent-assessment.json')
    need(sha(assessment_raw) == accepted['independentAssessmentSHA256'] == ASSESSMENT_SHA256)
    assessment = json.loads(assessment_raw)
    need(assessment['schema'] == 1 and assessment['executionAuthorized'] is False and assessment['nativeAcceptance'] is False and assessment['packageAcceptance'] is False and assessment['publicationAcceptance'] is False)
    original = assessment['original']
    need(original['runID'] == accepted['producerRunID'] and original['runAttempt'] == accepted['producerAttempt'] and original['head'] == accepted['producerQAHead'] and original['nativeJobID'] == accepted['producerJobID'])
    need(original['compatProofSHA256'] == accepted['proofSHA256'] and original['compatPlanSHA256'] == accepted['compatPlanSHA256'])
    need(accepted['producerRunID'] == 44386374590 and accepted['producerAttempt'] == 1 and accepted['producerQAHead'] == 'e795fd6ef33b68365fea29b2bb4144ebc92bd554' and accepted['producerJobID'] == 135093512725)
    need(accepted['producerArtifactID'] == 14466362767 and accepted['producerZIP_SHA256'] == '97141348eae2a5e08edab7d3494f2312579e27b3a324b5d57834ce8af02bd094')
    need(accepted['predecessor'] == p.PREDECESSOR and accepted['successor'] == p.RELEASE and accepted['statefulOrPublicationAcceptance'] is False)
    proof_raw = read(evidence / 'proof.json')
    plan_raw = read(evidence / 'compat-plan.json')
    need(sha(proof_raw) == accepted['proofSHA256'] and sha(plan_raw) == accepted['compatPlanSHA256'])
    need(read(inputs / 'compat-plan.json') == plan_raw)
    proof, plan = json.loads(proof_raw), json.loads(plan_raw)
    need(proof['runID'] == '44386374590' and proof['predecessor'] == p.PREDECESSOR and proof['source'] == p.RELEASE and proof['contractPassed'] is True and proof['completed'] is True)
    need(proof['nativeAcceptance'] is False and not proof['teardown']['errors'] and proof['teardown']['unresolvedOwnership'] is False)
    need([s['bridge']['about']['version'] for s in proof['stages']] == ['0.1.48', '0.1.49'])
    need([s['Source'] for s in plan['Stages']] == [p.PREDECESSOR, p.RELEASE] and plan['Target'] == 'darwin-arm64')
    for index, source in enumerate((p.PREDECESSOR, p.RELEASE)):
        stage, report = plan['Stages'][index], proof['stages'][index]
        need(stage['official'] is True and report['bridge']['about']['revision'] == source and report['mainSHA256'] == stage['binarySHA256'])
    meta = json.loads(read(inputs / 'release-inputs.json'))
    need(meta['refs'] == {'v0.1.48': p.PREDECESSOR, 'v0.1.49': p.RELEASE} and meta['PublicKey'] == accepted['publicKey'] == plan['PublicKey'])
    need(meta['releases'] == accepted['officialReleases'])
    assets = [{**asset, 'releaseID': release['id'], 'role': 'P' if release['tag'] == 'v0.1.48' else 'S'} for release in meta['releases'] for asset in release['assets']]
    need(assets == original['officialAssets'])
    need(sha(json.dumps(meta['sourceFileHashes'], sort_keys=True).encode()) == accepted['S_SourceFileHashesSHA256'])
    need(set(accepted['sourceFiles']) == set(FILES))
    for name in FILES:
        need(sha(read(repo / name)) == accepted['sourceFiles'][name])
    need(helper_hashes(read(repo / 'qa/restart-resume/runner-attachment-f48/runner_attachment.py')) == accepted['helperSegments'])
    return {'schema': 1, 'edgeStatus': 'reused-from', 'scope': accepted['scope'],
            'acceptanceSHA256': sha(raw), 'proofSHA256': sha(proof_raw),
            'compatPlanSHA256': sha(plan_raw), 'independentReviewSealSHA256': accepted['independentReviewSealSHA256'],
            'reusedFrom': {key: accepted[key] for key in ('producerRunID', 'producerAttempt', 'producerQAHead', 'producerJobID', 'producerArtifactID', 'producerZIP_SHA256')},
            'predecessor': p.PREDECESSOR, 'successor': p.RELEASE,
            'newOfficialInputsMatched': True, 'historicalRuntimeOrQCacheInputsUsed': False,
            'limitations': accepted['limitations'], 'statefulAcceptance': False, 'publicationAcceptance': False}
