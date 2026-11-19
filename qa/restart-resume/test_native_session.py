"""Inert R8 controls; exact raw samples supplied explicitly, never a live profile.
T199_R7_RAW names the sealed r7-local-diagnosis evidence packet. No App/Pi,
provider, compiler, credential or network action is performed by this suite.
"""
import ast
import copy
import importlib.util
import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

import native_session as n
import freeze_native_diagnostics as freezer

ROOT = Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location('t199_r8_attachment', ROOT / 'runner-attachment-f48/runner_attachment.py')
attachment = importlib.util.module_from_spec(spec)
spec.loader.exec_module(attachment)
original, fixture, core, collectors = attachment.dependencies(ROOT)
RUN = '0123456789abcdef0123456789abcdef'
SUB = '01a1f120804c' + 'a' * 36
OTHER = '01a1f120804c' + 'b' * 36
TEXT = '[t199:' + RUN + ':paused/lead] Remember this synthetic fact; this goal is unfinished.'
HIRE = '2026-11-19T00:06:41.483239Z'


def line(value):
    return (json.dumps(value) + '\n').encode()


def session(cwd='/synthetic/paused'):
    return [dict(type='session', version=3, id='fixture-session', cwd=cwd),
        dict(type='message', id='system', parentId=None, message=dict(role='system', content='Synthetic context')),
        dict(type='message', id='user', parentId='system', message=dict(role='user', content=TEXT, timestamp=1)),
        dict(type='custom', id='identity', parentId='user', customType='yesman-input-v1',
             data=dict(entryId='user', identity=dict(submissionId=SUB, origin=dict(source='user'))))]


def raw(entries):
    return b''.join(line(e) for e in entries)


def restart(entries, source='yesman', submission=None):
    e = copy.deepcopy(entries)
    e.append(dict(type='message', id='restart', parentId=e[-1]['id'], message=dict(role='user', content='[yesman] Yes Man restarted. Continue the unfinished goal.', timestamp=1)))
    ident = dict(origin=dict(source=source, kind='restart'))
    if submission:
        ident['submissionId'] = submission
    e.append(dict(type='custom', customType='yesman-input-v1', id='restart-id', parentId='restart', data=dict(entryId='restart', identity=ident)))
    return e


class Reader(unittest.TestCase):
    def parse(self, entries):
        return n.parse_session(raw(entries), format=n.NATIVE)

    def test_explicit_contract_and_no_legacy_fallback(self):
        self.assertEqual(len(self.parse(session())['users']), 1)
        with self.assertRaisesRegex(ValueError, 'explicit pinned'):
            n.parse_session(raw(session()), format='auto')
        e = session()
        e[1] = dict(type='custom', customType='yesman-origin', id='system', parentId=None,
                    data=dict(timestamp=1, origin=dict(source='user', submission=SUB)))
        e.pop()
        self.assertEqual(len(n.parse_legacy(original, raw(e))['users']), 1)
        with self.assertRaisesRegex(ValueError, 'legacy origin'):
            self.parse(e)
        e.append(session()[-1]); e[-1]['data']['identity'] = None
        with self.assertRaises(ValueError): self.parse(e)

    def test_missing_mismatched_duplicate_foreign_identity(self):
        variants = []
        for field, value in [('parentId', 'system'), ('customType', 'wrong')]:
            e = session(); e[-1][field] = value; variants.append(e)
        for value in (None, {}, {'submissionId': SUB}, {'submissionId': SUB, 'origin': {'source': 'lead'}},
                      {'submissionId': SUB, 'origin': {'source': 'user'}, 'segments': [OTHER]},
                      {'submissionId': 'bad', 'origin': {'source': 'user'}}):
            e = session(); e[-1]['data']['identity'] = value; variants.append(e)
        e = session(); e[-1]['data']['entryId'] = 'system'; variants.append(e)
        e = session(); e[-1]['data']['entryId'] = 'missing'; variants.append(e)
        e = session(); e[-1]['data']['extra'] = True; variants.append(e)
        variants.append(session()[:-1])
        e = session(); extra = copy.deepcopy(e[-1]); extra.update(id='second-id', parentId='identity'); e.append(extra); variants.append(e)
        e = session(); extra = copy.deepcopy(e[-1]); extra['parentId'] = 'identity'; e.append(extra); variants.append(e)
        e = restart(session()); e[-1]['data']['identity'] = session()[-1]['data']['identity']; variants.append(e)
        for index, entries in enumerate(variants):
            with self.subTest(index=index), self.assertRaises(ValueError): self.parse(entries)

    def test_canonical_chain_uniqueness_and_raw_prefix(self):
        e = session(); e[-1]['id'] = 'user'
        with self.assertRaises(ValueError): self.parse(e)
        e = session(); e[1]['parentId'] = 'missing'
        with self.assertRaises(ValueError): self.parse(e)
        for b in (b'', raw(session())[:-1], raw(session()).replace(b'"version": 3', b'"version": 3, "version": 3')):
            with self.assertRaises(ValueError): n.parse_session(b, format=n.NATIVE)
        e = session(); e[0]['parentSession'] = '/other'
        with self.assertRaises(ValueError): self.parse(e)
        e = session(); e[0]['cwd'] = '/other'
        with self.assertRaises(ValueError): n.session_delta(raw(session()), raw(e), SUB, TEXT, False, format=n.NATIVE)

    def test_timestamps_are_not_native_identity(self):
        e = session(); e[2]['message']['timestamp'] = {'untrusted': 'unused'}
        e[-1]['timestamp'] = 'different'
        self.assertEqual(self.parse(e)['users'][0]['origin']['submission'], SUB)
        e[-1]['data']['identity'] = None
        with self.assertRaises(ValueError): self.parse(e)

    def test_same_text_different_submission_cannot_substitute(self):
        e = session(); e[-1]['data']['identity']['submissionId'] = OTHER
        with self.assertRaisesRegex(ValueError, 'original receipt'):
            n.session_delta(raw(e), raw(e), SUB, TEXT, False, format=n.NATIVE)

    def test_eligible_paused_and_ordinary_reopen_counts(self):
        before = raw(session()); after = raw(restart(session()))
        self.assertEqual(n.session_delta(before, after, SUB, TEXT, True, format=n.NATIVE)['restartEntries'], 1)
        self.assertEqual(n.session_delta(after, after, SUB, TEXT, True, format=n.NATIVE)['restartEntries'], 1)
        self.assertEqual(n.session_delta(before, before, SUB, TEXT, False, format=n.NATIVE)['restartEntries'], 0)
        for a, eligible in ((before, True), (after, False), (raw(restart(session(), submission=OTHER)), True)):
            with self.assertRaises(ValueError): n.session_delta(before, a, SUB, TEXT, eligible, format=n.NATIVE)
        extra = restart(session()); extra.append(dict(type='message', id='again', parentId='restart-id', message=dict(role='user', content=TEXT)))
        extra.append(dict(type='custom', id='again-id', parentId='again', customType='yesman-input-v1', data=dict(entryId='again', identity=dict(submissionId=OTHER, origin=dict(source='user')))))
        with self.assertRaises(ValueError): n.session_delta(before, raw(extra), SUB, TEXT, True, format=n.NATIVE)


class Capture(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(); self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name); self.data = self.root / 'data'
        self.meta = self.data / 'yesmen/paused/.yesman'
        self.directory = self.meta / 'sessions/lead'; self.directory.mkdir(parents=True)
        self.entries = session(str(self.meta.parent))
        self.command = dict(submission=SUB, text=TEXT, args=['paused/lead', SUB, TEXT, [], HIRE])
        self.reader = n.NativeAdapter(original, self.root / 'captures', {'paused': self.command}, n.RUNTIME_SHA256)
        self.delivery = dict(version=2, records=[dict(id=SUB, agentSince=HIRE, session='a'*48+':fixture-session',
            fingerprint=n.sha(len(TEXT.encode()).to_bytes(8, 'big')+TEXT.encode()+bytes(8)), imageCount=0, state='dispatched')])
        self.pause = dict(hiredAt=HIRE, leadPausedAt='durable-stop')
        self.write_inputs()

    def write_inputs(self):
        (self.meta / 'yesman.json').write_bytes(line(dict(hiredAt=HIRE)))
        (self.meta / 'continuation.json').write_bytes(line(self.pause))
        (self.directory / 'canonical.jsonl').write_bytes(raw(self.entries))
        (self.directory / 'delivery.json').write_bytes(line(self.delivery))

    def capture(self):
        return self.reader.capture_actor(self.data, 'paused', RUN)

    def retained(self):
        row = self.reader.rows[-1]; folder = self.reader.output / row['capture']
        self.assertEqual(row['status'], 'refused')
        for name in ('yesman.json', 'continuation.json', 'delivery.json', 'session-0.jsonl'):
            body = (folder / name).read_bytes(); self.assertEqual(n.sha(body), row['files'][name]['sha256'])
            self.assertEqual((folder/name).stat().st_mode & 0o777, 0o600)
        self.assertEqual(json.loads((folder/'capture.json').read_bytes()), row)
        return folder

    def test_actual_capture_saves_all_bytes_before_first_decode(self):
        decode = n.decode; seen = []
        def observe(body):
            if not seen:
                folder = self.reader.output / self.reader.rows[-1]['capture']
                for name in ('yesman.json', 'continuation.json', 'delivery.json', 'session-0.jsonl'):
                    self.assertTrue((folder/name).exists())
            seen.append(True)
            return decode(body)
        with patch.object(n, 'decode', side_effect=observe): value = self.capture()
        self.assertEqual(value['leadPausedAt'], 'durable-stop')
        self.assertEqual(self.reader.parse_session(value['raw']['session.jsonl']), value['session'])

    def test_semantic_refusal_keeps_exact_raw_session_pause_delivery(self):
        self.entries[-1]['data']['identity'] = None; self.write_inputs()
        with self.assertRaises(ValueError): self.capture()
        folder = self.retained()
        self.assertEqual((folder/'session-0.jsonl').read_bytes(), raw(self.entries))
        self.assertEqual((folder/'continuation.json').read_bytes(), line(self.pause))
        self.assertEqual((folder/'delivery.json').read_bytes(), line(self.delivery))

    def test_bad_metadata_decode_still_retains_other_operands(self):
        (self.meta/'yesman.json').write_bytes(b'{malformed\n')
        with self.assertRaises(ValueError): self.capture()
        folder = self.retained(); self.assertEqual((folder/'yesman.json').read_bytes(), b'{malformed\n')

    def test_duplicate_sessions_retained_before_unique_session_refusal(self):
        (self.directory/'duplicate.jsonl').write_bytes(raw(self.entries))
        with self.assertRaisesRegex(ValueError, 'exactly one'): self.capture()
        self.retained(); self.assertEqual(self.reader.rows[-1]['sessionCountUpToFive'], 2)
        self.assertEqual(self.reader.rows[-1]['files']['session-1.jsonl']['sha256'], n.sha(raw(self.entries)))

    def test_foreign_submission_hire_session_pause_and_receipt_refuse(self):
        for variant in ('identity', 'hire', 'session', 'pause', 'receipt-id', 'receipt-duplicate', 'fingerprint', 'cwd'):
            with self.subTest(variant=variant):
                e, d, pause = copy.deepcopy(self.entries), copy.deepcopy(self.delivery), copy.deepcopy(self.pause)
                if variant=='identity': e[-1]['data']['identity']['submissionId']=OTHER
                if variant=='hire': self.command['args'][4]='other-hire'
                if variant=='session': d['records'][0]['session']='a'*48+':other'
                if variant=='pause': pause['hiredAt']='other-hire'
                if variant=='receipt-id': d['records'][0]['id']=OTHER
                if variant=='receipt-duplicate': d['records'].append(copy.deepcopy(d['records'][0]))
                if variant=='fingerprint': d['records'][0]['fingerprint']='f'*64
                if variant=='cwd': e[0]['cwd']='/other'
                (self.directory/'canonical.jsonl').write_bytes(raw(e)); (self.directory/'delivery.json').write_bytes(line(d)); (self.meta/'continuation.json').write_bytes(line(pause))
                with self.assertRaises((ValueError, KeyError)): self.capture()
                self.retained(); self.command['args'][4]=HIRE

    def test_missing_pause_or_delivery_is_not_accepted(self):
        (self.meta/'continuation.json').unlink()
        with self.assertRaisesRegex(ValueError, 'paused control'): self.capture()
        self.assertEqual(self.reader.rows[-1]['files']['continuation.json']['status'], 'absent')
        (self.directory/'delivery.json').unlink()
        with self.assertRaises(ValueError): self.capture()
        self.assertTrue((self.reader.output/self.reader.rows[-1]['capture']/'session-0.jsonl').exists())

    def test_missing_unstable_symlink_fifo_oversize_reads_remain_strict(self):
        for name in ('yesman.json', 'continuation.json'):
            path=self.meta/name; prior=path.read_bytes(); path.unlink(); path.symlink_to(self.directory/'delivery.json')
            with self.assertRaises(ValueError): self.capture()
            self.assertEqual(self.reader.rows[-1]['files'][name]['status'], 'unavailable-or-unstable')
            path.unlink();path.write_bytes(prior)
        path=self.directory/'canonical.jsonl'; prior=path.read_bytes();path.unlink();os.mkfifo(path)
        with self.assertRaises(ValueError): self.capture()
        path.unlink();path.write_bytes(prior)
        with path.open('ab') as stream:stream.truncate((4<<20)+1)
        with self.assertRaises(ValueError): self.capture()

    def test_original_actor_comparator_causally_fails_native_new_passes(self):
        before=self.capture(); after=copy.deepcopy(before)
        with self.assertRaisesRegex(ValueError, 'canonical origin parent missing'):
            original.actor_delta(before, after, self.command, False)
        self.assertEqual(self.reader.actor_delta(before,after,self.command,False)['sessions']['restartEntries'],0)
        self.assertEqual(self.reader.actor_delta(after,after,self.command,False)['sessions']['restartEntries'],0)

    def test_comparison_refusal_saves_both_sides_and_nonbyte_operands(self):
        before=self.capture();after=copy.deepcopy(before);after['leadPausedAt']=None
        with self.assertRaisesRegex(ValueError,'persisted pause'):self.reader.actor_delta(before,after,self.command,False)
        row=self.reader.rows[-1]; folder=self.reader.output/row['capture'];self.assertEqual(row['status'],'refused')
        self.assertEqual((folder/'before-session.jsonl').read_bytes(),before['raw']['session.jsonl'])
        self.assertEqual((folder/'after-delivery.json').read_bytes(),after['raw']['delivery.json'])
        self.assertIsNone(json.loads((folder/'operands.json').read_bytes())['after']['leadPausedAt'])
        after=copy.deepcopy(before);after['raw']['session.jsonl']=raw(restart(self.entries))
        with self.assertRaisesRegex(ValueError,'paused wake'):self.reader.actor_delta(before,after,self.command,False)
        after=copy.deepcopy(before);after['hiredAt']='another'
        with self.assertRaisesRegex(ValueError,'incarnation'):self.reader.actor_delta(before,after,self.command,False)

    def test_eligible_and_reopen_use_native_comparison_and_original_receipt_guard(self):
        before=self.capture();after=copy.deepcopy(before);after['raw']['session.jsonl']=raw(restart(self.entries))
        self.assertEqual(self.reader.actor_delta(before,after,self.command,True)['sessions']['restartEntries'],1)
        self.assertEqual(self.reader.actor_delta(after,after,self.command,True)['sessions']['restartEntries'],1)
        after['raw']['delivery.json']=line(dict(version=2,records=[]))
        with self.assertRaises(ValueError):self.reader.actor_delta(before,after,self.command,True)

    def test_comparison_immutable_receipt_link_cannot_change(self):
        before=self.capture();after=copy.deepcopy(before)
        changed=copy.deepcopy(self.delivery);changed['records'][0]['session']='b'*48+':fixture-session'
        after['raw']['delivery.json']=line(changed)
        with self.assertRaisesRegex(ValueError,'immutable receipt'):
            self.reader.actor_delta(before,after,self.command,False)
        self.assertEqual(self.reader.rows[-1]['status'],'refused')
        after=copy.deepcopy(before);after['raw']['delivery.json']=line({**self.delivery,'records':[{**self.delivery['records'][0],'revision':99,'state':'dispatched'}]})
        self.assertEqual(self.reader.actor_delta(before,after,self.command,False)['sessions']['originalEntries'],1)

    def test_budget_and_persistence_errors_do_not_return_success(self):
        with patch.object(n,'TOTAL_BYTES',10):
            with self.assertRaises(ValueError):self.capture()
        with patch.object(n,'write_new',side_effect=OSError('synthetic failure')):
            with self.assertRaises(OSError):self.capture()
        with patch.object(n,'MAX_CAPTURE',len(self.reader.rows)):
            with self.assertRaisesRegex(ValueError,'count budget'):self.capture()

    def test_freezer_records_index_only_no_profile_walk(self):
        self.capture(); source=self.root/'evidence'; source.mkdir(); dest=source/'stateful/update/actor-captures';dest.parent.mkdir(parents=True)
        os.rename(self.reader.output,dest)
        result=freezer.freeze(source,self.root/'frozen')
        self.assertEqual(result['entries']['stateful/update/actor-captures/index.json']['status'],'frozen-original-bytes')
        self.assertFalse(any('session-0' in key for key in result['entries']))
        self.assertEqual((self.root/'frozen/stateful/update/actor-captures/index.json').read_bytes(),(dest/'index.json').read_bytes())


class ActualSamples(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.packet=Path(os.environ['T199_R7_RAW'])
        manifest=(cls.packet/'manifest.json').read_bytes()
        if n.sha(manifest)!='0f9b5bd07439d507647109b2667483f747193e8aad193c0b93e59e418a986a13':raise ValueError('wrong sealed raw packet')
        cls.rows=json.loads(manifest)
        for name,row in cls.rows.items():
            b=(cls.packet/name).read_bytes()
            if len(b)!=row['bytes'] or n.sha(b)!=row['sha256']:raise ValueError('raw packet changed')
        cls.names=sorted(name for name in cls.rows if name.startswith('raw/') and name.endswith('/session-0.jsonl'))
        if len(cls.names)!=7:raise ValueError('seven exact raw phases required')

    def test_seven_actual_old_rejections_new_exact_identity(self):
        for name in self.names:
            with self.subTest(name=name):
                b=(self.packet/name).read_bytes()
                with self.assertRaisesRegex(ValueError,'canonical origin parent missing'):original.parse_session(b)
                p=n.parse_session(b,format=n.NATIVE);self.assertEqual(len(p['users']),1)
                receipt=json.loads((self.packet/Path(name).parent/'delivery.json').read_bytes())['records'][0]
                self.assertEqual(p['users'][0]['origin'],dict(source='user',submission=receipt['id']))
                self.assertEqual(p['users'][0]['nativeIdentity'],dict(submissionId=receipt['id'],origin=dict(source='user')))
                self.assertTrue(receipt['session'].endswith(':'+p['sessionID']))
                self.assertEqual(n.session_delta(b,b,receipt['id'],p['users'][0]['text'],False,format=n.NATIVE)['restartEntries'],0)

    def test_actual_hold_to_stop_and_normal_completion_preserve_prefix(self):
        for case,phase in (('paused-stop','after-stop'),('normal-control','normal-idle')):
            root=self.packet/'raw'/case
            before=(root/'provider-held/session-0.jsonl').read_bytes();after=(root/phase/'session-0.jsonl').read_bytes()
            parsed=n.parse_session(before,format=n.NATIVE);user=parsed['users'][0]
            self.assertEqual(n.session_delta(before,after,user['origin']['submission'],user['text'],False,format=n.NATIVE)['originalEntries'],1)
            self.assertEqual((root/'provider-held/delivery.json').read_bytes(),(root/phase/'delivery.json').read_bytes())

    def test_actual_negative_identity_variants_never_body_fallback(self):
        for name in self.names:
            es=[json.loads(x) for x in (self.packet/name).read_bytes().splitlines()]
            index=next(i for i,e in enumerate(es) if e.get('customType')=='yesman-input-v1')
            for variant in ('missing','null','parent','entry','type','duplicate','foreign'):
                e=copy.deepcopy(es)
                if variant=='missing':
                    e.pop(index)
                    if index<len(e):e[index]['parentId']=e[index-1]['id']
                if variant=='null':e[index]['data']['identity']=None
                if variant=='parent':e[index]['parentId']='other'
                if variant=='entry':e[index]['data']['entryId']='other'
                if variant=='type':e[index]['customType']='other'
                if variant=='duplicate':e.insert(index+1,copy.deepcopy(e[index]))
                if variant=='foreign':e[index]['data']['identity']['submissionId']=OTHER
                b=raw(e);old=n.parse_session((self.packet/name).read_bytes(),format=n.NATIVE)['users'][0]
                with self.subTest(name=name,variant=variant),self.assertRaises((ValueError,KeyError)):
                    n.session_delta(b,b,old['origin']['submission'],old['text'],False,format=n.NATIVE)


class CoupledPaths(unittest.TestCase):
    def test_actual_Host_constructor_selects_facade_before_any_effect(self):
        with tempfile.TemporaryDirectory() as directory:
            root=Path(directory);episode=root/'episode';episode.mkdir();evidence=root/'evidence';evidence.mkdir()
            with patch.object(attachment.Path,'home',return_value=root/'synthetic-home'):
                host=attachment.Host(None,original,fixture,core,collectors,{},
                    {'runtimeSHA256':n.RUNTIME_SHA256},[],episode,evidence,None,None,{},False)
            try:
                self.assertIsInstance(host.a,n.NativeAdapter)
                self.assertIs(host.a.submissions,host.submissions)
                self.assertIsNone(host.process)
                self.assertEqual(host.a.output,evidence/'actor-captures')
                self.assertIs(host.a.original,original)
            finally:
                host.log.close();host.image_diagnostics.close();host.diagnostics.close()

    def test_all_capture_comparison_calls_resolve_to_explicit_facade(self):
        source=(ROOT/'runner-attachment-f48/runner_attachment.py').read_text();tree=ast.parse(source)
        host=next(x for x in tree.body if isinstance(x,ast.ClassDef) and x.name=='Host')
        init=next(x for x in host.body if isinstance(x,ast.FunctionDef) and x.name=='__init__')
        self.assertIn('native_session.NativeAdapter',ast.unparse(init))
        for name in ('seed','capture_before','verify_resumption','verify_no_replay','snapshot_failure'):
            node=next(x for x in host.body if isinstance(x,ast.FunctionDef) and x.name==name)
            self.assertIn('self.a.capture_actor',ast.unparse(node))
        for name in ('verify_resumption','verify_no_replay'):
            node=next(x for x in host.body if isinstance(x,ast.FunctionDef) and x.name==name)
            self.assertIn('self.a.actor_delta',ast.unparse(node))
        self.assertIn('adapter.capture_actor(data, actor, run)',(ROOT/'image_proof.py').read_text())

    def test_contract_pins_match_shipped_queue_and_agent_session_patch(self):
        repo=ROOT.parents[1]
        self.assertEqual(n.sha((repo/'internal/piruntime/assets/pending-queue.js').read_bytes()),n.QUEUE_SHA256)
        patches=json.loads((repo/'internal/piruntime/assets/patch.json').read_bytes())['patches']
        row=next(p for p in patches if p['path'].endswith('/dist/core/agent-session.js'))
        self.assertEqual(row['after'],n.SESSION_SHA256)
        with tempfile.TemporaryDirectory() as directory:
            with self.assertRaisesRegex(ValueError,'runtime binding'):
                n.NativeAdapter(original,Path(directory)/'capture',{},'0'*64)
            self.assertFalse((Path(directory)/'capture').exists())

    def test_provider_routing_is_independent_payload_oracle(self):
        original_input=dict(role='user',content=TEXT)
        first=fixture.request_policy([original_input],RUN,['paused/lead','active/lead'])
        self.assertEqual((first['action'],first['originalCount'],first['restartCount']),('hold',1,0))
        continued=fixture.request_policy([original_input,dict(role='user',content='[yesman] Yes Man restarted. Continue.')],RUN,['paused/lead','active/lead'])
        self.assertEqual((continued['action'],continued['originalCount'],continued['restartCount']),('continue',1,1))
        with self.assertRaises(Exception):fixture.request_policy([original_input,original_input],RUN,['paused/lead'])
        with self.assertRaises(Exception):fixture.request_policy([original_input,dict(role='user',content='[yesman] Other notice')],RUN,['paused/lead'])


if __name__=='__main__':unittest.main()
