"""Bounded evidence collection for the synthetic cleanup fixture."""
from hashlib import sha256
import json
import math
import os
import re
import stat

JOURNAL_LIMIT = 128 * 1024
JOURNAL_LINE_LIMIT = 512


def journal_bytes(profile, leaf):
    filename = profile / leaf
    if not profile.is_dir() or profile.is_symlink() or profile.resolve() != profile:
        raise ValueError('Fixture profile is missing or redirected')
    try:
        fd = os.open(filename, os.O_RDONLY | os.O_NONBLOCK | os.O_NOFOLLOW)
    except FileNotFoundError:
        return None
    with os.fdopen(fd, 'rb') as handle:
        info = os.fstat(handle.fileno())
        if not stat.S_ISREG(info.st_mode) or info.st_nlink != 1 or info.st_uid != os.getuid():
            raise ValueError('Unexpected fixture journal identity')
        return handle.read(JOURNAL_LIMIT + 1)


def entry_metadata(row):
    if row['phase'] != 'script-entry':
        return False
    invocation = row['invocation']
    if not isinstance(invocation, str) or (invocation not in ('version','panel-pr-list','api-other','other')
            and not re.fullmatch(r'cleanup-open-pr/latency-[0-2][0-9]', invocation)):
        return False
    entered = row['enteredNS']
    if type(entered) is not int or not 0 < entered <= row['monotonicNS']:
        return False
    interpreter = row['interpreter']
    if not isinstance(interpreter, dict) or interpreter.keys() != {'implementation','version','executableIdentity'}:
        return False
    version, identity = interpreter['version'], interpreter['executableIdentity']
    return (interpreter['implementation'] in ('cpython','pypy','other') and isinstance(version, list) and
            len(version) == 3 and all(type(n) is int and 0 <= n <= 255 for n in version) and
            (identity is None or (isinstance(identity, list) and len(identity) == 2 and
             all(type(n) is int and 0 <= n < 1 << 64 for n in identity) and identity[1] > 0)))


def unique_object(pairs):
    obj = dict(pairs)
    if len(obj) != len(pairs):
        raise ValueError('Duplicate sidecar key')
    return obj


def shim_diagnostics(profile):
    payload = journal_bytes(profile, 'fixture-gh-diagnostic.log')
    if payload is None:
        return {'status':'missing','records':[]}
    fields = {'pid','ppid','monotonicNS','phase','branch','journalPhase','outcome'}
    standalone = {'script-entry','version-exit','outside-fixture-branch','unmatched-exit'}
    records, invalid = [], 0
    for line in payload[:JOURNAL_LIMIT].splitlines(keepends=True):
        try:
            if len(line) > JOURNAL_LINE_LIMIT or not line.endswith(b'\n'):
                raise ValueError('Invalid sidecar line')
            row = json.loads(line, object_pairs_hook=unique_object)
            entry_fields = {'enteredNS','invocation','interpreter'}
            if (not isinstance(row, dict) or row.keys() not in (fields, fields | entry_fields) or
                    any(type(row[k]) is not int or not 0 < row[k] < 1 << 63 for k in ('pid','ppid','monotonicNS'))):
                raise ValueError('Invalid sidecar identity')
            if row.keys() != fields and not entry_metadata(row):
                raise ValueError('Invalid sidecar entry metadata')
            phase, branch, journal, outcome = (row[k] for k in ('phase','branch','journalPhase','outcome'))
            scoped = isinstance(branch, str) and re.fullmatch(r'fixture:latency-[0-2][0-9]', branch)
            if not ((isinstance(phase,str) and phase in standalone and (branch,journal,outcome) == ('','','')) or
                    (scoped and phase in ('branch-match','sigterm') and (journal,outcome) == ('','')) or
                    (scoped and phase == 'journal-write' and journal in ('start','response-written','interrupted') and
                     outcome in ('written','identity-refused','limit-refused','short-write','io-error'))):
                raise ValueError('Unexpected sidecar fields')
            records.append(row)
        except (ValueError, UnicodeError, RecursionError):
            invalid += 1
    oversized = len(payload) > JOURNAL_LIMIT
    return {'status':'incomplete' if invalid or oversized else 'recorded', 'records':records,
            'invalidRows':invalid, 'oversized':oversized}


def shim_entry_summary(sidecar, app_pid):
    """Project observed App children without assigning them to a browser check."""
    rows = sidecar['records']
    children = {}
    for row in rows:
        if row['ppid'] == app_pid:
            children.setdefault(row['pid'], []).append(row)
    entries, unknown = [], 0
    for pid, child in children.items():
        first = child[0]
        if (first['phase'] != 'script-entry' or 'invocation' not in first or
                any(row['phase'] == 'script-entry' or row['monotonicNS'] < first['monotonicNS'] for row in child[1:])):
            unknown += 1
            continue
        entries.append({'pid':pid,'invocation':first['invocation'],'entryOnly':len(child)==1,
                        'entryToClassificationNS':first['monotonicNS']-first['enteredNS']})
    return {'status':sidecar['status'],'appPID':app_pid,'clock':'same-child-monotonic-deltas',
            'appChildren':entries,'unclassifiedAppChildren':unknown,
            'otherParentRows':sum(row['ppid'] != app_pid for row in rows),
            'preEntryTiming':'unobserved','goPhaseTiming':'unobserved','rpcAssignment':'unobserved'}


def fixture_journal(profile):
    """Read only the fixture's regular journal and export allowlisted rows."""
    payload = journal_bytes(profile, 'fixture-gh.log')
    if payload is None:
        return {'status': 'missing', 'records': [], 'incompleteStarts': []}
    oversized = len(payload) > JOURNAL_LIMIT
    lines = payload[:JOURNAL_LIMIT].splitlines(keepends=True)
    records, starts, invalid = [], {}, 0
    fields = {'branch', 'delaySeconds', 'responseClass', 'pid', 'ppid', 'monotonicNS', 'phase'}
    for line in lines:
        try:
            if len(line) > JOURNAL_LINE_LIMIT or not line.endswith(b'\n'):
                raise ValueError('Incomplete or oversized journal row')
            row = json.loads(line)
            if not isinstance(row, dict) or set(row) != fields:
                raise ValueError('Unexpected journal fields')
            branch = row['branch']
            if not isinstance(branch, str) or not re.fullmatch(r'fixture:latency-[0-2][0-9]', branch):
                raise ValueError('Unexpected fixture branch')
            delay = 30 if branch.endswith('-29') else .2
            response = 'open-pr' if branch.endswith('-01') else 'empty'
            if (row['delaySeconds'], row['responseClass']) != (delay, response):
                raise ValueError('Unexpected fixture response class or delay')
            if any(type(row[key]) is not int or row[key] <= 0 for key in ('pid', 'ppid', 'monotonicNS')):
                raise ValueError('Unexpected journal process identity or time')
            identity = (row['pid'], row['ppid'], branch)
            phase = row['phase']
            if phase == 'start' and identity not in starts:
                starts[identity] = row
            elif phase in ('response-written', 'interrupted') and identity in starts and row['monotonicNS'] >= starts[identity]['monotonicNS']:
                del starts[identity]
            else:
                raise ValueError('Unmatched or unordered journal phase')
            records.append(row)
        except (ValueError, UnicodeError, RecursionError):
            invalid += 1
    return {'status': 'incomplete' if oversized or invalid else 'recorded', 'records': records,
            'incompleteStarts': list(starts.values()), 'oversized': oversized, 'invalidRows': invalid}


CLEANUP_JOURNAL_STEPS = ('bulk-checking-cancel','slow-diagnostic','slow-retry-cancel','open-pr-kept',
                         'review-after-commit','bulk-before-commit')


def journal_frontier(records):
    data = json.dumps(records, sort_keys=True, separators=(',', ':')).encode()
    return {'count':len(records),'sha256':sha256(data).hexdigest()}


def validate_cleanup_journal(pid, frontiers, journal, sidecar, checks):
    """Join the signed App child journals to disjoint cleanup check windows."""
    assert type(pid) is int and pid > 0
    assert journal['status'] == sidecar['status'] == 'recorded'
    rows, notes = journal['records'], sidecar['records']
    assert all(row['ppid'] == pid for row in rows+notes)
    assert set(frontiers) == set(CLEANUP_JOURNAL_STEPS)
    indices = {}
    previous = 0
    for step in CLEANUP_JOURNAL_STEPS:
        frontier = frontiers[step]
        index = frontier['count']
        assert type(index) is int and previous <= index <= len(rows)
        assert journal_frontier(rows[:index]) == frontier
        indices[step] = index
        previous = index
    windows = (
        ('bulk-checking-cancel','slow-diagnostic','latency-29','latency-29','start',30),
        ('slow-retry-cancel','open-pr-kept','latency-01','latency-01','response-written',.2),
        ('review-after-commit','bulk-before-commit','heading','latency-29','start',30),
    )
    slow_pids = set()
    for before, after, target, branch, phase, delay in windows:
        ledger = checks[after]
        check = next(row for row in reversed(ledger) if row['method'] == 'CheckWorktreeCleanup')
        assert check['target'] == target and check['ok'] is True
        assert all(row['finished'] <= check['started'] for row in ledger
                   if row['method'] == 'CheckWorktreeCleanup' and row['id'] < check['id'])
        window = rows[indices[before]:indices[after]]
        proof, = [row for row in window if (row['branch'],row['phase'],row['delaySeconds']) == ('fixture:'+branch,phase,delay)]
        if phase == 'start':
            assert proof['pid'] not in slow_pids
            slow_pids.add(proof['pid'])
        start, = [row for row in window if row['pid']==proof['pid'] and row['branch']==proof['branch'] and row['phase']=='start']
        child = [note for note in notes if note['pid']==start['pid']]
        entry, = [note for note in child if note['phase']=='script-entry']
        match, = [note for note in child if note['phase']=='branch-match' and note['branch']==start['branch']]
        written, = [note for note in child if (note['phase'],note['branch'],note['journalPhase'],note['outcome']) ==
                    ('journal-write',start['branch'],'start','written')]
        assert entry['monotonicNS'] <= match['monotonicNS'] <= start['monotonicNS'] <= written['monotonicNS']


def valid_cleanup_snapshot(event, *, terminal):
    """Validate the bounded failure envelope and its clock before persisting it."""
    def clock(value):
        return type(value) in (int, float) and 0 <= value <= 400000 and math.isfinite(value)

    if set(event) != {'scenario','occurrence','step','ms','view','data'} or not clock(event['ms']):
        return False
    data, view = event['data'], event['view']
    common = {'failedAt','deadline','failedCall','cleanupRPCs'}
    specific = {'stoppedBy'} if terminal else {'error','loaded','diagnosticPending'}
    if not isinstance(data, dict) or set(data) != common | specific:
        return False
    if not all(clock(data[k]) for k in ('failedAt','deadline')) or not data['failedAt'] <= data['deadline'] <= data['failedAt']+15000:
        return False
    if not data['failedAt'] <= event['ms'] <= data['deadline']+1000:
        return False
    if terminal:
        if data['stoppedBy'] not in ('settled','bridge-horizon'):
            return False
    elif (type(data['loaded']) is not bool or type(data['diagnosticPending']) is not bool or
          not isinstance(data['error'], str) or not re.fullmatch(r'Timed out: actionable preview for (latency-29|latency-01|safe-review|heading)|Timed out: post-removal Info and Pulls refresh|Preview boundary exceeded|Expected exact 28 bulk offers\. Observed [0-9]{1,2}\.|Cleanup observer failed', data['error'])):
        return False
    if not isinstance(view, dict) or set(view) != {'open','title','hint','buttons'} or type(view['open']) is not bool:
        return False
    if not isinstance(view['buttons'], list) or len(view['buttons']) > 8:
        return False
    if any(not isinstance(button, dict) or set(button) != {'text','hidden','disabled'} or
           type(button['hidden']) is not bool or type(button['disabled']) is not bool for button in view['buttons']):
        return False
    if not isinstance(view['hint'], str) or not isinstance(view['title'], str):
        return False
    calls = data['cleanupRPCs']
    if not isinstance(calls, list) or len(calls) > 64:
        return False
    for i, row in enumerate(calls, 1):
        if not isinstance(row, dict) or type(row.get('id')) is not int or row['id'] != i or not clock(row.get('started')) or row['started'] > event['ms']:
            return False
        method = row.get('method')
        if method not in ('PrepareWorktreeCleanup','CheckWorktreeCleanup','CancelWorktreeCleanup','ConfirmWorktreeCleanup'):
            return False
        if (i > 1 and row['started'] < calls[i-2]['started']) or (('target' in row) != (method in ('PrepareWorktreeCleanup','CheckWorktreeCleanup'))):
            return False
        if 'finished' in row:
            if not clock(row['finished']) or not row['started'] <= row['finished'] <= event['ms'] or type(row.get('ok')) is not bool:
                return False
            if ('error' in row) == row['ok'] or ('result' in row) != (row['ok'] and method == 'CheckWorktreeCleanup'):
                return False
        elif {'ok','error','result'} & row.keys():
            return False
        if 'result' in row:
            result = row['result']
            flags = {'tokenPresent','partial','retryable','invalid'}
            if not isinstance(result, dict) or not flags | {'offers'} <= result.keys() <= flags | {'offers','held'}:
                return False
            if any(type(result[key]) is not bool for key in flags) or type(result['offers']) is not int or not 0 <= result['offers'] <= 32:
                return False
            held = result.get('held', [])
            if not isinstance(held, list) or result['offers']+len(held)>32 or ('held' in result) != (i == data['failedCall']):
                return False
            if any(not isinstance(item,dict) or set(item) != {'dir','reason'} for item in held):
                return False
        related = row.get('related')
        if method == 'PrepareWorktreeCleanup':
            if 'related' in row:
                return False
        else:
            if type(related) is not int or not 1 <= related < i:
                return False
            prior = calls[related-1]
            if prior.get('ok') is not True or prior['finished'] > row['started']:
                return False
            if method == 'CheckWorktreeCleanup':
                if prior['method'] != 'PrepareWorktreeCleanup' or prior['target'] != row['target']:
                    return False
                if any(r.get('related') == related and r['method'] == method for r in calls[:i-1]):
                    return False
            elif prior['method'] == 'CheckWorktreeCleanup':
                if prior['result']['tokenPresent'] is not True:
                    return False
            elif method != 'CancelWorktreeCleanup' or prior['method'] != 'PrepareWorktreeCleanup':
                return False
        if 'cancelID' in row and (type(row['cancelID']) is not int or not i < row['cancelID'] <= len(calls)):
            return False
    for row in calls:
        i, method = row['id'], row['method']
        keys = {i}
        if method == 'CheckWorktreeCleanup':
            keys.add(row['related'])
        cancel = next((other['id'] for other in calls[i:] if other['method'] == 'CancelWorktreeCleanup' and other['related'] in keys), None)
        if row.get('cancelID') != cancel:
            return False
    prepare = next((row for row in reversed(calls) if row['method'] == 'PrepareWorktreeCleanup'), None)
    failed = next((row for row in reversed(calls) if prepare and row['method'] == 'CheckWorktreeCleanup' and row['related'] == prepare['id']), prepare)
    if data['failedCall'] != (failed['id'] if failed else None) or (failed and type(data['failedCall']) is not int):
        return False
    if data['deadline'] != max(data['failedAt'], prepare['started']+15000 if prepare else data['failedAt']):
        return False
    pending = any('finished' not in row for row in calls)
    if terminal:
        horizon = event['ms'] >= data['deadline']
        if data['stoppedBy'] != ('bridge-horizon' if horizon else 'settled'):
            return False
        if not horizon and (pending or view['hint'] == 'Checking worktrees…'):
            return False
    else:
        if data['failedAt'] != event['ms'] or data['diagnosticPending'] != (pending and data['deadline'] > data['failedAt']):
            return False
        named = re.fullmatch(r'Timed out: actionable preview for (.+)', data['error'])
        if named and (failed is None or failed['target'] != named[1]):
            return False
    # These are the only text values in the browser's failure projection.
    directories = r'latency-[0-2][0-9]|keep-primary|safe-review'
    patterns = {
        'title': r'other|Remove worktree\?|Clean up worktrees|Worktree kept|No worktrees to remove|Could not check worktrees|Could not confirm the result|Cleanup results',
        'hint': r'other|Checking worktrees…|The worktree check did not finish within the time limit\.|These directories will stay in the workspace\.|This workspace has no recorded worktrees to clean up\.|This preview expired or was already used\. Open a new preview\.',
        'text': r'other|Cancel|Close|Try again|Remove worktree|Permanently remove (1 worktree|[0-9]{1,2} worktrees)',
        'method': r'(Prepare|Check|Cancel|Confirm)WorktreeCleanup',
        'target': directories + r'|heading|unknown',
        'dir': directories + '|',
        'error': r'context canceled|context deadline exceeded|This preview expired or was already used\. Open a new preview\.|The workspace is busy\. Try again\.|other rejection',
        'reason': r'This branch has an open pull request\.|This checkout is primary or has no recorded temporary purpose\.|A process is using this worktree\.|This worktree could not be checked within the time limit\.|\[unrecorded reason\]',
    }
    allowed = patterns.keys() | {'open','buttons','hidden','disabled','id','started','finished','ok','related','cancelID',
                                 'result','tokenPresent','partial','retryable','invalid','offers','held'}

    def bounded(value, key='', depth=0):
        if depth > 5:
            return False
        if isinstance(value, dict):
            return value.keys() <= allowed and all(bounded(item, name, depth+1) for name, item in value.items())
        if isinstance(value, list):
            return len(value) <= 64 and all(bounded(item, key, depth+1) for item in value)
        if isinstance(value, str):
            return key in patterns and len(value) <= 100 and re.fullmatch(patterns[key], value) is not None
        return key not in patterns and (type(value) is bool or clock(value))
    return bounded(view) and bounded(calls)


def bulk_preview_matches(preview, fixture, expected, labels):
    """Bind the complete offered set to the actual seeded checkout identities."""
    if not isinstance(preview, dict) or preview.get('target') != 'heading' or preview.get('invalid') is not False:
        return False
    if any(preview.get(key) is not True for key in ('tokenPresent', 'partial', 'retryable')):
        return False
    rows = preview.get('rows')
    if not isinstance(rows, list) or len(rows) != len(expected) + 3:
        return False
    by_dir = {}
    for row in rows:
        if not isinstance(row, dict) or not isinstance(row.get('dir'), str) or not isinstance(row.get('reason'), str):
            return False
        if row['dir'] in by_dir:
            return False
        by_dir[row['dir']] = row
    if {name for name, row in by_dir.items() if not row['reason']} != expected:
        return False
    for name in expected:
        row = by_dir[name]
        seed = fixture.get(name)
        if not seed or (row.get('instance'), row.get('head')) != (seed['id'], seed['head']):
            return False
    held = {'latency-01': 'openPR', 'latency-29': 'timeout', 'keep-primary': 'primary'}
    return all(name in by_dir and re.search(labels[label], by_dir[name]['reason'], re.IGNORECASE)
               for name, label in held.items())


def checked_path(scope, relative):
    path = scope / relative
    if not path.is_relative_to(scope) or path.resolve() != path:
        raise ValueError('Diagnostic path is outside the direct fixture path')
    return path
