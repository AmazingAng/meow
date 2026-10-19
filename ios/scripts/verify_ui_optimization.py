#!/usr/bin/env python3
"""Run UI/model checks in a fresh simulator against disposable HTTP fixtures.

This verifies UI behavior, not live APNs or production server integration.
"""
import argparse
from datetime import datetime, timedelta, timezone
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
import os
from pathlib import Path
import shutil
import subprocess
import threading
from urllib.parse import parse_qs, urlsplit

ROOT = Path(__file__).resolve().parents[2]


def timestamp(date):
    return date.isoformat().replace('+00:00', 'Z')


class Fixture(BaseHTTPRequestHandler):
    items = []
    lock = threading.RLock()

    @classmethod
    def reset(cls):
        now = datetime.now(timezone.utc)
        cls.items = []
        for ident, title, choices, expiry, read in [
            ('info', 'Build completed', False, 0, False),
            ('later', 'What should I work on next?', None, 3600, False),
            ('soon', 'Review the production deployment', ['approve', 'reject'], 1800, True),
        ]:
            body = 'Project: meow / iOS\n' + ('\n'.join(f'{n}. Review migration, configuration and rollback details.' for n in range(1, 41)) if ident == 'soon' else 'The agent is ready for your next step.')
            item = dict(id='ntf_' + ident, type='info' if choices is False else 'question',
                        title=title, body=body, source='Claude Code' if ident == 'soon' else 'Codex',
                        priority='normal', push_status='skipped', created_at=timestamp(now),
                        read_at=timestamp(now) if read else None)
            if choices is not False:
                item['question'] = dict(id='qst_' + ident, status='pending', choices=choices,
                                        expires_at=timestamp(now + timedelta(seconds=expiry)), answer=None)
            cls.items.append(item)

    def log_message(self, *args):
        pass

    def send_json(self, value, status=200):
        data = json.dumps(value).encode()
        self.send_response(status)
        self.send_header('Content-Type', 'application/json')
        self.end_headers()
        self.wfile.write(data)

    def do_GET(self):
        with self.lock:
            self.get()

    def get(self):
        url = urlsplit(self.path)
        if url.path == '/fixture/reset':
            self.reset()
            return self.send_json({})
        if url.path == '/fixture/scenario':
            self.reset()
            scenario = parse_qs(url.query)['name'][0]
            info, later, soon = self.items
            if scenario == 'pagination':
                soon['question']['expires_at'] = timestamp(datetime.now(timezone.utc) + timedelta(seconds=120))
                self.items[:] = [later] + [dict(info, id=f'ntf_info_{i}') for i in range(49)] + [soon]
            else:
                soon['body'] = 'Choose an answer.'
                if scenario == 'expiry':
                    soon['question']['expires_at'] = timestamp(datetime.now(timezone.utc) + timedelta(seconds=20))
                self.items[:] = [soon]
            return self.send_json({})
        if url.path == '/healthz':
            return self.send_json({'status': 'ok'})
        if url.path == '/v1/push-config':
            return self.send_json({'mode': 'direct'})
        if url.path == '/v1/inbox/counts':
            return self.send_json({'unread': sum(n['read_at'] is None for n in self.items),
                                   'pending': sum(n.get('question', {}).get('status') == 'pending' for n in self.items)})
        if url.path == '/v1/inbox':
            query = parse_qs(url.query)
            filtered = self.items
            mode = query.get('filter', ['all'])[0]
            if mode == 'pending':
                filtered = [n for n in filtered if n.get('question', {}).get('status') == 'pending']
            elif mode == 'unread':
                filtered = [n for n in filtered if n['read_at'] is None]
            search = query.get('q', [''])[0].lower()
            filtered = [n for n in filtered if search in (n['title'] + n['body']).lower()]
            start = int(query.get('cursor', ['0'])[0])
            limit = int(query.get('limit', ['50'])[0])
            page = filtered[start:start + limit]
            next_cursor = str(start + limit) if start + limit < len(filtered) else None
            return self.send_json({'notifications': page, 'next_cursor': next_cursor})
        for n in self.items:
            if url.path == '/v1/notifications/' + n['id']:
                return self.send_json(n)
        return self.send_json({}, 404)

    def do_POST(self):
        with self.lock:
            self.post()

    def post(self):
        body = json.loads(self.rfile.read(int(self.headers.get('Content-Length', 0))) or b'{}')
        now = timestamp(datetime.now(timezone.utc))
        if self.path == '/v1/devices':
            return self.send_json({'device': {'id': 'dev_ui', 'name': 'UI fixture', 'apns_env': 'sandbox',
                                             'has_apns_token': False, 'created_at': now, 'last_seen_at': now},
                                   'device_token': 'mdt_disposable_ui_fixture',
                                   'key': {'id': 'key_ui', 'name': 'Default', 'hint': 'fixture',
                                           'scopes': ['notify', 'ask'], 'created_at': now,
                                           'secret': 'meow_disposable_ui_fixture'}}, 201)
        if self.path == '/v1/notifications':
            return self.send_json(dict(self.items[0], id='ntf_phone_test', title=body.get('title', 'Test')), 201)
        for n in self.items:
            if self.path == '/v1/notifications/' + n['id'] + '/read' or self.path == '/v1/inbox/read':
                n['read_at'] = now
            q = n.get('question')
            if q and self.path == '/v1/questions/' + q['id'] + '/responses':
                q['answer'] = body['answer']
                q['status'] = 'rejected' if body['answer'] == 'reject' else 'answered'
                n['read_at'] = now
                return self.send_json(dict(q, question=n['title'], created_at=n['created_at'], resolved_at=now,
                                           notification_id=n['id']))
        return self.send_json({})


def run(args, **kwargs):
    return subprocess.run(args, cwd=ROOT, check=True, text=True, capture_output=True, **kwargs).stdout


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--output', required=True, type=Path)
    parser.add_argument('--device-type', default='com.apple.CoreSimulator.SimDeviceType.iPhone-17-Pro')
    parser.add_argument('--runtime', default='com.apple.CoreSimulator.SimRuntime.iOS-26-5')
    parser.add_argument('--ui-only', action='store_true', help='Skip already verified unit tests when checking another screen size.')
    parser.add_argument('--test-case', help='Run one UIOptimizationTests method for a focused recheck.')
    args = parser.parse_args()
    output = args.output.resolve()
    output.mkdir(parents=True, exist_ok=False)
    Fixture.reset()
    server = ThreadingHTTPServer(('127.0.0.1', 0), Fixture)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    device = None
    try:
        run(['xcodegen', 'generate', '--spec', 'ios/project.yml'])
        device = run(['xcrun', 'simctl', 'create', 'Meow UI Polish Temporary', args.device_type, args.runtime]).strip()
        env = os.environ.copy()
        env['TEST_RUNNER_MEOW_UI_OPTIMIZATION_SERVER'] = 'http://127.0.0.1:' + str(server.server_port)
        bundle = output / 'ui.xcresult'
        command = ['xcodebuild', 'test', '-project', 'ios/Meow.xcodeproj', '-scheme', 'Meow',
                   '-destination', 'platform=iOS Simulator,id=' + device,
                   '-derivedDataPath', str(output.parent / 'DerivedData'), '-resultBundlePath', str(bundle),
                   '-parallel-testing-enabled', 'NO',
                   '-only-testing:MeowUITests/UIOptimizationTests' + ('/' + args.test_case if args.test_case else ''),
                   'CODE_SIGN_IDENTITY=-', 'CODE_SIGNING_ALLOWED=YES']
        if not args.ui_only:
            command.append('-only-testing:MeowTests')
        with (output / 'test.log').open('w') as log:
            result = subprocess.run(command, cwd=ROOT, env=env, text=True, stdout=log, stderr=subprocess.STDOUT)
        summary = json.loads(run(['xcrun', 'xcresulttool', 'get', 'test-results', 'summary', '--path', str(bundle)]))
        (output / 'results.json').write_text(json.dumps(summary, indent=2) + '\n')
        raw = output / 'attachments'
        run(['xcrun', 'xcresulttool', 'export', 'attachments', '--path', str(bundle), '--output-path', str(raw)])
        shots = output / 'screenshots'
        shots.mkdir()
        for case in json.loads((raw / 'manifest.json').read_text()):
            for attachment in case.get('attachments', []):
                name = attachment.get('suggestedHumanReadableName', '')
                if name.endswith('.png') and not name.startswith('Screenshot'):
                    shutil.copy2(raw / attachment['exportedFileName'], shots / (name.split('_0_')[0].removesuffix('.png') + '.png'))
        print(json.dumps({k: summary[k] for k in ['result', 'passedTests', 'failedTests', 'skippedTests']}, indent=2))
        if result.returncode or summary['failedTests'] or summary['skippedTests']:
            raise SystemExit('Verification failed; see ' + str(output / 'test.log'))
    finally:
        server.shutdown()
        server.server_close()
        if device:
            subprocess.run(['xcrun', 'simctl', 'shutdown', device], capture_output=True)
            run(['xcrun', 'simctl', 'delete', device])


if __name__ == '__main__':
    main()
