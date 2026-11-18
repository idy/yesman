#!/usr/bin/env python3
"""Hosted-only orchestration and bounded public evidence for one native score run."""
import argparse
import json
import os
from pathlib import Path
import platform
import subprocess
import sys

HERE = Path(__file__).resolve().parent
KIT = HERE / 'kit'
sys.path.insert(0, str(KIT))
from contract import SOURCE, TREE, CSV, SETTINGS, check_native, sha

BRANCH = 'refs/heads/qa/model-score-package-acceptance'
QA_PARENT = 'a8830edc327f29379495d3cc4875ccd4b63f599c'
RECORDS = [
    'admission.json', 'plan.json', 'dependencies.log',
    'evidence/build-A.log', 'evidence/build-B.log',
    'evidence/bundle-A.json', 'evidence/bundle-B.json',
    'native-run/evidence/proof.json',
    'native-run/evidence/page-A.json', 'native-run/evidence/page-B.json',
    'native-run/evidence/bundle-A.json', 'native-run/evidence/bundle-B.json',
    'native-run/evidence/app-A.log', 'native-run/evidence/app-B.log',
    'native-run/evidence/catalog.jsonl',
]


def output(args, cwd=None):
    return subprocess.check_output(args, cwd=cwd, text=True).strip()


def require_hosted():
    assert sys.platform == 'darwin' and platform.machine() == 'arm64'
    assert os.environ.get('GITHUB_ACTIONS') == 'true'
    assert os.environ.get('RUNNER_ENVIRONMENT') == 'github-hosted'
    assert os.environ.get('RUNNER_OS') == 'macOS'
    assert os.environ.get('GITHUB_REPOSITORY') == 'idy/yesman'
    assert os.environ.get('GITHUB_REF') == BRANCH
    assert os.environ.get('GITHUB_EVENT_NAME') == 'push'
    assert os.environ.get('GITHUB_RUN_ATTEMPT') == '1'
    assert os.environ.get('GITHUB_RUN_ID', '').isdigit()


def check_inputs(source):
    inputs = json.loads((HERE / 'inputs.json').read_text())
    assert inputs['source'] == SOURCE and inputs['tree'] == TREE
    assert inputs['preparedKitZIP_SHA256'] == '8d9b2b27e5e848800d45be7c19cd13c4e74611931c91142b37d07775a1f4981a'
    assert {p.name: sha(p.read_bytes()) for p in KIT.iterdir() if p.is_file()} == inputs['selectedKitFiles']
    assert all(p.is_file() and not p.is_symlink() for p in KIT.iterdir())
    assert output(['git', 'rev-parse', 'HEAD'], source) == SOURCE
    assert output(['git', 'rev-parse', 'HEAD^{tree}'], source) == TREE
    assert not output(['git', 'status', '--porcelain=v1', '--untracked-files=all'], source)
    qa = HERE.parent.parent
    head = output(['git', 'rev-parse', 'HEAD'], qa)
    assert head == os.environ['GITHUB_SHA']
    assert output(['git', 'show', '-s', '--format=%P', 'HEAD'], qa) == QA_PARENT
    changed = output(['git', 'diff', '--name-only', SOURCE, head], qa).splitlines()
    assert sorted(changed) == sorted(inputs['qaPaths'])
    return {'productHead': SOURCE, 'productTree': TREE, 'qaHead': head,
            'qaTree': output(['git', 'rev-parse', 'HEAD^{tree}'], qa),
            'qaParent': QA_PARENT, 'qaPaths': changed,
            'qaFileSHA256': {p: sha((qa / p).read_bytes()) for p in changed},
            'preparedKitZIP_SHA256': inputs['preparedKitZIP_SHA256'],
            'selectedKitFiles': inputs['selectedKitFiles']}


def build(source, root):
    binding = check_inputs(source)
    sys.path.insert(0, str(source / 'scripts'))
    from check_macos_startup import require_hosted_runner
    require_hosted_runner()
    assert subprocess.run(['pgrep', '-x', 'Yes Man'], stdout=subprocess.DEVNULL).returncode == 1
    assert not root.exists(), 'A fresh job must not reuse package output'
    dep = root.parent / 't212-score-dependencies'
    dep.mkdir(mode=0o700)
    for name in ('home', 'gopath', 'mod', 'cache', 'temp', 'config'):
        (dep / name).mkdir(mode=0o700)
    env = {key: os.environ[key] for key in ('PATH', 'LANG') if key in os.environ}
    env.update(HOME=str(dep / 'home'), GOPATH=str(dep / 'gopath'),
               GOMODCACHE=str(dep / 'mod'), GOCACHE=str(dep / 'cache'),
               TMPDIR=str(dep / 'temp'), XDG_CONFIG_HOME=str(dep / 'config'),
               GOENV='off', GOTOOLCHAIN='local', GOPROXY='https://proxy.golang.org',
               GOSUMDB='sum.golang.org', GOPRIVATE='', GONOPROXY='', GONOSUMDB='')
    admission = {**binding, 'runID': os.environ['GITHUB_RUN_ID'], 'runAttempt': 1,
                 'repository': 'idy/yesman', 'event': 'push', 'ref': BRANCH,
                 'runnerEnvironment': os.environ['RUNNER_ENVIRONMENT'],
                 'runnerOS': os.environ['RUNNER_OS'], 'runnerArch': platform.machine(),
                 'runnerName': os.environ.get('RUNNER_NAME'),
                 'imageOS': os.environ.get('ImageOS'), 'imageVersion': os.environ.get('ImageVersion'),
                 'go': output(['go', 'version']), 'python': sys.version,
                 'xcode': output(['xcodebuild', '-version']),
                 'patchSHA256': sha(CSV), 'settingsSHA256': sha(SETTINGS)}
    # Preserve admission and dependency diagnostics even if compilation fails.
    artifact = root.parent / 't212-score-artifact'
    artifact.mkdir(mode=0o700)
    (artifact / 'admission.json').write_text(json.dumps(admission, indent=2) + '\n')
    with (artifact / 'dependencies.log').open('w') as log:
        subprocess.run(['go', 'mod', 'download'], cwd=source, env=env,
                       stdout=log, stderr=subprocess.STDOUT, check=True, timeout=300)
    # prepare.py creates root itself and uses an offline, clean build environment.
    subprocess.run([sys.executable, '-B', str(KIT / 'prepare.py'), '--repo', str(source),
                    '--output', str(root), '--build'], env=env, check=True, timeout=1300)


def collect_records(root, destination):
    """Copy only enumerated regular, bounded records, never packages/profile/cache."""
    destination.mkdir(mode=0o700, exist_ok=True)
    for name in RECORDS:
        source = root / name
        if not source.exists():
            continue
        assert source.is_file() and not source.is_symlink(), name
        assert source.resolve().is_relative_to(root.resolve()), name
        data = source.read_bytes()
        assert len(data) <= 4 * 1024 * 1024, name
        data.decode('utf-8')
        target = destination / name
        target.parent.mkdir(parents=True, exist_ok=True)
        assert not target.exists(), 'Refusing to overwrite an existing record: ' + name
        target.write_bytes(data)
    files = [p for p in destination.rglob('*') if p.is_file()]
    assert all(str(p.relative_to(destination)) in RECORDS for p in files)
    assert all(not p.is_symlink() and p.stat().st_size <= 4 * 1024 * 1024 for p in files)
    assert sum(p.stat().st_size for p in files) <= 16 * 1024 * 1024
    return files


def check_result(destination):
    proofpath = destination / 'native-run/evidence/proof.json'
    if not proofpath.exists():
        return {'nativeAcceptance': False, 'reason': 'No native proof; inspect job failure'}
    proof = json.loads(proofpath.read_text())
    if not proof.get('passed'):
        return {'nativeAcceptance': False, 'reason': 'Native proof is incomplete; inspect job failure'}
    plan = json.loads((destination / 'plan.json').read_text())
    admission = json.loads((destination / 'admission.json').read_text())
    assert plan['source'] == proof['source'] == admission['productHead'] == SOURCE
    assert plan['tree'] == proof['tree'] == admission['productTree'] == TREE
    assert proof['runID'] == admission['runID'] == os.environ['GITHUB_RUN_ID']
    assert proof['runAttempt'] == '1' and proof['runnerEnvironment'] == 'github-hosted'
    assert proof['planSHA256'] == sha((destination / 'plan.json').read_bytes())
    assert len(proof['stages']) == 2
    for stage in proof['stages']:
        key = stage['stage']
        page = json.loads((destination / ('native-run/evidence/page-' + key + '.json')).read_text())
        check_native(page, key, proof['home'])
        assert stage['exitCode'] == 0
        assert stage['imageSHA256'] == plan['stages'][key]['binarySHA256']
        assert stage['files'] == proof['before'] == {'patchSHA256': sha(CSV), 'settingsSHA256': sha(SETTINGS)}
        assert stage['crew'] == page['crew'] and stage['rows'] == page['rows']
    assert [s['stage'] for s in proof['stages']] == ['A', 'B']
    assert proof['stages'][0]['imageSHA256'] != proof['stages'][1]['imageSHA256']
    return {'nativeAcceptance': True, 'source': SOURCE, 'tree': TREE, 'runID': proof['runID']}


def main():
    require_hosted()  # Never turn workstation flags into hosted authority.
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('action', choices=['build', 'collect'])
    parser.add_argument('source', type=Path)
    args = parser.parse_args()
    root = Path(os.environ['RUNNER_TEMP']).resolve() / 't212-score-native'
    if args.action == 'build':
        build(args.source.resolve(), root)
    else:
        destination = root.parent / 't212-score-artifact'
        files = collect_records(root, destination)
        verdict = check_result(destination)
        (destination / 'validation.json').write_text(json.dumps(verdict, indent=2) + '\n')
        files.append(destination / 'validation.json')
        manifest = {str(p.relative_to(destination)): {'bytes': p.stat().st_size, 'sha256': sha(p.read_bytes())}
                    for p in sorted(files)}
        (destination / 'manifest.json').write_text(json.dumps(manifest, indent=2) + '\n')
        print(json.dumps({'records': len(files), **verdict}))


if __name__ == '__main__':
    main()
