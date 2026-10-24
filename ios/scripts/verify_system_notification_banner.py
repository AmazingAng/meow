#!/usr/bin/env python3
"""Show the shared layout fixture in a real disposable iOS system banner.

Uses simctl APNs, never an Apple push key, phone or production API.
"""
import argparse
from http.server import ThreadingHTTPServer
import json
import os
from pathlib import Path
import shutil
import subprocess
import threading

from verify_ui_optimization import Fixture, ROOT, run


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--output', type=Path, required=True)
    args = parser.parse_args()
    out = args.output.resolve(); out.mkdir(parents=True, exist_ok=False)
    fixture = json.loads((ROOT / 'ios/MeowTests/Fixtures/notification-layout.json').read_text())[0]
    payload = out / 'notification.apns'
    payload.write_text(json.dumps({'aps': {'alert': {
        'title': fixture['expectedTitle'], 'subtitle': fixture['expectedSubtitle'], 'body': fixture['expectedBody']},
        'mutable-content': 1, 'sound': 'meow.wav', 'category': 'MEOW_INFO'},
        'meow': {'id': 'ntf_system_banner', 'device_id': 'dev_ui', 'preview_format': 'plain'}}, ensure_ascii=False))
    device = None

    class BannerFixture(Fixture):
        def get(self):
            if self.path == '/fixture/system-push':
                try:
                    run(['xcrun', 'simctl', 'push', device, 'blue.meow.app', str(payload)])
                except subprocess.CalledProcessError as error:
                    return self.send_json({'error': str(error)}, 500)
                return self.send_json({'accepted': True})
            return super().get()

    BannerFixture.reset()
    server = ThreadingHTTPServer(('127.0.0.1', 0), BannerFixture)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    try:
        run(['xcodegen', 'generate', '--spec', 'ios/project.yml'])
        device = run(['xcrun', 'simctl', 'create', 'Meow system banner verification',
                      'com.apple.CoreSimulator.SimDeviceType.iPhone-17-Pro',
                      'com.apple.CoreSimulator.SimRuntime.iOS-26-5']).strip()
        run(['xcrun', 'simctl', 'boot', device])
        run(['xcrun', 'simctl', 'bootstatus', device, '-b'])
        env = os.environ.copy()
        env['TEST_RUNNER_MEOW_SYSTEM_BANNER_SERVER'] = 'http://127.0.0.1:' + str(server.server_port)
        bundle = out / 'banner.xcresult'
        command = ['xcodebuild', 'test', '-project', 'ios/Meow.xcodeproj', '-scheme', 'Meow',
                   '-destination', 'platform=iOS Simulator,id=' + device,
                   '-derivedDataPath', str(out.parent / 'SystemBannerDerivedData'), '-resultBundlePath', str(bundle),
                   '-parallel-testing-enabled', 'NO', '-only-testing:MeowUITests/SystemNotificationUITests',
                   'CODE_SIGN_IDENTITY=-', 'CODE_SIGNING_ALLOWED=YES']
        with (out / 'test.log').open('w') as log:
            result = subprocess.run(command, cwd=ROOT, env=env, stdout=log, stderr=subprocess.STDOUT)
        summary = json.loads(run(['xcrun', 'xcresulttool', 'get', 'test-results', 'summary', '--path', str(bundle)]))
        (out / 'results.json').write_text(json.dumps(summary, indent=2) + '\n')
        raw = out / 'attachments'
        run(['xcrun', 'xcresulttool', 'export', 'attachments', '--path', str(bundle), '--output-path', str(raw)])
        for case in json.loads((raw / 'manifest.json').read_text()):
            for attachment in case.get('attachments', []):
                if attachment.get('suggestedHumanReadableName', '').startswith('system-notification-session-project-body'):
                    shutil.copy2(raw / attachment['exportedFileName'], out / 'system-notification.png')
        if result.returncode or summary['failedTests'] or summary['skippedTests'] or summary['passedTests'] != 1:
            raise SystemExit('System banner verification failed: ' + str(out / 'test.log'))
        if not (out / 'system-notification.png').is_file():
            raise SystemExit('Missing system banner screenshot')
        print('System banner verified: ' + str(out / 'system-notification.png'))
    finally:
        server.shutdown(); server.server_close()
        if device:
            subprocess.run(['xcrun', 'simctl', 'shutdown', device], capture_output=True)
            run(['xcrun', 'simctl', 'delete', device])


if __name__ == '__main__':
    main()
