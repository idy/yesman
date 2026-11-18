"""Credential-free fresh native consumer of the pinned R5 preparation artifact."""
import json
import os
import re
from pathlib import Path
import subprocess
import sys

import compatibility_reuse
import consume_r5
from native_diagnostics import error_code
from provenance import first_attempt
from verify import verify


def run(root):
    first_attempt()
    repo = Path(__file__).resolve().parents[2]
    verify(repo)
    if any(os.environ.get(key) for key in ('GH_TOKEN', 'GITHUB_TOKEN', 'MYGO_UPDATER_PRIVATE_KEY')):
        raise ValueError('native credentials refused')
    root = Path(root).resolve()
    admitted = consume_r5.envelope(root)
    transfer_id = os.environ.get('CONSUMER_TRANSFER_ARTIFACT_ID', '')
    transfer_digest = os.environ.get('CONSUMER_TRANSFER_SHA256', '')
    if not re.fullmatch('[1-9][0-9]*', transfer_id) or not re.fullmatch('[0-9a-f]{64}', transfer_digest):
        raise ValueError('current consumer transfer action outputs required')
    if int(transfer_id) == admitted['originalArtifact']['artifactID']:
        raise ValueError('fresh consumer transfer cannot be relabeled prior artifact')
    outer = json.loads(consume_r5.read(root / 'consumer-contract.json'))
    if outer != {'schema': 2, 'kind': 'fresh-consumer-contract',
                 'originalContract': 'original/inputs/contract.json',
                 'executionEnvelope': 'consumer-envelope.json',
                 'executionEnvelopeSHA256': consume_r5.sha(consume_r5.read(root / 'consumer-envelope.json'))}:
        raise ValueError('new consumer contract differs')
    original = root / 'original/inputs'
    reused = compatibility_reuse.verify(repo, original)
    reused['newExecution'] = admitted['consumer']
    output = Path(os.environ['RUNNER_TEMP']) / 'restart-evidence'
    output.mkdir(exist_ok=False)
    def save(name, value):
        (output / name).write_text(json.dumps(value, indent=2) + '\n')
    save('compatibility-reused.json', reused)
    save('consumer-admission.json', {'schema': 2, 'producer': admitted['producer'],
         'originalArtifact': admitted['originalArtifact'], 'consumer': admitted['consumer'],
         'priorExecutionAuthoritySpent': True, 'originalReceiptsRelabeled': False,
         'newConsumerTransfer': {'artifactID': int(transfer_id), 'actionSHA256': transfer_digest,
                                 'runID': admitted['consumer']['runID'], 'runAttempt': 1},
         'nativeAcceptance': False})
    verifier = original / 'public-upgrade-verifier'
    verifier.chmod(0o700)  # verified immutable bytes; ZIP transport has no executable mode
    argv = [sys.executable, str(repo / 'scripts/check-production-upgrade.py'),
            str(original / 'plan.json'), str(output / 'stateful'), '--t199-attachment',
            str(repo / 'qa/restart-resume/runner-attachment-f48/runner_attachment.py'),
            str(root / 'consumer-contract.json')]
    save('entry-status.json', {'phase': 'stateful-subprocess', 'status': 'started', 'exitCode': None})
    try:
        # One subprocess; no episode replay. Inner deadlines and the hosted job
        # timeout bound native operations. A Python error still reaches upload.
        result = subprocess.run(argv, cwd=original, env=dict(os.environ), timeout=1200)
    except BaseException as error:
        save('entry-status.json', {'phase': 'stateful-subprocess', 'status': 'error',
                                  'errorCode': error_code(error), 'exitCode': None})
        raise
    save('entry-status.json', {'phase': 'stateful-subprocess', 'status': 'completed',
                              'exitCode': result.returncode})
    if result.returncode:
        raise SystemExit(result.returncode)


if __name__ == '__main__':
    run(sys.argv[1])
