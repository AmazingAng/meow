#!/usr/bin/env python3
"""Capture real Meow screens with fictional local-only data in a disposable iPhone.

Produces English/Simplified Chinese RGB PNGs; no production API, keys or pushes.
The temporary XCTest source is removed afterwards. App implementation is unchanged.
"""
import argparse
from datetime import datetime, timedelta, timezone
import hashlib
from http.server import ThreadingHTTPServer
import importlib.util
import json
import os
from pathlib import Path
import shutil
import struct
import subprocess
import threading
from urllib.parse import parse_qs, urlsplit

ROOT = Path(__file__).resolve().parents[2]
spec = importlib.util.spec_from_file_location('store_fixture', ROOT / 'ios/scripts/verify_ui_optimization.py')
fixture = importlib.util.module_from_spec(spec)
spec.loader.exec_module(fixture)


class StoreFixture(fixture.Fixture):
    locale = 'en-US'

    @classmethod
    def reset(cls):
        super().reset()
        zh = cls.locale == 'zh-Hans'
        rows = [
            ('landing', 'Orbit', 'Claude Code', 'result', 'Ship the landing page', '完成产品主页',
             'Ready for review. The page is responsive and the build checks passed.', '页面已完成，移动端适配和构建检查通过。回来看看吧。'),
            ('release', 'Meow', 'Codex', 'attention', 'Review the release plan', '确认发布计划',
             'Two rollout options are ready. Please choose in the agent on your computer.', '两种发布方案已准备好。请回到电脑，在 Agent 中选择下一步。'),
            ('api', 'Orbit', 'Cursor', 'failure', 'Fix the API connection', '检查 API 连接',
             'The preview service is unavailable. Details are saved in the task.', '预览服务暂时无法连接，错误详情已保存在任务中。'),
            ('markdown', 'Meow', 'Claude Code', 'result', 'Polish the mobile inbox', '优化移动端收件箱',
             'The inbox update is ready. Messages, projects, and sessions are easier to browse.', '收件箱已更新，消息、项目和会话更容易查找。'),
            ('tests', 'Orbit', 'Codex', 'result', 'Add sign-in tests', '补齐登录测试',
             'Added coverage for expired sessions and missing tokens. All checks passed.', '已补齐会话过期和凭据缺失的测试，全部检查通过。'),
            ('research', 'Papertrail', 'Claude Code', 'result', 'Summarize the research', '整理研究笔记',
             'Your notes are organized and ready to read, with links to the original sources.', '笔记已整理完毕，附上原始资料链接，随时可以阅读。'),
            ('build', 'Meow', 'Codex', 'result', 'Prepare the next build', '准备下个构建',
             'The next build is ready for testing on your iPhone.', '新构建已准备好，可以在 iPhone 上进行测试。'),
        ]
        now = datetime.now(timezone.utc)
        cls.items = []
        for index, (ident, project, agent, event, en, cn, en_body, cn_body) in enumerate(rows):
            body = cn_body if zh else en_body
            if ident == 'markdown':
                body = ('''## 收件箱更新已完成

你可以按 **会话、项目或 Agent** 浏览消息，已下载的历史在离线时也能搜索。

- 保留完整结果和上下文
- 本地查找以前的消息
- 把不再需要的消息标为已读或删除

| 检查项目 | 状态 |
| --- | --- |
| 中文排版 | 通过 |
| 本地搜索 | 通过 |
| 通知详情 | 通过 |

准备好了，回到电脑继续吧。''' if zh else '''## The inbox update is ready

Browse messages by **session, project, or agent**. Downloaded history stays searchable when you are offline.

- Keep the full result and its context
- Find earlier messages on your phone
- Mark messages read or remove what you no longer need

| Check | Status |
| --- | --- |
| Readable layout | Passed |
| Local search | Passed |
| Notification details | Passed |

Everything is ready. Return to your computer when it suits you.''')
            cls.items.append(dict(
                id='ntf_store_' + ident, type='alert' if event in ('attention', 'failure') else 'info',
                title=cn if zh else en, body=body, source=agent, event=event,
                priority='high' if event == 'attention' else 'normal', push_status='skipped',
                created_at=fixture.timestamp(now - timedelta(minutes=index * 4 + 2)),
                read_at=None if index < 3 else fixture.timestamp(now),
                context=dict(workspace=project, workspace_id=hashlib.sha256(project.encode()).hexdigest()[:32],
                             session=cn if zh else en, session_id='store-demo-' + ident)))

    def get(self):
        url = urlsplit(self.path)
        if url.path == '/fixture/storefront':
            type(self).locale = parse_qs(url.query)['locale'][0]
            self.path = '/fixture/reset'
        super().get()


SWIFT = r'''import XCTest

final class AppStoreCaptureUITests: XCTestCase {
    func testCaptureStoreScreens() throws {
        continueAfterFailure = false
        let server = try XCTUnwrap(ProcessInfo.processInfo.environment["MEOW_STORE_CAPTURE_SERVER"])
        for locale in ["en-US", "zh-Hans"] {
            let done = expectation(description: "isolated demo reset")
            URLSession.shared.dataTask(with: URL(string: server + "/fixture/storefront?locale=" + locale)!) { _, response, error in
                XCTAssertNil(error)
                XCTAssertEqual((response as? HTTPURLResponse)?.statusCode, 200)
                done.fulfill()
            }.resume()
            wait(for: [done], timeout: 30)
            let app = XCUIApplication()
            app.launchArguments = ["-meowServer", server, "-meowAutoRegister", "YES",
                "-AppleLanguages", locale == "zh-Hans" ? "(zh-Hans)" : "(en)",
                "-AppleLocale", locale == "zh-Hans" ? "zh_CN" : "en_US",
                "-UIPreferredContentSizeCategoryName", "UICTContentSizeCategoryL"]
            app.launch()
            XCTAssertTrue(app.buttons["inbox.browseMenu"].waitForExistence(timeout: 15))
            browse("messages", app)
            XCTAssertTrue(app.buttons["notification.ntf_store_landing"].waitForExistence(timeout: 10))
            capture(locale + "-01-messages", app)
            browse("sessions", app)
            XCTAssertTrue(app.buttons.matching(NSPredicate(format: "identifier BEGINSWITH %@", "inbox.group.")).firstMatch.waitForExistence(timeout: 10))
            capture(locale + "-02-sessions", app)
            browse("workspaces", app)
            XCTAssertTrue(app.buttons.matching(NSPredicate(format: "identifier BEGINSWITH %@", "inbox.group.")).firstMatch.waitForExistence(timeout: 10))
            capture(locale + "-03-projects", app)
            browse("messages", app)
            app.buttons["notification.ntf_store_markdown"].tap()
            XCTAssertTrue(app.staticTexts["notification.heading"].waitForExistence(timeout: 10))
            capture(locale + "-04-full-result", app)
            app.terminate()
        }
    }
    private func browse(_ mode: String, _ app: XCUIApplication) {
        app.buttons["inbox.browseMenu"].tap()
        XCTAssertTrue(app.buttons["browse." + mode].waitForExistence(timeout: 5))
        app.buttons["browse." + mode].tap()
    }
    private func capture(_ name: String, _ app: XCUIApplication) {
        let attachment = XCTAttachment(screenshot: app.screenshot())
        attachment.name = name
        attachment.lifetime = .keepAlways
        add(attachment)
    }
}
'''


def run(args):
    return subprocess.check_output(args, cwd=ROOT, text=True, stderr=subprocess.PIPE).strip()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--output', required=True, type=Path)
    args = parser.parse_args()
    out = args.output.resolve()
    out.mkdir(parents=True, exist_ok=False)
    source = ROOT / 'ios/MeowUITests/AppStoreCaptureUITests.swift'
    if source.exists():
        raise RuntimeError('temporary XCTest source already exists; refusing to overwrite')
    source.write_text(SWIFT)
    StoreFixture.reset()
    server = ThreadingHTTPServer(('127.0.0.1', 0), StoreFixture)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    device = None
    try:
        run(['xcodegen', 'generate', '--spec', 'ios/project.yml'])
        device = run(['xcrun', 'simctl', 'create', 'Meow Store Screens Temporary',
                      'com.apple.CoreSimulator.SimDeviceType.iPhone-17-Pro-Max',
                      'com.apple.CoreSimulator.SimRuntime.iOS-26-5'])
        StoreFixture.device_id = device
        StoreFixture.app_path = out / 'DerivedData/Build/Products/Debug-iphonesimulator/Meow.app'
        run(['xcrun', 'simctl', 'boot', device])
        run(['xcrun', 'simctl', 'bootstatus', device, '-b'])
        run(['xcrun', 'simctl', 'ui', device, 'appearance', 'light'])
        run(['xcrun', 'simctl', 'status_bar', device, 'override', '--time', '9:41',
             '--dataNetwork', 'wifi', '--wifiMode', 'active', '--wifiBars', '3',
             '--batteryState', 'charged', '--batteryLevel', '100'])
        env = dict(os.environ, TEST_RUNNER_MEOW_STORE_CAPTURE_SERVER='http://127.0.0.1:' + str(server.server_port))
        with (out / 'capture.log').open('w') as log:
            result = subprocess.run(['xcodebuild', 'test', '-project', 'ios/Meow.xcodeproj', '-scheme', 'Meow',
                 '-destination', 'platform=iOS Simulator,id=' + device,
                 '-derivedDataPath', str(out / 'DerivedData'), '-resultBundlePath', str(out / 'capture.xcresult'),
                 '-parallel-testing-enabled', 'NO', '-only-testing:MeowUITests/AppStoreCaptureUITests',
                 'CODE_SIGN_IDENTITY=-', 'CODE_SIGNING_ALLOWED=YES'], cwd=ROOT, env=env, stdout=log, stderr=subprocess.STDOUT)
        summary = json.loads(run(['xcrun', 'xcresulttool', 'get', 'test-results', 'summary', '--path', str(out / 'capture.xcresult')]))
        (out / 'results.json').write_text(json.dumps(summary, indent=2) + '\n')
        if result.returncode or summary['failedTests'] or summary['skippedTests'] or summary['passedTests'] != 1:
            raise RuntimeError('screenshot capture did not pass; see capture.log')
        run(['xcrun', 'xcresulttool', 'export', 'attachments', '--path', str(out / 'capture.xcresult'), '--output-path', str(out / 'attachments')])
        shots = out / 'screenshots'
        shots.mkdir()
        records = []
        for case in json.loads((out / 'attachments/manifest.json').read_text()):
            for attachment in case.get('attachments', []):
                name = attachment.get('suggestedHumanReadableName', '')
                if not name.startswith(('en-US-', 'zh-Hans-')) or not name.endswith('.png'):
                    continue
                destination = shots / name.split('_0_')[0]
                if not destination.name.endswith('.png'):
                    destination = destination.with_suffix('.png')
                shutil.copyfile(out / 'attachments' / attachment['exportedFileName'], destination)
                data = destination.read_bytes()
                size = struct.unpack('>II', data[16:24])
                if size not in ((1260, 2736), (1290, 2796), (1320, 2868)) or data[25] != 2:
                    raise RuntimeError('unexpected screenshot dimensions/alpha channel: ' + str(destination))
                records.append({'file': destination.name, 'dimensions': size, 'alpha': False,
                                'sha256': hashlib.sha256(data).hexdigest()})
        if len(records) != 8:
            raise RuntimeError(f'expected 8 localized screenshots, found {len(records)}')
        (out / 'screenshots.json').write_text(json.dumps({'fictional_demo_data': True, 'actual_app_screens': True,
            'production_network_used': False, 'screenshots': records}, indent=2) + '\n')
        print(json.dumps(records, indent=2))
    finally:
        server.shutdown()
        server.server_close()
        source.unlink(missing_ok=True)
        run(['xcodegen', 'generate', '--spec', 'ios/project.yml'])
        if device:
            subprocess.run(['xcrun', 'simctl', 'shutdown', device], capture_output=True)
            subprocess.run(['xcrun', 'simctl', 'delete', device], check=True)


if __name__ == '__main__':
    main()
