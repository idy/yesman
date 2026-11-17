#!/usr/bin/env python3
"""Trusted packaging step only: private QA successors, never formal releases.
Run from the workflow's own checkout with the candidate checkout as cwd.
MYGO_UPDATER_PRIVATE_KEY leaves the environment before the first subprocess.
Only the two signers get it: each MyGo build through a short-lived private
key file, and the precompiled upgrade-assets helper's own execution. go env,
git, Go/cgo compilers and the helper's compilation never see it.
Every subprocess runs in one owned process group (POSIX) or job (Windows);
nothing handed to a consumer is removed before that owner is confirmed empty.
The native/runtime jobs receive only the resulting public artifacts/plan.
"""
import base64,contextlib,hashlib,io,json,os,pathlib,shutil,signal,stat,subprocess,sys,tarfile,tempfile,threading,time

KEY='MYGO_UPDATER_PRIVATE_KEY'
PRODUCER=pathlib.Path(__file__).resolve().parents[1]
HELPER='tools/package/upgrade_assets.go.txt'
WINDOWS=os.name=='nt'

class Undrained(RuntimeError):
    """Processes that may still use owned resources were not confirmed stopped."""

class CleanupIncomplete(RuntimeError):
    """An owned resource could not be removed, or its pathname now names something else."""

class Stopped(RuntimeError):
    pass

class _Stop:
    signum=None
    group=None

STOP=_Stop()

def _on_signal(signum,frame):
    # Never raises: an exception here could land between fork and exec, or
    # between creating a Windows child and putting it in the job.
    first=STOP.signum is None
    if first:STOP.signum=signum
    group=STOP.group
    if group is not None:
        try:group.signal_stop(escalate=not first)
        except Exception as error:  # recorded; the blocked wait then stops waiting
            with contextlib.suppress(Exception):group.signal_error=group.signal_error or 'stop request failed: %r'%(error,)

def check_stop():
    if STOP.signum is not None:raise Stopped('stopped by signal %d'%STOP.signum)

def _install_handlers():
    names=['SIGINT','SIGBREAK'] if WINDOWS else ['SIGTERM','SIGINT','SIGHUP']
    previous={}
    for name in names:
        number=getattr(signal,name,None)
        if number is not None:previous[number]=signal.signal(number,_on_signal)
    return previous

def _identity(st):return (st.st_dev,st.st_ino)

if WINDOWS:
    import ctypes
    from ctypes import wintypes
    _k32=ctypes.WinDLL('kernel32',use_last_error=True)
    _ntdll=ctypes.WinDLL('ntdll')
    class _Basic(ctypes.Structure):
        _fields_=[('PerProcessUserTimeLimit',ctypes.c_int64),('PerJobUserTimeLimit',ctypes.c_int64),('LimitFlags',wintypes.DWORD),('MinimumWorkingSetSize',ctypes.c_size_t),('MaximumWorkingSetSize',ctypes.c_size_t),('ActiveProcessLimit',wintypes.DWORD),('Affinity',ctypes.c_size_t),('PriorityClass',wintypes.DWORD),('SchedulingClass',wintypes.DWORD)]
    class _IoCounters(ctypes.Structure):
        _fields_=[(n,ctypes.c_ulonglong) for n in ('ReadOperationCount','WriteOperationCount','OtherOperationCount','ReadTransferCount','WriteTransferCount','OtherTransferCount')]
    class _Extended(ctypes.Structure):
        _fields_=[('BasicLimitInformation',_Basic),('IoInfo',_IoCounters),('ProcessMemoryLimit',ctypes.c_size_t),('JobMemoryLimit',ctypes.c_size_t),('PeakProcessMemoryUsed',ctypes.c_size_t),('PeakJobMemoryUsed',ctypes.c_size_t)]
    class _Accounting(ctypes.Structure):
        _fields_=[('TotalUserTime',ctypes.c_int64),('TotalKernelTime',ctypes.c_int64),('ThisPeriodTotalUserTime',ctypes.c_int64),('ThisPeriodTotalKernelTime',ctypes.c_int64),('TotalPageFaultCount',wintypes.DWORD),('TotalProcesses',wintypes.DWORD),('ActiveProcesses',wintypes.DWORD),('TotalTerminatedProcesses',wintypes.DWORD)]
    _k32.CreateJobObjectW.argtypes=[wintypes.LPVOID,wintypes.LPCWSTR];_k32.CreateJobObjectW.restype=wintypes.HANDLE
    _k32.SetInformationJobObject.argtypes=[wintypes.HANDLE,ctypes.c_int,wintypes.LPVOID,wintypes.DWORD];_k32.SetInformationJobObject.restype=wintypes.BOOL
    _k32.AssignProcessToJobObject.argtypes=[wintypes.HANDLE,wintypes.HANDLE];_k32.AssignProcessToJobObject.restype=wintypes.BOOL
    _k32.TerminateJobObject.argtypes=[wintypes.HANDLE,wintypes.UINT];_k32.TerminateJobObject.restype=wintypes.BOOL
    _k32.QueryInformationJobObject.argtypes=[wintypes.HANDLE,ctypes.c_int,wintypes.LPVOID,wintypes.DWORD,ctypes.POINTER(wintypes.DWORD)];_k32.QueryInformationJobObject.restype=wintypes.BOOL
    _k32.TerminateProcess.argtypes=[wintypes.HANDLE,wintypes.UINT];_k32.TerminateProcess.restype=wintypes.BOOL
    _k32.CloseHandle.argtypes=[wintypes.HANDLE];_k32.CloseHandle.restype=wintypes.BOOL
    _ntdll.NtResumeProcess.argtypes=[wintypes.HANDLE];_ntdll.NtResumeProcess.restype=ctypes.c_long

class OwnedGroup:
    """Owner of every subprocess of one producer run (see docs/production-upgrade.md)."""
    GRACE=5.0
    QUIET=2.0
    POLL=0.2
    # First letters of process states ps reports on Linux and macOS.
    STATES=frozenset('DIKLPRSTUWXZt')

    def __init__(self):
        self.closed=False;self.failed=None;self.retained=[];self.timer=None;self.waiting=[]
        self.stop_at=None;self.signal_error=None
        if WINDOWS:
            self.job=_k32.CreateJobObjectW(None,None)
            if not self.job:raise ctypes.WinError(ctypes.get_last_error())
            info=_Extended();info.BasicLimitInformation.LimitFlags=0x2000  # JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
            if not _k32.SetInformationJobObject(self.job,9,ctypes.byref(info),ctypes.sizeof(info)):
                error=ctypes.WinError(ctypes.get_last_error());_k32.CloseHandle(self.job);raise error
            self.ident='job %#x in producer %d'%(self.job,os.getpid())
        else:
            # The anchor leads the group and lives until the group is confirmed
            # empty, so the group ID cannot be reused while it is signalled.
            self.anchor=subprocess.Popen([sys.executable,'-I','-S','-c','import os\ntry:os.read(0,1)\nexcept BaseException:pass'],stdin=subprocess.PIPE,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL,env={},process_group=0)
            self.pgid=self.anchor.pid
            if os.getpgid(self.pgid)!=self.pgid:
                self.anchor.kill();self.anchor.wait();raise RuntimeError('anchor did not lead its own process group')
            self.ident='process group %d in producer %d'%(self.pgid,os.getpid())

    # -- membership --------------------------------------------------------
    def live(self):
        """Live (non-zombie) members including the POSIX anchor; None when that cannot be told.
        One malformed, duplicate or missing row makes the whole listing unknown: a
        member is never treated as absent because its row could not be read."""
        if WINDOWS:
            info=_Accounting();size=wintypes.DWORD()
            if not _k32.QueryInformationJobObject(self.job,1,ctypes.byref(info),ctypes.sizeof(info),ctypes.byref(size)):return None
            return list(range(info.ActiveProcesses))
        try:
            listing=subprocess.run(['/bin/ps','-A','-o','pid=,pgid=,stat='],capture_output=True,stdin=subprocess.DEVNULL,env={'PATH':'/usr/bin:/bin','LC_ALL':'C'},timeout=15)
        except (OSError,subprocess.SubprocessError):return None
        if listing.returncode!=0:return None
        rows={}
        for line in listing.stdout.decode(errors='replace').splitlines():
            if not line.strip():continue
            fields=line.split()
            if len(fields)!=3 or not all(f.isascii() and f.isdigit() for f in fields[:2]) or fields[2][0] not in self.STATES:return None
            pid=int(fields[0])
            if pid in rows:return None
            rows[pid]=(int(fields[1]),fields[2])
        # A complete listing shows this producer and the (unreaped) anchor.
        if os.getpid() not in rows or rows.get(self.pgid,(None,''))[0]!=self.pgid:return None
        return [pid for pid,(group,state) in rows.items() if group==self.pgid and state[0] not in 'ZX']

    def _quiet(self,live):
        """Only the anchor (POSIX) or nothing (Windows) remains."""
        return live==[] if WINDOWS else live==[self.pgid]

    def _kill(self,number):
        """Signals the group; never raises. Returns None when sent (or nothing is left), else why not.
        macOS refuses (EPERM) a group of only exited (zombie) members, so a failure means
        stopped only if a complete membership listing then shows no live member."""
        try:os.killpg(self.pgid,number)
        except ProcessLookupError:return None
        except Exception as error:return '%s to %s failed: %s'%(signal.Signals(number).name,self.ident,error)
        return None

    def signal_stop(self,escalate):
        """From a signal handler: TERM now and KILL after GRACE (a further signal: KILL now).
        A failure is recorded; the blocked wait sees it and stops waiting."""
        if self.closed:return
        if self.stop_at is None:self.stop_at=time.monotonic()
        if WINDOWS:
            if not _k32.TerminateJobObject(self.job,1):self.signal_error=self.signal_error or 'TerminateJobObject failed: %d'%ctypes.get_last_error()
            return
        error=self._kill(signal.SIGKILL if escalate else signal.SIGTERM)
        if error:self.signal_error=self.signal_error or error
        if self.timer is None:
            # A consumer that ignores TERM must not keep the blocked wait (and
            # so the drain and cleanup) from ever happening.
            self.timer=threading.Timer(self.GRACE,self._escalate);self.timer.daemon=True;self.timer.start()

    def _escalate(self):
        if not self.closed:
            error=self._kill(signal.SIGKILL)
            if error and not self.closed:self.signal_error=self.signal_error or error

    def _release(self):
        """Marks the owner closed; afterwards no timer can signal the group ID."""
        self.closed=True
        if self.timer is not None:self.timer.cancel();self.timer.join()

    # -- running -----------------------------------------------------------
    def run(self,args,*,cwd=None,env=None,stdout=None,stderr=None,capture=False,timeout=None):
        """Runs args inside the group; check=True. Returns stdout bytes when capture."""
        if self.closed:raise RuntimeError('owned process group already closed')
        check_stop()
        env=dict(os.environ) if env is None else env
        place={} if cwd is None else {'cwd':cwd}
        out=subprocess.PIPE if capture else stdout
        if WINDOWS:p=self._launch_windows(args,cwd,env,out,stderr)
        else:p=subprocess.Popen(args,**place,env=env,stdin=subprocess.DEVNULL,stdout=out,stderr=stderr,process_group=self.pgid)
        output=self._wait(p,args,timeout)
        if p.returncode:raise subprocess.CalledProcessError(p.returncode,args,output)
        self.settle(aborting=False)
        check_stop()
        return output if capture else None

    def _abandon(self):
        """Why a stopped run must stop waiting for its consumer, or None."""
        if STOP.signum is None:return None
        if self.signal_error:return 'stopped by signal %d; %s'%(STOP.signum,self.signal_error)
        if self.stop_at is not None and time.monotonic()-self.stop_at>=3*self.GRACE:
            return 'stopped by signal %d; a consumer was still running %.0fs later'%(STOP.signum,3*self.GRACE)
        return None

    def _wait(self,p,args,timeout):
        """Bounded, interruptible wait: short waits (a Windows wait is not interrupted by the
        console-control handler, and a failed stop request must not block forever).
        The settle/drain that follows decides whether members are gone. An abandoned
        wait keeps its pipe open: on Windows a reader thread may still hold it, so it
        is closed only after the drain confirms the owner is empty."""
        self.waiting.append(p)
        deadline=None if timeout is None else time.monotonic()+timeout
        while True:
            try:output,_=p.communicate(timeout=self.POLL);break
            except subprocess.TimeoutExpired:pass
            reason=self._abandon()
            if reason:raise Stopped(reason)
            if deadline is not None and time.monotonic()>=deadline:raise subprocess.TimeoutExpired(args,timeout)
        with contextlib.suppress(ValueError):self.waiting.remove(p)
        return output

    def _reap_waiting(self):
        """After a confirmed drain: reap abandoned consumers and close their pipes."""
        for p in self.waiting:
            with contextlib.suppress(subprocess.TimeoutExpired):p.wait(timeout=10)
            if p.stdout:
                with contextlib.suppress(OSError):p.stdout.close()
        self.waiting=[]

    def _launch_windows(self,args,cwd,env,stdout,stderr):
        p=subprocess.Popen(args,cwd=cwd,env=env,stdin=subprocess.DEVNULL,stdout=stdout,stderr=stderr,creationflags=0x4)  # CREATE_SUSPENDED
        try:
            if not _k32.AssignProcessToJobObject(self.job,int(p._handle)):raise ctypes.WinError(ctypes.get_last_error())
            check_stop()
            status=_ntdll.NtResumeProcess(int(p._handle))
            if status!=0:raise OSError('NtResumeProcess failed: %#x'%(status&0xffffffff))
        except BaseException:
            _k32.TerminateProcess(int(p._handle),1);p.wait()
            if p.stdout:p.stdout.close()
            raise
        return p

    # -- settling ----------------------------------------------------------
    def settle(self,aborting):
        """Normal: only the anchor may remain. Aborting: stop everything. Raises Undrained when unconfirmed."""
        if self.closed:return
        if self.failed:raise Undrained(self.failed)
        if not aborting:
            end=time.monotonic()+self.QUIET
            while True:
                live=self.live()
                if live is not None and self._quiet(live):return
                if time.monotonic()>=end:break
                time.sleep(0.05)
            self.drain()
            raise RuntimeError('%s was not quiet after a subprocess ended (%s); it was stopped'%(self.ident,'members unknown' if live is None else 'live %s'%live))
        self.drain()

    def drain(self):
        """Stops every member and confirms the owner is empty; only then is it closed."""
        if self.closed:return
        if self.failed:raise Undrained(self.failed)
        live=None
        error=None
        if WINDOWS:
            if not _k32.TerminateJobObject(self.job,1):error='TerminateJobObject failed: %d'%ctypes.get_last_error()
            end=time.monotonic()+2*self.GRACE
            while True:
                live=self.live()
                if live==[]:
                    self._release();_k32.CloseHandle(self.job)
                    self._reap_waiting();return
                if error and live is not None:
                    self.failed='%s: %d live members not stopped (%s)'%(self.ident,len(live),error)
                    raise Undrained(self.failed)
                if time.monotonic()>=end:break
                time.sleep(0.05)
        else:
            for number in (signal.SIGTERM,signal.SIGKILL):
                end=time.monotonic()+self.GRACE
                while True:
                    error=self._kill(number)
                    live=self.live()
                    if live==[]:
                        # The anchor was kept unreaped until now, so the group ID stayed ours.
                        self._release();self.anchor.wait(timeout=10);self.anchor.stdin.close()
                        self._reap_waiting();return
                    if error and live is not None:
                        # Live members that cannot be signalled: waiting would not stop them.
                        self.failed='%s: live members %s not stopped (%s)'%(self.ident,live,error)
                        raise Undrained(self.failed)
                    if time.monotonic()>=end:break
                    time.sleep(0.05)
        self.failed='%s: %s not confirmed stopped%s'%(self.ident,'members unknown' if live is None else 'live members %s'%live,' (%s)'%error if error else '')
        raise Undrained(self.failed)

    def close(self):
        """Normal end: release the anchor and confirm the group is empty."""
        if self.closed:return
        self.settle(aborting=False)
        if not WINDOWS:self.anchor.stdin.close()
        self.drain()

def _write_receipt(group,reason):
    record={'reason':reason,'owner':group.ident if group else None,'retained':group.retained if group else [],'instructions':'Processes of this owner were not confirmed stopped. After they have ended, remove each retained path whose dev/inode still matches.'}
    text=json.dumps(record,indent=2)
    print('prepare-upgrade-qa: resources retained:\n'+text,file=sys.stderr)
    with contextlib.suppress(OSError,KeyError):
        path=pathlib.Path(os.environ['RUNNER_TEMP'])/('qa-producer-retained-%d.json'%os.getpid())
        path.write_text(text+'\n',encoding='utf-8')

def _settled(group,aborting,resource,*fds):
    """Settles group before resource removal. On Undrained the resource is kept as it
    is (bytes included), recorded as retained, and only this process's handles closed."""
    if group is None:return
    try:group.settle(aborting)
    except Undrained:
        group.retained.append(resource)
        for fd in fds:
            if fd is not None:
                with contextlib.suppress(OSError):os.close(fd)
        raise

def _empty_fd(dfd):
    for entry in os.scandir(dfd):
        name=entry.name
        if entry.is_dir(follow_symlinks=False):
            sub=os.open(name,os.O_RDONLY|os.O_DIRECTORY|os.O_NOFOLLOW,dir_fd=dfd)
            try:_empty_fd(sub)
            finally:os.close(sub)
            os.rmdir(name,dir_fd=dfd)
        else:os.unlink(name,dir_fd=dfd)

class _OwnedDir:
    def __init__(self,prefix):
        self.path=tempfile.mkdtemp(prefix=prefix,dir=os.environ['RUNNER_TEMP'])
        self.ident=_identity(os.lstat(self.path));self.dfd=None
        if not WINDOWS:
            self.dfd=os.open(self.path,os.O_RDONLY|os.O_DIRECTORY|os.O_NOFOLLOW)
            if _identity(os.fstat(self.dfd))!=self.ident:
                os.close(self.dfd);raise CleanupIncomplete('%s changed while it was being opened'%self.path)

    def describe(self):return {'path':self.path,'dev':self.ident[0],'ino':self.ident[1]}

    def _names_us(self):
        try:st=os.lstat(self.path)
        except FileNotFoundError:return False
        return stat.S_ISDIR(st.st_mode) and _identity(st)==self.ident

    def remove_empty(self,problems):
        """Removes the directory only if empty and still at its pathname; never its other contents."""
        try:
            if self._names_us():
                try:os.rmdir(self.path)
                except OSError as error:problems.append('cannot remove %s: %s'%(self.path,error))
            else:problems.append('%s no longer names the created directory; its occupant was left untouched'%self.path)
        finally:
            if self.dfd is not None:os.close(self.dfd);self.dfd=None

    def remove_top(self,problems):
        if WINDOWS:
            if self._names_us():
                try:shutil.rmtree(self.path)
                except OSError as error:problems.append('cannot remove %s: %s'%(self.path,error))
            else:problems.append('%s no longer names the created directory; its occupant was left untouched'%self.path)
            return
        try:
            try:_empty_fd(self.dfd)
            except OSError as error:problems.append('cannot empty %s: %s'%(self.path,error))
            if self._names_us():
                try:os.rmdir(self.path)
                except OSError as error:problems.append('cannot remove %s: %s'%(self.path,error))
            else:problems.append('%s no longer names the created directory; the original was emptied through its held handle, its occupant was left untouched'%self.path)
        finally:
            os.close(self.dfd);self.dfd=None

@contextlib.contextmanager
def owned_dir(prefix,group=None):
    """A private directory under RUNNER_TEMP, removed by identity after group settles."""
    d=_OwnedDir(prefix);failed=False
    try:yield d.path
    except BaseException:failed=True;raise
    finally:
        try:_settled(group,failed,d.describe(),d.dfd)
        except Undrained:d.dfd=None;raise
        problems=[];d.remove_top(problems)
        if problems:raise CleanupIncomplete('; '.join(problems))

@contextlib.contextmanager
def key_file(key,group=None):
    """The key exists on disk only while one MyGo build signs, outside tree/output."""
    d=_OwnedDir('qa-updater-key-');name='mygo-update.key';path=os.path.join(d.path,name);failed=False
    flags=os.O_RDWR|os.O_CREAT|os.O_EXCL|getattr(os,'O_NOFOLLOW',0)|getattr(os,'O_BINARY',0)|getattr(os,'O_NOINHERIT',0)|getattr(os,'O_CLOEXEC',0)
    try:fd=os.open(name,flags,0o600,dir_fd=d.dfd) if d.dfd is not None else os.open(path,flags,0o600)
    except BaseException:
        problems=[];d.remove_empty(problems);raise
    fident=_identity(os.fstat(fd))
    try:
        os.write(fd,(key.strip()+'\n').encode('ascii'))
        yield path
    except BaseException:failed=True;raise
    finally:
        try:_settled(group,failed,{**d.describe(),'file':name},fd,d.dfd)
        except Undrained:d.dfd=None;raise
        problems=[]
        # Removes the key bytes from the created file wherever it now is.
        try:os.ftruncate(fd,0)
        except OSError as error:problems.append('cannot clear the key file: %s'%error)
        try:
            if d.dfd is not None:
                try:st=os.stat(name,dir_fd=d.dfd,follow_symlinks=False)
                except FileNotFoundError:st=None
                if st is not None and _identity(st)==fident:os.unlink(name,dir_fd=d.dfd)
                else:problems.append('%s no longer names the created key file; it was cleared through its held handle, any occupant was left untouched'%path)
                os.close(fd)
            else:
                os.close(fd)
                try:st=os.lstat(path)
                except FileNotFoundError:st=None
                if st is not None and _identity(st)==fident:os.unlink(path)
                else:problems.append('%s no longer names the created key file; it was cleared before closing, any occupant was left untouched'%path)
        except OSError as error:problems.append('cannot remove the key file: %s'%error)
        d.remove_empty(problems)
        if problems:raise CleanupIncomplete('; '.join(problems))

def check_key(key,public):
    # A Go Ed25519 private key is seed||public; MyGo and the helper also check it.
    raw=base64.b64decode(key.strip(),validate=True)
    assert len(raw)==64 and raw[32:]==base64.b64decode(public.strip(),validate=True),'updater key does not match the released public key'

def helper_blobs(group,source,producer=PRODUCER):
    """Candidate's repository blob, and the producer checkout's helper as Git cleans it (its own EOL rules)."""
    candidate=group.run(['git','rev-parse','--verify',source+':'+HELPER],capture=True).decode().strip()
    checkout=group.run(['git','hash-object','--path='+HELPER,'--',HELPER],cwd=producer,capture=True).decode().strip()
    return candidate,checkout

def main():
    # First, before any subprocess, assertion or exception path: remove the key.
    key=os.environ.pop(KEY,'')
    previous=_install_handlers()
    try:_produce(key)
    finally:
        STOP.group=None
        for number,handler in previous.items():signal.signal(number,handler)

def _produce(key):
    assert key.strip(),'signing-only job requires the existing configured key'
    assert not any(key.strip() in v for v in os.environ.values()),'updater key duplicated in another variable'
    assert os.environ.get('GITHUB_ACTIONS')=='true' and os.environ.get('RUNNER_ENVIRONMENT')=='github-hosted'
    inputs=pathlib.Path(sys.argv[1]).resolve();out=pathlib.Path(sys.argv[2]).resolve();out.mkdir(parents=True,exist_ok=True)
    meta=json.loads((inputs/'release-inputs.json').read_text(encoding='utf-8'));check_key(key,meta['PublicKey'])
    check_stop()
    group=OwnedGroup();STOP.group=group;failed=True
    try:
        _successors(group,key,inputs,out,meta)
        group.close();failed=False
    finally:
        try:
            if failed:group.settle(aborting=True)
        except Undrained as error:
            _write_receipt(group,str(error));raise
        finally:
            if STOP.signum is not None:raise SystemExit(128+STOP.signum)

def _successors(group,key,inputs,out,meta):
    target=group.run(['go','env','GOOS'],capture=True).decode().strip()+'-'+group.run(['go','env','GOARCH'],capture=True).decode().strip()
    source=group.run(['git','rev-parse','HEAD'],capture=True).decode().strip();assert source==meta['refs']['v0.1.39']
    # The helper is compiled by this producer; it must be the candidate's helper.
    candidate,checkout=helper_blobs(group,source)
    assert candidate==checkout,'producer and candidate upgrade-assets helpers differ'
    stages=[]
    for tag in ('v0.1.38','v0.1.39'):
        original=meta['stages'][tag+'/'+target];s=dict(original)
        for field in ('Archive','Manifest','Web'):
            dest=out/original[field];dest.parent.mkdir(parents=True,exist_ok=True)
            if field=='Web':shutil.copytree(inputs/original[field],dest,dirs_exist_ok=True)
            else:shutil.copy2(inputs/original[field],dest)
        # Preserve every signed released delta and its original manifest URL.
        m=json.loads((inputs/original['Manifest']).read_text(encoding='utf-8'))
        for d in m.get('deltas',[]):
            name=d['url'].rsplit('/',1)[-1];shutil.copy2(inputs/tag/name,out/name)
        stages.append(s)
    with owned_dir('private-qa-successor-',group) as directory:
        root=pathlib.Path(directory)
        for version in ('0.1.900002','0.1.900003'):
            check_stop()
            tree=root/version;tree.mkdir()
            with tarfile.open(fileobj=io.BytesIO(group.run(['git','archive',source],capture=True))) as archive:archive.extractall(tree,filter='data')
            before={str(p.relative_to(tree)):hashlib.sha256(p.read_bytes()).hexdigest() for p in tree.rglob('*.go')}
            cfg=json.loads((tree/'mygo.json').read_text(encoding='utf-8'));assert cfg['updates']['publicKey']==meta['PublicKey']
            cfg['version']=version;cfg['updates']['deltas']=0
            notes=root/(version+'.md');notes.write_text('## '+version+'\n\nPrivate QA successor; no tag/publication.\n',encoding='utf-8');cfg['updates']['changelog']=str(notes)
            env=dict(os.environ,CGO_ENABLED='1' if target.startswith('darwin-') else '0',MYGO_LDFLAGS='-X github.com/egoist/mygo.packageUpdateFeed=http://127.0.0.1:37059/update-'+target+'.json',MACOSX_DEPLOYMENT_TARGET='13.0',CGO_CFLAGS='-O2 -g -mmacosx-version-min=13.0')
            assert KEY not in env
            with key_file(key,group) as signer:
                cfg['updates']['privateKey']=signer  # read only by MyGo's signer, never by compilers
                (tree/'mygo.json').write_text(json.dumps(cfg,indent=2),encoding='utf-8')
                with (out/('qa-build-'+version+'.log')).open('w') as log:group.run(['go','tool','mygo','build','-platform',target.replace('-','/'),'-skip-dmg','-skip-installer','-o',str(out/version)],cwd=tree,env=env,stdout=log,stderr=subprocess.STDOUT)
            assert before=={str(p.relative_to(tree)):hashlib.sha256(p.read_bytes()).hexdigest() for p in tree.rglob('*.go')}
            package=out/version/target;binary=package/('yesman.app/Contents/MacOS/Yes Man' if target.startswith('darwin-') else 'Yes Man'+('.exe' if target.startswith('windows-') else ''))
            # MyGo skips signing without a key; never continue with an unsigned successor.
            archive=package/('yesman-'+version+'-'+target+'.tar.gz');signed=package/('update-'+target+'.json')
            assert archive.is_file() and signed.is_file(),'unsigned QA successor';manifest=json.loads(signed.read_text(encoding='utf-8'))
            assert manifest['version']==version and manifest['size']==archive.stat().st_size and manifest['signature'],'unsigned QA successor'
            web=out/version/'web';shutil.copytree(tree/'web',web)
            stages.append({'Version':version,'Source':source,'Archive':str((package/('yesman-'+version+'-'+target+'.tar.gz')).relative_to(out)),'Manifest':str((package/('update-'+target+'.json')).relative_to(out)),'Web':str(web.relative_to(out)),'mainName':'Yes Man','binarySHA256':hashlib.sha256(binary.read_bytes()).hexdigest(),'sourceGoHashes':before,'official':False,'limits':['private QA version, normal production build from exact candidate source','temporary packaging version/notes/linked loopback feed; no source change or formal tag','not a future released version']})
    plan={'kind':'released-candidate-plus-private-QA','PublicKey':meta['PublicKey'],'Feed':'http://127.0.0.1:37059','Target':target,'Stages':stages,'releases':meta['releases'],'refs':meta['refs'],'sourceSHA':source,'limits':['first edge uses untouched published .38 and downloaded draft .39 bytes','subsequent edges are normal production-source private QA successors, not .40 releases','released feed discovery is supplied by a constrained VM TLS transport; original Ed25519 verification remains active']}
    path=out/'plan.json';path.write_text(json.dumps(plan,indent=2),encoding='utf-8')
    with owned_dir('qa-upgrade-assets-',group) as helper_dir:
        helper=os.path.join(helper_dir,'yesman-upgrade-assets'+('.exe' if target.startswith('windows-') else ''))
        assert KEY not in os.environ
        group.run(['go','run','./tools/package','-build-upgrade-assets',helper],cwd=PRODUCER,env=dict(os.environ))
        group.run([helper,str(path)],env=dict(os.environ,**{KEY:key.strip()}))
        group.run([helper,str(path),'verify'],env=dict(os.environ))
    check_stop()
    print('PASS: immutable official pair preserved; private production-source QA successors signed separately; no private key in runtime artifacts')
if __name__=='__main__':main()
