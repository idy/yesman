#!/usr/bin/env python3
"""Launch an unchanged production .app ONLY on a fresh GitHub-hosted macOS VM.

No e2e/debug build, permission prompts, test accounts, sudo, or TCC changes.
Phase 1 checks the embedded frontend's native window. Phase 2 serves byte-
identical production web files through the existing YESMAN_WEB_DIR override,
with one appended read-only driver; the real WebKit and Go bridge run normally.
"""
import argparse
import hashlib
import http.server
import json
import os
import pathlib
import shutil
import signal
import subprocess
import sys
import tempfile
import threading
import time
import uuid
from check_macos_bundle import inspect, release_arguments


def require_hosted_runner():
    assert sys.platform == 'darwin', 'macOS required'
    assert os.environ.get('GITHUB_ACTIONS') == 'true' and os.environ.get('RUNNER_ENVIRONMENT') == 'github-hosted', 'Refusing to launch outside a disposable GitHub-hosted runner'
    assert os.environ.get('RUNNER_OS') == 'macOS'
    assert subprocess.run(['pgrep', '-x', 'yesman'], stdout=subprocess.DEVNULL).returncode == 1, 'An existing yesman instance is present'
    # Do not override HOME: the runner supplies a fresh whole user profile,
    # covering NSUserDefaults/WebKit/MyGo state as well as our data directory.
    for path in ('yesman', 'Library/Application Support/yesman', 'Library/Preferences/dev.idy.yesman.plist', 'Library/WebKit/dev.idy.yesman', 'Library/HTTPStorages/dev.idy.yesman'):
        assert not (pathlib.Path.home() / path).exists(), 'Runner profile already contains yesman state: ' + path


def main():
    require_hosted_runner()
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('app')
    parser.add_argument('web')
    parser.add_argument('evidence')
    release_arguments(parser)
    args = parser.parse_args()
    app, web, evidence = [pathlib.Path(value).resolve() for value in (args.app, args.web, args.evidence)]
    evidence.mkdir(parents=True, exist_ok=True)
    bundle = inspect(app, args.release_tag, args.arch, args.source, args.expect_team_id, args.expect_adhoc)
    (evidence / 'bundle.json').write_text(json.dumps(bundle, indent=2))
    executable = app / 'Contents/MacOS' / bundle['info']['CFBundleExecutable']
    binary = executable.read_bytes()
    # A publisher must use web/ from the same final tag as the downloaded app.
    for name in ('index.html', 'app.js', 'grants.js', 'dom.js', 'i18n.js', 'app.css', 'locales/zh-Hans.js'):
        assert (web / name).read_bytes() in binary, 'Frontend does not match this binary: ' + name
    proof = {'bundleSHA256': bundle['binarySHA256'], 'runnerEnvironment': os.environ['RUNNER_ENVIRONMENT'], 'runnerOS': os.environ['RUNNER_OS'], 'runnerArch': os.environ.get('RUNNER_ARCH'), 'runID': os.environ.get('GITHUB_RUN_ID'), 'sourceSHA': os.environ.get('GITHUB_SHA'), 'phases': []}
    result = {}
    received = threading.Event()
    token = uuid.uuid4().hex

    class Collector(http.server.BaseHTTPRequestHandler):
        def log_message(self, *args):
            pass

        def do_OPTIONS(self):
            self.send_response(204)
            self.send_header('Access-Control-Allow-Origin', '*')
            self.send_header('Access-Control-Allow-Methods', 'POST, OPTIONS')
            self.send_header('Access-Control-Allow-Headers', 'Content-Type')
            self.send_header('Access-Control-Allow-Private-Network', 'true')
            self.end_headers()

        def do_CONNECT(self):
            # The production updater uses MyGo netproxy, which honors explicit
            # HTTPS_PROXY. Refuse external network without changing the binary.
            self.send_error(403, 'Network disabled during native smoke')

        def do_POST(self):
            if self.path != '/' + token:
                self.send_error(404)
                return
            size = int(self.headers.get('Content-Length', '0'))
            if size > 131072:
                self.send_error(413)
                return
            result.update(json.loads(self.rfile.read(size)))
            self.send_response(200)
            self.send_header('Access-Control-Allow-Origin', '*')
            self.end_headers()
            received.set()

    server = http.server.ThreadingHTTPServer(('127.0.0.1', 0), Collector)
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    endpoint = 'http://127.0.0.1:' + str(server.server_port)
    process = None
    try:
        with tempfile.TemporaryDirectory(prefix='yesman-native-smoke-', dir=os.environ['RUNNER_TEMP']) as temporary:
            temp = pathlib.Path(temporary)
            helper = temp / 'window-info'
            subprocess.run(['xcrun', 'swiftc', str(pathlib.Path(__file__).with_name('macos-smoke-windows.swift')), '-o', str(helper)], check=True)
            instrumented = temp / 'web'
            shutil.copytree(web, instrumented)
            driver = r'''
<script>
(() => {
  const calls = [], errors = [];
  window.addEventListener('error', e => errors.push(e.message));
  window.addEventListener('unhandledrejection', e => errors.push(String(e.reason)));
  const read = (method) => {
    if (!['Yesman.State', 'Yesman.Grants'].includes(method)) throw Error('Only read-only smoke calls are allowed');
    calls.push(method);
    return window.mygo.call(method);
  };
  document.addEventListener('click', event => {
    if (event.target.closest('[data-grant-action]')) { event.preventDefault(); event.stopImmediatePropagation(); }
  }, true);
  const until = async fn => { const end = Date.now()+30000; while(!fn()) { if(Date.now()>end) throw Error('Production page did not become ready'); await new Promise(r=>setTimeout(r,50)); } };
  const send = value => fetch(ENDPOINT, {method:'POST',headers:{'Content-Type':'text/plain'},body:JSON.stringify(value)});
  (async () => {
    await until(()=>document.querySelector('#settings-button'));
    document.querySelector('#settings-button').click();
    await until(()=>document.querySelector('#settings-nav [data-page="grant"]'));
    document.querySelector('#settings-nav [data-page="grant"]').click();
    await until(()=>document.querySelectorAll('[data-permission]').length===9 && document.querySelector('[data-grant-focus="refresh"]')?.getAttribute('aria-disabled')==='false');
    const state = await read('Yesman.State');
    const grants = await read('Yesman.Grants');
    await send({ok:true,home:state.home,yesmen:state.yesmen,agents:state.agents,grants,calls,errors,url:location.href,userAgent:navigator.userAgent,visibility:document.visibilityState,viewport:[innerWidth,innerHeight],rows:[...document.querySelectorAll('[data-permission]')].map(el=>({id:el.dataset.permission,text:el.innerText,width:el.getBoundingClientRect().width})),heading:document.querySelector('.grant-toolbar h2').textContent});
  })().catch(async error=>{await send({ok:false,error:String(error),calls,errors});});
})();
</script>
'''.replace('ENDPOINT', json.dumps(endpoint + '/' + token))
            index = instrumented / 'index.html'
            original = index.read_text()
            index.write_text(original.replace('<head>', '<head>' + driver, 1))
            # Every production asset is unchanged; only index.html has the driver.
            assert index.read_text().replace(driver, '', 1) == original
            proof['frontendOverride'] = {'driverOnlyInIndex': True, 'files': {str(p.relative_to(web)): hashlib.sha256(p.read_bytes()).hexdigest() for p in web.rglob('*') if p.is_file()}}
            for phase in ('embedded', 'production-page-with-observer'):
                data = temp / (phase + '-data')
                data.mkdir()
                env = {key: value for key, value in os.environ.items() if key in ('HOME', 'USER', 'LOGNAME', 'PATH', 'TMPDIR', 'LANG', 'LC_ALL', 'SHELL')}
                env.update({'YESMAN_HOME': str(data), 'YESMAN_PI': str(temp / 'pi-not-installed'), 'PI_CODING_AGENT_DIR': str(temp / 'pi-config'), 'HTTP_PROXY': endpoint, 'HTTPS_PROXY': endpoint, 'NO_PROXY': '127.0.0.1,localhost,::1'})
                if phase != 'embedded':
                    env['YESMAN_WEB_DIR'] = str(instrumented)
                with (evidence / (phase + '.log')).open('w') as log:
                    process = subprocess.Popen([str(executable)], cwd=temp, env=env, stdout=log, stderr=subprocess.STDOUT)
                    end = time.monotonic() + 45
                    windows = []
                    while time.monotonic() < end:
                        assert process.poll() is None, 'Production app exited before showing its window'
                        windows = json.loads(subprocess.check_output([str(helper), str(process.pid)]))
                        if any(w['onScreen'] and w['bounds'].get('Width', 0) > 100 and w['bounds'].get('Height', 0) > 100 for w in windows):
                            break
                        time.sleep(0.2)
                    else:
                        raise AssertionError('No visible native window for the launched process')
                    if phase != 'embedded':
                        assert received.wait(45), 'No actual WebKit/Go bridge result'
                        (evidence / 'page.json').write_text(json.dumps(result, indent=2))
                        assert result.get('ok'), result
                        assert result['home'] == str(data) and not result['yesmen'] and not result['agents'], result
                        assert not result['errors'] and len(result['rows']) == 9
                        assert result['grants']['platform'] == 'darwin'
                        assert all(not item.get('error') and item['state'] != 'unavailable' for item in result['grants']['items']), result['grants']
                        assert result['calls'] == ['Yesman.State', 'Yesman.Grants']
                        assert 'AppleWebKit/' in result['userAgent'] and result['visibility'] == 'visible'
                    # MyGo handles the first SIGTERM via App.Quit: normal window
                    # and app shutdown listeners run. A forced exit never passes.
                    process.send_signal(signal.SIGTERM)
                    code = process.wait(timeout=15)
                    assert code == 0, ('App did not exit normally', code)
                    assert not json.loads(subprocess.check_output([str(helper), str(process.pid)])), 'Native window remained after exit'
                    proof['phases'].append({'phase': phase, 'pid': process.pid, 'windows': windows, 'exitCode': code})
                    process = None
            assert hashlib.sha256(executable.read_bytes()).hexdigest() == proof['bundleSHA256'], 'Production executable changed during smoke'
            proof['passed'] = True
    finally:
        if process is not None and process.poll() is None:
            process.send_signal(signal.SIGTERM)
            try:
                process.wait(timeout=5)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait()
        server.shutdown()
        server.server_close()
        (evidence / 'smoke.json').write_text(json.dumps(proof, indent=2))
    print('PASS: unchanged production binary, native window, actual WebKit/Go read-only Grant page, normal shutdown')


if __name__ == '__main__':
    main()
