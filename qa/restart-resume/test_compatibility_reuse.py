"""Actual stateful-only entry/admission controls with inert normal inputs."""
import copy
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch

import compatibility_reuse as reuse
import provenance
import run_native as native

REPO = Path(__file__).resolve().parents[2]
EVIDENCE = REPO / 'qa/restart-resume/compatibility-reuse'


def source_hashes():
    """Fixed Git objects only; no checkout/ref/index/object-store mutation."""
    args = ['git', '--no-optional-locks', '-C', str(REPO)]
    entries = []
    for row in subprocess.check_output(args + ['ls-tree', '-rz', '--full-tree', provenance.RELEASE]).split(b'\0'):
        if row:
            left, name = row.split(b'\t')
            mode, kind, oid = left.decode().split()
            if kind != 'blob':
                raise ValueError('fixed source must contain regular blob entries')
            entries.append((name.decode(), oid))
    raw = subprocess.check_output(args + ['cat-file', '--batch'], input=('\n'.join(v for _, v in entries) + '\n').encode())
    offset = 0
    result = {}
    for name, oid in entries:
        end = raw.index(b'\n', offset)
        actual, kind, size = raw[offset:end].decode().split()
        size = int(size)
        if actual != oid or kind != 'blob':
            raise ValueError('fixed source object differs')
        result[name] = hashlib.sha256(raw[end + 1:end + 1 + size]).hexdigest()
        offset = end + 2 + size
    if offset != len(raw):
        raise ValueError('fixed source framing differs')
    return result


class CompatibilityReuse(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        accepted = json.loads((EVIDENCE / 'acceptance.json').read_bytes())
        cls.meta = {'refs': {'v0.1.48': provenance.PREDECESSOR, 'v0.1.49': provenance.RELEASE},
                    'PublicKey': accepted['publicKey'], 'releases': accepted['officialReleases'],
                    'sourceFileHashes': source_hashes()}

    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name).resolve()
        self.inputs = self.root / 'inputs'
        self.inputs.mkdir()
        (self.inputs / 'compat-plan.json').write_bytes((EVIDENCE / 'compat-plan.json').read_bytes())
        self.meta = copy.deepcopy(type(self).meta)
        (self.inputs / 'release-inputs.json').write_text(json.dumps(self.meta))

    def test_real_accepted_proof_and_new_official_input_match(self):
        result = reuse.verify(REPO, self.inputs)
        self.assertEqual(result['edgeStatus'], 'reused-from')
        self.assertEqual(result['reusedFrom']['producerRunID'], 44386374590)
        self.assertEqual(result['independentReviewSealSHA256'], reuse.REVIEW_SEAL)
        self.assertFalse(result['historicalRuntimeOrQCacheInputsUsed'])
        self.assertFalse(result['statefulAcceptance'] or result['publicationAcceptance'])
        self.assertIn('successor drain exit status unavailable; no TCC or whole-job success claim', result['limitations'])

    def test_mismatch_refuses_before_any_native_fallback(self):
        for case in ('plan', 'source', 'asset', 'proof', 'review', 'helper', 'missing'):
            with self.subTest(case=case):
                folder = self.root / case
                shutil.copytree(EVIDENCE, folder)
                (self.inputs / 'compat-plan.json').write_bytes((EVIDENCE / 'compat-plan.json').read_bytes())
                self.meta = copy.deepcopy(type(self).meta)
                accepted = json.loads((folder / 'acceptance.json').read_bytes())
                if case == 'plan':
                    value = json.loads((self.inputs / 'compat-plan.json').read_bytes())
                    value['Feed'] = 'http://127.0.0.1:37060'
                    (self.inputs / 'compat-plan.json').write_text(json.dumps(value))
                elif case == 'source':
                    self.meta['refs']['v0.1.49'] = 'a' * 40
                elif case == 'asset':
                    self.meta['releases'][1]['assets'][0]['digest'] = 'sha256:' + 'a' * 64
                elif case == 'proof':
                    (folder / 'proof.json').write_bytes(b'changed proof')
                elif case == 'review':
                    accepted['independentReviewSealSHA256'] = 'a' * 64
                elif case == 'helper':
                    accepted['helperSegments']['helper_rows'] = 'a' * 64
                else:
                    (folder / 'independent-assessment.json').unlink()
                (folder / 'acceptance.json').write_text(json.dumps(accepted))
                (self.inputs / 'release-inputs.json').write_text(json.dumps(self.meta))
                with self.assertRaises((ValueError, FileNotFoundError)):
                    reuse.verify(REPO, self.inputs, folder)

    def native_input(self):
        receipts = self.inputs / 'receipts'
        receipts.mkdir()
        verifier = self.inputs / 'public-upgrade-verifier'
        verifier.write_bytes(b'inert local control, never executed\n')
        verifier.chmod(0o600)
        handoff = {'workflowCommit': 'a' * 40, 'runID': '123', 'runAttempt': 1,
                   'dispatchApprovedQACommit': 'a' * 40,
                   'publicVerifierSHA256': hashlib.sha256(verifier.read_bytes()).hexdigest()}
        (receipts / 'hosted-handoff.json').write_text(json.dumps(handoff))
        (self.inputs / 'plan.json').write_bytes(b'inert selected stateful plan\n')
        rows = {p.relative_to(self.root).as_posix(): hashlib.sha256(p.read_bytes()).hexdigest()
                for p in self.root.rglob('*') if p.is_file()}
        (self.root / 'transfer-files.json').write_text(json.dumps(rows))
        return verifier

    def test_actual_native_main_invokes_only_missing_stateful_runner_once(self):
        verifier = self.native_input()
        runner_temp = self.root / 'runner-temp'
        runner_temp.mkdir()
        env = {'PATH': os.environ['PATH'], 'GITHUB_SHA': 'a' * 40, 'GITHUB_RUN_ID': '123',
               'GITHUB_RUN_ATTEMPT': '1', 'APPROVED_QA_COMMIT': 'a' * 40, 'RUNNER_TEMP': str(runner_temp)}
        with patch.dict(os.environ, env, clear=True), patch.object(sys, 'argv', ['run_native.py', str(self.root)]), patch.object(native.subprocess, 'run', return_value=SimpleNamespace(returncode=0)) as command:
            native.main()  # real manifest/input/handoff/reuse validation precedes the harmless stub
        command.assert_called_once()
        args = command.call_args.args[0]
        self.assertIn('plan.json', args)
        self.assertNotIn('compat-plan.json', args)
        self.assertTrue(any(str(x).endswith('runner-attachment-f48/runner_attachment.py') for x in args))
        result = json.loads((runner_temp / 'restart-evidence/compatibility-reused.json').read_bytes())
        self.assertEqual(result['newExecution']['runID'], '123')
        self.assertEqual(result['reusedFrom']['producerRunID'], 44386374590)
        self.assertFalse(result['historicalRuntimeOrQCacheInputsUsed'])
        self.assertEqual(verifier.stat().st_mode & 0o777, 0o700)

    def test_native_rerun_or_bad_reuse_stops_before_chmod_command_or_output(self):
        verifier = self.native_input()
        runner_temp = self.root / 'runner-temp'
        runner_temp.mkdir()
        env = {'PATH': os.environ['PATH'], 'GITHUB_SHA': 'a' * 40, 'GITHUB_RUN_ID': '123',
               'GITHUB_RUN_ATTEMPT': '2', 'APPROVED_QA_COMMIT': 'a' * 40, 'RUNNER_TEMP': str(runner_temp)}
        for attempt in ('2', '1'):
            env['GITHUB_RUN_ATTEMPT'] = attempt
            if attempt == '1':
                (self.inputs / 'compat-plan.json').write_bytes(b'changed input')
                rows = json.loads((self.root / 'transfer-files.json').read_bytes())
                rows['inputs/compat-plan.json'] = hashlib.sha256(b'changed input').hexdigest()
                (self.root / 'transfer-files.json').write_text(json.dumps(rows))
            with patch.dict(os.environ, env, clear=True), patch.object(sys, 'argv', ['run_native.py', str(self.root)]), patch.object(native.subprocess, 'run') as command:
                with self.assertRaises(ValueError):
                    native.main()
                command.assert_not_called()
            self.assertEqual(verifier.stat().st_mode & 0o777, 0o600)
            self.assertFalse((runner_temp / 'restart-evidence').exists())


if __name__ == '__main__':
    unittest.main()
