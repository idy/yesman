#!/usr/bin/env python3
"""Actual Windows GUI-subsystem CLI compatibility, old FAIL -> new PASS.

No native app loop is entered by the real-app error case: MYGO_GENERATE fails
before storage. The ordinary GUI case is checked by check-process-name.py.
"""
import ctypes
import hashlib
import json
import os
import pathlib
import re
import shutil
import subprocess
import sys
import tempfile
import time

OLD = '3d44faf7a547e5fb562312461da096f0dab73085'
PAYLOAD = b'stdin\x00raw\r\nUnicode \xe4\xb8\xad\xe6\x96\x87; quotes " \\ $(not-a-shell)\n'
STDERR = 'stderr: Unicode 中文; quotes "; slash \\\n'.encode()


def run(exe, env, cwd, args=()):
    p = subprocess.run([str(exe), *args], input=PAYLOAD, stdout=subprocess.PIPE, stderr=subprocess.PIPE, env=env, cwd=cwd, timeout=30)
    return {'exitCode':p.returncode,'stdoutHex':p.stdout.hex(),'stderrHex':p.stderr.hex()}


def wait_image(executable):
    # The intentionally broken old launcher exits before its real child.
    # Reap/wait that exact private image before deleting the fixture directory.
    quoted=str(executable).replace("'","''")
    deadline=time.monotonic()+15
    while time.monotonic()<deadline:
        ids=json.loads(subprocess.check_output(['powershell.exe','-NoProfile','-NonInteractive','-Command',"ConvertTo-Json -Compress -InputObject @(Get-CimInstance Win32_Process | Where-Object {$_.ExecutablePath -eq '"+quoted+"'} | Select-Object -ExpandProperty ProcessId)"],text=True))
        if not ids:return
        time.sleep(.1)
    raise AssertionError('Old counterexample child remained alive: '+str(executable))


def main():
    assert sys.platform == 'win32' and os.environ.get('GITHUB_ACTIONS') == 'true' and os.environ.get('RUNNER_ENVIRONMENT') == 'github-hosted'
    package, evidence = [pathlib.Path(p).resolve() for p in sys.argv[1:3]]
    evidence.mkdir(parents=True, exist_ok=True)
    proof = {'sourceSHA':os.environ['GITHUB_SHA'],'oldSource':OLD,'runID':os.environ['GITHUB_RUN_ID']}
    with tempfile.TemporaryDirectory(prefix='CLI 空格 ', dir=os.environ['RUNNER_TEMP']) as directory:
        temp = pathlib.Path(directory)
        env = {k:v for k,v in os.environ.items() if k in ('PATH','SystemRoot','SYSTEMROOT','WINDIR','TEMP','TMP','USERPROFILE','APPDATA','LOCALAPPDATA')}
        env.update(YESMAN_HOME=str(temp/'must remain absent'), YESMAN_PI=str(temp/'missing-pi'), PI_CODING_AGENT_DIR=str(temp/'pi-config'), YESMAN_CLI_SENTINEL='preserved 中文')
        cwd = temp/'working dir 中文';cwd.mkdir()
        old, new = temp/'old', temp/'new'
        old.mkdir();new.mkdir()
        hashes={}
        for name in ('main.go','launch_windows.go'):
            data=subprocess.check_output(['git','show',OLD+':tools/launcher/'+name])
            (old/name).write_bytes(data);hashes[name]=hashlib.sha256(data).hexdigest()
        subprocess.run(['go','build','-ldflags','-H=windowsgui','-o',str(old/'yesman.exe'),str(old/'main.go'),str(old/'launch_windows.go')],check=True)
        subprocess.run(['go','build','-o',str(new/'Yes Man.exe'),'./tools/launcher/testdata/stdio'],check=True)
        shutil.copy2(new/'Yes Man.exe',old/'Yes Man.exe')
        shutil.copy2(package/'yesman.exe',new/'yesman.exe')
        args=['', 'a b', '中文', 'quote"and\\', 'C:\\folder with spaces\\', '$(must-not-run)', 'line\nbreak']
        results={}
        for name,exe in [('direct',new/'Yes Man.exe'),('old',old/'yesman.exe'),('new',new/'yesman.exe')]:
            record=temp/(name+'.json');env['YESMAN_CLI_RECORD']=str(record)
            results[name]=run(exe,env,cwd,args)
            deadline=time.monotonic()+10
            while not record.exists() and time.monotonic()<deadline:time.sleep(.05)
            data=json.loads(record.read_text(encoding='utf-8'));assert data['Args']==args and data['Env']=='preserved 中文',(name,data)
            assert os.path.samefile(data['Cwd'],cwd)
            kernel=ctypes.WinDLL('kernel32',use_last_error=True)
            kernel.OpenProcess.restype=ctypes.c_void_p
            kernel.WaitForSingleObject.argtypes=[ctypes.c_void_p,ctypes.c_uint32]
            kernel.CloseHandle.argtypes=[ctypes.c_void_p]
            handle=kernel.OpenProcess(0x00100000,False,data['PID'])
            if handle:
                assert kernel.WaitForSingleObject(handle,10000)==0, 'Fixture child did not exit'
                kernel.CloseHandle(handle)
        assert results['direct']['exitCode']==results['new']['exitCode']==37
        assert results['direct']['stdoutHex']==results['new']['stdoutHex']==PAYLOAD.hex()
        assert results['direct']['stderrHex']==results['new']['stderrHex']==STDERR.hex()
        assert results['old']['exitCode']==0 and results['old']['stdoutHex']==results['old']['stderrHex']=='', results
        with (temp/'stdout.bin').open('wb') as out,(temp/'stderr.bin').open('wb') as err:
            files=subprocess.run([str(new/'yesman.exe'),*args],stdin=subprocess.DEVNULL,stdout=out,stderr=err,env=env,cwd=cwd,timeout=30)
        assert files.returncode==37 and (temp/'stdout.bin').read_bytes()==b'' and (temp/'stderr.bin').read_bytes()==STDERR
        proof['fileRedirectionExit']=files.returncode
        # The unmodified packaged real main must report an actual binding
        # generation failure with its nonzero status and diagnostic intact.
        for folder in (old,new):shutil.copy2(package/'Yes Man.exe',folder/'Yes Man.exe')
        blocked=temp/'not a directory';blocked.write_text('blocked')
        env['MYGO_GENERATE']=str(blocked/'client.ts')
        failures={name:run(exe,env,cwd) for name,exe in [('direct',new/'Yes Man.exe'),('old',old/'yesman.exe'),('new',new/'yesman.exe')]}
        assert failures['direct']['exitCode']==failures['new']['exitCode']!=0
        assert failures['old']['exitCode']==0 and failures['old']['stderrHex']==''
        normalize=lambda v:re.sub(rb'^\d{4}/\d{2}/\d{2} \d{2}:\d{2}:\d{2} ',b'',bytes.fromhex(v['stderrHex']))
        assert normalize(failures['direct'])==normalize(failures['new']) and normalize(failures['new'])
        assert not pathlib.Path(env['YESMAN_HOME']).exists()
        wait_image(old/'Yes Man.exe')
        proof.update(passed=True,originalBlobs=hashes,stdio=results,realBindingsFailure=failures,packagedLauncherSHA256=hashlib.sha256((package/'yesman.exe').read_bytes()).hexdigest(),packagedMainSHA256=hashlib.sha256((package/'Yes Man.exe').read_bytes()).hexdigest())
    (evidence/'windows-cli.json').write_text(json.dumps(proof,indent=2))
    print('PASS: original Windows launcher FAIL; actual packaged entry preserves stdin/stdout/stderr/exit37/argv/env/cwd and real app nonzero failure')


if __name__ == '__main__':main()
