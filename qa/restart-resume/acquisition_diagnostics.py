"""Small acquisition status only; never retain child text, URLs, env or files.
The two fixed diagnostics artifacts are never inputs to signing/binding/native.
"""
import json
import datetime
import platform
import os
from pathlib import Path
import re
import stat
import ssl
import subprocess
from urllib.error import URLError

PHASES = {'start', 'tool-version', 'metadata', 'asset', 'source-P', 'source-S', 'verify-source-P',
          'verify-source-S', 'npm-config', 'npm-ci', 'npm-integrity', 'finish'}
DETAILS = {'none', 'validation-failed', 'command-failed', 'command-timeout',
           'http-failed', 'transport-failed', 'config-double-load',
           'cli-output-guard', 'tls-failed', 'integrity-failed', 'npm-engine',
           'npm-network', 'npm-config', 'npm-missing-command', 'source-data-missing'}
RESOURCES = {'start':'entry', 'tool-version':'installed-tool-version', 'metadata':'github-api-metadata',
             'asset':'github-api-release-asset', 'source-P':'sealed-P-data', 'source-S':'fixed-S-codeload',
             'verify-source-P':'sealed-P-data', 'verify-source-S':'fixed-S-Git-objects',
             'npm-config':'private-empty-configs', 'npm-ci':'lock-pinned-public-npm',
             'npm-integrity':'lock-pinned-SHA512-content', 'finish':'complete'}
def now():return datetime.datetime.now(datetime.timezone.utc).isoformat()


def command_detail(stderr):
    """Classify in memory; return fixed vocabulary only, never child strings."""
    text = stderr[:65536].decode('utf-8', errors='replace')
    if 'double-loading config' in text: return 'config-double-load'
    if 'the response contains terminal escape sequences' in text: return 'cli-output-guard'
    if re.search(r'certificate verify|CERT_HAS_EXPIRED|UNABLE_TO_VERIFY|SELF_SIGNED', text, re.I): return 'tls-failed'
    if re.search(r'\bEINTEGRITY\b', text): return 'integrity-failed'
    if re.search(r'\bEBADENGINE\b', text): return 'npm-engine'
    if re.search(r'\b(ENOTFOUND|ECONNRESET|ECONNREFUSED|ETIMEDOUT|ETIMEOUT|EAI_AGAIN)\b', text): return 'npm-network'
    if re.search(r'\b(EUSAGE|EINVALIDCONFIG)\b', text): return 'npm-config'
    return 'command-failed'


def http_status(stderr):
    text = stderr[:65536].decode('utf-8', errors='replace')
    match = re.search(r'\bHTTP(?:/[0-9.]+)?[ :]+([1-5][0-9]{2})\b', text)
    return int(match[1]) if match else None


class Diagnostic:
    def __init__(self, role):
        if role not in {'download', 'public-npm'}: raise ValueError('unknown acquisition role')
        root = Path(os.environ['RUNNER_TEMP'])
        if not root.is_absolute() or not root.is_dir() or root.is_symlink(): raise ValueError('ordinary runner temp required')
        self.path = root / ('t199-' + role + '-diagnostics.json')
        self.fd = os.open(self.path, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
        self.row = {'schema': 1, 'role': role, 'state': 'started', 'phase': 'start', 'resource':'entry',
                    'startedAt':now(), 'updatedAt':now(), 'finishedAt':None,
                    'toolVersions':{'python':platform.python_version()},
                    'exitCode': None, 'httpStatus': None, 'detailCode': 'none',
                    'stdoutBytes': 0, 'stderrBytes': 0}
        self.write()

    def write(self):
        st = os.fstat(self.fd)
        if not stat.S_ISREG(st.st_mode) or st.st_nlink != 1: raise ValueError('ordinary diagnostic file required')
        self.row['updatedAt']=now()
        raw = (json.dumps(self.row, sort_keys=True) + '\n').encode()
        if len(raw) > 2048: raise ValueError('diagnostic budget')
        os.lseek(self.fd, 0, os.SEEK_SET); os.ftruncate(self.fd, 0)
        written = os.write(self.fd, raw)
        if written != len(raw): raise OSError('short diagnostic write')
        os.fsync(self.fd)

    def phase(self, phase):
        if phase not in PHASES: raise ValueError('unknown acquisition phase')
        self.row.update(state='running', phase=phase, resource=RESOURCES[phase], exitCode=None, httpStatus=None,
                        detailCode='none', stdoutBytes=0, stderrBytes=0)
        self.write()

    def api(self,endpoint):
        # Only canonical paths for this repository; never headers, redirects or queries.
        resource=endpoint.split('?',1)[0]
        if re.fullmatch(r'repos/idy/yesman/(?:releases(?:/[0-9]+(?:/assets)?|/assets/[0-9]+)?|git/(?:commits|trees)/[0-9a-f]{40}|git/ref/tags/v0\.1\.(?:48|49))',resource):
            self.row['resource']=resource;self.write()

    def command(self, result):
        code = result.returncode
        if not isinstance(code, int) or not -255 <= code <= 255: code = None
        self.row.update(exitCode=code, stdoutBytes=len(result.stdout), stderrBytes=len(result.stderr),
                        httpStatus=http_status(result.stderr),
                        detailCode='none' if result.returncode == 0 else command_detail(result.stderr))
        self.write()

    def version(self,tool,raw):
        if tool not in {'gh','npm'}:raise ValueError('unknown acquisition tool')
        match=re.match(rb'(?:gh version )?([0-9]{1,3}\.[0-9]{1,3}\.[0-9]{1,3})(?:\s|$)',raw[:128])
        if not match:raise ValueError('ordinary tool version required')
        self.row['toolVersions'][tool]=match[1].decode('ascii');self.write()

    def __enter__(self): return self

    def __exit__(self, kind, error, trace):
        try:
            self.row['finishedAt']=now()
            if kind is None:
                self.row.update(state='success', phase='finish', resource='complete', detailCode='none')
            else:
                self.row['state'] = 'failed'
                if isinstance(error, (subprocess.TimeoutExpired, TimeoutError)): self.row['detailCode'] = 'command-timeout'
                elif isinstance(error, URLError):
                    status = getattr(error, 'code', None)
                    if isinstance(status, int) and 100 <= status <= 599:
                        self.row.update(httpStatus=status, detailCode='http-failed')
                    else: self.row['detailCode'] = 'tls-failed' if isinstance(error.reason, ssl.SSLError) else 'transport-failed'
                elif isinstance(error, FileNotFoundError):
                    self.row['detailCode'] = ('source-data-missing' if self.row['phase']=='verify-source-P' else
                                              'npm-missing-command' if self.row['role']=='public-npm' else 'command-failed')
                elif self.row['detailCode'] == 'none':
                    status = getattr(error, 'code', None)
                    if isinstance(status, int) and 100 <= status <= 599:
                        self.row.update(httpStatus=status, detailCode='http-failed')
                    else: self.row['detailCode'] = 'validation-failed'
            if self.row['detailCode'] not in DETAILS: raise ValueError('unknown diagnostic code')
            self.write()
            print('T199_ACQUISITION_STATUS ' + json.dumps(self.row, sort_keys=True), flush=True)
        finally:
            os.close(self.fd)
        # Suppress no exception and print no arbitrary exception/child text.
        return False
