#!/usr/bin/env python3
"""Historical CLI source acquisition in independent, finite-ref Git fixtures.

No application or Windows binary runs here. The unchanged hosted Windows CLI
proof separately compiles and executes the actual pinned historical bytes.
"""
import importlib.util
import os
import pathlib
import subprocess
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('windows_cli', pathlib.Path(__file__).with_name('check-windows-cli.py'))
cli = importlib.util.module_from_spec(spec)
spec.loader.exec_module(cli)


def git(directory, *args, input=None):
    env = {k: v for k, v in os.environ.items() if not k.startswith('GIT_')}
    env.update(GIT_CONFIG_NOSYSTEM='1', GIT_CONFIG_GLOBAL=os.devnull,
               GIT_TERMINAL_PROMPT='0', GIT_NO_REPLACE_OBJECTS='1')
    return subprocess.run(['git', '-c', 'user.name=Source fixture', '-c', 'user.email=fixture@example.invalid',
                           '--git-dir=' + str(directory), *args], input=input, env=env,
                          stdout=subprocess.PIPE, stderr=subprocess.PIPE, check=True).stdout


class HistoricalSourceTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(prefix='historical source ')
        self.addCleanup(self.temporary.cleanup)
        self.root = pathlib.Path(self.temporary.name)
        self.upstream = self.root / 'upstream.git'
        git(self.upstream, 'init', '--bare', str(self.upstream))
        self.data = {'main.go': b'original main\n', 'launch_windows.go': b'original windows\n'}
        blobs = {name: git(self.upstream, 'hash-object', '-w', '--stdin', input=data).decode().strip() for name, data in self.data.items()}
        entries = ''.join('100644 blob ' + blobs[name] + '\t' + name + '\n' for name in sorted(blobs))
        launcher = git(self.upstream, 'mktree', input=entries.encode()).decode().strip()
        tools = git(self.upstream, 'mktree', input=('040000 tree ' + launcher + '\tlauncher\n').encode()).decode().strip()
        self.tree = git(self.upstream, 'mktree', input=('040000 tree ' + tools + '\ttools\n').encode()).decode().strip()
        self.revision = git(self.upstream, 'commit-tree', self.tree, input=b'historical counterexample\n').decode().strip()
        self.files = {name: (blobs[name], len(data)) for name, data in self.data.items()}
        current_blob = git(self.upstream, 'hash-object', '-w', '--stdin', input=b'current squashed source\n').decode().strip()
        current_tree = git(self.upstream, 'mktree', input=('100644 blob ' + current_blob + '\tcurrent.txt\n').encode()).decode().strip()
        # New root commit deliberately has no parent edge to the old source.
        self.current = git(self.upstream, 'commit-tree', current_tree, input=b'squashed main\n').decode().strip()
        git(self.upstream, 'update-ref', 'refs/heads/main', self.current)
        git(self.upstream, 'update-ref', 'refs/tags/current', self.current)

    def acquire(self, directory='acquisition.git', **kwargs):
        parameters = dict(repository=self.upstream.as_uri(), revision=self.revision, tree=self.tree, files=self.files)
        parameters.update(kwargs)
        return cli.historical_launcher(self.root / directory, **parameters)

    def test_squash_checkout_missing_then_independent_exact_acquisition(self):
        limited = self.root / 'limited.git'
        git(limited, 'init', '--bare', str(limited))
        git(limited, 'fetch', '--no-tags', '--depth=1', self.upstream.as_uri(), 'refs/heads/main:refs/heads/main', 'refs/tags/current:refs/tags/current')
        # No alternates or old object leaked in from the author's/pool's Git.
        with self.assertRaises(subprocess.CalledProcessError):
            git(limited, 'show', self.revision + ':tools/launcher/main.go')
        before = git(limited, 'show-ref')
        actual = subprocess.check_output
        def isolated(args, **kwargs):
            self.assertEqual(kwargs['env']['HOME'], str(self.root))
            self.assertEqual(kwargs['env']['USERPROFILE'], str(self.root))
            self.assertNotIn('GH_TOKEN', kwargs['env'])
            self.assertNotIn('GIT_ALTERNATE_OBJECT_DIRECTORIES', kwargs['env'])
            return actual(args, **kwargs)
        with patch.dict(os.environ, {'GIT_OBJECT_DIRECTORY': str(self.root/'absent-objects'),
                                     'GIT_ALTERNATE_OBJECT_DIRECTORIES': str(self.upstream/'objects'),
                                     'GIT_DIR': str(limited), 'GIT_CONFIG_COUNT': '1',
                                     'GIT_CONFIG_KEY_0': 'url.invalid.insteadOf', 'GIT_CONFIG_VALUE_0': 'file://',
                                     'HOME': str(self.root/'outside-home'), 'USERPROFILE': str(self.root/'outside-home'),
                                     'GH_TOKEN': 'synthetic-must-not-inherit'}), patch('subprocess.check_output', isolated):
            self.assertEqual(self.acquire(), self.data)
        self.assertEqual(git(limited, 'show-ref'), before)
        with self.assertRaises(subprocess.CalledProcessError):
            git(limited, 'cat-file', '-e', self.revision)

    def test_wrong_commit_with_same_files_rejected(self):
        # A different valid commit containing the same tree is still not old.
        alternate = git(self.upstream, 'commit-tree', self.tree, input=b'other identity\n').decode().strip()
        actual = subprocess.check_output
        def swapped(args, **kwargs):
            if args[-3:] == ['--', self.upstream.as_uri(), self.revision]:
                args = [*args[:-1], alternate]
            return actual(args, **kwargs)
        with patch('subprocess.check_output', swapped), self.assertRaisesRegex(RuntimeError, 'wrong commit'):
            self.acquire()

    def test_wrong_tree_rejected(self):
        with self.assertRaisesRegex(RuntimeError, 'root tree differs'):
            self.acquire(tree='1' * 40)

    def test_wrong_blob_rejected(self):
        files = dict(self.files, **{'main.go': ('1' * 40, len(self.data['main.go']))})
        with self.assertRaisesRegex(RuntimeError, 'path/mode/blob differs'):
            self.acquire(files=files)

    def test_wrong_size_rejected(self):
        blob, size = self.files['main.go']
        for wrong in (size - 1, size + 1):
            with self.subTest(size=wrong):
                with self.assertRaisesRegex(RuntimeError, 'byte bound|size differs'):
                    self.acquire(directory='size-' + str(wrong) + '.git', files={'main.go': (blob, wrong)})

    def test_missing_path_rejected(self):
        with self.assertRaisesRegex(RuntimeError, 'path/mode/blob differs'):
            self.acquire(tree=git(self.upstream, 'rev-parse', self.current + '^{tree}').decode().strip(), revision=self.current)

    def test_missing_commit_fails_fetch(self):
        with self.assertRaises(subprocess.CalledProcessError):
            self.acquire(revision='0' * 40)

    def test_missing_repository_fails_fetch(self):
        with self.assertRaises(subprocess.CalledProcessError):
            self.acquire(repository=(self.root/'absent.git').as_uri())

    def test_existing_git_directory_refused(self):
        (self.root/'acquisition.git').mkdir()
        with self.assertRaisesRegex(RuntimeError, 'must be new'):
            self.acquire()


if __name__ == '__main__':
    unittest.main(verbosity=2)
