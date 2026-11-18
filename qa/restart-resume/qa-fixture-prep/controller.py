"""Loopback controller/provider fragment for the existing hosted upgrade runner.

Import Controller from the authorized runner; it does not launch an app, sign a
package, install a CA, discover a profile or poll Actions. start() is runtime-only
and guarded to a hosted macOS ARM64 VM. Pure tests exercise policy/command
admission without starting sockets or pi.
"""
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
from pathlib import Path
import threading
import time
from urllib.parse import urlsplit, parse_qs
from fixture import Refused, require, validate_contract, request_policy, hosted_arm64

ALLOWED = {'HireNamed', 'Send', 'Submit', 'Stop', 'CheckForUpdates', 'RestartToUpdate'}


def pi_config(base):
    u = urlsplit(base)
    require(u.scheme == 'http' and u.hostname == '127.0.0.1' and u.port and u.path == '/v1' and not u.username and not u.password and not u.query and not u.fragment, 'provider must be loopback /v1')
    return {
        'settings.json': {'packages': [], 'extensions': [], 'skills': [], 'promptTemplates': [], 'themes': [],
                          'compaction': {'enabled': False}, 'retry': {'enabled': False}, 'defaultProjectTrust': 'no',
                          'defaultProvider': 't199-fixture', 'defaultModel': 'fixture'},
        'models.json': {'providers': {'t199-fixture': {'api': 'openai-completions', 'apiKey': 'synthetic-fixture-only',
                          'baseUrl': base, 'models': [{'id': 'fixture', 'name': 'T199 synthetic loopback', 'reasoning': False,
                          'input': ['text'], 'contextWindow': 100000, 'maxTokens': 1000,
                          'cost': {'input': 0, 'output': 0, 'cacheRead': 0, 'cacheWrite': 0}}]}}},
    }


def app_environment(ambient, data, config, web, temp, proxy):
    """Only public desktop/runtime routing reaches the production app/pi.
    Invoke in the disposable VM; HOME stays the clean VM user's native profile.
    The downloader/signing job's environment is never this process's input.
    """
    u = urlsplit(proxy)
    require(u.scheme == 'http' and u.hostname == '127.0.0.1' and u.port and u.path in ('', '/') and not u.username and not u.password and not u.query and not u.fragment, 'owned transport only')
    keep = ('PATH', 'HOME', 'USER', 'LOGNAME', 'LANG', 'LC_ALL')
    env = {k: ambient[k] for k in keep if k in ambient}
    env.update(YESMAN_HOME=str(data), PI_CODING_AGENT_DIR=str(config), YESMAN_WEB_DIR=str(web),
               TMPDIR=str(temp), TMP=str(temp), TEMP=str(temp), HTTP_PROXY=proxy, HTTPS_PROXY=proxy,
               NO_PROXY='127.0.0.1,localhost,::1')
    return env


class Controller:
    def __init__(self, contract, journal):
        self.c = validate_contract(contract)
        self.journal = Path(journal)
        self.lock = threading.RLock()
        self.pending = None
        self.seq = 0
        self.page = None
        self.reports = {}
        self.failed = False
        self.server = self.thread = None
        self.finished = threading.Event()

    def note(self, kind, **fields):
        with self.lock:
            row = {'seq': self.seq, 'at': time.time(), 'monotonic': time.monotonic(), 'kind': kind, **fields}
            self.seq += 1
            # Only synthetic fixture counts/data go here. Never log headers/env.
            with self.journal.open('a', encoding='utf-8') as f:
                f.write(json.dumps(row, sort_keys=True) + '\n')
            return row

    def report(self, page, observation):
        with self.lock:
            require(isinstance(page, str) and 1 <= len(page) <= 64, 'bad page identity')
            self.reports[page] = observation
            # Outer adapter accepts a new page only after matching About/image/
            # PID/source to expected S or Q; report arrival grants no authority.
            self.note('bridge-report', page=page, observation=observation)

    def select_page(self, page):
        with self.lock:
            require(page in self.reports, 'page has no observation')
            require(self.pending is None, 'unsettled mutation; do not rebind page')
            self.page = page
            self.note('page-selected', page=page)

    def issue(self, method, args):
        with self.lock:
            require(not self.failed and self.page is not None and self.pending is None, 'controller blocked/unknown prior outcome')
            require(method in ALLOWED and isinstance(args, list), 'method not allowed')
            actors = self.c['eligible'] + self.c['paused']
            if method in ('Send', 'Submit', 'Stop'):
                require(args and args[0] in actors, 'agent outside fixture')
            if method == 'Stop':
                require(len(args) == 1 and args[0] in self.c['paused'], 'Stop is only for the pause control')
            if method == 'HireNamed':
                require(len(args) == 1 and args[0] in {a.split('/')[0] for a in actors}, 'workspace outside fixture')
            if method in ('CheckForUpdates', 'RestartToUpdate'):
                require(not args, 'unexpected update args')
            self.pending = {'id': self.seq, 'page': self.page, 'method': method, 'args': args, 'served': False, 'started': False}
            self.note('command-published', command=self.pending.copy())
            return self.pending['id']

    def take(self, page):
        with self.lock:
            c = self.pending
            if not c or c['page'] != page or c['served'] or self.failed:
                return None
            c['served'] = True
            self.note('command-first-served', id=c['id'], page=page)
            return {k: c[k] for k in ('id', 'page', 'method', 'args')}

    def ack(self, kind, page, ident, result=None):
        with self.lock:
            c = self.pending
            require(c and c['page'] == page and c['id'] == ident and c['served'], 'stale/unsent acknowledgement')
            if kind == 'started':
                require(not c['started'], 'duplicate start')
                c['started'] = True
            else:
                require(kind == 'done' and c['started'], 'completion before start')
                self.pending = None
            self.note('command-' + kind, id=ident, page=page, result=result)

    def confirm_restart_departure(self, command_id):
        # RestartToUpdate can destroy the page before its Promise/ACK returns.
        # Only the outer native adapter can settle this, with old-exit and new
        # image/failed-launch evidence. It must never resend the command.
        with self.lock:
            c = self.pending
            require(c and c['id'] == command_id and c['method'] == 'RestartToUpdate' and c['started'], 'not an observed restart request')
            self.note('restart-departure-observed', id=command_id)
            self.pending = None

    def start(self):
        hosted_arm64()
        owner = self
        prefix = '/' + self.c['run']

        class Handler(BaseHTTPRequestHandler):
            def setup(self):
                super().setup()
                self.connection.settimeout(5)
            def log_message(self, *args): pass
            def end(self, code, value):
                body = json.dumps(value).encode()
                self.send_response(code)
                self.send_header('Content-Type', 'application/json')
                self.send_header('Access-Control-Allow-Origin', '*')
                self.send_header('Content-Length', str(len(body)))
                self.end_headers(); self.wfile.write(body)
            def do_OPTIONS(self):
                self.send_response(204)
                self.send_header('Access-Control-Allow-Origin', '*')
                self.send_header('Access-Control-Allow-Methods', 'GET,POST')
                self.send_header('Access-Control-Allow-Headers', 'Content-Type,Authorization')
                self.end_headers()
            def do_GET(self):
                u = urlsplit(self.path)
                if u.path != prefix + '/command': return self.end(404, {})
                return self.end(200, owner.take(parse_qs(u.query).get('page', [''])[0]))
            def do_POST(self):
                try:
                    require(not self.headers.get('Transfer-Encoding'), 'chunked body refused')
                    size = int(self.headers.get('Content-Length', '0'))
                    require(0 < size <= 8 << 20, 'body budget')
                    body = json.loads(self.rfile.read(size))
                    if self.path == '/v1/chat/completions': return self.provider(body)
                    require(self.path.startswith(prefix + '/'), 'wrong run')
                    kind = self.path[len(prefix) + 1:]
                    if kind == 'report': owner.report(body['page'], body['observation'])
                    elif kind in ('started', 'done'): owner.ack(kind, body['page'], body['id'], body.get('result'))
                    elif kind == 'error':
                        owner.failed = True; owner.note('bridge-error', page=body['page'], error=body['error'])
                    else: raise Refused('unknown endpoint')
                    self.end(200, {'ok': True})
                except (Refused, KeyError, ValueError) as e:
                    owner.failed = True; owner.note('controller-refusal', reason=str(e))
                    self.end(400, {'error': str(e)})
            def provider(self, body):
                require(body.get('model') == 'fixture' and body.get('stream') is True, 'only synthetic streaming model')
                result = request_policy(body['messages'], owner.c['run'], owner.c['eligible'] + owner.c['paused'])
                require(result['agent'] not in owner.c['paused'] or result['action'] == 'hold', 'paused actor requested continuation')
                request = owner.note('provider-request', **result)
                # Future runtime contains synthetic inputs only; keep the exact
                # model context so its digest/marker can be independently checked.
                request_path = owner.journal.with_name('provider-request-' + str(request['seq']) + '.json')
                request_path.write_text(json.dumps(body['messages'], sort_keys=True), encoding='utf-8')
                self.send_response(200); self.send_header('Content-Type', 'text/event-stream'); self.end_headers()
                def chunk(delta, finish=None):
                    v = {'id': 't199-fixture-' + str(request['seq']), 'object': 'chat.completion.chunk', 'created': int(time.time()), 'model': 'fixture',
                         'choices': [{'index': 0, 'delta': delta, 'finish_reason': finish}]}
                    self.wfile.write(('data: ' + json.dumps(v) + '\n\n').encode()); self.wfile.flush()
                try:
                    chunk({'role': 'assistant', 'content': 'Synthetic work in progress.'})
                    if result['action'] == 'hold':
                        # Stay Working until app shutdown cancels this real pi
                        # request. A bounded expiry is FAILURE, never completion.
                        deadline = time.monotonic() + 60
                        while not owner.finished.wait(.2):
                            if time.monotonic() >= deadline:
                                owner.failed = True; owner.note('provider-hold-expired', agent=result['agent']); break
                            self.wfile.write(b': held\n\n'); self.wfile.flush()
                        return
                    chunk({'content': ' Continued from the preserved synthetic session.'})
                    chunk({}, 'stop'); self.wfile.write(b'data: [DONE]\n\n'); self.wfile.flush()
                    owner.note('provider-continued', agent=result['agent'])
                except (BrokenPipeError, ConnectionResetError):
                    owner.note('provider-disconnected', agent=result['agent'], action=result['action'])
                except OSError as e:
                    owner.failed = True; owner.note('provider-transport-error', agent=result['agent'], error=type(e).__name__)

        self.server = ThreadingHTTPServer(('127.0.0.1', 0), Handler)
        self.server.daemon_threads = False  # close() joins handlers after finished wakes held streams
        self.thread = threading.Thread(target=self.server.serve_forever)
        self.thread.start()
        base = 'http://127.0.0.1:' + str(self.server.server_port)
        return {'driverEndpoint': base + prefix, 'providerBase': base + '/v1'}

    def close(self):
        self.finished.set()
        if self.server:
            self.server.shutdown(); self.server.server_close(); self.thread.join(5)
            require(not self.thread.is_alive(), 'controller did not join')
