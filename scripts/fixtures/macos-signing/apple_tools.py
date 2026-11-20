#!/usr/bin/env python3
"""Stand-ins for security, codesign, xcrun, hdiutil, diskutil and spctl.

scripts/test_macos_signing.py puts shims for them first on PATH. They never
touch a real keychain, Apple service or disk image: each call is logged to
$APPLE_TOOLS_STATE/log.jsonl and answered from $APPLE_TOOLS_STATE/scenario.json.
Ad-hoc signing ("-") and calls this file does not model go to the real tool,
which only ever sees the test's temporary files.
"""
import hashlib
import json
import os
import pathlib
import shutil
import stat
import sys

state = pathlib.Path(os.environ['APPLE_TOOLS_STATE'])
scenario = json.loads((state / 'scenario.json').read_text())
tool, args = sys.argv[1], sys.argv[2:]
fails = set(scenario.get('fail', []))


def log(**extra):
    entry = {'tool': tool, 'args': args, 'env': sorted(k for k in os.environ if k.startswith(('MACOS_SIGNING_', 'MACOS_NOTARY_')))}
    entry.update(extra)
    with open(state / 'log.jsonl', 'a') as out:
        out.write(json.dumps(entry) + '\n')


def real(path):
    log(passthrough=True)
    os.execv(path, [path] + args)


def search():
    file = state / 'search.json'
    return json.loads(file.read_text()) if file.exists() else ['/Users/fixture/Library/Keychains/login.keychain-db']


def describe(path):
    """The signature the scenario gives path, by architecture and kind."""
    for key, value in scenario.get('signatures', {}).items():
        if key in path:
            return value
    return None


def fail(message, code=1):
    sys.stderr.write(message + '\n')
    sys.exit(code)


if tool == 'security':
    command = args[0]
    if command == 'list-keychains':
        if '-s' in args:
            (state / 'search.json').write_text(json.dumps(args[args.index('-s') + 1:]))
            log()
        else:
            log()
            print('\n'.join('    "%s"' % p for p in search()))
    elif command == 'create-keychain':
        log()
        if 'create-keychain' in fails:
            fail('security: SecKeychainCreate: failed')
        pathlib.Path(args[-1]).write_bytes(b'fixture keychain')
    elif command == 'import':
        p12 = pathlib.Path(args[1])
        log(p12={'mode': oct(stat.S_IMODE(p12.stat().st_mode)), 'folderMode': oct(stat.S_IMODE(p12.parent.stat().st_mode)),
                 'sha256': hashlib.sha256(p12.read_bytes()).hexdigest()})
        if 'import' in fails:
            # Real tools may echo what they were given; the caller must redact it.
            fail('security: SecKeychainItemImport: MAC verification failed during PKCS12 import (wrong password?) ' + args[args.index('-P') + 1])
    elif command == 'find-identity':
        log()
        found = scenario.get('identities', [])
        valid = [i for i in found if i.get('valid', True)]
        if '-v' in args:
            for n, i in enumerate(valid, 1):
                print('  %d) %s "%s"' % (n, i['sha1'], i['name']))
            print('     %d valid identities found' % len(valid))
        else:
            print('\nPolicy: Code Signing\n  Matching identities')
            for n, i in enumerate(found, 1):
                print('  %d) %s "%s"%s' % (n, i['sha1'], i['name'], '' if i.get('valid', True) else ' (CSSMERR_TP_NOT_TRUSTED)'))
            print('     %d identities found\n\n  Valid identities only' % len(found))
            for n, i in enumerate(valid, 1):
                print('  %d) %s "%s"' % (n, i['sha1'], i['name']))
            print('     %d valid identities found' % len(valid))
    elif command == 'delete-keychain':
        log()
        target = pathlib.Path(args[-1])
        if target.exists():
            target.unlink()
        (state / 'search.json').write_text(json.dumps([p for p in search() if p != str(target)]))
    else:
        log()  # set-keychain-settings, unlock-keychain, set-key-partition-list
        if command in fails:
            fail('security: ' + command + ' failed')

elif tool == 'codesign':
    if '--sign' in args or '-s' in args:
        identity = args[args.index('--sign' if '--sign' in args else '-s') + 1]
        if identity == '-':
            real('/usr/bin/codesign')
        target = args[-1]
        kind = 'dmg' if target.endswith('.dmg') else 'app' if target.endswith('.app') else 'probe'
        log(kind=kind)
        if kind + '-sign' in fails:
            fail('%s: errSecInternalComponent' % target)
        sys.exit(0)
    if args[:1] == ['--verify'] or args[:2] == ['--verify', '--strict'] or '--verify' in args:
        target = args[-1]
        requirement = next((a[3:] for a in args if a.startswith('-R=')), None)
        log(requirement=requirement)
        if target.endswith('probe.txt'):
            sys.exit(1 if 'probe-requirement' in fails else 0)
        found = describe(target)
        if found is None:
            real('/usr/bin/codesign')
        if requirement is None:
            sys.exit(0 if found.get('deepValid', True) else 1)
        if requirement.startswith('anchor apple generic'):
            sys.exit(0 if found.get('team') in requirement and found.get('developerID', True) else 3)
        sys.exit(0 if requirement in found.get('satisfies', [found.get('dr')]) else 3)
    if '-d' in args:
        target = args[-1]
        log()
        if target.endswith('probe.txt'):
            sys.stderr.write('Executable=%s\nIdentifier=probe\nTeamIdentifier=%s\n' % (target, scenario.get('probeTeam', '')))
            sys.exit(0)
        found = describe(target)
        if found is None:
            real('/usr/bin/codesign')
        extract = next((a.split('=', 1)[1] for a in args if a.startswith('--extract-certificates=')), None)
        if extract:
            pathlib.Path(extract + '0').write_bytes(found.get('leaf', 'leaf').encode())
        elif '-r-' in args:
            print('designated => ' + found.get('dr', ''))
        else:
            lines = ['Executable=' + target, 'Identifier=' + found.get('identifier', 'dev.idy.yesman')]
            lines.append('CodeDirectory v=20500 size=1 flags=%s hashes=1+7 location=embedded' % found.get('flags', '0x10000(runtime)'))
            if found.get('adhoc'):
                lines.append('Signature=adhoc')
            else:
                lines += ['Authority=Developer ID Application: Fixture Person (%s)' % found.get('team'), 'Authority=Developer ID Certification Authority', 'Authority=Apple Root CA']
                lines.append(('Timestamp=' if found.get('timestamp', True) else 'Signed Time=') + 'Nov 17, 2026 at 10:00:00')
            lines.append('TeamIdentifier=' + (found.get('team') or 'not set'))
            sys.stderr.write('\n'.join(lines) + '\n')
        sys.exit(0)
    real('/usr/bin/codesign')

elif tool == 'xcrun':
    if args[:2] == ['notarytool', 'store-credentials']:
        log()
        if 'store-credentials' in fails:
            fail('Error: HTTP status code: 401. Invalid credentials. Username or password is incorrect. (%s)' % args[args.index('--password') + 1])
    elif args[:2] == ['notarytool', 'submit']:
        log()
        status = 'Invalid' if 'notarize' in fails else 'Accepted'
        print(json.dumps({'id': '00000000-0000-4000-8000-000000000001', 'status': status, 'message': 'Processing complete'}))
    elif args[:2] == ['stapler', 'staple']:
        log()
        if 'staple' in fails:
            fail('The staple and validate action failed! Error 65.')
        target = pathlib.Path(args[-1])
        if target.is_dir():
            (target / 'Contents' / '.fixture-notarization-ticket').write_text('stapled\n')
        else:
            with open(target, 'ab') as out:
                out.write(b'fixture-notarization-ticket')
    elif args[:2] == ['stapler', 'validate']:
        log()
        found = describe(args[-1])
        sys.exit(0 if found and found.get('stapled', True) else 65)
    else:
        real('/usr/bin/xcrun')

elif tool == 'spctl':
    log()
    found = describe(args[-1])
    if found and found.get('gatekeeper', True):
        sys.stderr.write('%s: accepted\nsource=Notarized Developer ID\n' % args[-1])
        sys.exit(0)
    sys.stderr.write('%s: rejected\nsource=Unnotarized Developer ID\n' % args[-1])
    sys.exit(3)

elif tool == 'hdiutil':
    log()
    if args[0] == 'create':
        pathlib.Path(args[-1]).write_bytes(b'fixture read-write image of ' + args[args.index('-srcfolder') + 1].encode())
    elif args[0] == 'attach':
        print('/dev/disk99          \tGUID_partition_scheme\n/dev/disk99s1       \tApple_HFS                      \t' + args[args.index('-mountpoint') + 1])

elif tool == 'diskutil':
    log()
    if args[-1] != '-h':
        shutil.copyfile(args[-2], args[-1])

else:
    fail('unknown fixture tool ' + tool)
