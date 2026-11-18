"""Mandatory pure admission checks for the selected same-run QA transfer."""
import hashlib
import json
import os
from pathlib import Path

CANDIDATE = 'cfaaeec242a7d372669b5e39739b71bbd15ad646'
TREE = 'c7b0c06488b613c56dd0e52ceba98e343a4e0369'
INTEGRATION = '077b642022eff788f41c6e2cc530551f3518a919'
RELEASE = 'e2c1e68851b2a16f4ac3cd0f77baffcc0cfd7dc0'
RELEASE_TREE = '7bef3ec31e545d33ede542326b515e18f44d24f7'
PREDECESSOR = '6f0a1f1400f667c09cc57a2b5d5fc93962334ee9'
WORKFLOW = '.github/workflows/application-upgrade-draft.yml'
REPOSITORY = 'idy/yesman'


def need(ok, why):
    if not ok: raise ValueError(why)
def sha(raw): return hashlib.sha256(raw).hexdigest()
def receipt(binding):
    raw = Path(binding['path']).read_bytes()
    need(sha(raw) == binding['sha256'], 'bound receipt bytes changed')
    return json.loads(raw)


def first_attempt():
    """This dedicated route supports new run IDs, never partial/full reruns."""
    need(os.environ.get('GITHUB_RUN_ATTEMPT') == '1', 'restart QA requires attempt 1; failures need a separately authorized new run')


def verify_contract(c, plan, plan_path, run_id):
    a = receipt(c['hostedHandoff'])
    need(a['repository'] == REPOSITORY and a['workflow'] == WORKFLOW and a['workflowCommit'] == os.environ['GITHUB_SHA'], 'selected repository/workflow/commit differs')
    need(a['runID'] == run_id and a['runAttempt'] == int(os.environ['GITHUB_RUN_ATTEMPT']), 'selected run/attempt differs')
    need(c['releaseSourceCommit'] == plan['sourceSHA'] == RELEASE, 'selected immutable release differs')
    need(a['releaseSourceCommit'] == RELEASE and a['packagePlanSHA256'] == c['packagePlanSHA256'] == sha(Path(plan_path).read_bytes()), 'handoff package plan differs')
    mapping = receipt(c['sourceMapping'])
    need(mapping['passed'] is True and mapping['candidate'] == CANDIDATE and mapping['candidateTree'] == TREE and mapping['integration'] == INTEGRATION and mapping['integrationTree'] == TREE, 'accepted source integration differs')
    need(mapping['releaseSourceCommit'] == RELEASE and mapping['releaseTree'] == RELEASE_TREE and mapping['releaseParents'] == [INTEGRATION] and mapping['changedPaths'] == ['CHANGELOG.md', 'mygo.json'], 'release metadata/source mapping differs')
    need(mapping['nonMetadataEntriesIdentical'] is True and a['sourceMappingSHA256'] == c['sourceMapping']['sha256'], 'release product bytes not joined to handoff')
    transfer = receipt(c['transferReceipt'])
    for field in ('repository', 'workflow', 'workflowCommit', 'runID', 'runAttempt'):
        need(transfer[field] == a[field], 'producer transfer identity differs: ' + field)
    need(transfer['artifactID'] == a['transferArtifactID'] and transfer['artifactDigest'] == a['transferArtifactDigest'] and transfer['artifactDigest'].startswith('sha256:') and transfer['zipDigestVerified'] is True, 'actual selected transfer differs')
    need(transfer['sourceCommit'] == RELEASE and a['transferReceiptSHA256'] == c['transferReceipt']['sha256'], 'transfer source/receipt differs')
    for key, stage in zip(('S', 'Q'), plan['Stages']):
        bound = c['artifacts'][key]
        verified = receipt(bound['verificationReceipt'])
        need(stage['Source'] == bound['sourceCommit'] == verified['sourceCommit'] == RELEASE and verified['sourceFilesVerified'] is True, 'both selected sources must be exactly S')
        need(a['artifactReceiptsSHA256'][key] == bound['verificationReceipt']['sha256'], 'package receipt not selected by handoff')
        digest = sha(Path(stage['Manifest']).read_bytes())
        need(a['manifestSHA256'][key] == bound['manifestSHA256'] == verified['manifestSHA256'] == digest, 'exact original manifest bytes differ')
        if key == 'Q':
            need(bound['producerRunID'] == verified['producerRunID'] == int(run_id), 'Q not from selected run')
            need(verified['producerRunAttempt'] == a['runAttempt'] and verified['producerWorkflowCommit'] == a['workflowCommit'] and verified['producerWorkflow'] == WORKFLOW and verified['producerRepository'] == REPOSITORY, 'Q producer identity differs')
            need(bound['transferArtifactID'] == verified['transferArtifactID'] == transfer['artifactID'] and verified['transferArtifactDigest'] == transfer['artifactDigest'], 'Q transfer differs')
    verifier = Path(c['publicVerifier'])
    need(not verifier.is_symlink() and sha(verifier.read_bytes()) == c['publicVerifierSHA256'] == a['publicVerifierSHA256'], 'bound public verifier differs')
    cache = receipt(c['publicNpmReceipt'])
    need(cache['integritiesVerified'] is True and cache['scriptsExecuted'] is False and cache['credentialsUsed'] is False and cache['repository'] == REPOSITORY and cache['workflowCommit'] == a['workflowCommit'] and str(cache['runID']) == run_id and cache['runAttempt'] == a['runAttempt'], 'selected public npm acquisition differs')
    need(a['publicNpmReceiptSHA256'] == c['publicNpmReceipt']['sha256'], 'npm receipt not joined to handoff')
    return a
