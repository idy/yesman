#!/usr/bin/env python3
"""Narrow fixtures for check-production-upgrade.py's failure diagnostics (t205).

No application, updater or network outside loopback: the runner's own feed
server, journal and failure snapshot, with synthetic files. The upgrade
assertions, timeouts and success path are not exercised or changed here.

    python3 -B scripts/test-production-upgrade-diagnostics.py
"""
import importlib.util
import json
import pathlib
import tempfile
import unittest
import urllib.request

spec = importlib.util.spec_from_file_location('upgrade', pathlib.Path(__file__).with_name('check-production-upgrade.py'))
upgrade = importlib.util.module_from_spec(spec)
spec.loader.exec_module(upgrade)


def plan_in(root):
    stages = []
    for i in range(4):
        d = root / f'stage{i}'
        d.mkdir()
        (d / 'update.json').write_text(json.dumps({'version': f'0.1.90000{i}', 'deltas': []}))
        (d / f'yesman-{i}.tar.gz').write_bytes(b'archive %d' % i)
        stages.append({'Manifest': str(d / 'update.json'), 'Archive': str(d / f'yesman-{i}.tar.gz'), 'Version': f'0.1.90000{i}'})
    (root / 'plan.json').write_text('{}')
    return {'Stages': stages, 'Target': 'windows-amd64', '_path': str(root / 'plan.json')}


class Diagnostics(unittest.TestCase):
    def setUp(self):
        self.dir = tempfile.TemporaryDirectory()
        self.root = pathlib.Path(self.dir.name)
        self.plan = plan_in(self.root)
        self.feed = upgrade.Feed(self.plan, 'archive')
        self.server = self.feed.serve('127.0.0.1', 0)
        self.url = 'http://127.0.0.1:%d' % self.server.server_address[1]

    def tearDown(self):
        self.server.shutdown()
        self.server.server_close()
        self.dir.cleanup()

    def get(self, path):
        try:
            with urllib.request.urlopen(self.url + path) as r:
                return r.status, r.read()
        except urllib.error.HTTPError as e:
            return e.code, b''

    def post(self, path, value):
        req = urllib.request.Request(self.url + path, data=json.dumps(value).encode(), method='POST')
        with urllib.request.urlopen(req) as r:
            return r.status

    def kinds(self):
        return [e['kind'] for e in self.feed.journal]

    def test_command_receipt_and_requests_are_told_apart(self):
        self.feed.target = 3
        self.feed.send('check')
        self.get('/command')
        self.get('/command')  # polled again: served once
        self.assertEqual(self.get('/update-windows-amd64.json')[0], 200)
        self.assertEqual(self.get('/yesman-3.tar.gz')[0], 200)
        self.assertEqual(self.get('/missing.tar.gz')[0], 404)
        self.assertEqual(self.kinds(), ['command-published', 'command-served', 'request', 'request-end', 'request', 'request-end', 'request', 'request-end'])
        j = list(self.feed.journal)
        self.assertEqual(j[0]['op'], 'check')
        self.assertEqual(j[0]['target'], 3)
        self.assertEqual(j[3]['version'], '0.1.900003')
        self.assertEqual(j[5]['bytes'], len(b'archive 3'))
        self.assertEqual(j[7]['status'], 404)
        # The recorded requests are as before: stage and file name only.
        self.assertEqual(self.feed.requests[1], {'stage': 3, 'file': 'yesman-3.tar.gz'})

    def test_a_rejected_await_is_kept_bounded_and_the_last_report_with_it(self):
        report = {'page': 'p1', 'about': {'version': '0.1.900002'}, 'update': {'state': 'downloading', 'version': '0.1.900003'},
                  'state': {'home': 'synthetic'}, 'settings': {'appearance': 'dark'}, 'localStorage': 'synthetic-token', 'errors': []}
        self.post('/report', report)
        self.post('/report', report)  # unchanged: one journal entry
        self.post('/awaiting', {'page': 'p1', 'method': 'CheckForUpdates', 'ms': 2001})
        self.post('/report', {'error': 'Error: ' + 'x' * 5000, 'errors': ['e'] * 9, 'method': 'CheckForUpdates'})
        self.assertEqual(self.kinds(), ['report', 'page-awaiting', 'report'])
        first, waited, failed = list(self.feed.journal)
        self.assertEqual(first['update'], {'state': 'downloading', 'version': '0.1.900003'})
        self.assertEqual(waited['method'], 'CheckForUpdates')
        self.assertEqual(failed['method'], 'CheckForUpdates')
        self.assertEqual(len(failed['error']), 300)
        self.assertEqual((failed['errors'], len(failed['firstErrors'])), (9, 3))
        text = json.dumps(list(self.feed.journal))
        for private in ('synthetic-token', 'synthetic', 'appearance'):
            self.assertNotIn(private, text)
        # The predicate the runner waits on reads feed.latest unchanged.
        self.assertIn('error', self.feed.latest)

    def test_journal_is_bounded(self):
        for i in range(upgrade.JOURNAL + 50):
            self.feed.note('x', i=i)
        self.assertEqual(len(self.feed.journal), upgrade.JOURNAL)
        self.assertEqual(self.feed.journal[-1]['i'], upgrade.JOURNAL + 49)

    def test_a_missing_report_still_writes_a_bounded_snapshot(self):
        target = self.root / 'archive-failure.json'
        upgrade.failure_snapshot(target, self.feed, {'progress': 'archive: requesting update 3'}, None, None, None, None, self.plan)
        out = json.loads(target.read_text())
        self.assertIsNone(out['latest'])
        self.assertEqual(out['reports'], 0)
        self.assertEqual(out['progress'], 'archive: requesting update 3')
        self.assertNotIn('snapshotError', out)

    def test_snapshot_is_taken_before_the_directory_is_removed_and_keeps_the_failure(self):
        install = self.root / 'Application install with spaces'
        install.mkdir()
        (install / 'Yes Man.exe').write_bytes(b'stage two image')
        (install / '.Application install with spaces.update-1').mkdir()
        (install / '.Application install with spaces.update-1' / 'tray.svg').write_bytes(b'')
        executable = lambda index: install / ('Yes Man.exe' if index == 2 else f'missing-{index}.exe')

        class Alive:
            pid = 4242
            def poll(self): return None

        target = self.root / 'delta-failure.json'
        seen = {}
        def failed():
            seen['installed'] = (install / 'Yes Man.exe').exists()
            upgrade.failure_snapshot(target, self.feed, {'progress': 'delta: requesting update 3', 'completed': [{'stage': 2}]}, Alive(), 7, install, executable, self.plan)
        with self.assertRaisesRegex(AssertionError, 'Upgrade never reached a result'):
            with tempfile.TemporaryDirectory() as d, upgrade.OnFailure(failed):
                raise AssertionError('Upgrade never reached a result')
        self.assertTrue(seen['installed'])
        out = json.loads(target.read_text())
        self.assertEqual(out['process'], {'pid': 4242, 'exit': None})
        self.assertTrue(out['guardHeld'])
        self.assertEqual(out['completed'], [{'stage': 2}])
        self.assertEqual(out['images']['2']['sha256'], upgrade.sha(install / 'Yes Man.exe'))
        update = [e for e in out['install'] if '.update-' in e['name']]
        self.assertEqual(update[0]['children'], ['tray.svg'])
        # A snapshot that itself fails never replaces the failure.
        with self.assertRaisesRegex(AssertionError, 'original'):
            with upgrade.OnFailure(lambda: 1 / 0):
                raise AssertionError('original')
        # Success leaves no snapshot.
        with upgrade.OnFailure(lambda: self.fail('called on success')):
            pass


if __name__ == '__main__':
    unittest.main()
