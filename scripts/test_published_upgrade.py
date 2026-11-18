#!/usr/bin/env python3
"""Offline adversarial checks of the exact privileged inline downloader.

All archives, releases and HTTP responses below are synthetic; this does not
run the final released-artifact gate or authenticate an actual release pair.
"""
import contextlib
import copy
import hashlib
import io
import json
import pathlib
import subprocess
import sys
import tarfile
import tempfile
import unittest
from unittest.mock import patch

from importlib.util import module_from_spec, spec_from_file_location
spec = spec_from_file_location('published_upgrade', pathlib.Path(__file__).with_name('prepare-published-upgrade.py'))
module = module_from_spec(spec)
spec.loader.exec_module(module)

OLD, NEW = 'v0.1.38', 'v0.1.39'
TARGETS = ('darwin-amd64', 'darwin-arm64', 'windows-amd64', 'linux-amd64')
KEY = 'synthetic-public-key'


def tar(files, link=False):
    out = io.BytesIO()
    with tarfile.open(fileobj=out, mode='w:gz') as archive:
        for name, data in files.items():
            item = tarfile.TarInfo(name)
            item.size = len(data)
            archive.addfile(item, io.BytesIO(data))
        if link:
            item = tarfile.TarInfo('repo/web/outside')
            item.type, item.linkname = tarfile.SYMTYPE, '/outside'
            archive.addfile(item)
    return out.getvalue()


class Fixture:
    def __init__(self):
        self.refs = {OLD: 'a' * 40, NEW: 'b' * 40}
        self.blobs, self.sources, self.releases = {}, {}, []
        self.calls, self.ref_reads, self.feed, self.mutate = [], {}, OLD[1:], None
        for i, tag in enumerate((OLD, NEW)):
            name = 'yesman' if tag == OLD else 'Yes Man'
            cfg = dict(version=tag[1:], identifier='dev.idy.yesman', name=name, updates=dict(publicKey=KEY))
            self.sources[self.refs[tag]] = tar({'repo/mygo.json': json.dumps(cfg).encode(), 'repo/web/index.html': b'<html>source</html>'})
            assets = []
            for target in TARGETS:
                binary = 'yesman.app/Contents/MacOS/' + name if target.startswith('darwin') else name + ('.exe' if target.startswith('windows') else '')
                archive_name = f'yesman-{tag[1:]}-{target}.tar.gz'
                self.blobs[tag, archive_name] = tar({binary: (tag + target).encode()})
                manifest = json.dumps(dict(version=tag[1:], url='https://github.com/idy/yesman/releases/download/' + tag + '/' + archive_name)).encode()
                self.blobs[tag, 'update-' + target + '.json'] = manifest
            for (t, asset_name), data in self.blobs.items():
                if t == tag:
                    assets.append(dict(id=100 + len(assets), name=asset_name, size=len(data), digest='sha256:' + hashlib.sha256(data).hexdigest()))
            self.releases.append(dict(tag_name=tag, id=i+1, draft=tag == NEW, prerelease=False, assets=assets))

    def output(self, cmd, **kwargs):
        self.calls.append(cmd)
        assert cmd[:2] == ['gh', 'api'], 'privileged downloader invoked repository/tool code: ' + repr(cmd)
        route = cmd[2]
        if route.startswith('repos/idy/yesman/git/ref/tags/'):
            tag = route.rsplit('/', 1)[-1]
            self.ref_reads[tag] = self.ref_reads.get(tag, 0) + 1
            sha = self.refs[tag]
            if self.mutate == 'tag-moved' and self.ref_reads[tag] > 1:
                sha = 'c' * 40
            return json.dumps(dict(object=dict(type='commit', sha=sha))).encode()
        if route.startswith('repos/idy/yesman/releases?'):
            return json.dumps(self.releases).encode()
        if route.startswith('repos/idy/yesman/tarball/'):
            return self.sources[route.rsplit('/', 1)[-1]]
        raise AssertionError(route)

    def run(self, cmd, **kwargs):
        self.calls.append(cmd)
        assert cmd[:3] == ['gh', 'release', 'download'], 'non-download command: ' + repr(cmd)
        tag, name, folder = cmd[3], cmd[cmd.index('-p')+1], pathlib.Path(cmd[cmd.index('-D')+1])
        data = self.blobs[tag, name]
        if self.mutate == 'corrupt-download':
            data += b'corruption'
        (folder/name).write_bytes(data)
        return subprocess.CompletedProcess(cmd, 0)

    def http(self, url):
        self.calls.append(['GET', url])
        return contextlib.closing(io.BytesIO(json.dumps(dict(version=self.feed)).encode()))

    def execute(self, out, pair=(OLD, NEW), source=None):
        namespace = {'__name__': 'offline_downloader'}
        exec(compile(source or module.downloader_source(), '<actual inline downloader>', 'exec'), namespace)
        with patch.object(sys, 'argv', ['downloader', *pair, str(out)]), patch('subprocess.check_output', self.output), patch('subprocess.run', self.run), patch('urllib.request.urlopen', self.http), patch.dict('os.environ', {'GITHUB_OUTPUT': str(out.parent/'job-output')}, clear=True), contextlib.redirect_stdout(io.StringIO()):
            namespace['main']()


class DownloaderTests(unittest.TestCase):
    def test_actual_inline_contract(self):
        cases = ['valid', 'old-pair', 'injection', 'old-draft', 'candidate-public', 'prerelease', 'stale-feed', 'duplicate', 'missing', 'zero', 'missing-digest', 'corrupt-download', 'manifest-version', 'tag-moved', 'wrong-source-version', 'wrong-key', 'linked-source']
        for case in cases:
            with self.subTest(case=case), tempfile.TemporaryDirectory() as directory:
                root = pathlib.Path(directory)
                out, f, pair = root/'inputs', Fixture(), (OLD, NEW)
                if case == 'old-pair': pair = ('v0.1.35', 'v0.1.36')
                if case == 'injection': pair = ('v0.1.38\nsource=evil', NEW)
                if case == 'old-draft': f.releases[0]['draft'] = True
                if case == 'candidate-public': f.releases[1]['draft'] = False
                if case == 'prerelease': f.releases[1]['prerelease'] = True
                if case == 'stale-feed': f.feed = '0.1.37'
                if case == 'duplicate': f.releases[0]['assets'].append(copy.deepcopy(f.releases[0]['assets'][0]))
                if case == 'missing': f.releases[0]['assets'].pop(0)
                if case == 'zero': f.releases[0]['assets'][0]['size'] = 0
                if case == 'missing-digest': f.releases[0]['assets'][0].pop('digest')
                if case in ('corrupt-download', 'tag-moved'): f.mutate = case
                if case == 'manifest-version':
                    k = (OLD, 'update-darwin-amd64.json')
                    value = json.loads(f.blobs[k]); value['version'] = '0.1.37'
                    f.blobs[k] = json.dumps(value).encode()
                    f.releases[0]['assets'][1].update(size=len(f.blobs[k]), digest='sha256:' + hashlib.sha256(f.blobs[k]).hexdigest())
                if case in ('wrong-source-version', 'wrong-key', 'linked-source'):
                    cfg = dict(version=NEW[1:], identifier='dev.idy.yesman', name='Yes Man', updates=dict(publicKey=KEY))
                    if case == 'wrong-source-version': cfg['version'] = '0.1.37'
                    if case == 'wrong-key': cfg['updates']['publicKey'] = 'other-key'
                    f.sources[f.refs[NEW]] = tar({'repo/mygo.json': json.dumps(cfg).encode(), 'repo/web/index.html': b'<html/>'}, link=case == 'linked-source')
                if case == 'valid':
                    f.execute(out, pair)
                    data = json.loads((out/'release-inputs.json').read_text())
                    self.assertEqual(len(data['stages']), 8)
                    self.assertEqual(data['refs'], f.refs)
                    self.assertEqual((root/'job-output').read_text(), 'source=' + f.refs[NEW] + '\n')
                    self.assertEqual((out/NEW/'web/index.html').read_bytes(), b'<html>source</html>')
                else:
                    with self.assertRaises((AssertionError, KeyError)):
                        f.execute(out, pair)
                    self.assertFalse((out/'release-inputs.json').exists())
                    self.assertFalse((root/'job-output').exists())
                    if case in ('old-pair', 'injection'): self.assertEqual(f.calls, [])
                print('PASS offline case:', case)


if __name__ == '__main__':
    unittest.main()
