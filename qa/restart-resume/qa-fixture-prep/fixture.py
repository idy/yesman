"""Tree-external S->Q prototype. No package launch/download/signing entry point.

A future authorized hosted adapter supplies already-verified artifacts and the
existing upgrade Feed/inspect helpers. The only live filesystem mutation here is
a reversible obstruction of the owned staged Q main; it never edits executable
bytes, the source tree, the updater or an app profile.
"""
from __future__ import annotations
import hashlib
import json
import os
from pathlib import Path
import re
import stat
import sys
from urllib.parse import urlsplit


HELPER_SHA256 = '6c4ec4630929b75d034ffaef8b58d2f7f9521336074d39a90ba8874846c26efc'


class Refused(ValueError):
    pass


def require(ok, why):
    if not ok:
        raise Refused(why)


def digest(data):
    return hashlib.sha256(data).hexdigest()


def object_id(value, n):
    return isinstance(value, str) and re.fullmatch('[0-9a-f]{%d}' % n, value) and value != '0' * n


def validate_contract(c):
    require(c.get('schema') == 1 and c.get('target') == 'darwin-arm64', 'one macOS ARM64 target only')
    require(c.get('scenario') in ('update', 'failed-launch-recovery'), 'unknown episode')
    require(isinstance(c.get('run'), str) and re.fullmatch('[a-f0-9]{32}', c['run']), 'run must be fresh 128-bit fixture identity')
    require(object_id(c.get('source'), 40) and object_id(c.get('tree'), 40), 'final source/tree missing')
    require(c.get('helperLiteralSHA256') == HELPER_SHA256, 'helper changed: re-review the obstruction before use')
    require(object_id(c.get('runtimeSHA256'), 64), 'managed runtime binding missing')
    require(object_id(c.get('extensionSHA256'), 64), 'production extension binding missing')
    require(c.get('eligible') and c.get('paused'), 'declare eligible and paused agent IDs')
    require(len(c['eligible']) == len(set(c['eligible'])) and len(c['paused']) == len(set(c['paused'])), 'duplicate agents')
    require(not set(c['eligible']) & set(c['paused']), 'paused agent cannot be eligible')
    for agent in c['eligible'] + c['paused']:
        require(re.fullmatch('[a-z0-9][a-z0-9-]{0,31}/[a-z0-9][a-z0-9-]{0,31}', agent), 'bad fixture agent')
    for key in ('S', 'Q'):
        s = c.get(key, {})
        require(isinstance(s, dict) and isinstance(s.get('version'), str) and re.fullmatch(r'0\.1\.[0-9]+', s['version']), 'missing version')
        require(s.get('source') == c['source'] and s.get('tree') == c['tree'], 'QA must use final source')
        for field in ('archiveSHA256', 'mainSHA256'):
            require(object_id(s.get(field), 64), 'missing artifact digest')
        require(s.get('evidence'), 'bind external package verification receipt')
    require(c['S']['version'] != c['Q']['version'], 'Q must be a private successor')
    require(c['Q'].get('privateQA') is True, 'Q is private, never an allocated product release')
    return c


def hosted_arm64():
    require(sys.platform == 'darwin' and os.uname().machine == 'arm64', 'macOS ARM64 required')
    require(os.environ.get('GITHUB_ACTIONS') == 'true' and os.environ.get('RUNNER_ENVIRONMENT') == 'github-hosted', 'disposable hosted VM required')
    require(os.geteuid() != 0, 'do not run as root')
    # Also call the existing naming.hosted() before first app launch. Do not
    # repeat its no-existing-profile check after this fixture has made its own.


def plain_path(p):
    p = Path(p).absolute()
    for parent in [*reversed(p.parents), p]:
        require(not parent.is_symlink(), 'linked fixture path refused')
    return p


def hash_file(p):
    # A hash of the actual held descriptor; refuse links and special files.
    fd = os.open(p, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
    try:
        st = os.fstat(fd)
        require(stat.S_ISREG(st.st_mode) and st.st_nlink == 1 and st.st_size <= 512 << 20, 'not a bounded unique regular file')
        h = hashlib.sha256()
        with os.fdopen(fd, 'rb', closefd=False) as f:
            for b in iter(lambda: f.read(1 << 20), b''):
                h.update(b)
        after = os.fstat(fd)
        require((st.st_dev, st.st_ino, st.st_size, st.st_mtime_ns) == (after.st_dev, after.st_ino, after.st_size, after.st_mtime_ns), 'file changed while hashed')
        return {'sha256': h.hexdigest(), 'dev': st.st_dev, 'ino': st.st_ino, 'mode': stat.S_IMODE(st.st_mode), 'bytes': st.st_size}
    finally:
        os.close(fd)


def obstruction_plan(install, staged_bundle, run, expected_main):
    install, staged = plain_path(install), plain_path(staged_bundle)
    require(re.fullmatch('[a-f0-9]{32}', run), 'invalid run')
    require(staged.is_relative_to(install) and staged.name == 'yesman.app', 'outside owned install')
    rel = staged.relative_to(install)
    require(len(rel.parts) == 3 and rel.parts[0].startswith('.yesman.app.update-') and rel.parts[1] == 'app', 'staged layout must be bound to actual final helper; replace adapter if different')
    main = plain_path(staged / 'Contents/MacOS/Yes Man')
    held = main.with_name('.t199-held-main-' + run)
    require(not held.exists() and not held.is_symlink(), 'obstruction name already exists')
    identity = hash_file(main)
    require(identity['sha256'] == expected_main and identity['mode'] & 0o111, 'staged Q main differs')
    return {'run': run, 'helperLiteralSHA256': HELPER_SHA256, 'stagedBundle': str(staged), 'install': str(install), 'relativeMain': 'Contents/MacOS/Yes Man', 'heldName': held.name, 'main': identity}


def arm(plan):
    """After real Update=ready/signature validation, before RestartToUpdate.

    No timing race: the executable remains absent across the entire quit/swap/
    launch interval. Hold the outer runner's sole fixture-controller ownership.
    """
    hosted_arm64()
    require(Path(plan['install']).is_relative_to(plain_path(os.environ['RUNNER_TEMP'])), 'install is outside owned runner scratch')
    require(obstruction_plan(plan['install'], plan['stagedBundle'], plan['run'], plan['main']['sha256']) == plan, 'obstruction plan changed')
    main = plain_path(Path(plan['stagedBundle']) / plan['relativeMain'])
    held = main.with_name(plan['heldName'])
    require(hash_file(main) == plan['main'], 'staged file replaced before arm')
    require(not held.exists() and not held.is_symlink(), 'held path occupied')
    os.rename(main, held)
    require(not main.exists() and hash_file(held) == plan['main'], 'arm readback failed; do not retry')
    return {'event': 'launch-obstruction-armed', 'run': plan['run'], 'main': plan['main']}


def recover(plan, installed_bundle):
    """Only AFTER old process and helper terminated, final intent captured,
    no Q process/bridge or claim observed. Return no launch command: the outer
    authorized adapter must revalidate bundle/codesign before explicit open.
    """
    hosted_arm64()
    require(plan.get('helperLiteralSHA256') == HELPER_SHA256, 'recovery helper binding changed')
    require(Path(plan['install']).is_relative_to(plain_path(os.environ['RUNNER_TEMP'])), 'install is outside owned runner scratch')
    installed = plain_path(installed_bundle)
    require(installed == Path(plan['install']) / 'yesman.app', 'wrong recovery bundle')
    main = plain_path(installed / plan['relativeMain'])
    held = main.with_name(plan['heldName'])
    require(not main.exists() and not main.is_symlink(), 'launch path occupied; keep it')
    require(hash_file(held) == plan['main'], 'held main changed; refuse recovery')
    os.rename(held, main)
    require(hash_file(main) == plan['main'] and not held.exists(), 'recovery readback failed; do not retry')
    return {'event': 'fixture-main-restored', 'run': plan['run'], 'main': plan['main']}


def text_of(message):
    content = message.get('content', '')
    if isinstance(content, str):
        return content
    return '\n'.join(x.get('text', '') for x in content if isinstance(x, dict) and x.get('type') == 'text')


def request_policy(messages, run, actors):
    """Pure local-provider policy. Identity markers are synthetic fixture input,
    never product message-origin inference. Real origin checks use session data.
    """
    users = [text_of(m) for m in messages if m.get('role') == 'user']
    tagged = [(a, '[t199:' + run + ':' + a + ']') for a in actors]
    matches = [(a, tag) for a, tag in tagged if any(tag in s for s in users)]
    require(len(matches) == 1, 'provider request must bind one declared actor')
    agent, tag = matches[0]
    require(sum(s.count(tag) for s in users) == 1, 'original marker duplicated')
    restarts = [s for s in users if s.startswith('[yesman] Yes Man restarted')]
    require(len(restarts) <= 1, 'restart note duplicated in context')
    # Generic continuation/report is intentionally not silently answered; bind
    # the composed behavior before adding one expected response to the policy.
    system_inputs = [s for s in users if s.startswith('[yesman]') and s not in restarts]
    require(not system_inputs, 'undeclared continuation/report prompt: inspect composition')
    return {'agent': agent, 'action': 'continue' if restarts else 'hold', 'userCount': len(users), 'originalCount': 1, 'restartCount': len(restarts), 'messagesSHA256': digest(json.dumps(messages, sort_keys=True).encode())}


def driver_text(endpoint):
    u = urlsplit(endpoint)
    require(u.scheme == 'http' and u.hostname == '127.0.0.1' and u.port and not u.username and not u.password and not u.query and not u.fragment, 'loopback fixture endpoint required')
    require(re.fullmatch('/[a-f0-9]{32}', u.path), 'run-scoped endpoint required')
    return Path(__file__).with_name('driver.js').read_text().replace('__ENDPOINT__', json.dumps(endpoint))


def assert_episode(c, e):
    """Offline oracle for adapter-collected files/native observations. It does
    not turn caller-authored JSON into native acceptance: retain its raw sources.
    """
    validate_contract(c)
    require(e['run'] == c['run'] and e['scenario'] == c['scenario'], 'episode mismatch')
    require(e['before']['home'] == e['after']['home'], 'wrong data root')
    require(e['before']['pid'] != e['after']['pid'], 'no new process')
    for phase, stage in [('before', 'S'), ('after', 'Q')]:
        v = e[phase]
        require(v['version'] == c[stage]['version'] and v['mainSHA256'] == c[stage]['mainSHA256'] and v['source'] == c['source'], 'wrong runtime image/source')
        require(v['instanceCount'] == 1, 'duplicate/forwarding app instance')
    last = e['last']
    require(last['kind'] == 'update' and last['fromVersion'] == c['S']['version'] and last['toVersion'] == c['Q']['version'] and last['version'] == c['Q']['version'], 'wrong update result')
    require(re.fullmatch('[a-f0-9]{32}', last['id']) and not last.get('unfinished') and not last.get('rejected'), 'no completed final claim')
    outcomes = {x['yesman'] + '/' + x['name']: x['outcome'] for x in last['agents']}
    require(len(outcomes) == len(last['agents']), 'duplicate outcomes')
    require(set(outcomes) <= set(c['eligible'] + c['paused']), 'unexpected agent admitted')
    require({a for a, outcome in outcomes.items() if outcome == 'resumed'} == set(c['eligible']), 'unexpected resumed set')
    for a in c['eligible']:
        require(outcomes.get(a) == 'resumed', 'eligible agent did not resume')
    require(not any(outcomes.get(a) == 'resumed' for a in c['paused']), 'paused agent woke')
    for a in c['eligible'] + c['paused']:
        v = e['sessions'][a]
        require(v['beforeID'] == v['afterID'] and v['beforePath'] == v['afterPath'] and v['prefixPreserved'], 'session fork/loss')
        require(v['originalEntries'] == 1, 'original input replay')
        require(v['restartEntries'] == (1 if a in c['eligible'] else 0), 'restart duplicated/missing or pause lost')
        require(v['providerContinuationRequests'] == (1 if a in c['eligible'] else 0), 'model continuation count')
    require(e['restartDirNames'] == ['last.json'], 'actionable/abandoned intent left')
    require(e['reopen']['lastID'] == last['id'] and e['reopen']['newRestartEntries'] == 0 and e['reopen']['newProviderRequests'] == 0, 'second open replay')
    if c['scenario'] == 'failed-launch-recovery':
        f = e['failure']
        require(f['armedBeforeRestart'] and f['oldExited'] and f['helperExited'] and f['launchErrorObserved'], 'no actual failed launch')
        require(f['QProcesses'] == 0 and f['QBridgeReports'] == 0 and f['claimedNames'] == [], 'claim occurred before recovery')
        intent = f['finalIntent']
        require(f['intentUnchangedUntilRecovery'] and 0 <= f['recoveryAgeSeconds'] < 900, 'expired/changed intent')
        require(intent['phase'] == 'final' and intent['id'] == last['id'] and intent['kind'] == 'update' and
                intent['fromVersion'] == c['S']['version'] and intent['toVersion'] == c['Q']['version'], 'not the preserved final intent')
        require(f['restoredMainSHA256'] == c['Q']['mainSHA256'] and f['bundleVerificationPassed'], 'unverified recovery image')
    return {'contractPASS': True, 'nativeAcceptance': False, 'reason': 'raw artifact/session/native receipts still need independent binding'}
