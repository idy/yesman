"""Bounded evidence collection for the synthetic cleanup fixture."""
import json
import os
import re
import stat

JOURNAL_LIMIT = 128 * 1024
JOURNAL_LINE_LIMIT = 512


def fixture_journal(profile):
    """Read only the fixture's regular journal and export allowlisted rows."""
    filename = profile / 'fixture-gh.log'
    if not profile.is_dir() or profile.is_symlink() or profile.resolve() != profile:
        raise ValueError('Fixture profile is missing or redirected')
    try:
        fd = os.open(filename, os.O_RDONLY | os.O_NONBLOCK | os.O_NOFOLLOW)
    except FileNotFoundError:
        return {'status': 'missing', 'records': [], 'incompleteStarts': []}
    with os.fdopen(fd, 'rb') as handle:
        info = os.fstat(handle.fileno())
        if not stat.S_ISREG(info.st_mode) or info.st_nlink != 1 or info.st_uid != os.getuid():
            raise ValueError('Unexpected fixture journal identity')
        payload = handle.read(JOURNAL_LIMIT + 1)
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
