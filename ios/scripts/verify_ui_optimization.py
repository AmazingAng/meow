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
import uuid
from urllib.parse import parse_qs, urlsplit

ROOT = Path(__file__).resolve().parents[2]


def timestamp(date):
    return date.isoformat().replace('+00:00', 'Z')


class Fixture(BaseHTTPRequestHandler):
    items = []
    response_count = 0
    missing_key = False
    key_attempts = 0
    key_revocations = 0
    sender_key_old = 0
    sender_key_new = 0
    active_sender_keys = set()
    key_reset_check = False
    reset_sender_secret = None
    connection_error = False
    lock = threading.RLock()

    @classmethod
    def reset(cls):
        now = datetime.now(timezone.utc)
        cls.items = []
        cls.response_count = 0
        cls.missing_key = False
        cls.key_attempts = 0
        cls.key_revocations = 0
        cls.sender_key_old = 0
        cls.sender_key_new = 0
        cls.active_sender_keys = {'meow_disposable_ui_fixture', 'meow_recovered_ui_fixture'}
        cls.key_reset_check = False
        cls.reset_sender_secret = None
        cls.connection_error = False
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
        if url.path == '/fixture/stats':
            return self.send_json({'responses': type(self).response_count, 'unread': sum(n['read_at'] is None for n in self.items),
                                   'key_attempts': type(self).key_attempts, 'key_revocations': type(self).key_revocations,
                                   'sender_key_old': type(self).sender_key_old, 'sender_key_new': type(self).sender_key_new})
        if url.path == '/fixture/reset':
            self.reset()
            return self.send_json({})
        if url.path == '/fixture/scenario':
            self.reset()
            scenario = parse_qs(url.query)['name'][0]
            info, later, soon = self.items
            if scenario == 'key-reset':
                type(self).key_reset_check = True
            elif scenario == 'pagination':
                soon['question']['expires_at'] = timestamp(datetime.now(timezone.utc) + timedelta(seconds=120))
                self.items[:] = [later] + [dict(info, id=f'ntf_info_{i}') for i in range(49)] + [soon]
            elif scenario == 'onboarding-missing-key':
                type(self).missing_key = True
            elif scenario == 'connection-error':
                type(self).connection_error = True
            elif scenario == 'activity':
                self.items[:] = [info]
            elif scenario == 'empty-table-header':
                self.items[:] = [dict(info, title='New result · meow', body='**Bark Server 不持久保存通知正文和消息历史。**\n\n与目前 Meow 的区别是：\n\n| | Bark | Meow |\n| --- | --- | --- |\n| 服务端保存正文 | 不保存 | 保存到 SQLite |\n| 手机查看历史 | 本地记录 | 本地缓存，可向服务器同步 |\n| 服务端补拉历史 | 不提供 | 支持 |\n| 删除消息 | 删除手机本地记录 | 当前设计同时删除服务器记录与本地缓存 |\n| 空单元格 | | 最后一列 |')]
            elif scenario == 'compact-categories':
                records = [
                    ('result', 'New result', 'Codex', 'Refine notification layout', 'The inbox now uses three lines per notification.'),
                    ('attention', 'Needs your attention', 'Claude Code', '发布前检查', '需要确认后才能继续发布。请在电脑端处理。'),
                    ('failure', '运行出错', 'Claude Code', '检查服务连接', '连接超时，请检查服务器状态。'),
                    ('custom', 'Build 19 is ready', 'Codex', 'Release preparation', 'All checks passed. Ready for device testing.'),
                    ('legacy', 'Deployment completed', 'Codex', None, 'Older notifications keep their original title.'),
                ]
                self.items[:] = [dict(info, **({'event': key} if key in ('result', 'attention', 'failure') else {}), id='ntf_' + key, title=title + (' · meow' if session else ''),
                                     source=source, body=body, priority='high' if key in ('attention', 'failure') else 'normal',
                                     type='alert' if key in ('attention', 'failure') else 'info',
                                     context=dict(workspace='meow', session=session, session_id=key) if session else None)
                                 for key, title, source, session, body in records]
            elif scenario == 'detail-header':
                records = [
                    ('result', 'New result · meow-repo', 'Claude Code', 'meow-repo', 'meow 用户 onboarding 流程', 'result'),
                    ('attention', 'Needs your attention · meow-repo', 'Codex', 'meow-repo', '发布前检查', 'attention'),
                    ('failure', 'Run failed · meow-repo', 'Codex', 'meow-repo', '检查服务连接', 'failure'),
                    ('id_only', 'New result · meow-repo', 'Codex', 'meow-repo', None, 'result'),
                    ('service', 'Backup completed', 'Backup service', 'ops', None, None),
                    ('legacy', 'Service is healthy', 'Monitor', None, None, None),
                ]
                self.items[:] = [dict(info, id='ntf_detail_' + key, title=title, source=source,
                                     body='PR #5 方向是对的，大部分改动可以合并。\n\n## 合并前建议修\n\n请检查 **安装命令** 和 `meow login`，保留脚本接入说明。',
                                     context=dict(workspace=workspace, session=session,
                                                  session_id='thread-detail-42' if key == 'id_only' else (key if session else None)) if workspace else None,
                                     **({'event': event} if event else {}))
                                 for key, title, source, workspace, session, event in records]
            elif scenario == 'grouping':
                rows = [
                    ('group_a1', 'Codex', 'meow', 'wa', 'Deploy', 'needle first result'),
                    ('group_a2', 'Codex', 'meow', 'wa', 'Deploy', 'second unrelated result'),
                    ('group_b', 'Codex', 'meow', 'wb', 'Review', 'review result'),
                    ('group_c', 'Claude Code', 'xapi', 'wc', 'Design', 'design result'),
                    ('group_legacy', 'Monitor', None, None, None, 'legacy service result'),
                ]
                self.items[:] = [dict(info, id='ntf_' + key, source=source, body=body, event='result', read_at=None,
                                     context=dict(workspace=workspace, workspace_id=wid, session=session, session_id=wid + ':' + session) if session else None)
                                 for key, source, workspace, wid, session, body in rows]
            elif scenario == 'session-identity':
                records = [
                    ('identity_first', None, 'stable-id', 'First unnamed identity result'),
                    ('identity_named', 'Initial session name', 'stable-id', 'Named identity result'),
                    ('identity_renamed', 'Renamed identity session', 'stable-id', 'Renamed identity result'),
                    ('identity_latest', None, 'stable-id', 'Latest unnamed identity result'),
                    ('identity_other', 'Renamed identity session', 'other-id', 'Different conversation result'),
                ]
                now = datetime.now(timezone.utc)
                self.items[:] = [dict(info, id='ntf_' + key, body=body, event='result', read_at=None,
                                     created_at=timestamp(now + timedelta(seconds=index)),
                                     context=dict(workspace='identity-project', workspace_id='identity-project-id', session=name, session_id=sid))
                                 for index, (key, name, sid, body) in enumerate(records)]
            elif scenario == 'formatting':
                bodies = {
                    'markdown': '## 推送已恢复\n\n收到，这条回复用于测试桌面版的自动推送。回复结束后，留意 Meow 是否收到 **「New result」** 通知。\n\n- **Markdown** 正常显示\n- 使用 `meow doctor` 检查连接\n\n> 完整正文仍可在详细信息中查看。\n\n```sh\nmeow doctor\n```\n\n| 项目 | 状态 |\n| --- | --- |\n| App | 正常 |\n| Hook | 正常 |',
                    'envelope': '{"suggestion":"收到了，自动推送正常了"}',
                    'json': '{"status":"ok","checks":["Markdown","JSON"],"count":9007199254740993}',
                }
                self.items[:] = [dict(info, id='ntf_' + name, title='New result · meow', body=body)
                                 for name, body in bodies.items()]
            else:
                soon['body'] = 'Choose an answer.'
                if scenario == 'answered':
                    soon['question']['status'] = 'answered'
                    soon['question']['answer'] = 'Historical decision'
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
            if self.connection_error:
                return self.send_json({'error': {'code': 'unavailable', 'message': 'Connection test failed.'}}, 503)
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

    def do_DELETE(self):
        if self.path in ('/v1/keys/key_ui', '/v1/keys/key_recovered'):
            type(self).key_revocations += 1
            type(self).active_sender_keys.discard('meow_disposable_ui_fixture' if self.path.endswith('key_ui') else 'meow_recovered_ui_fixture')
            return self.send_json({})
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
                                           'secret': None if self.missing_key else 'meow_disposable_ui_fixture'}}, 201)
        if self.path == '/v1/keys':
            type(self).key_attempts += 1
            if self.key_attempts == 1:
                return self.send_json({'error': {'code': 'unavailable', 'message': 'Try creating the key again.'}}, 503)
            secret = 'meow_ui_reset_' + uuid.uuid4().hex if self.key_reset_check else 'meow_recovered_ui_fixture'
            type(self).reset_sender_secret = secret
            type(self).active_sender_keys.add(secret)
            return self.send_json({'id': 'key_reset' if self.key_reset_check else 'key_recovered', 'name': 'Default', 'hint': 'fixture',
                                   'scopes': ['notify', 'ask'], 'created_at': now,
                                   'secret': secret}, 201)
        if self.path == '/v1/notifications':
            secret = self.headers.get('Authorization', '').removeprefix('Bearer ')
            if self.key_reset_check and secret not in self.active_sender_keys:
                return self.send_json({'error': {'code': 'unauthorized', 'message': 'Invalid test sender key'}}, 401)
            if secret == self.reset_sender_secret:
                type(self).sender_key_new += 1
            else:
                type(self).sender_key_old += 1
            return self.send_json(dict(self.items[0], id='ntf_phone_test', title=body.get('title', 'Test')), 201)
        for n in self.items:
            if self.path == '/v1/notifications/' + n['id'] + '/read' or self.path == '/v1/inbox/read':
                n['read_at'] = now
            q = n.get('question')
            if q and self.path == '/v1/questions/' + q['id'] + '/responses':
                type(self).response_count += 1
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
    parser.add_argument('--test-case', action='append', help='Run a UIOptimizationTests method; repeat for a focused group.')
    parser.add_argument('--appearance', choices=['light', 'dark'], help='Set the disposable simulator appearance before testing.')
    parser.add_argument('--coverage', action='store_true', help='Collect code coverage in the result bundle.')
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
        if args.appearance:
            run(['xcrun', 'simctl', 'boot', device])
            run(['xcrun', 'simctl', 'bootstatus', device, '-b'])
            run(['xcrun', 'simctl', 'ui', device, 'appearance', args.appearance])
        env = os.environ.copy()
        env['TEST_RUNNER_MEOW_UI_OPTIMIZATION_SERVER'] = 'http://127.0.0.1:' + str(server.server_port)
        bundle = output / 'ui.xcresult'
        command = ['xcodebuild', 'test', '-project', 'ios/Meow.xcodeproj', '-scheme', 'Meow',
                   '-destination', 'platform=iOS Simulator,id=' + device,
                   '-derivedDataPath', str(output.parent / 'DerivedData'), '-resultBundlePath', str(bundle),
                   '-parallel-testing-enabled', 'NO',
                   'CODE_SIGN_IDENTITY=-', 'CODE_SIGNING_ALLOWED=YES']
        if args.coverage:
            command += ['-enableCodeCoverage', 'YES']
        command += ['-only-testing:MeowUITests/UIOptimizationTests' + ('/' + name if name else '')
                    for name in (args.test_case or [''])]
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
