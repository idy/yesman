#!/usr/bin/env python3
"""Inspect a built/downloaded macOS app without launching or changing it."""
import hashlib
import json
import pathlib
import plistlib
import re
import subprocess
import sys


def output(*args):
    return subprocess.check_output(args, stderr=subprocess.PIPE).decode()


def inspect(app):
    app = pathlib.Path(app).resolve()
    info = plistlib.loads((app / 'Contents/Info.plist').read_bytes())
    binary = app / 'Contents/MacOS' / info['CFBundleExecutable']
    subprocess.run(['codesign', '--verify', '--deep', '--strict', str(app)], check=True)
    entitlements = plistlib.loads(subprocess.check_output(['codesign', '-d', '--entitlements', '-', str(app)], stderr=subprocess.PIPE))
    assert entitlements == {key: True for key in (
        'com.apple.security.device.audio-input', 'com.apple.security.device.camera', 'com.apple.security.device.bluetooth')}, entitlements
    assert info['CFBundleIdentifier'] == 'dev.idy.yesman', info
    for key in ('NSBluetoothAlwaysUsageDescription', 'NSCameraUsageDescription', 'NSMicrophoneUsageDescription'):
        assert isinstance(info.get(key), str) and info[key].strip(), key
    assert not list((app / 'Contents/Resources').rglob('*.entitlements.plist')), 'Signing input must not ship as a runtime resource'
    load = output('otool', '-l', str(binary))
    minos = re.findall(r'^\s+minos (\S+)', load, re.M)
    sdk = re.findall(r'^\s+sdk (\S+)', load, re.M)
    assert minos and all(v == info['LSMinimumSystemVersion'] for v in minos), (minos, info)
    libraries = output('otool', '-L', str(binary))
    for name in ('Foundation', 'AppKit', 'AVFoundation', 'CoreBluetooth', 'UserNotifications', 'ApplicationServices'):
        assert name + '.framework' in libraries, name
    build = output('go', 'version', '-m', str(binary))
    assert 'CGO_ENABLED=1' in build and 'mygo_noinspector' in build, build
    return {
        'app': str(app), 'binarySHA256': hashlib.sha256(binary.read_bytes()).hexdigest(),
        'info': info, 'entitlements': entitlements, 'machOMinOS': minos, 'machOSDK': sdk,
        'libraries': libraries, 'goBuild': build,
        # Build-machine metadata belongs to the runner log. The Mach-O above is
        # authoritative for a downloaded draft, which may use a different SDK.
        'inspectionXcode': output('xcodebuild', '-version'),
        'inspectionSDK': output('xcrun', '--show-sdk-version'),
        'inspectionOS': output('sw_vers'),
    }


if __name__ == '__main__':
    result = inspect(sys.argv[1])
    pathlib.Path(sys.argv[2]).write_text(json.dumps(result, indent=2))
    print('Verified production bundle:', result['binarySHA256'], 'minOS', result['machOMinOS'], 'SDK', result['machOSDK'])
