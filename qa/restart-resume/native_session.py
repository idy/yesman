"""QA reader for the exact S/Q native runtime. No legacy auto-detection.

The historical adapter stays sealed. This facade replaces only its actor/session
readers in the two stateful S/Q episodes. Raw operands are durable before decode;
unknown identity never falls back to a timestamp, text, or legacy side entry.
"""
from __future__ import annotations
import hashlib
import itertools
import json
import os
from pathlib import Path
import re

NATIVE = '1.5.2-yesman.5-498b685a8d6ccde6/yesman-input-v1'
RUNTIME_SHA256 = '3652e1b07378b9d6846733fbd9bef04ac1168ae4e68281475d30af9a45769329'
QUEUE_SHA256 = 'a732d7efba0a7dc3c4db2a2ed4b273bbd10228d4883b75052da4027ab4bdd084'
SESSION_SHA256 = 'c87fe036b81668c8a2e7e44d801e9cd14ba15e1e4d4fcd22b38a27b878968d9e'
SUBMISSION = re.compile('[a-f0-9]{48}')
MAX_CAPTURE = 32
TOTAL_BYTES = 32 << 20


def need(value, code):
    if not value:
        raise ValueError(code)


def sha(raw):
    return hashlib.sha256(raw).hexdigest()


def pairs(items):
    value = {}
    for k, v in items:
        need(k not in value, 'duplicate JSON key')
        value[k] = v
    return value


def decode(raw):
    return json.loads(raw, object_pairs_hook=pairs)


def text_of(message):
    content = message.get('content')
    if isinstance(content, str):
        return content
    # This minimal fixture submits no images or other content blocks.
    need(isinstance(content, list) and all(isinstance(x, dict) and x.get('type') == 'text' and
         isinstance(x.get('text'), str) for x in content), 'unexpected fixture user content')
    return '\n'.join(x['text'] for x in content)


def parse_session(raw, *, format):
    need(format == NATIVE, 'explicit pinned native format required')
    need(isinstance(raw, bytes) and 0 < len(raw) <= 4 << 20 and raw.endswith(b'\n'), 'incomplete/oversized session snapshot')
    entries = [decode(line) for line in raw.splitlines()]
    h = entries[0]
    need(isinstance(h, dict) and h.get('type') == 'session' and h.get('version') == 3 and
         isinstance(h.get('id'), str) and h['id'] and isinstance(h.get('cwd'), str) and h['cwd'] and
         not h.get('parentSession'), 'not a fresh v3 session')
    by_id, users, identified, submissions = {}, [], set(), set()
    leaf = None
    for index, e in enumerate(entries[1:], 1):
        need(isinstance(e, dict) and isinstance(e.get('id'), str) and e['id'] and
             e['id'] != h['id'] and e['id'] not in by_id and 'parentId' in e and
             e['parentId'] == leaf, 'fork/duplicate/broken parent in minimal fixture')
        need(e.get('customType') != 'yesman-origin', 'legacy origin in explicitly native session')
        message = e.get('message', {})
        need(isinstance(message, dict), 'invalid canonical message')
        if e.get('type') == 'message' and message.get('role') == 'user':
            need(index + 1 < len(entries), 'native identity child missing')
            child = entries[index + 1]
            need(isinstance(child, dict) and child.get('type') == 'custom' and
                 child.get('customType') == 'yesman-input-v1' and child.get('parentId') == e['id'],
                 'native identity child missing')
            data = child.get('data')
            need(isinstance(data, dict) and set(data) == {'entryId', 'identity'} and data['entryId'] == e['id'],
                 'native immutable entry link differs')
            identity = data['identity']
            need(isinstance(identity, dict) and set(identity) <= {'submissionId', 'origin'} and
                 isinstance(identity.get('origin'), dict), 'native identity missing/foreign fields')
            origin = identity['origin']
            if origin == {'source': 'user'}:
                submission = identity.get('submissionId')
                need(isinstance(submission, str) and SUBMISSION.fullmatch(submission) and
                     submission not in submissions, 'native submission missing/duplicate')
                submissions.add(submission)
                canonical_origin = dict(origin, submission=submission)
            else:
                need(origin == {'source': 'yesman', 'kind': 'restart'} and set(identity) == {'origin'},
                     'foreign native origin or fabricated restart submission')
                canonical_origin = dict(origin)
            need(isinstance(child.get('id'), str) and child['id'], 'native child ID missing')
            identified.add(child['id'])
            users.append({'entry': e['id'], 'parent': e['parentId'], 'text': text_of(message),
                          'origin': canonical_origin, 'identityEntry': child['id'],
                          'nativeIdentity': identity})
        if e.get('customType') == 'yesman-input-v1':
            need(e.get('type') == 'custom' and e['id'] in identified, 'orphan/duplicate native identity')
        by_id[e['id']] = e
        leaf = e['id']
    need(users, 'no canonical user input')
    return {'format': NATIVE, 'sessionID': h['id'], 'cwd': h['cwd'], 'leafID': leaf,
            'users': users, 'bytes': len(raw), 'sha256': sha(raw)}


def parse_legacy(original, raw):
    """Explicit older-stage path; never called on a failed native parse."""
    return original.parse_session(raw)


def session_delta(before_raw, after_raw, submission, seed_text, eligible, *, format):
    before = parse_session(before_raw, format=format)
    after = parse_session(after_raw, format=format)
    need(after_raw.startswith(before_raw) and before['sessionID'] == after['sessionID'] and
         before['cwd'] == after['cwd'], 'session prefix/identity lost')
    for state in (before, after):
        original = [u for u in state['users'] if u['origin'].get('submission') == submission]
        need(len(original) == 1 and original[0]['origin'] == {'source': 'user', 'submission': submission} and
             original[0]['text'] == seed_text, 'original receipt replay/replacement')
    restart = [u for u in after['users'] if u['origin'] == {'source': 'yesman', 'kind': 'restart'}]
    need(len(after['users']) == 1 + int(eligible) and len(restart) == int(eligible), 'extra/missing input or paused wake')
    # Payload oracle only; identity above comes exclusively from the native links.
    need(all(u['text'].startswith('[yesman] Yes Man restarted') and
             'submissionId' not in u['nativeIdentity'] for u in restart), 'restart payload differs')
    return {'before': before, 'after': after, 'originalEntries': 1,
            'restartEntries': len(restart), 'prefixPreserved': True}


def write_new(path, raw):
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
    with os.fdopen(fd, 'wb') as stream:
        stream.write(raw)
        stream.flush()
        os.fsync(stream.fileno())


def json_bytes(value):
    return (json.dumps(value, indent=2, sort_keys=True) + '\n').encode()


class NativeAdapter:
    def __init__(self, original, output, submissions, runtime_sha256):
        need(runtime_sha256 == RUNTIME_SHA256, 'native reader runtime binding differs')
        self.original, self.output = original, Path(output)
        self.submissions = submissions
        self.rows, self.total = [], 0
        self.output.mkdir(mode=0o700, exist_ok=False)

    def __getattr__(self, name):
        return getattr(self.original, name)

    def parse_session(self, raw):
        return parse_session(raw, format=NATIVE)

    def session_delta(self, before_raw, after_raw, submission, seed_text, eligible):
        return session_delta(before_raw, after_raw, submission, seed_text, eligible, format=NATIVE)

    def _index(self):
        raw = json_bytes({'format': NATIVE, 'captures': self.rows, 'rawBytes': self.total,
                          'maxCaptures': MAX_CAPTURE, 'totalBudget': TOTAL_BYTES,
                          'nativeAcceptance': False})
        tmp = self.output / 'index.json.next'
        write_new(tmp, raw)
        os.replace(tmp, self.output / 'index.json')

    def _begin(self, operation, workspace):
        need(len(self.rows) < MAX_CAPTURE, 'raw capture count budget exhausted')
        folder = self.output / f'{len(self.rows):02d}-{workspace}-{operation}'
        folder.mkdir(mode=0o700)
        row = {'capture': folder.name, 'operation': operation, 'workspace': workspace,
               'files': {}, 'status': 'capturing-before-decode'}
        self.rows.append(row)
        self._index()
        return folder, row

    def _raw(self, folder, row, name, body, limit):
        if body is None:
            row['files'][name] = {'status': 'absent'}
            return
        need(isinstance(body, bytes) and len(body) <= limit and self.total + len(body) <= TOTAL_BYTES,
             'raw capture byte budget exhausted')
        write_new(folder / name, body)
        self.total += len(body)
        row['files'][name] = {'status': 'retained-original-bytes', 'bytes': len(body), 'sha256': sha(body)}

    def _finish(self, folder, row, error=None):
        row['status'] = 'refused' if error else 'validated'
        if error:
            row['errorType'] = type(error).__name__
            # Semantic messages are fixed reader vocabulary; OS path/error text is omitted.
            row['reason'] = str(error) if isinstance(error, ValueError) and not isinstance(error, json.JSONDecodeError) else 'read/decode refused'
        write_new(folder / 'capture.json', json_bytes(row))
        self._index()

    def capture_actor(self, data, workspace, run):
        need(workspace in ('active', 'paused') and re.fullmatch('[a-f0-9]{32}', run), 'wrong fixture scope')
        root = Path(data).absolute()
        meta = root / 'yesmen' / workspace / '.yesman'
        folder, row = self._begin('capture', workspace)
        bodies, errors = {}, []
        def acquire(name, path, limit, optional=False):
            try:
                raw = self.original.read_owned(root, path, limit)
                self._raw(folder, row, name, raw, limit)
                bodies[name] = raw
            except FileNotFoundError:
                row['files'][name] = {'status': 'absent'}
                bodies[name] = None
                if not optional:
                    errors.append('required raw operand absent: ' + name)
            except (OSError, ValueError):
                row['files'][name] = {'status': 'unavailable-or-unstable'}
                errors.append('raw operand refused: ' + name)
        try:
            # No JSON/session semantic decode occurs until every bounded operand
            # has been attempted and all available bytes fsynced to evidence.
            acquire('yesman.json', meta / 'yesman.json', 1 << 20)
            acquire('continuation.json', meta / 'continuation.json', 1 << 20, optional=True)
            directory = meta / 'sessions/lead'
            acquire('delivery.json', directory / 'delivery.json', 2 << 20)
            need(not directory.is_symlink(), 'linked session directory')
            paths = sorted(itertools.islice(directory.glob('*.jsonl'), 5))
            row['sessionNames'] = [p.name for p in paths[:4]]
            row['sessionCountUpToFive'] = len(paths)
            row['sessionEnumerationBound'] = 5
            for index, path in enumerate(paths[:4]):
                acquire(f'session-{index}.jsonl', path, 4 << 20)
            self._index()
            need(not errors, '; '.join(errors))
            need(len(paths) == 1, 'minimal fixture must have exactly one canonical session')
            y = decode(bodies['yesman.json'])
            raw = bodies['session-0.jsonl']
            parsed = parse_session(raw, format=NATIVE)
            need(parsed['cwd'] == str(meta.parent), 'session cwd mismatch')
            command = self.submissions[workspace]
            need(command['args'][4] == y['hiredAt'], 'capture belongs to another workspace hire')
            original = [u for u in parsed['users'] if u['origin'].get('submission') == command['submission']]
            need(len(original) == 1 and original[0]['text'] == command['text'] and
                 command['text'].startswith('[t199:' + run + ':' + workspace + '/lead]'), 'foreign fixture input identity/payload')
            need(all(u['origin'].get('submission') in (None, command['submission']) for u in parsed['users']), 'foreign canonical submission')
            pause_raw = bodies['continuation.json']
            pause = decode(pause_raw) if pause_raw is not None else {'hiredAt': y['hiredAt']}
            need(pause['hiredAt'] == y['hiredAt'], 'pause belongs to another workspace hire')
            need(workspace != 'paused' or pause.get('leadPausedAt'), 'paused control was cleared')
            delivery = decode(bodies['delivery.json'])
            need(delivery.get('version') == 2 and isinstance(delivery.get('records'), list), 'unexpected receipt format')
            records = delivery['records']
            need(len(records) == 1 and records[0]['id'] == command['submission'], 'missing/foreign/duplicate receipt')
            receipt = records[0]
            need(receipt['agentSince'] == y['hiredAt'] and
                 isinstance(receipt.get('session'), str) and
                 re.fullmatch('[a-f0-9]{48}:' + re.escape(parsed['sessionID']), receipt['session']), 'receipt session/hire differs')
            text = command['text'].encode()
            fingerprint = sha(len(text).to_bytes(8, 'big') + text + bytes(8))
            need(receipt['fingerprint'] == fingerprint and receipt.get('imageCount', 0) == 0, 'receipt payload differs')
            value = {'workspace': workspace, 'hiredAt': y['hiredAt'], 'sessionPath': str(paths[0]),
                     'session': parsed, 'leadPausedAt': pause.get('leadPausedAt'),
                     'receiptIDs': [r['id'] for r in records], 'rawCapture': str(folder),
                     'raw': {'session.jsonl': raw, 'continuation.json': pause_raw, 'delivery.json': bodies['delivery.json']}}
        except Exception as error:
            self._finish(folder, row, error)
            raise
        self._finish(folder, row)
        return value

    def actor_delta(self, before, after, command, eligible):
        workspace = before['workspace']
        need(workspace in ('active', 'paused'), 'wrong comparison actor')
        folder, row = self._begin('comparison', workspace)
        try:
            for side, value in (('before', before), ('after', after)):
                for name, limit in (('session.jsonl', 4 << 20), ('continuation.json', 1 << 20), ('delivery.json', 2 << 20)):
                    self._raw(folder, row, side + '-' + name, value['raw'][name], limit)
            # Non-byte comparison operands also survive any subsequent refusal.
            self._raw(folder, row, 'operands.json', json_bytes({'eligible': eligible,
                'submission': command['submission'], 'text': command['text'],
                'before': {k: before[k] for k in ('workspace', 'hiredAt', 'sessionPath', 'leadPausedAt')},
                'after': {k: after[k] for k in ('workspace', 'hiredAt', 'sessionPath', 'leadPausedAt')}}), 64 << 10)
            self._index()
            need(before['workspace'] == after['workspace'] and before['hiredAt'] == after['hiredAt'] and
                 before['sessionPath'] == after['sessionPath'], 'workspace/session incarnation changed')
            need(command['args'][4] == before['hiredAt'] and
                 command['submission'] == self.submissions[workspace]['submission'], 'comparison command hire/identity differs')
            if not eligible:
                need(before['leadPausedAt'] and before['leadPausedAt'] == after['leadPausedAt'], 'persisted pause cleared')
            sessions = session_delta(before['raw']['session.jsonl'], after['raw']['session.jsonl'],
                command['submission'], command['text'], eligible, format=NATIVE)
            delivery = self.original.receipt_delta(before['raw']['delivery.json'], after['raw']['delivery.json'], command['submission'])
            old_receipt = decode(before['raw']['delivery.json'])['records'][0]
            new_receipt = decode(after['raw']['delivery.json'])['records'][0]
            need(all(old_receipt.get(k) == new_receipt.get(k) for k in
                 ('session', 'agentSince', 'order')), 'immutable receipt link differs')
            result = {'sessions': sessions, 'receipt': delivery, 'nativeAcceptance': False}
        except Exception as error:
            self._finish(folder, row, error)
            raise
        self._finish(folder, row)
        return result
