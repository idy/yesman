#!/usr/bin/env python3
"""Exercise the actual workflow tag validator and signed-bundle release checks.

Optional args: APP TAG SOURCE ARCH REPORT. Inspections never launch the app.
"""
import json
import os
import pathlib
import subprocess
import sys
import tempfile
import textwrap

scripts = pathlib.Path(__file__).resolve().parent
workflow = (scripts.parent / '.github/workflows/macos-draft-smoke.yml').read_text()
# Execute the production pre-check verbatim, not a mirrored regex or grep test.
block = workflow.split('      - name: Validate release tag before resolving any ref\n', 1)[1].split('      - name: Download the exact draft archives\n', 1)[0]
code = textwrap.dedent(block.split("          python3 - <<'PY'\n", 1)[1].rsplit('          PY\n', 1)[0])
results = []
with tempfile.TemporaryDirectory(prefix='yesman-release-gate-') as temporary:
    output = pathlib.Path(temporary) / 'github-output'
    for tag, valid in [('v0.1.31', True), ('v0.1.0', True), ('main', False), ('refs/tags/v0.1.31', False), ('v0.2.1', False), ('v0.1.031', False), ('v0.1.31\nsource=evil', False), ('$(touch unintended)', False), ('', False)]:
        output.write_text('sentinel\n')
        env = dict(os.environ, RELEASE_TAG=tag, GITHUB_OUTPUT=str(output))
        process = subprocess.run([sys.executable, '-c', code], env=env, capture_output=True)
        assert (process.returncode == 0) == valid, (tag, process.stderr.decode())
        assert output.read_text() == 'sentinel\n' + ('tag=' + tag + '\n' if valid else ''), tag
        results.append({'case': 'tag-format', 'tag': tag, 'accepted': valid})
    if len(sys.argv) > 1:
        app, tag, source, arch, report = sys.argv[1:]
        for name, requested_tag, requested_source, requested_arch, expected_error, extra in [
            ('correct-signed-bundle', tag, source, arch, None, []),
            ('wrong-version', 'v0.1.999999', source, arch, 'Bundle version differs', []),
            ('wrong-architecture', tag, source, 'amd64' if arch == 'arm64' else 'arm64', 'Binary GOARCH differs', []),
            ('wrong-source', tag, '0' * 40, arch, 'Binary source differs', []),
            ('invalid-tag', 'main', source, arch, 'Expected a canonical', []),
            # An ad-hoc build must not pass where a Developer ID team is expected.
            ('adhoc-where-developer-id-expected', tag, source, arch, 'Expected a Developer ID signature', ['--expect-team-id', 'ABCDE12345']),
            ('malformed-team-id', tag, source, arch, 'Expected a 10-character Apple Team ID', ['--expect-team-id', 'idy']),
        ]:
            target = pathlib.Path(temporary) / (name + '.json')
            process = subprocess.run([sys.executable, str(scripts / 'check_macos_bundle.py'), app, str(target), '--release-tag', requested_tag, '--arch', requested_arch, '--source', requested_source, *extra], capture_output=True)
            if expected_error:
                assert process.returncode != 0 and expected_error in process.stderr.decode(), (name, process.stderr.decode())
                assert not target.exists(), 'Failed inspection must not create success evidence'
            else:
                assert process.returncode == 0, process.stderr.decode()
                checked = json.loads(target.read_text())
                assert checked['expectedRelease'] == {'tag': tag, 'arch': arch, 'source': source}
                assert checked['signature']['identifier'] == 'dev.idy.yesman' and checked['signature']['designatedRequirement'], checked['signature']
            results.append({'case': name, 'accepted': expected_error is None, 'expectedFailure': expected_error})
        pathlib.Path(report).parent.mkdir(parents=True, exist_ok=True)
        pathlib.Path(report).write_text(json.dumps(results, indent=2))
print(json.dumps(results, indent=2))
