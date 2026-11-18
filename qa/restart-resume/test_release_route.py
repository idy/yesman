"""Benign normal contracts for QA049-1/2/3. No network or package execution."""
import ast
import hashlib
import importlib.util
import io
import os
import subprocess
import tempfile
import unittest
import zipfile
from pathlib import Path
from unittest.mock import patch

import bind
import provenance as p

ROOT = Path(__file__).resolve().parents[2]
WORKFLOW = ROOT / p.WORKFLOW
GUARD = '    if: inputs.restart_qa_commit == github.sha && github.run_attempt == 1\n'


class NormalRoute(unittest.TestCase):
    def test_workflow_source_depth_and_all_five_first_attempt_guards(self):
        text = WORKFLOW.read_text()
        for job in ('restart-download', 'restart-public-npm', 'restart-sign-qa', 'restart-bind', 'restart-native'):
            block = text.split('\n  ' + job + ':\n', 1)[1].split('\n  restart-', 1)[0]
            self.assertTrue(block.startswith(GUARD), job)
            self.assertIn('persist-credentials: false', block)
        signing = text.split('\n  restart-sign-qa:\n', 1)[1].split('\n  restart-bind:\n', 1)[0]
        source_checkout = signing.split('          ref: ' + p.RELEASE + '\n', 1)[1].split('      - ', 1)[0]
        self.assertIn('          fetch-depth: 2', source_checkout)
        self.assertIn('          persist-credentials: false', source_checkout)

    def test_normal_bare_upload_api_prefix_and_computed_zip_at_transfer(self):
        content = b'normal synthetic transfer contract\n'
        output = io.BytesIO()
        with zipfile.ZipFile(output, 'w') as archive:
            archive.writestr('selected/data.txt', content)
        raw = output.getvalue()
        action_output = hashlib.sha256(raw).hexdigest()  # upload-artifact output
        api_digest = 'sha256:' + action_output
        api = {'id': 7, 'expired': False, 'digest': api_digest, 'workflow_run': {'id': 123}}
        run = {'head_sha': 'a' * 40, 'run_attempt': 1, 'path': p.WORKFLOW}
        calls = []
        def get(endpoint, binary=False):
            calls.append((endpoint, binary))
            return {('repos/idy/yesman/actions/artifacts/7', False): api,
                    ('repos/idy/yesman/actions/runs/123', False): run,
                    ('repos/idy/yesman/actions/artifacts/7/zip', True): raw}[(endpoint, binary)]
        with tempfile.TemporaryDirectory() as directory, patch.dict(os.environ, {'GITHUB_RUN_ID': '123', 'GITHUB_SHA': 'a' * 40, 'GITHUB_RUN_ATTEMPT': '1'}), patch.object(bind, 'get', side_effect=get):
            destination = Path(directory) / 'transfer'
            receipt = bind.transfer(7, action_output, destination)
            self.assertEqual((destination / 'selected/data.txt').read_bytes(), content)
            self.assertEqual(receipt['artifactDigest'], api_digest)
            self.assertEqual(receipt['uploadActionSHA256'], action_output)
            self.assertEqual(receipt['zipSHA256'], action_output)
            self.assertTrue(receipt['firstAttemptOnly'] and receipt['zipDigestVerified'])
            self.assertEqual(receipt['artifactID'], 7)
            self.assertEqual(receipt['artifactAPI'], api)
            self.assertEqual(len(calls), 3)

    def test_normal_first_attempt_command_guards_precede_effects(self):
        with patch.dict(os.environ, {'GITHUB_RUN_ATTEMPT': '1'}):
            self.assertIsNone(p.first_attempt())
        for name, function in [('download.py', 'main'), ('public_npm_cache.py', 'acquire'), ('bind.py', 'main'), ('run_native.py', 'main'), ('compat.py', 'run'), ('runner-attachment-f48/runner_attachment.py', 'run')]:
            module = ast.parse((Path(__file__).parent / name).read_text())
            node = next(n for n in module.body if isinstance(n, ast.FunctionDef) and n.name == function)
            self.assertEqual(ast.unparse(node.body[0]).split('(')[0].split('.')[-1], 'first_attempt', name)

    def test_normal_actual_S_depth_two_parent_metadata_admission(self):
        spec = importlib.util.spec_from_file_location('producer_admission', ROOT / 'scripts/prepare-upgrade-qa.py')
        producer = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(producer)
        with tempfile.TemporaryDirectory() as directory:
            scratch = Path(directory)
            shallow = scratch / 'depth-two-boundary'
            shallow.write_text(p.INTEGRATION + '\n')
            # Read the exact fixed objects through the sole owner's worktree.
            # A private shallow boundary models S+parent only; no checkout,
            # fetch, index, refs or shared Git files are changed.
            git_env = {k: os.environ[k] for k in ('PATH', 'SYSTEMROOT', 'WINDIR') if k in os.environ}
            git_env.update(HOME=str(scratch), GIT_CONFIG_NOSYSTEM='1', GIT_CONFIG_GLOBAL=os.devnull,
                           GIT_OPTIONAL_LOCKS='0', GIT_SHALLOW_FILE=str(shallow))
            reads = []
            class Group:
                def run(owner, args, **kwargs):
                    if args[:2] == ['go', 'env']:
                        return {'GOOS': b'darwin', 'GOARCH': b'arm64'}[args[2]]
                    args = list(args)
                    if args == ['git', 'rev-parse', 'HEAD']:
                        args[-1] = p.RELEASE  # fixed S input role; owner tree stays on QA HEAD
                    value = subprocess.check_output(['git', '-C', str(ROOT), *args[1:]], env=git_env, timeout=10)
                    reads.append((args, value.decode().strip()))
                    return value
            self.assertEqual(Group().run(['git', 'rev-parse', '--is-shallow-repository']).strip(), b'true')
            class AdmissionReached(Exception): pass
            def after_admission(*args): raise AdmissionReached()
            meta = {'refs': {'v0.1.49': p.RELEASE}, 'restartAcceptance': {'candidate': p.CANDIDATE, 'tree': p.TREE, 'productIntegration': p.INTEGRATION}}
            with patch.dict(os.environ, {'GITHUB_RUN_ATTEMPT': '1'}), patch.object(producer, 'helper_blobs', side_effect=after_admission):
                with self.assertRaises(AdmissionReached):
                    producer._successors(Group(), '', scratch, scratch, meta)
            self.assertIn((['git', 'cat-file', '-e', p.INTEGRATION + '^{commit}'], ''), reads)
            self.assertIn((['git', 'show', '-s', '--format=%P', p.RELEASE], p.INTEGRATION), reads)
            self.assertIn((['git', 'diff', '--name-only', p.INTEGRATION, p.RELEASE], 'CHANGELOG.md\nmygo.json'), reads)
            print('READ-ONLY normal S admission: parent=' + p.INTEGRATION + '; metadata paths=CHANGELOG.md,mygo.json; stopped before helper/build/sign')


if __name__ == '__main__':
    unittest.main()
