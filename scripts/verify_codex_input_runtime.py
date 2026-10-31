#!/usr/bin/env python3
"""Exercise Codex input reminders with a loopback model and isolated state.

Uses no real model account, production server, or user hooks. Generated fixture
hooks are reviewed through the supported Codex CLI flow in a disposable home,
then hooks/list must report them trusted before input coverage is measured.
"""
import argparse
import fcntl
import json
import os
from pathlib import Path
import queue
import pty
import re
import select
import shlex
import shutil
import subprocess
import sys
import struct
import tempfile
import termios
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer


def require(ok, message):
    if not ok:
        raise RuntimeError(message)


def trust_fixture(codex, env, root, output, name):
    # Use the supported review UI, never inspect or edit Codex's trust database.
    # The app-server subcommand does not honor the TUI/exec trust-bypass flag.
    require(root.name.startswith('meow-codex-input-') and root.is_relative_to(Path(tempfile.gettempdir())), 'refusing non-fixture trust')
    require(not any((p / '.git').exists() for p in [root, *root.parents]), 'fixture must be outside a real repository')
    master, slave = pty.openpty()
    fcntl.ioctl(slave, termios.TIOCSWINSZ, struct.pack('HHHH', 24, 100, 0, 0))

    def terminal_session():
        os.setsid()
        fcntl.ioctl(slave, termios.TIOCSCTTY, 0)

    process = subprocess.Popen([codex, '--no-alt-screen', '--no-daemon', '-C', str(root)],
        stdin=slave, stdout=slave, stderr=slave, env={**env, 'TERM': 'xterm-256color'}, preexec_fn=terminal_session)
    os.close(slave)
    log = bytearray()

    def until(needle):
        pending = bytearray()
        deadline = time.monotonic() + 20
        while time.monotonic() < deadline:
            if select.select([master], [], [], .2)[0]:
                chunk = os.read(master, 65536)
                # Respond only to terminal capability/cursor queries, never to
                # Codex approval prompts. Hook review is an explicit step below.
                for query, reply in [(b'\x1b]10;?', b'\x1b]10;rgb:eeee/eeee/eeee\x1b\\'),
                                     (b'\x1b]11;?', b'\x1b]11;rgb:0000/0000/0000\x1b\\'),
                                     (b'\x1b[6n', b'\x1b[1;1R'), (b'\x1b[?u', b'\x1b[?0u'),
                                     (b'\x1b[c', b'\x1b[?1;2c')]:
                    if query in chunk:
                        os.write(master, reply)
                log.extend(chunk)
                pending.extend(chunk)
                text = re.sub(r'\x1b\[[0-?]*[ -/]*[@-~]', '', pending.decode(errors='replace'))
                if needle in text:
                    return
        raise RuntimeError('fixture hook-review prompt missing: ' + needle)

    try:
        until('Trust all and continue')
        time.sleep(.2)  # Allow the startup terminal-query reader to yield to UI input.
        os.write(master, b'2')
        time.sleep(.1)
        os.write(master, b'\r')
        until('Ask Codex')
    finally:
        (output / (name + '-fixture-review.log')).write_bytes(log)
        # Closing the PTY also releases terminal-drain waits during TUI exit.
        os.close(master)
        try:
            process.wait(timeout=4)
        except subprocess.TimeoutExpired:
            process.terminate()
            try:
                process.wait(timeout=3)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait(timeout=5)


def case(codex, meow, output, name, tool, question):
    received = queue.Queue()
    notifications = []

    class Fixture(BaseHTTPRequestHandler):
        calls = 0

        def log_message(self, *args):
            pass

        def reply(self, data, status=200):
            data = json.dumps(data).encode()
            self.send_response(status)
            self.send_header('Content-Type', 'application/json')
            self.send_header('Content-Length', str(len(data)))
            self.end_headers()
            self.wfile.write(data)

        def do_GET(self):
            self.reply({'device_id': 'dev_fixture', 'name': 'Fixture'})

        def do_POST(self):
            body = json.loads(self.rfile.read(int(self.headers['Content-Length'])))
            if self.path == '/v1/notifications':
                notifications.append(body)
                if body['event'] == 'attention':
                    received.put((body, self.headers.get('Idempotency-Key')))
                return self.reply({'id': 'ntf_fixture'}, 201)
            Fixture.calls += 1
            if Fixture.calls == 1:
                item = {'id': 'fc_fixture', 'type': 'function_call', 'call_id': 'call_fixture',
                        'name': tool, 'arguments': json.dumps({'questions': [question]})}
            else:
                item = {'id': 'msg_fixture', 'type': 'message', 'role': 'assistant',
                        'content': [{'type': 'output_text', 'text': 'Fixture complete'}]}
            events = [
                {'type': 'response.created', 'response': {'id': 'resp_fixture', 'status': 'in_progress', 'output': []}},
                {'type': 'response.output_item.done', 'output_index': 0, 'item': item},
                {'type': 'response.completed', 'response': {'id': 'resp_fixture', 'status': 'completed', 'output': [item],
                    'usage': {'input_tokens': 1, 'output_tokens': 1, 'total_tokens': 2}}},
            ]
            data = ''.join('data: ' + json.dumps(e) + '\n\n' for e in events).encode()
            self.send_response(200)
            self.send_header('Content-Type', 'text/event-stream')
            self.send_header('Content-Length', str(len(data)))
            self.end_headers()
            self.wfile.write(data)

    server = ThreadingHTTPServer(('127.0.0.1', 0), Fixture)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    process = None
    try:
        with tempfile.TemporaryDirectory(prefix='meow-codex-input-') as folder:
            root = Path(folder)
            home = root / 'codex'
            home.mkdir()
            config = output / (name + '-fixture-meow.json')
            config.write_text(json.dumps({'server': 'http://127.0.0.1:' + str(server.server_port), 'key': 'meow_fixture'}))
            env = os.environ.copy()
            for key in ['OPENAI_API_KEY', 'MEOW_SERVER', 'MEOW_KEY', 'MEOW_HOOK_DISABLED', 'MEOW_EXECUTABLE']:
                env.pop(key, None)
            env.update(CODEX_HOME=str(home), MEOW_CONFIG=str(config), MEOW_LANGUAGE='en')
            setup = subprocess.run([str(meow), 'setup', 'codex', '--yes', '--no-test'], env=env, capture_output=True, text=True, timeout=20)
            (output / (name + '-setup.log')).write_text(setup.stdout + setup.stderr)
            require(setup.returncode == 0, 'fixture setup failed')
            trace = output / (name + '-trace.jsonl')
            capture = root / 'capture.py'
            capture.write_text('import json,sys\nfrom pathlib import Path\nd=json.load(sys.stdin)\nwith Path(' + repr(str(trace)) + ').open("a") as f:f.write(json.dumps(d)+"\\n")\n')
            hooks = json.loads((home / 'hooks.json').read_text())
            hooks['hooks']['PreToolUse'].append({'hooks': [{'type': 'command', 'command': shlex.quote(sys.executable) + ' ' + shlex.quote(str(capture))}]})
            (home / 'hooks.json').write_text(json.dumps(hooks))
            (home / 'config.toml').write_text('model = "gpt-5.4"\nmodel_provider = "fixture"\n[features]\nplugins = false\n[model_providers.fixture]\nname = "fixture"\nbase_url = "http://127.0.0.1:' + str(server.server_port) + '/v1"\nwire_api = "responses"\nrequires_openai_auth = false\n')
            trust_fixture(codex, env, root, output, name)
            with (output / (name + '-stderr.log')).open('w') as stderr:
                process = subprocess.Popen([codex, 'app-server', '--stdio'], env=env,
                    stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=stderr, text=True)
                messages = queue.Queue()

                def reader():
                    for line in process.stdout:
                        try:
                            messages.put(json.loads(line))
                        except ValueError:
                            messages.put({'error': 'malformed app-server JSON'})

                threading.Thread(target=reader, daemon=True).start()

                def send(value):
                    process.stdin.write(json.dumps(value) + '\n')
                    process.stdin.flush()

                def response(identifier):
                    while True:
                        row = messages.get(timeout=25)
                        require('error' not in row, str(row.get('error')))
                        if row.get('id') == identifier:
                            return row['result']

                send({'id': 1, 'method': 'initialize', 'params': {'clientInfo': {'name': 'meow_fixture', 'version': '1'}, 'capabilities': {'experimentalApi': True}}})
                response(1)
                send({'method': 'initialized', 'params': {}})
                send({'id': 10, 'method': 'hooks/list', 'params': {'cwds': [str(root)]}})
                discovered = response(10)
                (output / (name + '-hooks-discovered.json')).write_text(json.dumps(discovered, indent=2))
                discovered_hooks = [h for entry in discovered['data'] for h in entry['hooks']]
                require(discovered_hooks and all(h['trustStatus'] == 'trusted' for h in discovered_hooks), 'fixture hooks were not trusted through the CLI')
                params = {'cwd': str(root), 'ephemeral': True, 'modelProvider': 'fixture', 'model': 'gpt-5.4', 'sandbox': 'read-only'}
                if tool.endswith('_async'):
                    params['dynamicTools'] = [{'type': 'function', 'name': tool, 'description': 'Fixture question', 'inputSchema': {'type': 'object', 'properties': {'questions': {'type': 'array'}}}}]
                send({'id': 2, 'method': 'thread/start', 'params': params})
                thread = response(2)['thread']['id']
                send({'id': 3, 'method': 'turn/start', 'params': {'threadId': thread, 'input': [{'type': 'text', 'text': 'Run fixture'}],
                    'collaborationMode': {'mode': 'plan', 'settings': {'model': 'gpt-5.4', 'developer_instructions': None}}}})
                prompted = False
                while True:
                    row = messages.get(timeout=25)
                    require('error' not in row, str(row.get('error')))
                    method = row.get('method')
                    if method in ['item/tool/requestUserInput', 'item/tool/call']:
                        prompted = True
                        body, key = received.get(timeout=8)
                        require(question.get('question', question.get('title')) in body['body'] and key and body['priority'] == 'high', 'input reminder absent or missing question before reply')
                        if method == 'item/tool/requestUserInput':
                            send({'id': row['id'], 'result': {'answers': {question['id']: {'answers': ['Fixture reply']}}}})
                        else:
                            send({'id': row['id'], 'result': {'contentItems': [{'type': 'inputText', 'text': 'Fixture reply'}], 'success': True}})
                    elif method == 'turn/completed':
                        break
                require(prompted, 'Codex did not actually request user input')
                rows = [json.loads(line) for line in trace.read_text().splitlines()] if trace.exists() else []
                attention = [n for n in notifications if n['event'] == 'attention']
                require(len(attention) == 1 and any(r['tool_name'] == tool for r in rows), 'reminder count or actual hook path incorrect')
                result = {'tool': tool, 'actual_input_request': prompted,
                          'pre_tool_events': [{k: r.get(k) for k in ['hook_event_name', 'tool_name', 'tool_use_id']} for r in rows],
                          'attention_notifications': len(attention), 'coverage': 'reminder received before fixture answer'}
                process.terminate()
                process.wait(timeout=10)
                process = None
                return result
    finally:
        if process is not None:
            process.terminate()
            try:
                process.wait(timeout=10)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait()
        server.shutdown()
        server.server_close()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--output', required=True, type=Path)
    parser.add_argument('--meow', required=True, type=Path)
    parser.add_argument('--codex', default=shutil.which('codex'))
    args = parser.parse_args()
    out = args.output.resolve()
    out.mkdir(parents=True, exist_ok=False)
    cases = [
        ('choices', 'request_user_input', {'id': 'q', 'header': 'Region', 'question': 'Choose fixture region?', 'options': [{'label': 'Asia', 'description': 'Fixture A'}, {'label': 'Europe', 'description': 'Fixture B'}]}),
        ('dynamic', 'request_user_input_async', {'title': 'Fixture async question?', 'options': ['Yes', 'No']}),
        ('text', 'request_user_input_async', {'title': 'Enter fixture version?'}),
    ]
    results = [case(args.codex, args.meow.resolve(), out, *c) for c in cases]
    report = {'codex_version': subprocess.check_output([args.codex, '--version'], text=True).strip(), 'cases': results,
              'limits': 'Loopback model and HTTP notification receipt using real Codex, not physical APNs or desktop card rendering. Only fixture hook trust was changed.'}
    (out / 'report.json').write_text(json.dumps(report, indent=2))
    print(json.dumps(report, indent=2))


if __name__ == '__main__':
    main()
