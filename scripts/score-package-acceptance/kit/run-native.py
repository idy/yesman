#!/usr/bin/env python3
"""One native A→B private package replacement on a fresh hosted macOS ARM64 VM.

Never run on a workstation. Never use open/installer/lsregister, TCC APIs,
trust-store changes, credentials, or the updater. No --force/guard override.
"""
import argparse
import http.server
import json
import os
from pathlib import Path
import platform
import shutil
import signal
import subprocess
import sys
import threading
import time
import uuid
from contract import SOURCE, TREE, seed, check_files, check_native, sha

KIT=Path(__file__).resolve().parent

def require_hosted():
    if not (sys.platform=='darwin' and platform.machine()=='arm64'
            and os.environ.get('GITHUB_ACTIONS')=='true'
            and os.environ.get('RUNNER_ENVIRONMENT')=='github-hosted'
            and os.environ.get('RUNNER_OS')=='macOS'
            and os.environ.get('GITHUB_RUN_ID','').isdigit()):
        raise RuntimeError('Requires a disposable GitHub-hosted macOS ARM64 runner; refusing native launch here')

def main():
    require_hosted() # Before any filesystem mutation, helper build or application operation.
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('plan',type=Path)
    args=parser.parse_args()
    planpath=args.plan.resolve();root=planpath.parent;plan=json.loads(planpath.read_text())
    assert root.is_relative_to(Path(os.environ['RUNNER_TEMP']).resolve())
    assert plan['source']==SOURCE and plan['tree']==TREE and plan['built'] and plan['platform']=='darwin/arm64'
    for name,digest in plan['kitSHA256'].items():assert sha((KIT/name).read_bytes())==digest,name
    src=Path(plan['stages']['A']['sourceDirectory'])
    sys.path.insert(0,str(src/'scripts'))
    from check_macos_startup import require_hosted_runner
    from check_macos_bundle import inspect
    require_hosted_runner() # Original whole-user-profile/native-app refusal checks.
    assert subprocess.run(['pgrep','-x','Yes Man'],stdout=subprocess.DEVNULL).returncode==1
    work=root/'native-run';work.mkdir(mode=0o700)
    evidence=work/'evidence';evidence.mkdir()
    data=work/'data';seed(data)
    pi_config=work/'pi-config';pi_config.mkdir()
    install=work/'install';install.mkdir();target=install/'Yes Man.app'
    helper=work/'process-info'
    buildenv={k:os.environ[k] for k in ('PATH','LANG') if k in os.environ}
    buildenv.update(HOME=str(root/'home'),TMPDIR=str(root/'temp'),CLANG_MODULE_CACHE_PATH=str(root/'cache/clang'))
    subprocess.run(['xcrun','swiftc','-module-cache-path',str(root/'cache/swift'),str(src/'scripts/name-process-macos.swift'),'-o',str(helper)],env=buildenv,check=True,timeout=60)
    # Pin Python's executable in a private fixture launcher; no personal pi config.
    import shlex
    catalog=work/'catalog'
    catalog.write_text('#!/bin/sh\nexec '+shlex.quote(sys.executable)+' '+shlex.quote(str(KIT/'catalog.py'))+' "$@"\n')
    catalog.chmod(0o700)
    proof={'source':SOURCE,'tree':TREE,'runID':os.environ['GITHUB_RUN_ID'],'runAttempt':os.environ.get('GITHUB_RUN_ATTEMPT'),
           'runnerOS':os.environ['RUNNER_OS'],'runnerArch':platform.machine(),'runnerEnvironment':os.environ['RUNNER_ENVIRONMENT'],
           'planSHA256':sha(planpath.read_bytes()),'catalogSHA256':sha((KIT/'catalog.py').read_bytes()),
           'home':str(data),'install':str(target),'before':check_files(data),'stages':[],'passed':False}
    proc=None
    try:
        for stage in ('A','B'):
            spec=plan['stages'][stage];app=Path(spec['app']).resolve();assert app.is_relative_to(root)
            assert {str(p.relative_to(app)):sha(p.read_bytes()) for p in app.rglob('*') if p.is_file()}==spec['bundleFiles']
            if target.exists():shutil.rmtree(target) # Only this run's previous, normally exited private image.
            shutil.copytree(app,target,symlinks=True)
            inspected=inspect(target)
            executable=target/'Contents/MacOS'/inspected['info']['CFBundleExecutable']
            assert sha(executable.read_bytes())==spec['binarySHA256']
            (evidence/('bundle-'+stage+'.json')).write_text(json.dumps(inspected,indent=2)+'\n')
            web=Path(spec['sourceDirectory'])/'web';instrumented=work/('web-'+stage);shutil.copytree(web,instrumented)
            for path,digest in spec['webSources'].items():assert sha((Path(spec['sourceDirectory'])/path).read_bytes())==digest,path
            payload={};received=threading.Event();token=uuid.uuid4().hex
            class Collector(http.server.BaseHTTPRequestHandler):
                def log_message(self,*args):pass
                def do_OPTIONS(self):
                    self.send_response(204);self.send_header('Access-Control-Allow-Origin','*')
                    self.send_header('Access-Control-Allow-Methods','POST, OPTIONS')
                    self.send_header('Access-Control-Allow-Headers','Content-Type')
                    self.send_header('Access-Control-Allow-Private-Network','true');self.end_headers()
                def do_CONNECT(self):self.send_error(403,'Network disabled in score acceptance')
                def do_POST(self):
                    if self.path!='/'+token:self.send_error(404);return
                    length=int(self.headers.get('Content-Length','0'))
                    if not 0<length<=131072:self.send_error(413);return
                    try:value=json.loads(self.rfile.read(length))
                    except (ValueError,UnicodeError):self.send_error(400);return
                    payload.update(value);self.send_response(200)
                    self.send_header('Access-Control-Allow-Origin','*');self.end_headers();received.set()
            server=http.server.ThreadingHTTPServer(('127.0.0.1',0),Collector)
            thread=threading.Thread(target=server.serve_forever,daemon=True);thread.start()
            endpoint='http://127.0.0.1:'+str(server.server_port)
            driver='<script>'+ (KIT/'observer.js').read_text().replace('SCORE_ENDPOINT',json.dumps(endpoint+'/'+token))+'</script>'
            original=(instrumented/'index.html').read_text()
            (instrumented/'index.html').write_text(original.replace('<head>','<head>'+driver,1))
            assert (instrumented/'index.html').read_text().replace(driver,'',1)==original
            assert all((instrumented/p.relative_to(web)).read_bytes()==p.read_bytes() for p in web.rglob('*') if p.is_file() and p.name!='index.html')
            env={k:os.environ[k] for k in ('HOME','USER','LOGNAME','PATH','TMPDIR','SHELL') if k in os.environ}
            env.update(YESMAN_HOME=str(data),YESMAN_PI=str(catalog),PI_CODING_AGENT_DIR=str(pi_config),
                       YESMAN_WEB_DIR=str(instrumented),SCORE_CATALOG_LOG=str(evidence/'catalog.jsonl'),LANG='en_US.UTF-8',
                       HTTP_PROXY=endpoint,HTTPS_PROXY=endpoint,NO_PROXY='127.0.0.1,localhost,::1')
            try:
                with (evidence/('app-'+stage+'.log')).open('w') as log:
                    proc=subprocess.Popen([str(executable)],cwd=work,env=env,stdout=log,stderr=subprocess.STDOUT)
                    assert received.wait(45), 'No real WebKit/Go observer report'
                    assert proc.poll() is None, 'The launched app exited/forwarded to another instance'
                    (evidence/('page-'+stage+'.json')).write_text(json.dumps(payload,indent=2)+'\n')
                    check_native(payload,stage,data)
                    actual=json.loads(subprocess.check_output([str(helper),str(proc.pid)],env=env,text=True))
                    assert actual['bundleIdentifier']=='dev.idy.yesman' and actual['localizedName']=='Yes Man'
                    assert Path(actual['executable']).resolve()==executable.resolve()
                    assert any(w['onScreen'] and w['bounds'].get('Width',0)>100 for w in actual['windows'])
                    assert sha(Path(actual['executable']).read_bytes())==spec['binarySHA256']
                    files=check_files(data)
                    pid=proc.pid;proc.send_signal(signal.SIGTERM);code=proc.wait(timeout=15)
                    assert code==0,('abnormal exit',code)
                    assert not json.loads(subprocess.check_output([str(helper),str(pid)],env=env,text=True))['windows']
                    proc=None
                    assert sha(executable.read_bytes())==spec['binarySHA256']
                    check_files(data)
                    proof['stages'].append({'stage':stage,'pid':pid,'exitCode':code,'actualProcess':actual,
                        'imageSHA256':spec['binarySHA256'],'files':files,'crew':payload['crew'],'stateHome':payload['state']['home'],
                        'settingsFile':payload['settings']['settingsFile'],'rows':payload['rows']})
            finally:
                server.shutdown();server.server_close();thread.join(timeout=3)
            (evidence/'proof.json').write_text(json.dumps(proof,indent=2)+'\n')
        a,b=proof['stages'];assert a['pid']!=b['pid'] and a['imageSHA256']!=b['imageSHA256']
        assert a['files']==b['files']==proof['before']
        assert [s['crew']['members'][0]['score']['speed'] for s in proof['stages']]==[120,210]
        proof['passed']=True
    finally:
        if proc is not None and proc.poll() is None:
            proc.send_signal(signal.SIGTERM)
            try:proc.wait(timeout=5)
            except subprocess.TimeoutExpired:proc.kill();proc.wait()
        (evidence/'proof.json').write_text(json.dumps(proof,indent=2)+'\n')
    print('PASS: private installed native A→B images, same profile/CSV/settings, changed inherited speed 120→210')

if __name__=='__main__':main()
