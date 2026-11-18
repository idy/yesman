"""Normal isolated offline production Runtime/installer controls; no App/provider.
Requires a verified selected public cache and a new task-private scratch root.
Raw npm logs/config/child text remain private scratch and are removed on exit.
"""
import argparse
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import shutil
import subprocess
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import time

import public_npm_cache as cache


def sha(raw):
    return hashlib.sha256(raw).hexdigest()


def run(cache_root, work, only_corrected=False):
    repo = Path(__file__).resolve().parents[2]
    work = Path(work).absolute()
    if work.exists() or work != Path(os.environ['T199_CONTROL_ROOT']).absolute():
        raise ValueError('new explicit task control root required')
    for parent in work.parents:
        if parent.is_symlink():
            raise ValueError('ordinary control parents required')
    work.mkdir()
    (work / 'build-tmp').mkdir()
    (work / 'build-home').mkdir()
    lock = (repo / 'internal/piruntime/assets/package-lock.json').read_bytes()
    package = (repo / 'internal/piruntime/assets/package.json').read_bytes()
    if sha(package) != cache.PACKAGE:
        raise ValueError('pinned package changed')
    cache.verify(cache_root, lock)
    env = {'PATH': os.environ['PATH'], 'HOME': str(work / 'build-home'), 'TMPDIR': str(work / 'build-tmp'),
           'GOCACHE': str(work / 'go-cache'), 'GOMODCACHE': str(work / 'go-mod'),
           'GOTMPDIR': str(work / 'build-tmp'), 'GOPROXY': 'off', 'GOSUMDB': 'off',
           'GOENV': 'off', 'GOTOOLCHAIN': 'local', 'CGO_ENABLED': '0'}
    binary = work / 'runtime-control'
    result = subprocess.run(['go', 'build', '-o', str(binary), './qa/restart-resume/runtime-control'],
                            cwd=repo, env=env, capture_output=True, timeout=60)
    if result.returncode:
        raise ValueError('QA control compilation failed')
    requests = []
    class Deny(BaseHTTPRequestHandler):
        def log_message(self, *args):
            pass
        def deny(self):
            role = 'public-registry' if 'registry.npmjs.org' in self.path else 'other'
            requests.append({'method': self.command, 'role': role})
            self.send_response(403)
            self.end_headers()
        do_CONNECT = do_GET = do_POST = deny
    server = ThreadingHTTPServer(('127.0.0.1', 0), Deny)
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    proxy = 'http://127.0.0.1:' + str(server.server_address[1])
    spec = importlib.util.spec_from_file_location('offline_ready_attachment', repo / 'qa/restart-resume/runner-attachment-f48/runner_attachment.py')
    attachment = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(attachment)
    adapter, _, _, _ = attachment.dependencies(repo / 'qa/restart-resume')
    bindings = json.loads((repo / 'qa/restart-resume/runner-attachment-f48/source-binding.json').read_bytes())
    rows = []
    real_node = str(Path(shutil.which('node')).resolve())
    real_npm = str(Path(shutil.which('npm')).resolve())
    if not real_npm.endswith('/npm/bin/npm-cli.js'):
        raise ValueError('control requires the ordinary installed npm CLI path')
    try:
        # Historical code is fixed read-only Git input, never a moving branch.
        old = subprocess.check_output(['git', '--no-optional-locks', '-C', str(repo), 'show',
                                       'e795fd6ef33b68365fea29b2bb4144ebc92bd554:qa/restart-resume/public_npm_cache.py'], timeout=10)
        namespace = {'__name__': 'r4_cache_control'}
        exec(compile(old, '<fixed-R4-cache-control>', 'exec'), namespace)
        cases = [('corrected-normal', cache.bootstrap)] if only_corrected else [('r4-normal', namespace['bootstrap']), ('corrected-normal', cache.bootstrap)]
        for name, bootstrap in cases:
            case = work / name
            data = case / 'data'
            data.mkdir(parents=True)
            (case / 'home').mkdir()
            (case / 'tmp').mkdir()
            receipt = bootstrap(cache_root, data, lock)
            tools = case / 'tools'
            tools.mkdir()
            trace = case / 'tool-observation.jsonl'
            # Observe the actual production Go installer invocation, then exec
            # the unchanged installed Node. Read only fixed safe public fields.
            observer = '''#!/usr/bin/env python3
import hashlib,json,os,pathlib,sys
node=NODE
npm=NPM
trace=TRACE
config=CONFIG
def digest(path):
 p=pathlib.Path(path);s=p.stat()
 if not p.is_file() or s.st_size>128<<20:raise SystemExit(91)
 return hashlib.sha256(p.read_bytes()).hexdigest()
role='production-installer' if len(sys.argv)>1 and pathlib.Path(sys.argv[1]).name=='install.mjs' else 'managed-cli-version'
row={'role':role,'nodePath':node,'nodeSHA256':digest(node),'credentialsAbsent':all(not os.environ.get(k)for k in('GH_TOKEN','GITHUB_TOKEN','MYGO_UPDATER_PRIVATE_KEY','OPENAI_API_KEY','ANTHROPIC_API_KEY'))}
if role=='production-installer':
 actual=os.environ.get('YESMAN_RUNTIME_NPM')
 row.update(npmCLIPath=actual if actual==npm else None,npmCLIMatchesInstalled=actual==npm,npmCLISHA256=digest(npm),userConfigMatches=os.environ.get('NPM_CONFIG_USERCONFIG')==config,globalConfigIsNull=os.environ.get('NPM_CONFIG_GLOBALCONFIG')==os.devnull,configSHA256=digest(config),installScriptSHA256=digest(sys.argv[1]))
fd=os.open(trace,os.O_WRONLY|os.O_CREAT|os.O_APPEND,0o600)
try:os.write(fd,(json.dumps(row)+'\\n').encode());os.fsync(fd)
finally:os.close(fd)
os.execv(node,[node,*sys.argv[1:]])
'''
            observer = observer.replace('node=NODE', 'node=' + repr(real_node)).replace('npm=NPM', 'npm=' + repr(real_npm)).replace('trace=TRACE', 'trace=' + repr(str(trace))).replace('config=CONFIG', 'config=' + repr(receipt['npmUserConfig']))
            (tools / 'node').write_text(observer)
            (tools / 'node').chmod(0o700)
            child_env = {'PATH': str(tools) + os.pathsep + os.environ['PATH'], 'HOME': str(case / 'home'),
                         'TMPDIR': str(case / 'tmp'), 'TMP': str(case / 'tmp'), 'TEMP': str(case / 'tmp'),
                         'NPM_CONFIG_USERCONFIG': receipt['npmUserConfig'], 'NPM_CONFIG_GLOBALCONFIG': os.devnull,
                         'NPM_CONFIG_REGISTRY': 'https://registry.npmjs.org/',
                         'HTTP_PROXY': proxy, 'HTTPS_PROXY': proxy, 'NO_PROXY': '127.0.0.1,localhost,::1',
                         'T199_CONTROL_DATA': str(data)}
            before = len(requests)
            started = time.monotonic()
            value = subprocess.run([str(binary), str(data)], env=child_env, cwd=repo,
                                   capture_output=True, timeout=75)
            output = json.loads(value.stdout)
            if value.returncode or not output['passed']:
                raise ValueError('offline normal production control failed: ' + name + '/' + output['phase'])
            runtime = data / '.runtime/pi-managed/1.5.2-yesman.5-498b685a8d6ccde6'
            ready = json.loads((runtime / 'ready.json').read_bytes())
            manifest = json.loads((runtime / 'patch.json').read_bytes())
            after = {p['path']: sha((runtime / 'node_modules' / p['path']).read_bytes()) for p in manifest['patches']}
            if any(after[p['path']] != p['after'] for p in manifest['patches']):
                raise ValueError('production after hashes differ')
            objects = cache.verify(data / '.runtime/pi-managed/npm-cache', lock)
            runtime_receipt = attachment.capture_runtime(adapter, data, bindings, bindings['runtimeSourceSHA256'])
            tool_rows = [json.loads(line) for line in trace.read_text().splitlines()]
            installer = [row for row in tool_rows if row['role'] == 'production-installer']
            if len(installer) != 1 or not all(installer[0][key] for key in ('credentialsAbsent', 'npmCLIMatchesInstalled', 'userConfigMatches', 'globalConfigIsNull')) or installer[0]['installScriptSHA256'] != sha((repo / 'internal/piruntime/assets/install.mjs').read_bytes()):
                raise ValueError('actual safe installer tool/config observation differs')
            row = {'case': name, 'exit': value.returncode, 'elapsedSeconds': round(time.monotonic() - started, 3),
                   'stdoutBytes': len(value.stdout), 'stderrBytes': len(value.stderr), 'result': output,
                   'ready': ready, 'readySHA256': sha((runtime / 'ready.json').read_bytes()),
                   'patchAfterHashes': after, 'cacheObjects': len(objects['objects']),
                   'cacheBytes': objects['bytes'], 'proxyRequests': requests[before:],
                   'configSHA256': sha(Path(receipt['npmUserConfig']).read_bytes()),
                   'actualToolObservations': tool_rows,
                   'actualCaptureRuntimeEntryPassed': True,
                   'sixAssetRuntimeSHA256': runtime_receipt['runtimeSHA256'],
                   'actualCaptureReadySHA256': runtime_receipt['readySHA256'],
                   'rawLogsOrConfigRetained': False}
            if name == 'corrected-normal' and row['proxyRequests']:
                raise ValueError('corrected offline setup attempted network')
            rows.append(row)
            # Only freshly created, fully joined control profiles are reclaimed.
            shutil.rmtree(case)
        rejected = subprocess.run([str(binary), str(work)], cwd=repo,
                                  env={'PATH': os.environ['PATH'], 'HOME': str(work / 'build-home'),
                                       'T199_CONTROL_DATA': str(work), 'GH_TOKEN': 'synthetic-control-only'},
                                  capture_output=True, timeout=5)
        rejection = json.loads(rejected.stdout)
        if rejected.returncode != 1 or rejection.get('reason') != 'credential-present':
            raise ValueError('keyless control admission failed')
        versions = {}
        for name, command in [('node', ['node', '--version']), ('npm', ['npm', '--version']), ('go', ['go', 'version'])]:
            value = subprocess.run(command, env=env, capture_output=True, timeout=10, cwd=repo)
            if value.returncode:
                raise ValueError('tool version unavailable')
            versions[name] = value.stdout.decode().strip()
        return {'passed': True, 'cases': rows, 'versions': versions,
                'controlBinarySHA256': sha(binary.read_bytes()),
                'source': {p: sha((repo / p).read_bytes()) for p in
                           ('qa/restart-resume/runtime-control/main.go', 'internal/piruntime/runtime.go',
                            'internal/piruntime/environment.go', 'internal/piruntime/assets/install.mjs')},
                'pinnedPackageSHA256': cache.PACKAGE, 'pinnedLockSHA256': cache.LOCK,
                'credentialAdmission': rejection, 'appOrProviderExecuted': False,
                'historicalHostedRuntimeOutcome': 'Still cut short; this is an isolated normal control, not recovered R4 ready proof'}
    finally:
        server.shutdown()
        server.server_close()
        thread.join(timeout=5)
        if thread.is_alive():
            raise ValueError('control proxy did not stop')


if __name__ == '__main__':
    parser = argparse.ArgumentParser()
    parser.add_argument('cache')
    parser.add_argument('work')
    parser.add_argument('output')
    parser.add_argument('--only-corrected', action='store_true')
    args = parser.parse_args()
    value = run(Path(args.cache).resolve(), args.work, args.only_corrected)
    with Path(args.output).open('x') as output:
        json.dump(value, output, indent=2)
        output.write('\n')
    print(json.dumps({'passed': value['passed'], 'cases': [{'case': x['case'], 'elapsedSeconds': x['elapsedSeconds'],
                      'state': x['result']['state'], 'patchFiles': len(x['patchAfterHashes']),
                      'proxyRequests': x['proxyRequests']} for x in value['cases']]}, indent=2))
