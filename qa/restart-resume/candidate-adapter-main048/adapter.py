"""Candidate-bound QA adapter. Pure planners/readers; no application launch.

Live profile writes require the same disposable-host guard as the prototype.
The caller retains existing package/signature/native checks; this file cannot
convert synthetic JSON or an unverified private-Q receipt into native PASS.
"""
from __future__ import annotations
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import re
import stat
import sys

CANDIDATE = '8a73d96335dc5d2fa787f9b15e1b812bd58b0728'
TREE = '395ecf86348f0fa5923af118e886efc7c31c39f7'
PREDECESSOR = '6f0a1f1400f667c09cc57a2b5d5fc93962334ee9'
PROTOTYPE_SEAL = '4f36fd03d62ca6b48e1fd198e238d6b1866f33737cebc415c51201f38d3deb24'


def need(ok, why):
    if not ok:
        raise ValueError(why)


def sha(b):
    return hashlib.sha256(b).hexdigest()


def load_prototype():
    folder = Path(__file__).resolve().parent.parent / 'qa-fixture-prep'
    raw = (folder / 'manifest.json').read_bytes()
    need(sha(raw) == PROTOTYPE_SEAL, 'sealed prototype manifest changed')
    manifest = json.loads(raw)
    for name in ('fixture.py', 'controller.py', 'driver.js'):
        b = (folder / name).read_bytes()
        need(sha(b) == manifest[name]['sha256'] and len(b) == manifest[name]['bytes'], 'prototype changed: ' + name)
    modules = []
    for name in ('fixture', 'controller'):
        path = folder / (name + '.py')
        if name in sys.modules:
            need(Path(sys.modules[name].__file__).resolve() == path, 'module name already used elsewhere')
            module = sys.modules[name]
        else:
            spec = importlib.util.spec_from_file_location(name, path)
            module = importlib.util.module_from_spec(spec)
            sys.modules[name] = module
            spec.loader.exec_module(module)
        modules.append(module)
    return modules


def seed_plan(vm_home, provider_base):
    _, core = load_prototype()
    home = Path(vm_home).absolute()
    # Generated fixture config at the fresh VM defaults avoids depending on
    # Launch Services forwarding custom HOME/PI_CODING_AGENT_DIR variables.
    config = home / '.pi/agent'
    data = home / 'yesman'
    settings = {'autoFetch': 0, 'appearance': 'light', 'crew': {'options': [
        {'provider': 't199-fixture', 'model': 'fixture', 'effort': '', 'score': {'intelligence': 1}}],
        'yesman': 't199-fixture/fixture'}}
    files = {data / 'settings.json': settings}
    files.update({config / name: value for name, value in core.pi_config(provider_base).items()})
    return {'home': str(home), 'data': str(data), 'piConfig': str(config),
            'files': {str(p): (json.dumps(v, indent=2) + '\n').encode() for p, v in files.items()}}


def write_fresh_seed(plan):
    fixture, _ = load_prototype()
    fixture.hosted_arm64()
    need(Path.home().resolve() == Path(plan['home']).resolve(), 'use only the clean hosted VM account')
    need(not Path(plan['data']).exists() and not Path(plan['piConfig']).exists(), 'existing profile refused')
    home = Path(plan['home'])
    need(not (home / 'Library/Application Support/yesman').exists() and
         not (home / 'Library/Preferences/dev.idy.yesman.plist').exists(), 'existing native app profile refused')
    # A partial write is retained as a failed fixture, never retried in place.
    for name, body in plan['files'].items():
        p = fixture.plain_path(name)
        need(p.is_relative_to(Path(plan['home'])), 'seed outside VM fixture home')
        p.parent.mkdir(parents=True, exist_ok=True)
        fd = os.open(p, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
        try:
            with os.fdopen(fd, 'wb', closefd=False) as f:
                f.write(body); f.flush(); os.fsync(fd)
        finally:
            os.close(fd)


def submit_command(agent, hired_at, run, millis, entropy):
    need(agent in ('active/lead', 'paused/lead'), 'minimal fixture actors only')
    need(re.fullmatch('[a-f0-9]{32}', run), 'invalid run')
    need(isinstance(millis, int) and 0 < millis < 1 << 48 and re.fullmatch('[a-f0-9]{36}', entropy), 'submission ID inputs')
    # web/submission.js uses 12 timestamp hex digits + 36 random hex digits.
    ident = f'{millis:012x}' + entropy
    text = '[t199:' + run + ':' + agent + '] Remember this synthetic fact; this goal is unfinished.'
    return {'method': 'Submit', 'args': [agent, ident, text, [], hired_at], 'submission': ident, 'text': text}


def bind_bridge(observation, native, expected_home, stage, epoch='working'):
    """Bind real bridge data to the existing runner's independent native/image
    observation and its externally verified artifact receipt. Q exports may lack
    VCS metadata. sourceProvenance is deliberately not called observedRevision.
    """
    about, state = observation['about'], observation['state']
    need(state['home'] == str(expected_home) and not state.get('piError') and not state.get('workspaceErrors'), 'wrong/unhealthy data root')
    need(about['version'] == stage['version'] and not about.get('piError'), 'wrong/unhealthy app version')
    need(epoch in ('initial', 'working', 'resumed'), 'unknown bridge epoch')
    need(about.get('piManaged') is True, 'not managed pi')
    if epoch == 'initial':
        need(about.get('piState') in ('unprepared', 'preparing', 'ready'), 'unhealthy initial managed runtime')
    else:
        need(about.get('piState') == 'ready', 'not real ready managed pi')
    need(native['mainSHA256'] == stage['mainSHA256'] and native['instanceCount'] == 1 and native['pid'] > 0, 'wrong/duplicate native image')
    need(stage['candidate'] == CANDIDATE and stage['candidateTree'] == TREE and stage['verificationReceipt'], 'artifact not bound to reviewed candidate')
    if stage['official']:
        need(about.get('revision') == stage['sourceCommit'] and not about.get('modified'), 'official binary VCS mismatch')
    else:
        need(stage.get('privateQA') is True and stage.get('sourceFilesVerified') is True, 'private-Q build provenance missing')
        need(not about.get('revision') or about['revision'] == stage['sourceCommit'], 'private-Q has wrong VCS identity')
    return {'pid': native['pid'], 'mainSHA256': native['mainSHA256'], 'version': about['version'],
            'home': state['home'], 'instanceCount': 1, 'sourceProvenance': stage['sourceCommit'],
            'observedRevision': about.get('revision'), 'artifactReceipt': stage['verificationReceipt']}


def read_owned(root, path, limit):
    root, path = Path(root).absolute(), Path(path).absolute()
    need(path.is_relative_to(root), 'outside synthetic fixture root')
    for p in [*reversed(path.parents), path]:
        need(not p.is_symlink(), 'linked fixture path')
    fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
    try:
        st = os.fstat(fd)
        need(stat.S_ISREG(st.st_mode) and st.st_nlink == 1 and st.st_size <= limit, 'not a bounded unique regular file')
        with os.fdopen(fd, 'rb', closefd=False) as f:
            b = f.read(limit + 1)
        end = os.fstat(fd)
        need(len(b) <= limit and (st.st_dev, st.st_ino, st.st_size, st.st_mtime_ns) ==
             (end.st_dev, end.st_ino, end.st_size, end.st_mtime_ns), 'file changed during capture')
        return b
    finally:
        os.close(fd)


def no_duplicates(pairs):
    obj = {}
    for key, value in pairs:
        need(key not in obj, 'duplicate JSON key')
        obj[key] = value
    return obj


def decode(raw):
    return json.loads(raw, object_pairs_hook=no_duplicates)


def parse_session(raw):
    need(len(raw) <= 4 << 20 and raw.endswith(b'\n'), 'incomplete/oversized session snapshot')
    entries = [decode(line) for line in raw.splitlines()]
    h = entries[0]
    need(h['type'] == 'session' and h['version'] == 3 and h.get('id') and h.get('cwd') and not h.get('parentSession'), 'not a fresh v3 session')
    by_id, users = {}, []
    leaf = None
    for e in entries[1:]:
        need(e.get('id') and e['id'] not in by_id and e.get('parentId') == leaf, 'fork/duplicate/broken parent in minimal fixture')
        parent = by_id.get(leaf, {})
        if e['type'] == 'message' and e['message']['role'] == 'user':
            m = e['message']
            need(parent.get('type') == 'custom' and parent.get('customType') == 'yesman-origin' and
                 parent['data']['timestamp'] == m['timestamp'], 'canonical origin parent missing')
            content = m['content']
            text = content if isinstance(content, str) else '\n'.join(x['text'] for x in content if x['type'] == 'text')
            users.append({'entry': e['id'], 'parent': leaf, 'text': text, 'origin': parent['data']['origin']})
        by_id[e['id']] = e
        leaf = e['id']
    return {'sessionID': h['id'], 'cwd': h['cwd'], 'leafID': leaf, 'users': users, 'bytes': len(raw), 'sha256': sha(raw)}


def session_delta(before_raw, after_raw, submission, seed_text, eligible):
    before, after = parse_session(before_raw), parse_session(after_raw)
    need(after_raw.startswith(before_raw) and before['sessionID'] == after['sessionID'] and before['cwd'] == after['cwd'], 'session prefix/identity lost')
    original = [x for x in after['users'] if x['origin'].get('submission') == submission]
    restart = [x for x in after['users'] if x['origin'].get('source') == 'yesman' and x['origin'].get('kind') == 'restart']
    need(len(original) == 1 and original[0]['origin']['source'] == 'user' and original[0]['text'] == seed_text, 'original receipt replay/replacement')
    need(len(after['users']) == 1 + int(eligible) and len(restart) == int(eligible), 'extra/missing input or paused wake')
    need(all(x['text'].startswith('[yesman] Yes Man restarted') and not x['origin'].get('submission') for x in restart), 'restart is not a separate system input')
    return {'before': before, 'after': after, 'originalEntries': 1, 'restartEntries': len(restart), 'prefixPreserved': True}


def capture_actor(data, workspace, run):
    need(workspace in ('active', 'paused') and re.fullmatch('[a-f0-9]{32}', run), 'wrong fixture scope')
    root = Path(data).absolute()
    meta = root / 'yesmen' / workspace / '.yesman'
    y = decode(read_owned(root, meta / 'yesman.json', 1 << 20))
    session_dir = meta / 'sessions/lead'
    need(not session_dir.is_symlink(), 'linked session directory')
    paths = list(session_dir.glob('*.jsonl'))
    need(len(paths) == 1, 'minimal fixture must have exactly one canonical session')
    raw = read_owned(root, paths[0], 4 << 20)
    parsed = parse_session(raw)
    need(parsed['cwd'] == str(meta.parent), 'session cwd mismatch')
    marker = '[t199:' + run + ':' + workspace + '/lead]'
    need(any(x['text'].startswith(marker) for x in parsed['users']), 'session belongs to another fixture actor')
    try:
        pause_raw = read_owned(root, meta / 'continuation.json', 1 << 20)
    except FileNotFoundError:
        need(workspace == 'active', 'paused control has no durable pause')
        pause_raw = None
    pause = decode(pause_raw) if pause_raw is not None else {'hiredAt': y['hiredAt']}
    need(pause['hiredAt'] == y['hiredAt'], 'pause belongs to another workspace hire')
    need(workspace != 'paused' or pause.get('leadPausedAt'), 'paused control was cleared')
    delivery_raw = read_owned(root, session_dir / 'delivery.json', 2 << 20)
    delivery = decode(delivery_raw)
    need(delivery.get('version') == 2 and isinstance(delivery.get('records'), list), 'unexpected receipt format')
    return {'workspace': workspace, 'hiredAt': y['hiredAt'], 'sessionPath': str(paths[0]), 'session': parsed,
            'leadPausedAt': pause.get('leadPausedAt'), 'receiptIDs': [x['id'] for x in delivery['records']],
            'raw': {'session.jsonl': raw, 'continuation.json': pause_raw, 'delivery.json': delivery_raw}}


def receipt_delta(before_raw, after_raw, submission):
    before, after = decode(before_raw), decode(after_raw)
    need(before['version'] == after['version'] == 2, 'wrong receipt version')
    def index(disk):
        rows = disk['records']
        out = {r['id']: r for r in rows}
        need(len(out) == len(rows), 'duplicate receipt ID')
        return out
    old, new = index(before), index(after)
    need(set(old) == set(new) and submission in old, 'receipt lost or restart fabricated a submission')
    need(old[submission]['fingerprint'] == new[submission]['fingerprint'] and
         old[submission].get('imageCount', 0) == new[submission].get('imageCount', 0), 'original receipt payload changed')
    return {'id': submission, 'stateBefore': old[submission]['state'], 'stateAfter': new[submission]['state'],
            'consumptionClaimed': False}


def actor_delta(before, after, command, eligible):
    need(before['workspace'] == after['workspace'] and before['hiredAt'] == after['hiredAt'] and
         before['sessionPath'] == after['sessionPath'], 'workspace/session incarnation changed')
    if not eligible:
        need(before['leadPausedAt'] and before['leadPausedAt'] == after['leadPausedAt'], 'persisted pause cleared')
    s = session_delta(before['raw']['session.jsonl'], after['raw']['session.jsonl'], command['submission'], command['text'], eligible)
    d = receipt_delta(before['raw']['delivery.json'], after['raw']['delivery.json'], command['submission'])
    return {'sessions': s, 'receipt': d, 'nativeAcceptance': False}


def capture_restart(data):
    root = Path(data).absolute(); directory = root / 'restart'
    need(not directory.is_symlink(), 'linked restart directory')
    if not directory.exists():
        return {'names': [], 'claimedNames': [], 'files': {}, 'directoryAbsent': True}
    names = sorted(x.name for x in directory.iterdir())
    captured = {}
    for name, limit in [('intent.json', 64 << 10), ('last.json', 256 << 10)]:
        if name in names:
            raw = read_owned(root, directory / name, limit)
            captured[name] = {'raw': raw, 'sha256': sha(raw), 'value': decode(raw)}
    return {'names': names, 'claimedNames': [n for n in names if n.startswith('claimed-')], 'files': captured}


def bind_final_to_result(failure, recovered):
    need(failure['claimedNames'] == [] and failure['names'] == ['intent.json'], 'not an unclaimed final')
    intent = failure['files']['intent.json']['value']
    need(intent['version'] == 3 and intent['phase'] == 'final', 'nonfinal checkpoint')
    need(recovered['names'] == ['last.json'], 'claim/result did not finish')
    last = recovered['files']['last.json']['value']
    need(last['id'] == intent['id'] and last['kind'] == intent['kind'] == 'update' and
         last['fromVersion'] == intent['fromVersion'] and last['toVersion'] == intent['toVersion'] == last['version'] and
         not last.get('unfinished') and not last.get('rejected'), 'wrong recovery result')
    rows = last['agents']
    need(len(rows) == 1 and rows[0].get('yesman') == 'active' and rows[0].get('name') == 'lead' and
         rows[0].get('outcome') == 'resumed', 'unexpected resumed set in minimal fixture')
    return {'id': last['id'], 'finalSHA256': failure['files']['intent.json']['sha256'],
            'resultSHA256': recovered['files']['last.json']['sha256'], 'nativeAcceptance': False}
