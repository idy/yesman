"""Explicit R5 producer / fresh consumer admission; never rewrites old receipts.
The original same-run route and provenance.py remain unchanged. This route admits
only the one independently verified R5 artifact, using a new attempt1 envelope.
"""
import hashlib
import io
import json
import os
from pathlib import Path, PurePosixPath
import re
import stat
import subprocess
import sys
import zipfile

import provenance as p
from verify import verify

ROOT = Path(__file__).resolve().parents[2]
MODE = 'r5-44399948584'
SCENARIOS = ['update', 'failed-launch-recovery']


def need(value, reason):
    if not value:
        raise ValueError(reason)


def sha(raw):
    return hashlib.sha256(raw).hexdigest()


def pins():
    value = json.loads((ROOT / 'qa/restart-resume/reuse-r5-pins.json').read_bytes())
    need(value['schema'] == 1 and value['mode'] == MODE and value['priorAuthoritySpent'] is True,
         'R5 reuse pins differ')
    return value


def read(path, limit=128 << 20):
    path = Path(path).absolute()
    for parent in path.parents:
        need(stat.S_ISDIR(parent.lstat().st_mode), 'consumer input parent refused')
    fd = os.open(path, os.O_RDONLY | os.O_NONBLOCK | os.O_NOFOLLOW)
    try:
        before = os.fstat(fd)
        need(stat.S_ISREG(before.st_mode) and before.st_nlink == 1 and before.st_size <= limit,
             'consumer input type/budget refused')
        with os.fdopen(fd, 'rb', closefd=False) as stream:
            raw = stream.read(limit + 1)
        after = os.fstat(fd)
        signature = lambda st: (st.st_dev, st.st_ino, st.st_mode, st.st_nlink, st.st_size, st.st_mtime_ns, st.st_ctime_ns)
        need(len(raw) == before.st_size and len(raw) <= limit and
             signature(before) == signature(after) == signature(path.lstat()), 'consumer input changed')
        return raw
    finally:
        os.close(fd)


def current_identity():
    p.first_attempt()
    need(os.environ.get('GITHUB_REPOSITORY') == p.REPOSITORY and
         re.fullmatch('[0-9a-f]{40}', os.environ.get('GITHUB_SHA', '')) and
         os.environ.get('APPROVED_QA_COMMIT') == os.environ['GITHUB_SHA'] and
         os.environ.get('RESTART_NATIVE_FROM') == MODE and
         os.environ.get('RESTART_PREDECESSOR') == 'v0.1.48' and
         os.environ.get('RESTART_CANDIDATE') == 'v0.1.49' and
         re.fullmatch('[1-9][0-9]*', os.environ.get('GITHUB_RUN_ID', '')),
         'fresh consumer dispatch identity differs')
    need(os.environ['GITHUB_RUN_ID'] != str(pins()['producer']['runID']), 'old execution authority spent')
    head = subprocess.check_output(['git', '--no-optional-locks', '-C', str(ROOT), 'rev-parse', 'HEAD'], timeout=10).decode().strip()
    tree = subprocess.check_output(['git', '--no-optional-locks', '-C', str(ROOT), 'rev-parse', 'HEAD^{tree}'], timeout=10).decode().strip()
    need(head == os.environ['GITHUB_SHA'], 'consumer checkout differs from actual workflow')
    return {'repository': p.REPOSITORY, 'workflow': p.WORKFLOW,
            'workflowCommit': head, 'checkoutTree': tree,
            'workflowSHA256': sha(read(ROOT / p.WORKFLOW)),
            'runID': os.environ['GITHUB_RUN_ID'], 'runAttempt': 1}


def validate_consumer_run(run, consumer):
    need(run['id'] == int(consumer['runID']) and run['run_attempt'] == 1 and
         run['head_sha'] == consumer['workflowCommit'] and run['workflow_id'] == 420883319 and
         run['path'] == p.WORKFLOW and run['event'] == 'workflow_dispatch' and
         run['repository']['id'] == run['head_repository']['id'] == 1454019582 and
         run['repository']['full_name'] == p.REPOSITORY and
         run['head_branch'] == 'qa/restart-resume-package-049' and run['status'] == 'in_progress',
         'new consumer API admission differs')


def validate_metadata(pin, run, jobs, artifact, releases, refs, manifests):
    producer, selected = pin['producer'], pin['artifact']
    need(run['id'] == producer['runID'] and run['run_attempt'] == 1 and
         run['head_sha'] == producer['qaHead'] and run['head_branch'] == 'qa/restart-resume-package-049' and
         run['workflow_id'] == producer['workflowID'] and run['path'] == producer['workflowPath'] and
         run['repository']['id'] == run['head_repository']['id'] == producer['repositoryID'] and
         run['repository']['full_name'] == p.REPOSITORY and run['event'] == 'workflow_dispatch' and
         run['status'] == 'completed' and run['conclusion'] == 'failure', 'original R5 run identity differs')
    need(jobs['total_count'] == len(jobs['jobs']) == 8, 'complete original attempt1 jobs required')
    for expected in producer['jobs']:
        actual = [job for job in jobs['jobs'] if job['id'] == expected['id']]
        need(len(actual) == 1 and actual[0]['status'] == 'completed' and
             all(actual[0][key] == expected[key] for key in expected), 'accepted preparation job differs')
    need(artifact['id'] == selected['artifactID'] and artifact['name'] == selected['name'] and
         artifact['size_in_bytes'] == selected['zipBytes'] and not artifact['expired'] and
         artifact['digest'] == 'sha256:' + selected['sha256'] and
         artifact['workflow_run']['id'] == producer['runID'] and
         artifact['workflow_run']['head_sha'] == producer['qaHead'] and
         artifact['workflow_run']['repository_id'] == artifact['workflow_run']['head_repository_id'] == producer['repositoryID'],
         'original bound artifact identity/nonexpiry differs')
    need(refs == {'v0.1.48': p.PREDECESSOR, 'v0.1.49': p.RELEASE, 'main': p.RELEASE},
         'current immutable P/S/main differs')
    need(set(releases) == set(pin['officialReleases']), 'current release coverage differs')
    for tag, expected in pin['officialReleases'].items():
        actual = releases[tag]
        need(all(actual[key] == expected[key] for key in ('id', 'tag_name', 'draft', 'prerelease', 'target_commitish')),
             'current official release state differs')
        project = lambda rows: sorted([{key: row[key] for key in ('id', 'name', 'size', 'digest')} for row in rows], key=lambda row: row['id'])
        need(project(actual['assets']) == project(expected['assets']), 'current complete official asset set differs')
    for expected in [a for a in pin['officialAssets'] if a['name'] == 'update-darwin-arm64.json']:
        raw = manifests[expected['role']]
        need(len(raw) == expected['size'] and 'sha256:' + sha(raw) == expected['digest'],
             'current original manifest bytes differ')
    return True


def archive_members(raw, pin):
    need(len(raw) == pin['artifact']['zipBytes'] and sha(raw) == pin['artifact']['sha256'],
         'original raw bound ZIP differs')
    result = {}
    total = 0
    with zipfile.ZipFile(io.BytesIO(raw)) as archive:
        need(len(archive.infolist()) == pin['artifact']['memberCount'], 'original ZIP member count differs')
        for member in archive.infolist():
            path = PurePosixPath(member.filename)
            need(not path.is_absolute() and '..' not in path.parts and '\\' not in member.filename and
                 path.as_posix() == member.filename and not stat.S_ISLNK(member.external_attr >> 16) and
                 not member.is_dir() and member.filename not in result and member.filename in pin['members'],
                 'original ZIP path/type/coverage refused')
            expected = pin['members'][member.filename]
            need(member.file_size == expected['bytes'] and member.file_size <= 128 << 20, 'original member budget differs')
            total += member.file_size
            need(total <= pin['artifact']['expandedBytes'], 'original ZIP expanded budget')
            value = archive.read(member)
            need(sha(value) == expected['sha256'], 'original ZIP member hash differs')
            result[member.filename] = value
    need(set(result) == set(pin['members']) and total == pin['artifact']['expandedBytes'], 'original ZIP coverage differs')
    return result


def verify_original_files(root):
    pin = pins()
    original = Path(root) / 'original'
    names = set()
    for path in original.rglob('*'):
        if path.is_symlink():
            raise ValueError('original transfer symlink refused')
        if path.is_dir():
            continue
        relative = path.relative_to(original).as_posix()
        need(relative in pin['members'], 'unexpected original transfer file')
        raw = read(path)
        row = pin['members'][relative]
        need(len(raw) == row['bytes'] and sha(raw) == row['sha256'], 'original transfer changed')
        names.add(relative)
    need(names == set(pin['members']), 'original transfer coverage differs')
    return original


def envelope(root, expected_digest=None):
    root = Path(root).absolute()
    raw = read(root / 'consumer-envelope.json', 2 << 20)
    need(sha(raw) == (expected_digest if expected_digest is not None else os.environ.get('CONSUMER_ENVELOPE_SHA256')), 'selected consumer envelope digest differs')
    value = json.loads(raw)
    need(value['schema'] == 2 and value['kind'] == 'pinned-prior-producer-fresh-native-consumer' and
         value['mode'] == MODE and value['consumer'] == current_identity() and
         value['scenarios'] == SCENARIOS and value['task'] == 't199' and
         value['priorExecutionAuthoritySpent'] is True and value['authorizedByNewDispatchInput'] is True and
         value['producer'] == pins()['producer'] and value['originalArtifact'] == pins()['artifact'] and
         value['originalReceiptsRelabeled'] is False and value['rawZIPVerified'] is True, 'fresh consumer envelope differs')
    validate_consumer_run(value['consumerRunAPI'], value['consumer'])
    observations = value['acquisition']
    manifest_bytes = {key: bytes.fromhex(raw) for key, raw in observations['manifestHex'].items()}
    validate_metadata(pins(), observations['run'], observations['jobs'], observations['artifact'],
                      observations['releases'], observations['refs'], manifest_bytes)
    verify_original_files(root)
    return value


def verify_original_contract(c, plan, plan_path, run_id, execution):
    """Exact historical contract plus independent new execution admission.
    All old same-run relationships stay the original R5 tuple, never the new run.
    """
    path = Path(plan_path).absolute().parent
    need(c == json.loads(read(path / 'contract.json')) and
         sha(read(path / 'contract.json')) == pins()['originalReceiptPins']['contractSHA256'],
         'historical contract changed')
    need(execution == envelope(path.parents[1]) and execution['consumer']['runID'] == run_id,
         'new consumer execution differs')
    old = json.loads(read(path / 'receipts/hosted-handoff.json'))
    producer = pins()['producer']
    need(old['workflowCommit'] == producer['qaHead'] and old['runID'] == str(producer['runID']) and
         old['runAttempt'] == producer['runAttempt'] == 1 and old['dispatchApprovedQACommit'] == producer['qaHead'] and
         old['repository'] == p.REPOSITORY and old['workflow'] == p.WORKFLOW and
         old['task'] == 't199' and old['authorized'] is True and old['scenarios'] == SCENARIOS,
         'historical producer identity differs')
    need(sha(read(plan_path)) == old['packagePlanSHA256'] == c['packagePlanSHA256'] == pins()['originalReceiptPins']['planSHA256'] and
         plan['sourceSHA'] == c['releaseSourceCommit'] == p.RELEASE, 'original plan/source differs')
    # Each of these old bytes was independently joined to R5 by t214 and is
    # rehashed in verify_original_files, including all original nested API rows.
    for key, name in [('hostedHandoff', 'hosted-handoff.json'), ('sourceMapping', 'source-mapping.json'),
                      ('transferReceipt', 'transfer.json'), ('publicNpmReceipt', 'public-npm.json')]:
        need(c[key]['path'] == 'receipts/' + name and c[key]['sha256'] == sha(read(path / 'receipts' / name)),
             'historical receipt binding differs')
    for key, stage in zip(('S', 'Q'), plan['Stages']):
        verified = c['artifacts'][key]
        need(old['artifactReceiptsSHA256'][key] == verified['verificationReceipt']['sha256'] and
             old['manifestSHA256'][key] == sha(read(stage['Manifest'])) == verified['manifestSHA256'],
             'historical stage receipt/manifest differs')
        if key == 'Q':
            need(verified['producerRunID'] == producer['runID'] and verified['producerWorkflowCommit'] == producer['qaHead'] and
                 verified['producerRunAttempt'] == producer['runAttempt'], 'Q original producer tuple differs')
    need(old['publicVerifierSHA256'] == sha(read(path / c['publicVerifier'])) == pins()['originalReceiptPins']['publicVerifierSHA256'],
         'original public verifier differs')
    return old


def get(endpoint, binary=None):
    need(endpoint.startswith('repos/idy/yesman/'), 'fixed repository GET only')
    args = ['gh', 'api', '--method', 'GET', endpoint]
    if binary == 'zip':
        args += ['--allow-escape-sequences']  # ordinary exact-ID raw redirect, no binary Accept override
    elif binary == 'manifest':
        args += ['-H', 'Accept: application/octet-stream']
    result = subprocess.run(args, capture_output=True, timeout=120)
    need(result.returncode == 0, 'consumer acquisition GET failed; no retry')
    return result.stdout if binary else json.loads(result.stdout)


def acquire(output):
    p.first_attempt()
    verify(ROOT)
    consumer = current_identity()
    pin = pins()
    output = Path(output).absolute()
    need(not output.exists(), 'fresh consumer acquisition destination required')
    producer = pin['producer']
    consumer_run = get(f"repos/idy/yesman/actions/runs/{consumer['runID']}/attempts/1")
    validate_consumer_run(consumer_run, consumer)
    run = get(f"repos/idy/yesman/actions/runs/{producer['runID']}/attempts/1")
    jobs = get(f"repos/idy/yesman/actions/runs/{producer['runID']}/attempts/1/jobs?per_page=100")
    artifact = get(f"repos/idy/yesman/actions/artifacts/{pin['artifact']['artifactID']}")
    releases = {}
    for tag, row in pin['officialReleases'].items():
        value = get(f"repos/idy/yesman/releases/{row['id']}")
        value['assets'] = get(f"repos/idy/yesman/releases/{row['id']}/assets?per_page=100")
        need(len(value['assets']) < 100, 'release pagination unexpected')
        releases[tag] = value
    refs = {}
    for name in ('v0.1.48', 'v0.1.49', 'main'):
        kind = 'heads' if name == 'main' else 'tags'
        obj = get(f'repos/idy/yesman/git/ref/{kind}/{name}')['object']
        need(obj['type'] == 'commit', 'direct immutable commit required')
        refs[name] = obj['sha']
    manifests = {}
    for row in pin['officialAssets']:
        if row['name'] == 'update-darwin-arm64.json':
            manifests[row['role']] = get(f"repos/idy/yesman/releases/assets/{row['id']}", 'manifest')
    validate_metadata(pin, run, jobs, artifact, releases, refs, manifests)
    raw = get(f"repos/idy/yesman/actions/artifacts/{pin['artifact']['artifactID']}/zip", 'zip')
    members = archive_members(raw, pin)  # all hashing precedes filesystem extraction
    output.mkdir(parents=True, exist_ok=False)
    for name, content in members.items():
        destination = output / 'original' / name
        destination.parent.mkdir(parents=True, exist_ok=True)
        destination.write_bytes(content)
    record = {'schema': 2, 'kind': 'pinned-prior-producer-fresh-native-consumer', 'mode': MODE,
              'task': 't199', 'producer': producer, 'originalArtifact': pin['artifact'],
              'consumer': consumer, 'consumerRunAPI': consumer_run, 'scenarios': SCENARIOS, 'priorExecutionAuthoritySpent': True,
              'authorizedByNewDispatchInput': True, 'originalReceiptsRelabeled': False,
              'rawZIPVerified': True, 'acquisition': {'run': run, 'jobs': jobs, 'artifact': artifact,
              'releases': releases, 'refs': refs, 'manifestHex': {k: v.hex() for k, v in manifests.items()}}}
    data = (json.dumps(record, indent=2) + '\n').encode()
    (output / 'consumer-envelope.json').write_bytes(data)
    outer = {'schema': 2, 'kind': 'fresh-consumer-contract', 'originalContract': 'original/inputs/contract.json',
             'executionEnvelope': 'consumer-envelope.json', 'executionEnvelopeSHA256': sha(data)}
    # Deliberately outside the historical original payload/transfer manifest.
    (output / 'consumer-contract.json').write_text(json.dumps(outer, indent=2) + '\n')
    # The trusted acquisition step publishes only a public hash, before upload.
    envelope(output, sha(data))
    if os.environ.get('GITHUB_OUTPUT'):
        with open(os.environ['GITHUB_OUTPUT'], 'a') as handle:
            handle.write('envelope_sha256=' + sha(data) + '\n')
    print('PASS: fixed R5 producer bytes acquired; explicit fresh attempt1 consumer envelope, no receipt relabeling')


if __name__ == '__main__':
    acquire(sys.argv[1])
