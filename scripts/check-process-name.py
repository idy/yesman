#!/usr/bin/env python3
"""Observe the real packaged process ONLY in a fresh GitHub-hosted VM.

The first launch uses embedded production web assets. The second uses the
same production binary and assets with a read-only State observer appended to
index.html through the existing YESMAN_WEB_DIR override. No test build, model,
permission request, settings edit, or provider credentials are involved.
"""
import ctypes
import hashlib
import http.server
import json
import os
import pathlib
import plistlib
import shutil
import signal
import subprocess
import sys
import tempfile
import threading
import time
import uuid


def hosted():
    assert os.environ.get('GITHUB_ACTIONS') == 'true' and os.environ.get('RUNNER_ENVIRONMENT') == 'github-hosted', 'Refusing to launch outside a disposable hosted VM'
    paths = ['yesman', 'Library/Application Support/yesman', 'Library/Preferences/dev.idy.yesman.plist']
    if sys.platform == 'win32':
        paths = []
        for key in ('APPDATA', 'LOCALAPPDATA'):
            assert not (pathlib.Path(os.environ[key]) / 'yesman').exists(), 'Runner already has an app profile'
    for path in paths:
        assert not (pathlib.Path.home() / path).exists(), 'Runner already has an app profile: ' + path


def powershell(code):
    return json.loads(subprocess.check_output(['powershell.exe', '-NoLogo', '-NoProfile', '-NonInteractive', '-Command', code], text=True))


def windows_pids(executable):
    quoted = str(executable).replace("'", "''")
    # -InputObject keeps [] and [pid] arrays intact; piping would enumerate
    # a singleton to a scalar (or produce no JSON at all for zero matches).
    return powershell("ConvertTo-Json -Compress -InputObject @((Get-CimInstance Win32_Process | Where-Object {$_.ExecutablePath -eq '" + quoted + "'}).ProcessId)")


def windows(pid):
    from ctypes import wintypes
    user = ctypes.WinDLL('user32', use_last_error=True)
    callback = ctypes.WINFUNCTYPE(wintypes.BOOL, wintypes.HWND, wintypes.LPARAM)
    user.GetWindowThreadProcessId.argtypes = [wintypes.HWND, ctypes.POINTER(wintypes.DWORD)]
    user.IsWindowVisible.argtypes = [wintypes.HWND]
    user.GetClassNameW.argtypes = [wintypes.HWND, wintypes.LPWSTR, ctypes.c_int]
    user.GetWindowTextW.argtypes = [wintypes.HWND, wintypes.LPWSTR, ctypes.c_int]
    found = []
    @callback
    def visit(hwnd, _):
        owner = wintypes.DWORD()
        user.GetWindowThreadProcessId(hwnd, ctypes.byref(owner))
        if owner.value == pid:
            name, title = ctypes.create_unicode_buffer(256), ctypes.create_unicode_buffer(512)
            user.GetClassNameW(hwnd, name, 256)
            user.GetWindowTextW(hwnd, title, 512)
            found.append({'handle': int(hwnd), 'class': name.value, 'title': title.value, 'visible': bool(user.IsWindowVisible(hwnd))})
        return True
    user.EnumWindows(visit, 0)
    return found


def native(pid, helper):
    if sys.platform == 'darwin':
        value = json.loads(subprocess.check_output([str(helper), str(pid)]))
        lib = ctypes.CDLL('/usr/lib/libproc.dylib')
        buf = ctypes.create_string_buffer(1024)
        assert lib.proc_name(pid, buf, len(buf)) > 0
        value['processName'] = buf.value.decode()
        value['psComm'] = subprocess.check_output(['ps', '-p', str(pid), '-o', 'comm='], text=True).strip()
        return value
    if sys.platform == 'win32':
        value = powershell(f"$p=Get-Process -Id {pid}; $v=[Diagnostics.FileVersionInfo]::GetVersionInfo($p.Path); [ordered]@{{processName=$p.ProcessName;executable=$p.Path;fileDescription=$v.FileDescription;productName=$v.ProductName}} | ConvertTo-Json -Compress")
        value['windows'] = windows(pid)
        return value
    root = pathlib.Path('/proc') / str(pid)
    tree = subprocess.check_output(['xwininfo', '-root', '-tree'], text=True)
    return {'processName': (root / 'comm').read_text().strip(), 'executable': os.readlink(root / 'exe'), 'windows': tree, 'cmdline': (root / 'cmdline').read_bytes().decode().split('\0')}


class WindowsChild:
    def __init__(self, pid):
        from ctypes import wintypes
        self.pid, self.returncode = pid, None
        self.kernel = ctypes.WinDLL('kernel32', use_last_error=True)
        self.kernel.OpenProcess.argtypes = [wintypes.DWORD, wintypes.BOOL, wintypes.DWORD]
        self.kernel.OpenProcess.restype = wintypes.HANDLE
        self.kernel.GetExitCodeProcess.argtypes = [wintypes.HANDLE, ctypes.POINTER(wintypes.DWORD)]
        self.kernel.TerminateProcess.argtypes = [wintypes.HANDLE, wintypes.UINT]
        self.handle = self.kernel.OpenProcess(0x00100000 | 0x1000 | 1, False, pid)
        assert self.handle
    def poll(self):
        from ctypes import wintypes
        code = wintypes.DWORD()
        assert self.kernel.GetExitCodeProcess(self.handle, ctypes.byref(code))
        if code.value != 259: self.returncode = code.value
        return self.returncode
    def wait(self, timeout):
        end = time.monotonic() + timeout
        while self.poll() is None:
            if time.monotonic() > end: raise subprocess.TimeoutExpired('Windows child', timeout)
            time.sleep(.1)
        return self.returncode
    def terminate(self): self.kernel.TerminateProcess(self.handle, 1)
    kill = terminate


def quit_process(process, info):
    if sys.platform == 'win32':
        # Deliver the public session-ending message ONLY to this synthetic
        # process's MyGoApp window, exercising MyGo's normal shutdown hooks.
        from ctypes import wintypes
        user = ctypes.WinDLL('user32')
        user.PostMessageW.argtypes = [wintypes.HWND, wintypes.UINT, wintypes.WPARAM, wintypes.LPARAM]
        hwnd = next(w['handle'] for w in info['windows'] if w['class'] == 'MyGoApp')
        assert user.PostMessageW(hwnd, 0x0016, 1, 0)
        assert user.PostMessageW(hwnd, 0x0012, 0, 0) # end this process's event loop after shutdown hooks
    else:
        process.send_signal(signal.SIGTERM)
    assert process.wait(timeout=20) == 0, 'Production process did not shut down normally'


def main():
    hosted()
    package, web, evidence = [pathlib.Path(p).resolve() for p in sys.argv[1:4]]
    evidence.mkdir(parents=True, exist_ok=True)
    helper = None
    metadata = {}
    if sys.platform == 'darwin':
        app = package / 'yesman.app'
        metadata = plistlib.loads((app / 'Contents/Info.plist').read_bytes())
        assert metadata['CFBundleExecutable'] == 'Yes Man', metadata
        for key in ('CFBundleName', 'CFBundleDisplayName'):
            assert metadata[key] == 'yesman', metadata # base matches the stable filename
        localized = plistlib.loads((app / 'Contents/Resources/en.lproj/InfoPlist.strings').read_bytes())
        assert localized['CFBundleName'] == localized['CFBundleDisplayName'] == 'Yes Man'
        metadata['localizedNames'] = localized
        assert metadata['CFBundleIdentifier'] == 'dev.idy.yesman'
        subprocess.run(['codesign', '--verify', '--deep', '--strict', str(app)], check=True)
        executable = app / 'Contents/MacOS/Yes Man'
        assert (app / 'Contents/MacOS/yesman').resolve() == executable
        helper = evidence / 'native-process-info'
        subprocess.run(['xcrun', 'swiftc', str(pathlib.Path(__file__).with_name('name-process-macos.swift')), '-o', str(helper)], check=True)
    else:
        executable = package / ('Yes Man.exe' if sys.platform == 'win32' else 'Yes Man')
    binary = executable.read_bytes()
    for name in ('index.html', 'app.js', 'dom.js', 'i18n.js', 'app.css', 'locales/zh-Hans.js'):
        assert (web / name).read_bytes() in binary, 'Frontend/binary mismatch: ' + name
    proof = {'binarySHA256': hashlib.sha256(binary).hexdigest(), 'metadata': metadata, 'runID': os.environ.get('GITHUB_RUN_ID'), 'sourceSHA': os.environ.get('GITHUB_SHA'), 'platform': sys.platform, 'phases': []}
    result, received, token = {}, threading.Event(), uuid.uuid4().hex
    class Collector(http.server.BaseHTTPRequestHandler):
        def log_message(self, *args): pass
        def do_CONNECT(self): self.send_error(403, 'External network disabled')
        def do_OPTIONS(self):
            self.send_response(204)
            self.send_header('Access-Control-Allow-Origin', '*')
            self.send_header('Access-Control-Allow-Methods', 'POST, OPTIONS')
            self.send_header('Access-Control-Allow-Headers', 'Content-Type')
            self.send_header('Access-Control-Allow-Private-Network', 'true')
            self.end_headers()
        def do_POST(self):
            assert self.path == '/' + token
            size = int(self.headers.get('Content-Length', '0'))
            assert size <= 131072
            result.update(json.loads(self.rfile.read(size)))
            self.send_response(200)
            self.send_header('Access-Control-Allow-Origin', '*')
            self.end_headers()
            received.set()
    server = http.server.ThreadingHTTPServer(('127.0.0.1', 0), Collector)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    endpoint = 'http://127.0.0.1:' + str(server.server_port)
    process = None
    try:
        with tempfile.TemporaryDirectory(prefix='Yes Man smoke ', dir=os.environ['RUNNER_TEMP'], ignore_cleanup_errors=True) as directory:
            temp = pathlib.Path(directory)
            observed = temp / 'web'
            shutil.copytree(web, observed)
            driver = '''<script>
window.addEventListener('DOMContentLoaded', async () => {
 const errors=[]; window.addEventListener('error', e=>errors.push(e.message));
 try {
  const end=Date.now()+30000;
  while(!document.querySelector('#settings-button')) { if(Date.now()>end) throw Error('page not ready'); await new Promise(r=>setTimeout(r,50)); }
  const state=await window.mygo.call('Yesman.State');
  await fetch(ENDPOINT,{method:'POST',body:JSON.stringify({ok:true,state,brand:document.querySelector('.mds-brand-name')?.textContent,title:document.title,visibility:document.visibilityState,errors})});
 } catch(e) { await fetch(ENDPOINT,{method:'POST',body:JSON.stringify({ok:false,error:String(e),errors})}); }
});</script>'''.replace('ENDPOINT', json.dumps(endpoint + '/' + token))
            index = observed / 'index.html'
            original = index.read_text()
            index.write_text(original.replace('<head>', '<head>' + driver, 1))
            assert index.read_text().replace(driver, '', 1) == original
            for phase in ('embedded', 'legacy-cli', 'production-page-with-observer'):
                data = temp / (phase + ' data')
                data.mkdir()
                env = {k: v for k, v in os.environ.items() if k in ('HOME', 'USER', 'LOGNAME', 'PATH', 'TMPDIR', 'LANG', 'LC_ALL', 'SHELL', 'SystemRoot', 'SYSTEMROOT', 'WINDIR', 'USERPROFILE', 'APPDATA', 'LOCALAPPDATA', 'TEMP', 'TMP', 'DISPLAY', 'XAUTHORITY', 'XDG_RUNTIME_DIR', 'DBUS_SESSION_BUS_ADDRESS')}
                env.update(YESMAN_HOME=str(data), YESMAN_PI=str(temp / 'missing-pi'), PI_CODING_AGENT_DIR=str(temp / 'pi-config'), HTTP_PROXY=endpoint, HTTPS_PROXY=endpoint, NO_PROXY='127.0.0.1,localhost,::1')
                if phase == 'production-page-with-observer': env['YESMAN_WEB_DIR'] = str(observed)
                with (evidence / (phase + '.log')).open('w') as log:
                    entry = executable
                    if phase == 'legacy-cli': entry = executable.with_name('yesman.exe' if sys.platform == 'win32' else 'yesman')
                    legacy_gui = sys.platform == 'win32' and phase == 'legacy-cli'
                    process = subprocess.Popen([str(entry)], cwd=temp, env=env,
                        stdin=subprocess.DEVNULL if legacy_gui else None,
                        stdout=subprocess.DEVNULL if legacy_gui else log,
                        stderr=subprocess.DEVNULL if legacy_gui else subprocess.STDOUT,
                        creationflags=subprocess.DETACHED_PROCESS if legacy_gui else 0)
                    if phase == 'legacy-cli' and sys.platform == 'win32':
                        assert process.wait(timeout=20) == 0, 'Compatibility launcher failed'
                        deadline = time.monotonic() + 30
                        while time.monotonic() < deadline:
                            ids = windows_pids(executable)
                            if ids:
                                assert len(ids) == 1, ids
                                process = WindowsChild(ids[0])
                                break
                            time.sleep(.2)
                        else: raise AssertionError('Compatibility launcher did not start the real image')
                    deadline, info = time.monotonic() + 60, {}
                    while time.monotonic() < deadline:
                        assert process.poll() is None, 'Production app exited before showing a window'
                        info = native(process.pid, helper)
                        if sys.platform == 'darwin': visible = any(w['onScreen'] and w['bounds'].get('Width', 0) > 100 for w in info['windows'])
                        elif sys.platform == 'win32': visible = any(w['visible'] and w['title'] == 'Yes Man' for w in info['windows'])
                        else: visible = '"Yes Man"' in info['windows']
                        if visible: break
                        time.sleep(.2)
                    else: raise AssertionError('No visible native window: ' + str(info))
                    assert info['processName'] == 'Yes Man', info
                    assert pathlib.Path(info['executable']).resolve() == executable.resolve(), info
                    if sys.platform == 'darwin':
                        assert info['localizedName'] == 'Yes Man' and info['bundleIdentifier'] == 'dev.idy.yesman', info
                        assert info['fileDisplayName'] in ('Yes Man', 'Yes Man.app'), info
                    if sys.platform == 'win32':
                        assert info['fileDescription'] == info['productName'] == 'Yes Man', info
                    if phase == 'embedded':
                        # A second launch through the legacy entry must forward
                        # to this PID under the same stable single-instance key.
                        legacy = executable.with_name('yesman.exe' if sys.platform == 'win32' else 'yesman')
                        gui = sys.platform == 'win32'
                        second = subprocess.Popen([str(legacy)], cwd=temp, env=env,
                            stdin=subprocess.DEVNULL if gui else None,
                            stdout=subprocess.DEVNULL if gui else log,
                            stderr=subprocess.DEVNULL if gui else subprocess.STDOUT,
                            creationflags=subprocess.DETACHED_PROCESS if gui else 0)
                        assert second.wait(timeout=20) == 0
                        assert process.poll() is None, 'Second launch replaced the running instance'
                        if sys.platform == 'win32':
                            end = time.monotonic() + 20
                            while time.monotonic() < end:
                                ids = windows_pids(executable)
                                if ids == [process.pid]: break
                                time.sleep(.2)
                            else: raise AssertionError('Duplicate application instance: ' + str(ids))
                        proof['singleInstanceLegacyEntry'] = True
                    if phase == 'production-page-with-observer':
                        assert received.wait(60), 'No actual webview/Go bridge result'
                        assert result.get('ok') and result['state']['home'] == str(data) and not result['state']['yesmen'] and not result['state']['agents'], result
                        assert result['brand'] == result['title'] == 'Yes Man' and not result['errors'] and result['visibility'] == 'visible', result
                        (evidence / 'page.json').write_text(json.dumps(result, indent=2))
                    quit_process(process, info)
                    if sys.platform == 'win32' and os.environ.get('YESMAN_NATIVE_INSTALL_CONTRACT') == '1':
                        import winreg
                        # Only the parent hosted NSIS fixture sets this marker;
                        # the production app receives no test environment flag.
                        with winreg.OpenKey(winreg.HKEY_CURRENT_USER, r'Software\Microsoft\Windows\CurrentVersion\Uninstall\dev.idy.yesman', 0, winreg.KEY_READ | winreg.KEY_SET_VALUE) as key:
                            label = winreg.QueryValueEx(key, 'DisplayName')[0]
                            if phase == 'embedded':
                                assert label == 'Yes Man'
                                winreg.SetValueEx(key, 'DisplayName', 0, winreg.REG_SZ, 'Custom installation label')
                            elif phase == 'legacy-cli':
                                assert label == 'Custom installation label', 'App overwrote a custom registration label'
                                winreg.SetValueEx(key, 'DisplayName', 0, winreg.REG_SZ, 'yesman')
                                proof['customInstalledLabelPreserved'] = True
                            else:
                                assert label == 'Yes Man'
                    proof['phases'].append({'phase': phase, 'invocation': 'detached GUI with NUL handles' if legacy_gui else 'direct main/Unix exec', 'pid': process.pid, 'native': info, 'exitCode': process.returncode})
                    process = None
            assert hashlib.sha256(executable.read_bytes()).hexdigest() == proof['binarySHA256']
            # The display name must not silently select a second empty profile.
            if sys.platform == 'win32':
                for key in ('APPDATA', 'LOCALAPPDATA'):
                    assert not (pathlib.Path(os.environ[key]) / 'Yes Man').exists()
            elif sys.platform == 'darwin':
                assert not (pathlib.Path.home() / 'Library/Application Support/Yes Man').exists()
            else:
                assert not (pathlib.Path.home() / '.config/Yes Man').exists()
                assert not (pathlib.Path.home() / '.cache/Yes Man').exists()
            proof['stableProfilePaths'] = True
            proof['passed'] = True
    finally:
        if process is not None and process.poll() is None:
            process.terminate()
            try: process.wait(timeout=10)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait()
        if 'temp' in locals():
            shutil.rmtree(temp, ignore_errors=True) # after stopping the owned process
        server.shutdown()
        server.server_close()
        (evidence / 'process-name.json').write_text(json.dumps(proof, indent=2))
    print('PASS: packaged main process/application name, visible window, real webview/Go State bridge, normal exit')


if __name__ == '__main__': main()
