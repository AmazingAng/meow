#!/usr/bin/env python3
"""Verify iOS fixes against a disposable local service and dedicated simulator.

Requires the existing Xcode command-line tools and Go; no Python dependencies.
Never pass your everyday simulator: --simulator-id must contain 'Meow Fix'.
"""
import argparse
import hashlib
import http.client
import http.server
import json
import os
from pathlib import Path
import shutil
import socket
import subprocess
import tempfile
import threading
import time
import urllib.request

ROOT = Path(__file__).resolve().parents[2]


def command(args, *, cwd=ROOT, env=None, log=None, check=True):
    if log:
        with Path(log).open('w') as stream:
            result = subprocess.run(args, cwd=cwd, env=env, stdout=stream, stderr=subprocess.STDOUT)
        if check and result.returncode:
            raise RuntimeError(f"command failed ({result.returncode}); see {log}")
        return result
    return subprocess.run(args, cwd=cwd, env=env, check=check, capture_output=True, text=True)


def output_json(args):
    return json.loads(command(args).stdout)


def source_hash(root):
    digest = hashlib.sha256()
    for path in sorted(root.rglob('*')):
        if path.is_file() and path.suffix in ('.swift', '.go', '.py', '.yml', '.pbxproj', '.xcscheme', '.xcstrings'):
            digest.update(str(path.relative_to(root)).encode())
            digest.update(path.read_bytes())
    return digest.hexdigest()


def available_port():
    with socket.socket() as sock:
        sock.bind(('127.0.0.1', 0))
        return sock.getsockname()[1]


def api(base, path, key=None, body=None):
    headers = {'Content-Type': 'application/json'}
    if key:
        headers['Authorization'] = 'Bearer ' + key
    request = urllib.request.Request(base + path, headers=headers,
                                    data=None if body is None else json.dumps(body).encode())
    with urllib.request.urlopen(request, timeout=10) as response:
        return json.load(response)


def test(root, device, output, name, selectors, extra_env=None, expect_failure=False):
    bundle = output / (name + '.xcresult')
    env = os.environ.copy()
    if extra_env:
        env.update({'TEST_RUNNER_' + k: v for k, v in extra_env.items()})
    args = ['xcodebuild', 'test', '-project', str(root / 'ios/Meow.xcodeproj'), '-scheme', 'Meow',
            '-destination', 'platform=iOS Simulator,id=' + device,
            '-derivedDataPath', str(output / 'DerivedData'), '-resultBundlePath', str(bundle),
            '-enableCodeCoverage', 'YES', 'CODE_SIGN_IDENTITY=-', 'CODE_SIGNING_ALLOWED=YES', '-quiet']
    args += ['-only-testing:' + selector for selector in selectors]
    result = command(args, cwd=root, env=env, log=output / (name + '.log'), check=False)
    summary = output_json(['xcrun', 'xcresulttool', 'get', 'test-results', 'summary', '--path', str(bundle)])
    if expect_failure:
        # A compiler/infrastructure failure is not a killed behavior mutant.
        if result.returncode != 65 or summary['failedTests'] < 1 or summary['totalTestCount'] < 1:
            raise RuntimeError(f"{name}: mutant did not fail an executed test")
    elif result.returncode or summary['failedTests'] or summary['skippedTests'] or summary['totalTestCount'] < 1:
        raise RuntimeError(f"{name}: tests failed/skipped; see {output / (name + '.log')}")
    print(name + ': ' + summary['result'] + ' (' + str(summary['passedTests']) + ' passed, '
          + str(summary['failedTests']) + ' failed)', flush=True)
    return summary, bundle


def screenshots(bundle, destination, name):
    raw = destination.parent / ('attachments-' + name)
    command(['xcrun', 'xcresulttool', 'export', 'attachments', '--path', str(bundle), '--output-path', str(raw)])
    manifest = json.loads((raw / 'manifest.json').read_text())
    for case in manifest:
        for attachment in case.get('attachments', []):
            suggested = attachment.get('suggestedHumanReadableName', '')
            # Only export explicitly named screenshots, never automatic key-page captures.
            for label in ['onboarding-inbox-loaded', 'inbox', 'settings', 'approval-detail',
                          'approval-rejected', 'custom-choice-detail', 'question-text-reply', 'question-answered',
                          'offline-reply-saved', 'server-switch-choices', 'discard-reply-saved', 'server-switched-after-discard',
                          'key-rotation-confirmation', 'server-editor', 'server-switch-confirmation',
                          'inbox-empty-unread', 'reply-keyboard', 'reply-draft', 'server-switch-chinese',
                          'server-invalid-address', 'onboarding-welcome', 'onboarding-notifications']:
                if suggested == label + '.png' or suggested.startswith(label + '_'):
                    shutil.copy2(raw / attachment['exportedFileName'], destination / (label + '.png'))


def mutants(device, out):
    definitions = [
        ('unsaved-send', 'ios/Meow/App/ReplyQueue.swift',
         'return .notSaved(Self.storageMessage(error))', '// mutant: continue without saving',
         'MeowTests/ReplyQueueTests/testUnwritableQueueDoesNotSendOrClaimPersistence'),
        ('switch-with-pending', 'ios/Meow/App/ReplyQueue.swift',
         'guard items.isEmpty, inFlight == 0, storageError == nil else { return false }',
         'guard storageError == nil else { return false }',
         'MeowTests/SessionRegressionTests/testServerSwitchPreservesPendingReplyAndIdentity'),
        ('registration-server', 'ios/Meow/App/AppModel.swift',
         'guard !isRegistered, !isRegistering, !isChangingServer else { return }',
         'guard !isRegistered, !isChangingServer else { return }',
         'MeowTests/SessionRegressionTests/testServerCannotChangeDuringRegistration'),
        ('stale-inbox', 'ios/Meow/App/AppModel.swift', '        loadGeneration += 1\n        searchTask?.cancel()',
         '        // mutant: accept old inbox\n        searchTask?.cancel()',
         'MeowTests/SessionRegressionTests/testOldInboxResponseCannotRepopulateAfterSwitch'),
        ('discard-persistence', 'ios/Meow/App/ReplyQueue.swift',
         '        try save([])', '        // mutant: skip durable discard',
         'MeowTests/SessionRegressionTests/testFailedDiscardPreservesServerAndDurableReply'),
        ('late-result', 'ios/Meow/App/ReplyQueue.swift',
         '            let question = try await task.value\n            guard deliveryEpoch == epoch else { return .discarded }',
         '            let question = try await task.value\n            // mutant: accept old session result',
         'MeowTests/SessionRegressionTests/testDiscardDoesNotWaitForOrAcceptALateResponse'),
        ('discard-cancellation', 'ios/Meow/App/ReplyQueue.swift',
         '        for entry in active.values { entry.task.cancel() }',
         '        // mutant: leave old sends running',
         'MeowTests/ReplyQueueTests/testDiscardCancelsSendAndStopsOldFlushAfterResume'),
        ('old-flush', 'ios/Meow/App/ReplyQueue.swift',
         '            guard !paused, epoch == batchEpoch else { break }',
         '            guard !paused else { break }',
         'MeowTests/ReplyQueueTests/testDiscardCancelsSendAndStopsOldFlushAfterResume'),
        ('late-network-error', 'ios/Meow/App/ReplyQueue.swift',
         '            guard deliveryEpoch == epoch else { return .discarded }\n            return .queued(reply)',
         '            // mutant: resurrect discarded network failure\n            return .queued(reply)',
         'MeowTests/ReplyQueueTests/testLateNetworkAndPermanentErrorsStayDiscarded'),
        ('first-load', 'ios/Meow/App/AppModel.swift',
         '        hasFinishedOnboarding = true\n        await refresh()',
         '        hasFinishedOnboarding = true\n        // mutant: omit first refresh',
         'MeowTests/SessionRegressionTests/testFinishOnboardingLoadsInboxWithoutSceneTransition'),
    ]
    reports = {}
    with tempfile.TemporaryDirectory(prefix='meow-mutants-') as folder:
        clone = Path(folder) / 'source'
        shutil.copytree(ROOT, clone)
        for name, relative, old, new, selector in definitions:
            path = clone / relative
            original = path.read_text()
            if original.count(old) != 1:
                raise RuntimeError('mutant anchor missing/ambiguous: ' + name)
            try:
                path.write_text(original.replace(old, new, 1))
                reports[name], _ = test(clone, device, out, 'mutant-' + name, [selector], expect_failure=True)
            finally:
                path.write_text(original)
            if source_hash(clone) != source_hash(ROOT):
                raise RuntimeError('mutant restoration changed the source tree')
    return reports


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--simulator-id', required=True)
    parser.add_argument('--output', required=True, type=Path)
    parser.add_argument('--mutations', action='store_true')
    args = parser.parse_args()
    devices = output_json(['xcrun', 'simctl', 'list', 'devices', '--json'])['devices']
    device = next(d for group in devices.values() for d in group if d['udid'] == args.simulator_id)
    if 'Meow Fix' not in device['name']:
        raise RuntimeError('refusing to reset an everyday simulator; create one named Meow Fix Verification')
    if device['state'] != 'Booted':
        command(['xcrun', 'simctl', 'boot', args.simulator_id])
    out = args.output.resolve()
    out.mkdir(parents=True, exist_ok=False)
    report = {'sourceHash': source_hash(ROOT), 'xcode': command(['xcodebuild', '-version']).stdout,
              'go': command(['go', 'version']).stdout, 'simulator': device}
    command(['go', 'test', '-race', './...'], log=out / 'go-test.log')
    report['goRace'] = 'passed'
    report['unit'], unit_bundle = test(ROOT, args.simulator_id, out, 'unit', ['MeowTests'])
    if report['unit']['passedTests'] < 23:
        raise RuntimeError('expected all regression tests, at least 23 unit tests')
    if args.mutations:
        report['mutations'] = mutants(args.simulator_id, out)
    # Clear only this disposable simulator before testing real onboarding.
    apps_text = command(['xcrun', 'simctl', 'listapps', args.simulator_id]).stdout
    apps = json.loads(subprocess.run(['plutil', '-convert', 'json', '-o', '-', '-'],
        input=apps_text, capture_output=True, text=True, check=True).stdout)
    if 'blue.meow.app' in apps:
        command(['xcrun', 'simctl', 'uninstall', args.simulator_id, 'blue.meow.app'])
    command(['xcrun', 'simctl', 'keychain', args.simulator_id, 'reset'])
    command(['go', 'build', '-o', str(out / 'meow-server'), './server/cmd/meow-server'])
    port = available_port()
    env = os.environ.copy()
    env.update(MEOW_ADDR='127.0.0.1:' + str(port), MEOW_DB=str(out / 'test.sqlite'))
    server_log = (out / 'server.log').open('w')
    process = subprocess.Popen([str(out / 'meow-server')], env=env, stdout=server_log, stderr=server_log)
    captured = []
    replies_offline = threading.Event()

    class Proxy(http.server.BaseHTTPRequestHandler):
        def log_message(self, *unused):
            pass

        def forward(self):
            data = self.rfile.read(int(self.headers.get('Content-Length', 0)))
            if replies_offline.is_set() and self.path.endswith('/responses'):
                self.send_response(503)
                self.send_header('Content-Type', 'application/json')
                self.end_headers()
                self.wfile.write(b'{"error":{"code":"offline_test","message":"Temporarily unavailable"}}')
                return
            connection = http.client.HTTPConnection('127.0.0.1', port, timeout=30)
            try:
                headers = {k: v for k, v in self.headers.items() if k.lower() not in ('host', 'connection')}
                try:
                    connection.request(self.command, self.path, body=data, headers=headers)
                except OSError:
                    self.send_error(503)
                    return
                response = connection.getresponse()
                body = response.read()
                if self.path == '/v1/devices' and response.status == 201:
                    captured.append(json.loads(body))  # memory only; never log bearer credentials
                self.send_response(response.status)
                self.send_header('Content-Type', 'application/json')
                self.send_header('Content-Length', str(len(body)))
                self.end_headers()
                self.wfile.write(body)
            finally:
                connection.close()

        do_GET = do_POST = do_PATCH = do_DELETE = forward

    proxy = http.server.ThreadingHTTPServer(('127.0.0.1', 0), Proxy)
    thread = threading.Thread(target=proxy.serve_forever, daemon=True)
    thread.start()
    base = 'http://127.0.0.1:' + str(proxy.server_port)
    try:
        for _ in range(100):
            try:
                api(base, '/healthz')
                break
            except (OSError, http.client.HTTPException):
                time.sleep(.1)
        else:
            raise RuntimeError('local server did not start')
        report['onboarding'], onboard_bundle = test(ROOT, args.simulator_id, out, 'onboarding',
            ['MeowUITests/OnboardingRegressionUITests'], {'MEOW_UITEST_SERVER': base})
        if not captured:
            raise RuntimeError('app never registered through the real API')
        key = captured[-1]['key']['secret']
        questions = {}
        for name, choices in [('text', None), ('approval', ['approve', 'reject']), ('choice', ['postgres', 'sqlite'])]:
            body = {'question': {'text': 'What should I work on next?', 'approval': 'Deploy to production?',
                                 'choice': 'Which database?'}[name],
                    'body': 'Project: services/api\nAll tests passed.\nSimulator verification only.',
                    'source': 'Claude Code' if name == 'approval' else 'Codex', 'timeout': 86400}
            if choices:
                body['choices'] = choices
            questions[name] = api(base, '/v1/questions', key, body)
        api(base, '/v1/notifications', key, {'title': 'UI test: build completed', 'source': 'Build bot',
                                           'body': 'Project: meow/ios\nAll tests passed.'})
        answer = 'Continue with the iOS integration tests.'
        report['flows'], flows_bundle = test(ROOT, args.simulator_id, out, 'flows',
            ['MeowUITests/AnswerFlowUITests', 'MeowUITests/ScreenFlowUITests'],
            {'MEOW_UITEST_NOTIFICATION': questions['text']['notification_id'], 'MEOW_UITEST_ANSWER': answer,
             'MEOW_UITEST_APPROVAL_NOTIFICATION': questions['approval']['notification_id'],
             'MEOW_UITEST_CHOICE_NOTIFICATION': questions['choice']['notification_id']})
        for name, expected in [('text', answer), ('approval', 'reject'), ('choice', 'sqlite')]:
            result = api(base, '/v1/questions/' + questions[name]['id'], key)
            if result['answer'] != expected:
                raise RuntimeError('the server did not receive the exact ' + name + ' answer')
        report['realAPIAnswers'] = 'text/approval/custom choice verified'
        composer = api(base, '/v1/questions', key, {'question': 'Review the updated interface',
            'body': 'Check the reply controls and keyboard without losing the draft.',
            'source': 'Meow', 'timeout': 86400})
        report['design'], design_bundle = test(ROOT, args.simulator_id, out, 'design',
            ['MeowUITests/DesignRegressionUITests'],
            {'MEOW_UITEST_NOTIFICATION': questions['text']['notification_id'],
             'MEOW_UITEST_COMPOSER_NOTIFICATION': composer['notification_id']})
        offline = api(base, '/v1/questions', key, {'question': 'Offline reply verification',
            'body': 'This answer must survive choosing Keep waiting and app restart.', 'timeout': 86400})
        replies_offline.set()
        report['offline'], offline_bundle = test(ROOT, args.simulator_id, out, 'offline',
            ['MeowUITests/OfflineReplyUITests'],
            {'MEOW_UITEST_OFFLINE_NOTIFICATION': offline['notification_id']})
        replies_offline.clear()
        command(['xcrun', 'simctl', 'launch', args.simulator_id, 'blue.meow.app'])
        for _ in range(100):
            result = api(base, '/v1/questions/' + offline['id'], key)
            if result['answer'] == 'Keep this queued answer.':
                break
            time.sleep(.1)
        else:
            raise RuntimeError('the offline answer did not survive restart and retry')
        report['offlineRestart'] = 'saved reply retried after restart with original identity'
        discard = api(base, '/v1/questions', key, {'question': 'Discard reply verification',
            'body': 'Explicitly discard this local answer before switching servers.', 'timeout': 86400})
        replies_offline.set()
        report['discard'], discard_bundle = test(ROOT, args.simulator_id, out, 'discard',
            ['MeowUITests/DiscardReplyUITests'],
            {'MEOW_UITEST_OFFLINE_NOTIFICATION': discard['notification_id']})
        replies_offline.clear()
        group = command(['xcrun', 'simctl', 'get_app_container', args.simulator_id,
                         'blue.meow.app', 'group.blue.meow.app']).stdout.strip()
        # Check the exact App Group queue after switching and relaunching.
        queue_file = Path(group) / 'pending-replies.json'
        if json.loads(queue_file.read_text()) != []:
            raise RuntimeError('discard did not durably clear the simulator reply queue')
        result = api(base, '/v1/questions/' + discard['id'], key)
        if result['answer'] is not None:
            raise RuntimeError('discarded offline reply unexpectedly reached the old server')
        report['discardSwitch'] = 'onboarding survives relaunch; durable queue empty; old server unanswered'
        destination = out / 'screenshots'
        destination.mkdir()
        screenshots(onboard_bundle, destination, 'onboarding')
        screenshots(flows_bundle, destination, 'flows')
        screenshots(design_bundle, destination, 'design')
        screenshots(offline_bundle, destination, 'offline')
        screenshots(discard_bundle, destination, 'discard')
        report['screenshots'] = sorted(p.name for p in destination.glob('*.png'))
        required_screenshots = {'onboarding-welcome', 'onboarding-notifications', 'onboarding-inbox-loaded',
            'inbox', 'settings', 'approval-detail', 'approval-rejected', 'custom-choice-detail',
            'question-text-reply', 'question-answered', 'offline-reply-saved', 'server-switch-choices',
            'discard-reply-saved', 'server-switched-after-discard', 'key-rotation-confirmation',
            'server-editor', 'server-switch-confirmation', 'inbox-empty-unread', 'reply-keyboard',
            'reply-draft', 'server-switch-chinese', 'server-invalid-address'}
        if set(report['screenshots']) != {name + '.png' for name in required_screenshots}:
            raise RuntimeError('expected every named UI screenshot; missing or unexpected attachments')
        report['coverage'] = {}
        for name, bundle in [('unit', unit_bundle), ('onboarding', onboard_bundle), ('flows', flows_bundle), ('design', design_bundle), ('offline', offline_bundle), ('discard', discard_bundle)]:
            coverage = output_json(['xcrun', 'xccov', 'view', '--report', '--json', str(bundle)])
            report['coverage'][name] = [
                {k: f[k] for k in ['name', 'coveredLines', 'executableLines', 'lineCoverage']}
                for target in coverage['targets'] if target['name'] == 'Meow.app'
                for f in target['files'] if f['name'] in ('ReplyQueue.swift', 'AppModel.swift')]
        # Fail closed if Xcode omitted the instrumented core files.
        if not report['coverage']['unit']:
            raise RuntimeError('core-file coverage was not produced')
        report['sourceHashAfter'] = source_hash(ROOT)
        if report['sourceHash'] != report['sourceHashAfter']:
            raise RuntimeError('source files changed during verification')
        (out / 'summary.json').write_text(json.dumps(report, indent=2))
        print('Verified; screenshots and summary: ' + str(out), flush=True)
    finally:
        proxy.shutdown()
        proxy.server_close()
        process.terminate()
        process.wait(timeout=10)
        server_log.close()


if __name__ == '__main__':
    main()
