#!/usr/bin/env python3
"""Inspect a built/downloaded macOS app without launching or changing it."""
import argparse
import hashlib
import json
import pathlib
import plistlib
import re
import subprocess


def output(*args):
    return subprocess.check_output(args, stderr=subprocess.PIPE).decode()


def check_release(bundle, tag, arch, source):
    """Bind a downloaded binary to the requested tag, asset arch and exact commit."""
    assert re.fullmatch(r'v0\.1\.(0|[1-9][0-9]*)', tag), 'Expected a canonical v0.1.x release tag'
    assert arch in ('arm64', 'amd64'), 'Unexpected asset architecture'
    assert re.fullmatch(r'[0-9a-f]{40}', source), 'Expected the full tag commit'
    info = bundle['info']
    assert info['CFBundleShortVersionString'] == tag[1:] and info['CFBundleVersion'] == tag[1:], 'Bundle version differs from requested release'
    settings = {item['Key']: item['Value'] for item in bundle['goBuildInfo']['Settings']}
    assert settings.get('GOOS') == 'darwin', 'Binary GOOS differs from asset platform'
    assert settings.get('GOARCH') == arch, 'Binary GOARCH differs from asset architecture'
    assert bundle['machOArchitecture'] == {'arm64': 'arm64', 'amd64': 'x86_64'}[arch], 'Mach-O architecture differs from asset'
    assert settings.get('vcs.revision') == source and settings.get('vcs.modified') == 'false', 'Binary source differs from exact clean tag commit'
    bundle['expectedRelease'] = {'tag': tag, 'arch': arch, 'source': source}


def release_arguments(parser):
    parser.add_argument('--release-tag')
    parser.add_argument('--arch')
    parser.add_argument('--source')


def inspect(app, tag=None, arch=None, source=None):
    app = pathlib.Path(app).resolve()
    info = plistlib.loads((app / 'Contents/Info.plist').read_bytes())
    binary = app / 'Contents/MacOS' / info['CFBundleExecutable']
    subprocess.run(['codesign', '--verify', '--deep', '--strict', str(app)], check=True)
    entitlements = plistlib.loads(subprocess.check_output(['codesign', '-d', '--entitlements', ':-', str(app)], stderr=subprocess.PIPE))
    assert entitlements == {key: True for key in (
        'com.apple.security.device.audio-input', 'com.apple.security.device.camera', 'com.apple.security.device.bluetooth')}, entitlements
    assert info['CFBundleIdentifier'] == 'dev.idy.yesman', info
    for key in ('NSBluetoothAlwaysUsageDescription', 'NSCameraUsageDescription', 'NSMicrophoneUsageDescription'):
        assert isinstance(info.get(key), str) and info[key].strip(), key
    assert not list((app / 'Contents/Resources').rglob('*.entitlements.plist')), 'Signing input must not ship as a runtime resource'
    load = output('otool', '-l', str(binary))
    minos = re.findall(r'^\s+minos (\S+)', load, re.M)
    sdk = re.findall(r'^\s+sdk (\S+)', load, re.M)
    assert info['LSMinimumSystemVersion'] == '13.0', 'Unexpected deployment target'
    assert minos == ['13.0'] and sdk, (minos, sdk, info)
    libraries = output('otool', '-L', str(binary))
    for name in ('Foundation', 'AppKit', 'AVFoundation', 'CoreBluetooth', 'UserNotifications', 'ApplicationServices', 'Security'):
        assert name + '.framework' in libraries, name
    build = output('go', 'version', '-m', str(binary))
    assert 'CGO_ENABLED=1' in build and 'mygo_noinspector' in build, build
    bundle = {
        'app': str(app), 'binarySHA256': hashlib.sha256(binary.read_bytes()).hexdigest(),
        'info': info, 'entitlements': entitlements, 'machOMinOS': minos, 'machOSDK': sdk,
        'libraries': libraries, 'goBuild': build,
        'goBuildInfo': json.loads(output('go', 'version', '-m', '-json', str(binary))),
        'machOArchitecture': output('lipo', '-archs', str(binary)).strip(),
        # Build-machine metadata belongs to the runner log. The Mach-O above is
        # authoritative for a downloaded draft, which may use a different SDK.
        'inspectionXcode': output('xcodebuild', '-version'),
        'inspectionSDK': output('xcrun', '--show-sdk-version'),
        'inspectionOS': output('sw_vers'),
    }
    if any(value is not None for value in (tag, arch, source)):
        assert all(value is not None for value in (tag, arch, source)), 'Tag, architecture and source must be supplied together'
        check_release(bundle, tag, arch, source)
    return bundle


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('app')
    parser.add_argument('output')
    release_arguments(parser)
    args = parser.parse_args()
    result = inspect(args.app, args.release_tag, args.arch, args.source)
    pathlib.Path(args.output).write_text(json.dumps(result, indent=2))
    print('Verified production bundle:', result['binarySHA256'], 'minOS', result['machOMinOS'], 'SDK', result['machOSDK'])
