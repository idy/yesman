"""Explicit prior producer admission and fresh native entry, entirely inert data.
T199_R5_DATA points to retained local receipt/ZIP evidence for one bounded full
transport control. No HTTP, candidate imports, App/Pi/npm or credentials run.
"""
import copy
import hashlib
import io
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch
import zipfile

import consume_r5 as consumer
import run_consumer_native as native
import provenance


class Consumer(unittest.TestCase):
    def test_small_zip_exact_hash_path_count_and_duplicate_negatives(self):
        def make(names):
            out = io.BytesIO()
            with zipfile.ZipFile(out, 'w') as archive:
                for name in names:
                    archive.writestr(name, b'inert')
            raw = out.getvalue()
            pin = {'artifact': {'zipBytes': len(raw), 'sha256': consumer.sha(raw),
                               'memberCount': len(names), 'expandedBytes': 5 * len(names)},
                   'members': {n: {'bytes': 5, 'sha256': consumer.sha(b'inert')} for n in names}}
            return raw, pin
        raw, pin = make(['inputs/plain.json'])
        self.assertEqual(consumer.archive_members(raw, pin), {'inputs/plain.json': b'inert'})
        for value in ('../escape', '/absolute', 'inputs/../escape', 'back\\slash'):
            raw, pin = make([value])
            with self.assertRaises(ValueError):
                consumer.archive_members(raw, pin)
        raw, pin = make(['same', 'same'])
        with self.assertRaises(ValueError):
            consumer.archive_members(raw, pin)
        raw, pin = make(['one'])
        pin['artifact']['sha256'] = '0' * 64
        with self.assertRaises(ValueError):
            consumer.archive_members(raw, pin)

    def test_current_identity_refuses_old_attempts_and_unapproved_inputs_before_git(self):
        base = {'GITHUB_REPOSITORY': provenance.REPOSITORY, 'GITHUB_SHA': 'a' * 40,
                'GITHUB_RUN_ID': '99999', 'GITHUB_RUN_ATTEMPT': '1', 'APPROVED_QA_COMMIT': 'a' * 40,
                'RESTART_NATIVE_FROM': consumer.MODE, 'RESTART_PREDECESSOR': 'v0.1.48', 'RESTART_CANDIDATE': 'v0.1.49'}
        for key, bad in [('GITHUB_RUN_ATTEMPT', '2'), ('GITHUB_RUN_ID', '44399948584'),
                         ('APPROVED_QA_COMMIT', 'b' * 40), ('RESTART_NATIVE_FROM', ''),
                         ('RESTART_CANDIDATE', 'v0.1.50')]:
            with patch.dict(os.environ, {**base, key: bad}, clear=True), patch.object(consumer.subprocess, 'check_output') as git:
                with self.assertRaises(ValueError):
                    consumer.current_identity()
                git.assert_not_called()

    def test_GET_is_explicit_read_only_and_raw_zip_has_no_media_override_retry(self):
        with patch.object(consumer.subprocess, 'run', return_value=SimpleNamespace(returncode=0, stdout=b'raw', stderr=b'')) as run:
            self.assertEqual(consumer.get('repos/idy/yesman/actions/artifacts/14471299244/zip', 'zip'), b'raw')
        args = run.call_args.args[0]
        self.assertEqual(args[:4], ['gh', 'api', '--method', 'GET'])
        self.assertIn('--allow-escape-sequences', args)
        self.assertNotIn('-H', args)
        with patch.object(consumer.subprocess, 'run', return_value=SimpleNamespace(returncode=1, stdout=b'', stderr=b'error')) as run:
            with self.assertRaisesRegex(ValueError, 'no retry'):
                consumer.get('repos/idy/yesman/actions/artifacts/14471299244/zip', 'zip')
            run.assert_called_once()

    def test_workflow_native_only_has_exact_two_jobs_and_credential_separation(self):
        # The actual YAML parser is actionlint in the author checks. This control
        # verifies the exact route guards and dangerous native-step capabilities.
        text = (consumer.ROOT / provenance.WORKFLOW).read_text()
        import re
        jobs = {match[1]: match[2] for match in re.finditer(r'^  ([a-z][a-z-]+):\n(.*?)(?=^  [a-z][a-z-]+:\n|\Z)', text, re.M | re.S)}
        for name in ('restart-download', 'restart-public-npm', 'restart-sign-qa', 'restart-bind', 'restart-native', 'download'):
            self.assertIn("inputs.restart_native_from == ''", jobs[name].splitlines()[0])
        for name in ('restart-consumer-download', 'restart-consumer-native'):
            self.assertIn("inputs.restart_native_from == 'r5-44399948584'", jobs[name].splitlines()[0])
            self.assertIn('github.run_attempt == 1', jobs[name].splitlines()[0])
        block = jobs['restart-consumer-native']
        self.assertIn('needs: restart-consumer-download', block)
        self.assertNotIn('GH_TOKEN', block)
        self.assertNotIn('secrets.', block)
        self.assertNotIn('~/yesman', block)
        self.assertLess(block.index('name: restart-consumer-native-diagnostics'), block.index('name: restart-consumer-native-proof'))
        self.assertIn('      contents: write', jobs['restart-consumer-download'])
        self.assertIn('      actions: read', jobs['restart-consumer-download'])

    @unittest.skipUnless(os.environ.get('T199_R5_DATA'), 'explicit retained R5 data evidence required')
    def test_actual_retained_R5_data_and_native_entry_normal_failure_without_App(self):
        evidence = Path(os.environ['T199_R5_DATA'])
        pin = consumer.pins()
        raw = (evidence / 'restart-bound-inputs.zip').read_bytes()
        bodies = json.loads((evidence / 'metadata.json').read_bytes())
        manifests = {key: bytes.fromhex(value) for key, value in bodies.pop('manifestHex').items()}
        consumer.validate_metadata(pin, **bodies, manifests=manifests)
        for fault in ('expired', 'job', 'run', 'asset', 'manifest', 'refs'):
            changed = copy.deepcopy(bodies)
            modified_manifests = dict(manifests)
            if fault == 'expired': changed['artifact']['expired'] = True
            elif fault == 'job': changed['jobs']['jobs'][0]['conclusion'] = 'failure'
            elif fault == 'run': changed['run']['head_sha'] = 'b' * 40
            elif fault == 'asset': changed['releases']['v0.1.49']['assets'][0]['id'] += 1
            elif fault == 'manifest': modified_manifests['S'] += b' '
            else: changed['refs']['main'] = 'b' * 40
            with self.assertRaises(ValueError):
                consumer.validate_metadata(pin, **changed, manifests=modified_manifests)
        members = consumer.archive_members(raw, pin)
        self.assertEqual(len(members), 474)
        identity = {'repository': provenance.REPOSITORY, 'workflow': provenance.WORKFLOW,
                    'workflowCommit': 'a' * 40, 'checkoutTree': 'b' * 40, 'workflowSHA256': 'c' * 64,
                    'runID': '99999', 'runAttempt': 1}
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory).resolve()
            for name, content in members.items():
                path = root / 'original' / name
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_bytes(content)
            consumer_run = {**bodies['run'], 'id': 99999, 'head_sha': 'a' * 40, 'status': 'in_progress', 'conclusion': None}
            record = {'schema': 2, 'kind': 'pinned-prior-producer-fresh-native-consumer', 'mode': consumer.MODE,
                      'task': 't199', 'producer': pin['producer'], 'originalArtifact': pin['artifact'],
                      'consumer': identity, 'consumerRunAPI': consumer_run, 'scenarios': consumer.SCENARIOS, 'priorExecutionAuthoritySpent': True,
                      'authorizedByNewDispatchInput': True, 'originalReceiptsRelabeled': False, 'rawZIPVerified': True,
                      'acquisition': {**bodies, 'manifestHex': {k: v.hex() for k, v in manifests.items()}}}
            # All synthetic consumer identity is explicitly a test double. The
            # real production function never modifies a GITHUB_* environment.
            envelope_raw = (json.dumps(record, indent=2) + '\n').encode()
            (root / 'consumer-envelope.json').write_bytes(envelope_raw)
            outer = {'schema': 2, 'kind': 'fresh-consumer-contract', 'originalContract': 'original/inputs/contract.json',
                     'executionEnvelope': 'consumer-envelope.json', 'executionEnvelopeSHA256': consumer.sha(envelope_raw)}
            (root / 'consumer-contract.json').write_text(json.dumps(outer))
            self.assertEqual(consumer.verify_original_files(root), root / 'original')
            env = {'PATH': os.environ['PATH'], 'HOME': str(root / 'synthetic-home'), 'TMPDIR': str(root),
                   'GITHUB_SHA': 'a' * 40, 'GITHUB_RUN_ID': '99999', 'GITHUB_RUN_ATTEMPT': '1',
                   'CONSUMER_ENVELOPE_SHA256': consumer.sha(envelope_raw),
                   'CONSUMER_TRANSFER_ARTIFACT_ID': '999999', 'CONSUMER_TRANSFER_SHA256': 'd' * 64}
            with patch.dict(os.environ, env, clear=True), patch.object(consumer, 'current_identity', return_value=identity):
                def saved_GET(endpoint, binary=None):
                    selected = pin['artifact']['artifactID']
                    if endpoint == 'repos/idy/yesman/actions/runs/99999/attempts/1': return copy.deepcopy(consumer_run)
                    if endpoint == f'repos/idy/yesman/actions/runs/44399948584/attempts/1': return copy.deepcopy(bodies['run'])
                    if endpoint.endswith('/attempts/1/jobs?per_page=100'): return copy.deepcopy(bodies['jobs'])
                    if endpoint == f'repos/idy/yesman/actions/artifacts/{selected}': return copy.deepcopy(bodies['artifact'])
                    if endpoint == f'repos/idy/yesman/actions/artifacts/{selected}/zip': return raw
                    for tag, release in pin['officialReleases'].items():
                        if endpoint == f"repos/idy/yesman/releases/{release['id']}": return copy.deepcopy(bodies['releases'][tag])
                        if endpoint == f"repos/idy/yesman/releases/{release['id']}/assets?per_page=100": return copy.deepcopy(bodies['releases'][tag]['assets'])
                    if '/git/ref/' in endpoint:
                        name = endpoint.rsplit('/', 1)[-1]
                        return {'object': {'type': 'commit', 'sha': bodies['refs'][name]}}
                    for asset in pin['officialAssets']:
                        if asset['name'] == 'update-darwin-arm64.json' and endpoint == f"repos/idy/yesman/releases/assets/{asset['id']}": return manifests[asset['role']]
                    self.fail('unexpected acquisition endpoint: ' + endpoint)
                with patch.object(consumer, 'get', side_effect=saved_GET) as acquired:
                    consumer.acquire(root / 'retrieved')
                self.assertEqual(len(acquired.call_args_list), 14)
                retrieved = root / 'retrieved'
                self.assertEqual(consumer.verify_original_files(retrieved), retrieved / 'original')
                self.assertEqual(consumer.envelope(root), record)
                original_cwd = Path.cwd()
                try:
                    os.chdir(root / 'original/inputs')
                    plan = json.loads(Path('plan.json').read_bytes())
                    for stage in plan['Stages']:
                        for key in ('Archive', 'Manifest', 'Web'):
                            stage[key] = str(Path(stage[key]).absolute())
                    contract = json.loads(Path('contract.json').read_bytes())
                    consumer.verify_original_contract(contract, plan, Path('plan.json'), '99999', record)
                finally:
                    os.chdir(original_cwd)
                original_run = subprocess.run
                for exit_code in (0, 7):
                    temp = root / str(exit_code);temp.mkdir()
                    os.environ['RUNNER_TEMP'] = str(temp)
                    observed = []
                    def harmless_driver(args, **kwargs):
                        observed.append((args, kwargs['env']['GITHUB_RUN_ID'], kwargs['timeout']))
                        return original_run([sys.executable, '-c', 'raise SystemExit(' + str(exit_code) + ')'],
                            env=env, timeout=3, capture_output=True)
                    with patch.object(native.subprocess, 'run', side_effect=harmless_driver):
                        if exit_code:
                            with self.assertRaises(SystemExit) as stopped:
                                native.run(root)
                            self.assertEqual(stopped.exception.code, 7)
                        else:
                            native.run(root)
                    self.assertEqual(len(observed), 1)
                    self.assertEqual(observed[0][1:], ('99999', 1200))
                    self.assertEqual(observed[0][0][-1], str(root / 'consumer-contract.json'))
                    receipt = json.loads((temp / 'restart-evidence/entry-status.json').read_bytes())
                    self.assertEqual(receipt['exitCode'], exit_code)
                    self.assertEqual((root / 'original/inputs/receipts/hosted-handoff.json').read_bytes(), members['inputs/receipts/hosted-handoff.json'])
                    consumer.verify_original_files(root)
                # No native/provider activity is claimed by harmless exit controls.
                print(json.dumps({'control': 'R5-474-files-and-consumer-entry', 'nativeAppExecuted': False,
                                  'originalReceiptBytesPreserved': True, 'harmlessDriverExits': [0, 7],
                                  'newConsumerRunID': 'synthetic-99999', 'priorProducerRunID': 44399948584}))


if __name__ == '__main__':
    unittest.main()
