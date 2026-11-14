"""Ephemeral transport for untouched released binaries. Hosted VM only.

This CA is a transport-test CA, not the updater signing key. Only exact cached
upgrade inputs are served. The application still verifies its original Ed25519
archive/delta key; neither binary nor signed payload is patched.
"""
import contextlib
import hashlib
import os
import pathlib
import shutil
import ssl
import subprocess
import sys
import tempfile

ALLOWED={'github.com','api.github.com','objects.githubusercontent.com','release-assets.githubusercontent.com'}

def make_context(directory):
    root=pathlib.Path(directory);root.mkdir(parents=True,exist_ok=True)
    key=root/'transport-key.pem';cert=root/'transport-cert.pem'
    names=','.join('DNS:'+h for h in sorted(ALLOWED))
    subprocess.run(['openssl','req','-x509','-newkey','rsa:2048','-nodes','-keyout',str(key),'-out',str(cert),'-days','1','-subj','/CN=Yes Man isolated upgrade transport','-addext','subjectAltName='+names],stdout=subprocess.DEVNULL,stderr=subprocess.PIPE,check=True)
    key.chmod(0o600)
    context=ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER);context.set_alpn_protocols(['http/1.1']);context.load_cert_chain(cert,key)
    return context,cert

@contextlib.contextmanager
def trusted_transport():
    assert os.environ.get('GITHUB_ACTIONS')=='true' and os.environ.get('RUNNER_ENVIRONMENT')=='github-hosted'
    with tempfile.TemporaryDirectory(prefix='upgrade-transport-',dir=os.environ['RUNNER_TEMP']) as d:
        context,cert=make_context(d)
        der=ssl.PEM_cert_to_DER_cert(cert.read_text(encoding='ascii'));fingerprint=hashlib.sha1(der).hexdigest()
        try:
            if sys.platform=='darwin':
                # Fresh disposable VM admin trust domain, never TCC/Grant or
                # a workstation login keychain. Remove this exact cert below.
                subprocess.run(['sudo','security','add-trusted-cert','-d','-r','trustRoot','-k','/Library/Keychains/System.keychain',str(cert)],check=True,stdout=subprocess.DEVNULL)
            elif sys.platform=='win32':
                subprocess.run(['certutil','-user','-addstore','-f','Root',str(cert)],check=True,stdout=subprocess.DEVNULL)
            yield context,str(cert),fingerprint
        finally:
            if sys.platform=='darwin':subprocess.run(['sudo','security','delete-certificate','-Z',fingerprint,'/Library/Keychains/System.keychain'],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
            elif sys.platform=='win32':subprocess.run(['certutil','-user','-delstore','Root',fingerprint],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
