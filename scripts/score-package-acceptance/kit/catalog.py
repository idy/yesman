#!/usr/bin/env python3
"""Local deterministic pi catalog fixture. Never sends provider traffic."""
import json
import os
import sys

if sys.argv[1:] == ['--version']:
    print('score-package-catalog-fixture-1')
    raise SystemExit(0)
if sys.argv[1:] != ['--mode', 'rpc', '--no-session']:
    raise SystemExit('catalog fixture accepts only --mode rpc --no-session or --version')
models = [
    {'provider': 'fixture', 'id': 'review-route/qa-csv-fixture', 'name': 'QA CSV Fixture', 'reasoning': False},
    {'provider': 'fixture', 'id': 'review-route/qa-patch-only', 'name': 'QA Patch Only', 'reasoning': False},
]
for line in sys.stdin:
    request = json.loads(line)
    method = request.get('type')
    if os.environ.get('SCORE_CATALOG_LOG'):
        with open(os.environ['SCORE_CATALOG_LOG'], 'a') as log:
            log.write(json.dumps({'pid': os.getpid(), 'method': method})+'\n')
    ok = method == 'get_available_models'
    response = {'type': 'response', 'id': request.get('id'), 'command': method, 'success': ok}
    if ok:
        response['data'] = {'models': models}
    else:
        response['error'] = 'unsupported catalog fixture request'
    print(json.dumps(response), flush=True)
