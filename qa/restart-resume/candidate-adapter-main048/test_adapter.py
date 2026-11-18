"""Pure adapter contracts. Dummy JSONL/bundle bytes only; no live profile,
server, pi, native command, package execution or source test is run.
"""
import copy
import json
from pathlib import Path
import tempfile
import unittest
from adapter import (CANDIDATE, TREE, seed_plan, submit_command, bind_bridge, parse_session,
                     session_delta, receipt_delta, capture_actor, capture_restart, bind_final_to_result)
from host_proof import process_rows, bundle_inventory, helper_error

RUN = 'a' * 32
SUB = '000000000001' + 'b' * 36
TEXT = '[t199:' + RUN + ':active/lead] unfinished synthetic fact'


def line(v): return (json.dumps(v) + '\n').encode()


def session(cwd, text=TEXT):
    return b''.join(line(e) for e in [
        {'type': 'session', 'version': 3, 'id': 'same-session', 'cwd': str(cwd)},
        {'type': 'custom', 'id': 'o0', 'parentId': None, 'customType': 'yesman-origin', 'data': {'timestamp': 1, 'origin': {'source': 'user', 'submission': SUB}}},
        {'type': 'message', 'id': 'u0', 'parentId': 'o0', 'message': {'role': 'user', 'timestamp': 1, 'content': [{'type': 'text', 'text': text}]}},
    ])


def resumed(raw):
    return raw + line({'type': 'custom', 'id': 'r0', 'parentId': 'u0', 'customType': 'yesman-origin', 'data': {'timestamp': 2, 'origin': {'source': 'yesman', 'kind': 'restart'}}}) + line(
        {'type': 'message', 'id': 'r1', 'parentId': 'r0', 'message': {'role': 'user', 'timestamp': 2, 'content': [{'type': 'text', 'text': '[yesman] Yes Man restarted to update'}]}})


class AdapterContracts(unittest.TestCase):
    def test_profile_and_real_submit_schema(self):
        plan = seed_plan('/synthetic-hosted-home', 'http://127.0.0.1:37051/v1')
        data = json.loads(plan['files']['/synthetic-hosted-home/yesman/settings.json'])
        self.assertEqual(data['crew']['yesman'], 't199-fixture/fixture')
        self.assertEqual(data['crew']['options'][0]['score'], {'intelligence': 1})
        self.assertEqual(len(plan['files']), 3)
        cfg = json.loads(plan['files']['/synthetic-hosted-home/.pi/agent/settings.json'])
        self.assertEqual(cfg['extensions'], []); self.assertEqual(cfg['retry'], {'enabled': False})
        cmd = submit_command('active/lead', '2026-11-18T00:00:00Z', RUN, 1000, 'b' * 36)
        self.assertEqual(cmd['method'], 'Submit'); self.assertEqual(len(cmd['submission']), 48)
        self.assertEqual(cmd['args'][3:], [[], '2026-11-18T00:00:00Z'])
        with self.assertRaises(ValueError): submit_command('other/lead', '', RUN, 1000, 'b' * 36)

    def test_initial_runtime_and_private_Q_revision_are_distinct(self):
        obs = {'about': {'version': '0.1.900002', 'piManaged': True, 'piState': 'ready'},
               'state': {'home': '/synthetic/data'}}
        native = {'pid': 10, 'mainSHA256': '4' * 64, 'instanceCount': 1}
        stage = {'version': '0.1.900002', 'mainSHA256': '4' * 64, 'candidate': CANDIDATE, 'candidateTree': TREE,
                 'verificationReceipt': 'synthetic-control', 'official': False, 'privateQA': True,
                 'sourceFilesVerified': True, 'sourceCommit': 'c' * 40}
        result = bind_bridge(obs, native, '/synthetic/data', stage)
        self.assertIsNone(result['observedRevision']); self.assertEqual(result['sourceProvenance'], 'c' * 40)
        obs['about']['piState'] = 'unprepared'
        bind_bridge(obs, native, '/synthetic/data', stage, epoch='initial')
        with self.assertRaises(ValueError): bind_bridge(obs, native, '/synthetic/data', stage)
        obs['about']['piState'] = 'ready'; stage['official'] = True
        with self.assertRaises(ValueError): bind_bridge(obs, native, '/synthetic/data', stage)
        obs['about']['revision'] = 'c' * 40; bind_bridge(obs, native, '/synthetic/data', stage)
        for field, value in [('home', '/wrong')]:
            obs['state'][field] = value
            with self.assertRaises(ValueError): bind_bridge(obs, native, '/synthetic/data', stage)

    def test_raw_session_origin_identity_pause_and_replay_oracles(self):
        before = session('/synthetic/ws'); after = resumed(before)
        result = session_delta(before, after, SUB, TEXT, True)
        self.assertEqual(result['restartEntries'], 1)
        session_delta(before, before, SUB, TEXT, False)
        with self.assertRaises(ValueError): session_delta(before, after, SUB, TEXT, False)
        with self.assertRaises(ValueError): session_delta(before, after.replace(b'same-session', b'other-session'), SUB, TEXT, True)
        with self.assertRaises(ValueError): parse_session(after + after.splitlines(keepends=True)[-1])
        with self.assertRaises(ValueError): parse_session(after.replace(b'"kind": "restart"', b'"kind": "tasks"').replace(b'"parentId": "r0"', b'"parentId": "o0"'))
        with self.assertRaises(ValueError): parse_session(before.replace(b'"timestamp": 1, "origin"', b'"timestamp": 99, "origin"'))
        with self.assertRaises(ValueError): parse_session(before[:-1])

    def test_receipt_is_preserved_not_inferred_consumed(self):
        disk = {'version': 2, 'records': [{'id': SUB, 'fingerprint': 'd' * 64, 'imageCount': 0, 'state': 'accepted'}]}
        after = copy.deepcopy(disk); after['records'][0]['state'] = 'interrupted'
        self.assertFalse(receipt_delta(json.dumps(disk), json.dumps(after), SUB)['consumptionClaimed'])
        after['records'].append({'id': 'unexpected-system-submission'})
        with self.assertRaises(ValueError): receipt_delta(json.dumps(disk), json.dumps(after), SUB)

    def test_collectors_read_only_declared_synthetic_files(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            for actor in ('active', 'paused'):
                meta = root / 'yesmen' / actor / '.yesman'; sd = meta / 'sessions/lead'; sd.mkdir(parents=True)
                (meta / 'yesman.json').write_text(json.dumps({'name': actor, 'hiredAt': 'same-hire'}))
                (sd / 'one.jsonl').write_bytes(session(meta.parent, TEXT.replace('active/lead', actor + '/lead')))
                (sd / 'delivery.json').write_text(json.dumps({'version': 2, 'records': [{'id': SUB}]}))
                if actor == 'paused': (meta / 'continuation.json').write_text(json.dumps({'hiredAt': 'same-hire', 'leadPausedAt': 'stop-time'}))
            self.assertEqual(capture_actor(root, 'paused', RUN)['leadPausedAt'], 'stop-time')
            self.assertIsNone(capture_actor(root, 'active', RUN)['raw']['continuation.json'])
            p = root / 'yesmen/paused/.yesman/continuation.json'; p.unlink()
            with self.assertRaises(ValueError): capture_actor(root, 'paused', RUN)
            self.assertEqual(capture_restart(root)['names'], [])

    def test_unclaimed_final_binds_same_completed_result(self):
        intent = {'version': 3, 'phase': 'final', 'id': 'e' * 32, 'kind': 'update', 'fromVersion': 'S', 'toVersion': 'Q'}
        last = {k: v for k, v in intent.items() if k not in ('phase',)}; last['version'] = 'Q'
        last['agents'] = [{'yesman': 'active', 'name': 'lead', 'outcome': 'resumed'}]
        failed = {'names': ['intent.json'], 'claimedNames': [], 'files': {'intent.json': {'value': intent, 'sha256': '1' * 64}}}
        final = {'names': ['last.json'], 'files': {'last.json': {'value': last, 'sha256': '2' * 64}}}
        self.assertFalse(bind_final_to_result(failed, final)['nativeAcceptance'])
        bad = copy.deepcopy(final); bad['files']['last.json']['value']['id'] = 'f' * 32
        with self.assertRaises(ValueError): bind_final_to_result(failed, bad)
        bad = copy.deepcopy(final); bad['files']['last.json']['value']['agents'].append({'yesman': 'paused', 'name': 'lead', 'outcome': 'resumed'})
        with self.assertRaises(ValueError): bind_final_to_result(failed, bad)
        failed['claimedNames'] = ['claimed-other.json']
        with self.assertRaises(ValueError): bind_final_to_result(failed, final)

    def test_inventory_and_process_parser_are_image_scoped(self):
        rows = process_rows(b'10 1 /fixture/yesman.app/Contents/MacOS/Yes Man\n20 1 /other/Yes Man\n', '/fixture/yesman.app/Contents/MacOS/Yes Man')
        self.assertEqual([r['pid'] for r in rows], [10])
        self.assertFalse(helper_error(b'timeout waiting for app')['launchFailureProven'])
        with tempfile.TemporaryDirectory() as tmp:
            b = Path(tmp) / 'yesman.app'; m = b / 'Contents/MacOS/Yes Man'; m.parent.mkdir(parents=True); m.write_bytes(b'dummy-not-MachO')
            original = bundle_inventory(b)
            held = m.with_name('.held'); m.rename(held)
            with self.assertRaises(ValueError): bundle_inventory(b)
            bundle_inventory(b, require_main=False); held.rename(m)
            self.assertEqual(bundle_inventory(b), original)


if __name__ == '__main__': unittest.main(verbosity=2)
