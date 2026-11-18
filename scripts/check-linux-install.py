#!/usr/bin/env python3
"""Exercise the actual generated installer and gio desktop launch on hosted Linux."""
import hashlib
import json
import os
import pathlib
import shutil
import signal
import subprocess
import sys
import tempfile
import time

assert sys.platform == 'linux'
assert os.environ.get('GITHUB_ACTIONS') == 'true' and os.environ.get('RUNNER_ENVIRONMENT') == 'github-hosted'
package, web, evidence = [pathlib.Path(p).resolve() for p in sys.argv[1:4]]
evidence.mkdir(parents=True, exist_ok=True)
with tempfile.TemporaryDirectory(prefix='Yes Man install ', dir=os.environ['RUNNER_TEMP']) as directory:
    profile = pathlib.Path(directory) / 'Profile with spaces'
    profile.mkdir()
    env = {k:v for k,v in os.environ.items() if k in ('PATH','DISPLAY','XAUTHORITY','DBUS_SESSION_BUS_ADDRESS','XDG_RUNTIME_DIR','LANG')}
    env.update(HOME=str(profile), XDG_DATA_HOME=str(profile / '.local/share'), XDG_CONFIG_HOME=str(profile / '.config'), XDG_CACHE_HOME=str(profile / '.cache'), YESMAN_HOME=str(profile / 'workspace-vault-data'), YESMAN_PI=str(profile / 'missing-pi'), PI_CODING_AGENT_DIR=str(profile / 'pi-config'), HTTP_PROXY='http://127.0.0.1:9', HTTPS_PROXY='http://127.0.0.1:9')
    sentinel = profile / 'workspace-vault-session-sentinel'
    sentinel.write_text('preserved')
    archive = list(package.glob('yesman-*-linux-*.tar.gz'))
    assert len(archive) == 1
    subprocess.run(['sh',str(package/'install.sh'),str(archive[0])],env=env,check=True)
    install = profile / '.local/yesman.app'
    executable = install / 'Yes Man'
    assert executable.is_file() and (profile/'.local/bin/yesman').resolve() == install/'yesman'
    desktop = profile / '.local/share/applications/yesman.desktop'
    text = desktop.read_text()
    assert '\nName=Yes Man\n' in text and '\nExec="' + str(install/'yesman') + '"\n' in text, text
    assert hashlib.sha256(executable.read_bytes()).digest() == hashlib.sha256((package/'Yes Man').read_bytes()).digest()
    # Repeat the install to prove recognition of the stable old directory;
    # the script must not create a second display-name-based installation.
    subprocess.run(['sh',str(package/'install.sh'),str(archive[0])],env=env,check=True)
    assert not (profile/'.local/Yes Man.app').exists()
    subprocess.run(['gio','launch',str(desktop)],env=env,check=True)
    pid = None
    try:
        deadline = time.monotonic()+60
        while time.monotonic()<deadline:
            matches=[]
            for child in pathlib.Path('/proc').iterdir():
                if not child.name.isdigit(): continue
                try:
                    if (child/'exe').resolve() == executable: matches.append(int(child.name))
                except (OSError,RuntimeError): pass
            if matches:
                assert len(matches)==1,matches
                pid=matches[0]
                if '"Yes Man"' in subprocess.check_output(['xwininfo','-root','-tree'],text=True): break
            time.sleep(.2)
        else: raise AssertionError('Desktop entry did not launch a visible app')
        assert pathlib.Path('/proc',str(pid),'comm').read_text().strip()=='Yes Man'
        result={'desktop':text,'pid':pid,'processName':'Yes Man','executable':str(executable),'installRepeated':True,'cli':str(profile/'.local/bin/yesman')}
        os.kill(pid,signal.SIGTERM)
        deadline=time.monotonic()+20
        while pathlib.Path('/proc',str(pid)).exists() and time.monotonic()<deadline: time.sleep(.1)
        assert not pathlib.Path('/proc',str(pid)).exists(),'Desktop process remained after normal quit'
        pid=None
        subprocess.run(['sh',str(package/'install.sh'),'--uninstall'],env=env,check=True)
        assert not install.exists() and not desktop.exists() and not (profile/'.local/bin/yesman').is_symlink()
        assert sentinel.read_text()=='preserved'
        result.update(passed=True,sentinel='preserved',uninstalled=True)
        (evidence/'linux-install.json').write_text(json.dumps(result,indent=2))
    finally:
        if pid is not None:
            try:os.kill(pid,signal.SIGTERM)
            except ProcessLookupError:pass
print('PASS: generated installer, repeated install, CLI, quoted desktop launch, real comm, uninstall and data sentinel')
