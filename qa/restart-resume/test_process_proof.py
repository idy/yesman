"""Normal owned-child/pipe/lsof observations; no App or discovered signalling."""
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import time
import unittest
from unittest.mock import patch

import process_proof
from guards import Owned, identity


class Observations(unittest.TestCase):
    def test_only_known_staging_installer_and_npm_roles(self):
        observer = process_proof.Processes('/synthetic-data')
        self.assertEqual(observer.role('node /synthetic-data/.runtime/pi-managed/.prepare-abc/install.mjs')[0], 'runtime-installer')
        self.assertEqual(observer.role('node /public/npm/bin/npm-cli.js ci --prefix /synthetic-data/.runtime/pi-managed/.prepare-abc --ignore-scripts'), ('pinned-npm-ci', '/public/npm/bin/npm-cli.js'))
        self.assertEqual(observer.role('node /unrelated/install.mjs')[0], 'owned-descendant')
        self.assertEqual(observer.role('node /public/npm/bin/npm-cli.js ci --prefix /outside/.prepare-abc')[0], 'owned-descendant')

    def test_profile_lsof_reports_actual_open_and_closed_owned_file(self):
        with tempfile.TemporaryDirectory() as directory:
            profile = Path(directory).resolve()
            path = profile / 'synthetic.keep'
            with path.open('w') as held:
                held.write('inert owned control')
                held.flush()
                live = process_proof.profiles([('fixture-data', profile)])['paths'][0]
                self.assertEqual(live['status'], 'observed-consumer')
                self.assertIn(os.getpid(), live['PIDs'])
            closed = process_proof.profiles([('fixture-data', profile)])['paths'][0]
            self.assertEqual(closed['status'], 'no-observed-consumer')
            self.assertEqual(process_proof.profiles([('fixture-data', profile / 'absent')])['paths'][0]['status'], 'absent')
            (profile / 'dangling').symlink_to(profile / 'missing')
            self.assertEqual(process_proof.profiles([('fixture-data', profile / 'dangling')])['paths'][0]['status'], 'refused-link')

    @unittest.skipUnless(sys.platform == 'darwin', 'kernel ownership control uses Darwin libproc')
    def test_real_owned_descendant_pipe_then_exit_is_observed_without_signals(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory).resolve()
            script = root / 'ordinary-child.py'
            script.write_text('import subprocess,sys,time\nc=subprocess.Popen([sys.executable,"-c","import time;print(\\\"ready\\\",flush=True);time.sleep(30)"],stdout=subprocess.PIPE)\nassert c.stdout.readline().strip()==b"ready"\nprint(c.pid,flush=True)\ntime.sleep(30)\n')
            env = {'PATH': os.environ['PATH'], 'HOME': str(root), 'TMPDIR': str(root)}
            parent = subprocess.Popen([sys.executable, str(script)], env=env, stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
            child = None
            try:
                pid = int(parent.stdout.readline())
                owner = identity(parent.pid)
                held = Owned(parent.pid, owner['executable'], child=parent)
                child_owner = identity(pid)
                child = Owned(pid, child_owner['executable'])
                observer = process_proof.Processes(root)
                observer.register(held)
                with patch.object(process_proof.os, 'kill') as signalling:
                    observer.poll(force=True)
                    value = observer.snapshot()
                    self.assertIn(pid, [x['owner']['pid'] for x in value['children']])
                    self.assertNotIn(os.getpid(), [x['owner']['pid'] for x in value['children']])
                    self.assertTrue(value['unresolvedObserved'])
                    pipes = observer.pipes(child_owner)
                    signalling.assert_not_called()
                self.assertEqual(pipes['status'], 'observed')
                self.assertGreaterEqual(pipes['pipeOrFIFODescriptors'], 2)
                held.drain()
                self.assertTrue(observer.snapshot()['unresolvedObserved'])
                child.drain()  # separately created and held exact identity, never a discovered PID
                self.assertFalse(observer.snapshot()['unresolvedObserved'])
                self.assertFalse(observer.snapshot()['installerSeen'])
                self.assertFalse(observer.snapshot()['npmSeen'])
                print(json.dumps({'control': 'owned-child-pipe', 'rootPID': parent.pid, 'childPID': pid,
                                  'pipeObservation': pipes, 'allObservedExited': not observer.snapshot()['unresolvedObserved'],
                                  'rootChildExitStatus': parent.returncode, 'discoveredSignals': 0}))
            finally:
                try:
                    if child is not None:
                        child.drain()
                finally:
                    if parent.poll() is None:
                        parent.terminate()
                        parent.wait(timeout=5)
                    for stream in (parent.stdin, parent.stdout, parent.stderr):
                        stream.close()


if __name__ == '__main__':
    unittest.main()
