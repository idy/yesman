#!/usr/bin/env python3
"""Inspect a built/downloaded macOS app without launching or changing it."""
import argparse
import hashlib
import json
import pathlib
import plistlib
import re
import subprocess


IDENTIFIER = 'dev.idy.yesman'
TEAM_ID = re.compile(r'[A-Z0-9]{10}')


def output(*args):
    return subprocess.check_output(args, stderr=subprocess.PIPE).decode()


def developer_id_requirement(team, identifier=None):
    """Apple's Developer ID Application policy for one team, evaluated by codesign."""
    assert TEAM_ID.fullmatch(team or ''), 'Expected a 10-character Apple Team ID'
    requirement = ('anchor apple generic and certificate 1[field.1.2.840.113635.100.6.2.6] exists'
                   ' and certificate leaf[field.1.2.840.113635.100.6.1.13] exists'
                   ' and certificate leaf[subject.OU] = "%s"' % team)
    return requirement + (' and identifier "%s"' % identifier if identifier else '')


def signature(path):
    """Describe the signature of path as codesign reports it, without judging it."""
    details = subprocess.run(['codesign', '-d', '--verbose=4', str(path)], capture_output=True)
    assert details.returncode == 0, 'codesign cannot describe the signature of ' + pathlib.Path(path).name
    text = details.stderr.decode() + details.stdout.decode()
    fields = {}
    for line in text.splitlines():
        key, sep, value = line.partition('=')
        if sep and key not in fields:
            fields[key] = value
    flags = re.search(r'\bflags=0x[0-9a-f]+\(([^)]*)\)', text)
    requirement = subprocess.run(['codesign', '-d', '-r-', str(path)], capture_output=True)
    # An implicit requirement, as ad hoc's cdhash one, prints as a comment.
    designated = re.search(r'^(# )?designated => (.+)$', requirement.stdout.decode() + requirement.stderr.decode(), re.M)
    return {
        'identifier': fields.get('Identifier'),
        'teamIdentifier': fields.get('TeamIdentifier'),
        'authorities': [line[len('Authority='):] for line in text.splitlines() if line.startswith('Authority=')],
        'adhoc': 'Signature=adhoc' in text,
        'flags': flags.group(1).split(',') if flags else [],
        # A secure timestamp prints Timestamp=; a signature without one prints Signed Time=.
        'secureTimestamp': fields.get('Timestamp'),
        'designatedRequirement': designated.group(2).strip() if designated else None,
        'implicitRequirement': bool(designated and designated.group(1)),
    }


def leaf_sha1(path, work):
    """SHA-1 of the signing certificate, as security find-identity names identities."""
    prefix = pathlib.Path(work) / (pathlib.Path(path).name + '-cert')
    extracted = subprocess.run(['codesign', '-d', '--extract-certificates=' + str(prefix), str(path)], capture_output=True)
    leaf = pathlib.Path(str(prefix) + '0')
    assert extracted.returncode == 0 and leaf.is_file(), 'Cannot read the signing certificate of ' + pathlib.Path(path).name
    return hashlib.sha1(leaf.read_bytes()).hexdigest().upper()


def check_developer_id(path, team, kind='app', identity=None, work=None):
    """Require a notarized, stapled Developer ID signature of team on an app or disk image.

    kind 'app' also requires the bundle identifier, the hardened runtime and a
    secure timestamp; identity, a certificate SHA-1, pins the exact leaf.
    """
    path = pathlib.Path(path)
    requirement = developer_id_requirement(team, IDENTIFIER if kind == 'app' else None)
    found = signature(path)
    assert not found['adhoc'], 'Expected a Developer ID signature, found an ad-hoc one: ' + path.name
    assert found['teamIdentifier'] == team, 'Signed by another team than the expected Developer ID team: ' + path.name
    assert subprocess.run(['codesign', '--verify', '--strict', '-R=' + requirement, str(path)], capture_output=True).returncode == 0, \
        'Signature does not satisfy the expected Developer ID Application requirement: ' + path.name
    if kind == 'app':
        assert subprocess.run(['codesign', '--verify', '--deep', '--strict', str(path)], capture_output=True).returncode == 0, 'Nested signature is invalid: ' + path.name
        assert found['identifier'] == IDENTIFIER, 'Unexpected signing identifier: ' + path.name
        assert 'runtime' in found['flags'], 'Hardened runtime is not enabled: ' + path.name
        assert found['secureTimestamp'], 'Signature has no secure timestamp: ' + path.name
        assert found['designatedRequirement'], 'No designated requirement: ' + path.name
    if identity:
        assert work, 'A scratch folder is needed to read the certificate'
        assert leaf_sha1(path, work) == identity.upper(), 'Signed with another certificate than the prepared identity: ' + path.name
    assert subprocess.run(['xcrun', 'stapler', 'validate', str(path)], capture_output=True).returncode == 0, 'No valid stapled notarization ticket: ' + path.name
    assess = ['spctl', '--assess', '--verbose=4'] + (['--type', 'execute'] if kind == 'app' else ['--type', 'open', '--context', 'context:primary-signature'])
    verdict = subprocess.run(assess + [str(path)], capture_output=True)
    text = (verdict.stdout + verdict.stderr).decode()
    assert verdict.returncode == 0 and 'source=Notarized Developer ID' in text, 'Gatekeeper does not accept it as notarized Developer ID: ' + path.name
    found['expectedTeam'] = team
    found['requirement'] = requirement
    found['gatekeeper'] = text.strip()
    return found


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
    parser.add_argument('--expect-team-id', help='require a notarized Developer ID signature of this team')


def inspect(app, tag=None, arch=None, source=None, expect_team=None):
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
    for name in ('Foundation', 'AppKit', 'AVFoundation', 'CoreBluetooth', 'UserNotifications', 'ApplicationServices'):
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
        'signature': signature(app),
    }
    if expect_team:
        bundle['signature'] = check_developer_id(app, expect_team)
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
    result = inspect(args.app, args.release_tag, args.arch, args.source, args.expect_team_id)
    pathlib.Path(args.output).write_text(json.dumps(result, indent=2))
    print('Verified production bundle:', result['binarySHA256'], 'minOS', result['machOMinOS'], 'SDK', result['machOSDK'])
