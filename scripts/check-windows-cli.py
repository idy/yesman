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
OLD_TREE = '23616a61be3cfef5219d0806f1e4b88acd81b3ff'
OLD_FILES = {
    'main.go': ('e556d9cb99c345e39f191e80efd43be94be55953', 578),
    'launch_windows.go': ('c738f40539ba328f1276f47b02b5465e3d537b6d', 355),
}
OLD_REPOSITORY = 'https://github.com/idy/yesman.git'
PAYLOAD = b'stdin\x00raw\r\nUnicode \xe4\xb8\xad\xe6\x96\x87; quotes " \\ $(not-a-shell)\n'
STDERR = 'stderr: Unicode 中文; quotes "; slash \\\n'.encode()


def historical_launcher(directory, repository=OLD_REPOSITORY, revision=OLD, tree=OLD_TREE, files=OLD_FILES):
    """Fetch the pinned counterexample independently of checkout reachability.

    Optional identities are for self-owned synthetic Git tests; the hosted CLI
    path below always uses the literal historical defaults, never env/CLI refs.
    Nothing is read from or fetched into the application checkout's Git store.
    """
    def require(ok, message):
        if not ok:
            raise RuntimeError(message)

    require(not directory.exists(), 'Historical Git directory must be new')
    require(re.fullmatch('[0-9a-f]{40}', revision) and re.fullmatch('[0-9a-f]{40}', tree), 'Historical identity must use full object IDs')
    env = {k: v for k, v in os.environ.items() if k in (
        'PATH', 'SystemRoot', 'SYSTEMROOT', 'WINDIR', 'HOME', 'USERPROFILE', 'TMPDIR', 'TEMP', 'TMP')}
    env.update(HOME=str(directory.parent), USERPROFILE=str(directory.parent),
               GIT_CONFIG_NOSYSTEM='1', GIT_CONFIG_GLOBAL=os.devnull,
               GIT_TERMINAL_PROMPT='0', GIT_NO_REPLACE_OBJECTS='1')

    def git(*args):
        return subprocess.check_output([
            'git', '-c', 'credential.helper=', '-c', 'core.askPass=',
            '-c', 'protocol.allow=never', '-c', 'protocol.https.allow=always',
            '-c', 'protocol.file.allow=always', '-c', 'fetch.fsckObjects=true',
            '--git-dir=' + str(directory), *args,
        ], env=env, stderr=subprocess.PIPE, timeout=60)

    git('init', '--bare', str(directory))
    git('fetch', '--no-tags', '--depth=1', '--', repository, revision)
    require(git('rev-parse', '--verify', 'FETCH_HEAD^{commit}').decode().strip() == revision, 'Historical fetch returned the wrong commit')

    def object_bytes(oid, kind, maximum):
        require(git('cat-file', '-t', oid).decode().strip() == kind, 'Historical object has the wrong type')
        size = int(git('cat-file', '-s', oid))
        require(0 < size <= maximum, 'Historical object exceeds its byte bound')
        data = git('cat-file', kind, oid)
        require(len(data) == size and hashlib.sha1(kind.encode() + b' ' + str(size).encode() + b'\x00' + data).hexdigest() == oid, 'Historical object hash differs')
        return data

    commit = object_bytes(revision, 'commit', 16384)
    require(commit.split(b'\n', 1)[0] == b'tree ' + tree.encode(), 'Historical root tree differs')
    object_bytes(tree, 'tree', 65536)
    result = {}
    for name, (oid, size) in files.items():
        require(name in ('main.go', 'launch_windows.go') and re.fullmatch('[0-9a-f]{40}', oid) and 0 < size <= 16384, 'Invalid historical file identity')
        path = 'tools/launcher/' + name
        expected = '100644 blob ' + oid + '\t' + path + '\n'
        require(git('ls-tree', '-r', tree, '--', path).decode() == expected, 'Historical file path/mode/blob differs: ' + name)
        data = object_bytes(oid, 'blob', size)
        require(len(data) == size, 'Historical file size differs: ' + name)
        result[name] = data
    return result


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
        sources=historical_launcher(temp/'historical.git')
        proof['historicalSource']={'commit':OLD,'tree':OLD_TREE,'files':OLD_FILES}
        for name in ('main.go','launch_windows.go'):
            data=sources[name]
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
