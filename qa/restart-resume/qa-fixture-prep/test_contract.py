"""Pure contract checks; no server/pi/package launch or signing.

Run with TMPDIR and PYTHONPYCACHEPREFIX under this task's scratch directory.
Positive rows are manufactured contract controls, never native proof.
"""
import copy
import json
from pathlib import Path
import tempfile
import unittest
from fixture import Refused, validate_contract, assert_episode, request_policy, obstruction_plan, driver_text, digest
from controller import Controller, app_environment, pi_config


def contract(scenario='update'):
    return {'schema': 1, 'run': 'a' * 32, 'target': 'darwin-arm64', 'scenario': scenario,
            'source': 'b' * 40, 'tree': 'c' * 40, 'helperLiteralSHA256': '6c4ec4630929b75d034ffaef8b58d2f7f9521336074d39a90ba8874846c26efc', 'runtimeSHA256': 'd' * 64, 'extensionSHA256': 'e' * 64,
            'eligible': ['active/lead'], 'paused': ['paused/lead'],
            'S': {'version': '0.1.81', 'source': 'b' * 40, 'tree': 'c' * 40, 'archiveSHA256': '1' * 64, 'mainSHA256': '2' * 64, 'evidence': 'synthetic-control'},
            'Q': {'version': '0.1.900002', 'source': 'b' * 40, 'tree': 'c' * 40, 'archiveSHA256': '3' * 64, 'mainSHA256': '4' * 64, 'evidence': 'synthetic-control', 'privateQA': True}}


def episode(c):
    last = {'id': 'f' * 32, 'kind': 'update', 'fromVersion': c['S']['version'], 'toVersion': c['Q']['version'], 'version': c['Q']['version'],
            'agents': [{'yesman': 'active', 'name': 'lead', 'outcome': 'resumed'}]}
    return {'run': c['run'], 'scenario': c['scenario'],
            'before': {'home': 'synthetic/data', 'pid': 1, 'version': c['S']['version'], 'mainSHA256': c['S']['mainSHA256'], 'source': c['source'], 'instanceCount': 1},
            'after': {'home': 'synthetic/data', 'pid': 2, 'version': c['Q']['version'], 'mainSHA256': c['Q']['mainSHA256'], 'source': c['source'], 'instanceCount': 1},
            'last': last, 'restartDirNames': ['last.json'], 'reopen': {'lastID': last['id'], 'newRestartEntries': 0, 'newProviderRequests': 0},
            'sessions': {a: {'beforeID': 's-' + a, 'afterID': 's-' + a, 'beforePath': a + '.jsonl', 'afterPath': a + '.jsonl',
                           'prefixPreserved': True, 'originalEntries': 1, 'restartEntries': int(a in c['eligible']),
                           'providerContinuationRequests': int(a in c['eligible'])} for a in c['eligible'] + c['paused']},
            'failure': {'armedBeforeRestart': True, 'oldExited': True, 'helperExited': True, 'launchErrorObserved': True,
                        'QProcesses': 0, 'QBridgeReports': 0, 'claimedNames': [],
                        'finalIntent': {'phase': 'final', 'id': last['id'], 'kind': 'update', 'fromVersion': c['S']['version'], 'toVersion': c['Q']['version']}, 'intentUnchangedUntilRecovery': True,
                        'recoveryAgeSeconds': 4, 'restoredMainSHA256': c['Q']['mainSHA256'], 'bundleVerificationPassed': True}}


class Contracts(unittest.TestCase):
    def test_replaceable_input_bindings(self):
        c = contract()
        validate_contract(c)
        with self.assertRaises(Refused):
            validate_contract(json.loads(Path(__file__).with_name('contract.template.json').read_text()))
        for mutate in [lambda c: c.update(target='darwin-amd64'), lambda c: c.update(source='0' * 40),
                       lambda c: c.update(helperLiteralSHA256='7' * 64),
                       lambda c: c['Q'].update(source='7' * 40), lambda c: c['Q'].update(privateQA=False),
                       lambda c: c['paused'].append('active/lead')]:
            bad = copy.deepcopy(c); mutate(bad)
            with self.assertRaises(Refused): validate_contract(bad)

    def test_wrong_image_duplicate_note_pause_and_second_open_rejected(self):
        c = contract(); good = episode(c)
        self.assertFalse(assert_episode(c, good)['nativeAcceptance'])
        mutations = [lambda e: e['after'].update(mainSHA256='5' * 64), lambda e: e['after'].update(home='wrong-profile'),
                     lambda e: e['after'].update(pid=1), lambda e: e['after'].update(instanceCount=2),
                     lambda e: e['sessions']['active/lead'].update(restartEntries=2),
                     lambda e: e['sessions']['paused/lead'].update(providerContinuationRequests=1),
                     lambda e: e['sessions']['active/lead'].update(afterID='fork'),
                     lambda e: e['sessions']['active/lead'].update(originalEntries=2),
                     lambda e: e['reopen'].update(newProviderRequests=1), lambda e: e.update(restartDirNames=['intent.json', 'last.json'])]
        for mutate in mutations:
            bad = copy.deepcopy(good); mutate(bad)
            with self.assertRaises(Refused): assert_episode(c, bad)

    def test_failure_is_unclaimed_final_then_same_id_recovery(self):
        c = contract('failed-launch-recovery'); good = episode(c)
        assert_episode(c, good)
        for field, value in [('armedBeforeRestart', False), ('launchErrorObserved', False), ('QProcesses', 1),
                             ('QBridgeReports', 1), ('claimedNames', ['claimed.json']), ('recoveryAgeSeconds', 901),
                             ('bundleVerificationPassed', False), ('finalIntent', {'phase': 'provisional', 'id': 'f' * 32}),
                             ('finalIntent', {'phase': 'final', 'id': '9' * 32})]:
            bad = copy.deepcopy(good); bad['failure'][field] = value
            with self.assertRaises(Refused): assert_episode(c, bad)

    def test_provider_holds_then_continues_only_preserved_context(self):
        c = contract(); actor = 'active/lead'; tag = '[t199:' + c['run'] + ':' + actor + ']'
        original = {'role': 'user', 'content': tag + ' Remember the synthetic fact: blue comet.'}
        note = {'role': 'user', 'content': '[yesman] Yes Man restarted to update'}
        self.assertEqual(request_policy([original], c['run'], [actor])['action'], 'hold')
        self.assertEqual(request_policy([original, note], c['run'], [actor])['action'], 'continue')
        for messages in [[note], [original, original, note], [original, note, note],
                         [original, {'role': 'user', 'content': '[yesman] These tasks need a next step'}]]:
            with self.assertRaises(Refused): request_policy(messages, c['run'], [actor])

    def test_commands_never_replay_after_serving_or_page_change(self):
        with tempfile.TemporaryDirectory() as d:
            ctl = Controller(contract(), Path(d) / 'journal')
            ctl.report('S-page', {'version': 'synthetic'}); ctl.select_page('S-page')
            ident = ctl.issue('RestartToUpdate', [])
            self.assertIsNone(ctl.take('Q-page'))
            self.assertIsNotNone(ctl.take('S-page')); self.assertIsNone(ctl.take('S-page'))
            with self.assertRaises(Refused): ctl.issue('RestartToUpdate', [])
            with self.assertRaises(Refused): ctl.ack('done', 'S-page', ident)
            ctl.ack('started', 'S-page', ident)
            with self.assertRaises(Refused): ctl.ack('done', 'Q-page', ident)
            ctl.confirm_restart_departure(ident)
            ctl.report('Q-page', {'version': 'synthetic-Q'}); ctl.select_page('Q-page')
            self.assertIsNone(ctl.take('Q-page'))
            with self.assertRaises(Refused): ctl.issue('Reveal', ['/Applications'])
            self.assertEqual(len([x for x in Path(d, 'journal').read_text().splitlines() if json.loads(x)['kind'] == 'command-first-served']), 1)

    def test_obstruction_plan_is_prearmed_and_keeps_exact_bytes(self):
        # Dummy text only: do not run arm/recover or any executable here.
        with tempfile.TemporaryDirectory() as d:
            install = Path(d) / 'owned install'
            staged = install / '.yesman.app.update-fixture' / 'app' / 'yesman.app'
            main = staged / 'Contents/MacOS/Yes Man'; main.parent.mkdir(parents=True)
            main.write_bytes(b'synthetic-not-an-app'); main.chmod(0o755)
            plan = obstruction_plan(install, staged, 'a' * 32, digest(main.read_bytes()))
            self.assertEqual(plan['main']['bytes'], 20)
            self.assertTrue(main.exists())
            with self.assertRaises(Refused): obstruction_plan(install, staged, 'a' * 32, '7' * 64)
            held = main.with_name(plan['heldName']); held.write_bytes(b'occupied')
            with self.assertRaises(Refused): obstruction_plan(install, staged, 'a' * 32, digest(main.read_bytes()))

    def test_runtime_env_is_allowlisted_and_loopback(self):
        env = app_environment({'PATH': '/bin', 'HOME': '/clean-vm', 'GH_TOKEN': 'synthetic', 'OPENAI_API_KEY': 'synthetic',
                               'MYGO_UPDATER_PRIVATE_KEY': 'synthetic', 'PI_CODING_AGENT_DIR': '/ambient'}, 'data', 'config', 'web', 'tmp', 'http://127.0.0.1:37059')
        self.assertFalse(any(k in env for k in ('GH_TOKEN', 'OPENAI_API_KEY', 'MYGO_UPDATER_PRIVATE_KEY')))
        self.assertEqual(env['PI_CODING_AGENT_DIR'], 'config')
        cfg = pi_config('http://127.0.0.1:12345/v1')
        self.assertEqual(cfg['settings.json']['packages'], [])
        for url in ['https://api.example/v1', 'http://localhost:12345/v1', 'http://user:pass@127.0.0.1:12345/v1']:
            with self.assertRaises(Refused): pi_config(url)
        script = driver_text('http://127.0.0.1:12345/' + 'a' * 32)
        self.assertNotIn('__ENDPOINT__', script)
        with self.assertRaises(Refused): driver_text('http://example.com/' + 'a' * 32)


if __name__ == '__main__':
    unittest.main(verbosity=2)
