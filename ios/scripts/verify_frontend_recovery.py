#!/usr/bin/env python3
"""Exercise AppModel and durable queues on macOS without booting a simulator."""
import argparse
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import socket
import time
import urllib.request

ROOT = Path(__file__).resolve().parents[2]
spec = importlib.util.spec_from_file_location('failures', ROOT / 'ios/scripts/verify_notification_failures.py')
base = importlib.util.module_from_spec(spec)
spec.loader.exec_module(base)


def package(output):
    path = output / 'package'
    base.package(path)
    # Keep configuration parsing unchanged. Replace only real credential and
    # App Group storage, so tests never alter the user's macOS keychain/defaults.
    shared = path / 'Sources/Shared.swift'
    text = shared.read_text().split('/// Generic-password Keychain')[0]
    text = text.replace('"group.blue.meow.app"', '"group.blue.meow.native-recovery-tests"')
    text, count = re.subn(r'    static var containerURL: URL \{.*?^    \}',
                         '    static var containerURL: URL {\n        URL(fileURLWithPath: ' +
                         json.dumps(str(output / 'isolated-storage'), ensure_ascii=False) + ')\n    }',
                         text, flags=re.M | re.S)
    if count != 1:
        raise RuntimeError('could not isolate App Group filesystem boundary')
    shared.write_text(text)
    model = (ROOT / 'ios/Meow/App/AppModel.swift').read_text()
    model = model.replace('import UIKit\n', '// UIKit is adapted by Platform.swift.\n')
    model = model.replace('UNUserNotificationCenter.current()', 'RecoveryNotificationCenter.current()')
    model = model.replace('UNAuthorizationStatus', 'RecoveryAuthorizationStatus')
    (path / 'Sources/AppModel.swift').write_text(model)
    shutil.copy2(ROOT / 'ios/Meow/App/InboxStore.swift', path / 'Sources/InboxStore.swift')
    cache = path / 'Sources/InboxStore.swift'
    cache.write_text(cache.read_text().replace(
        'FileManager.default.urls(for: .applicationSupportDirectory, in: .userDomainMask)[0]',
        'URL(fileURLWithPath: ' + json.dumps(str(output / 'isolated-storage'), ensure_ascii=False) + ')'))
    shutil.copy2(ROOT / 'ios/scripts/frontend_recovery/Platform.swift', path / 'Sources/Platform.swift')
    shutil.copy2(ROOT / 'ios/MeowTests/FrontendRecoveryTests.swift', path / 'Tests/FrontendRecoveryTests.swift')
    shutil.copy2(ROOT / 'ios/Meow/Views/InboxPresentation.swift', path / 'Sources/InboxPresentation.swift')
    shutil.copy2(ROOT / 'ios/Meow/Models/InboxGrouping.swift', path / 'Sources/InboxGrouping.swift')
    for name in ['InboxPresentationTests.swift', 'PendingInboxTests.swift', 'LocalInboxTests.swift', 'OnboardingDetectionTests.swift', 'InboxGroupingTests.swift', 'InboxGroupingModelTests.swift', 'InboxSessionIdentityTests.swift']:
        shutil.copy2(ROOT / 'ios/MeowTests' / name, path / 'Tests' / name)
    original = (ROOT / 'ios/MeowTests/MeowTests.swift').read_text()
    header = r'^(?:private final class ReviewURLProtocol[^\n]+|final class SessionRegressionTests: XCTestCase|extension SessionRegressionTests|final class OnboardingCommandTests: XCTestCase) \{'
    blocks = re.findall(header + r'.*?^\}', original, re.M | re.S)
    if len(blocks) != len(re.findall(header, original, re.M)) or len(blocks) < 4:
        raise RuntimeError('incomplete original model regression extraction')
    code = '\n\n'.join(blocks).replace('final class SessionRegressionTests:', '@MainActor\nfinal class SessionRegressionTests:')
    (path / 'Tests/OriginalModelTests.swift').write_text('import XCTest\n@testable import Meow\n' + code)
    shutil.copy2(ROOT / 'ios/MeowTests/GatewaySessionTests.swift', path / 'Tests/GatewaySessionTests.swift')
    components = (ROOT / 'ios/Meow/Views/Components.swift').read_text()
    blocks = re.findall(r'^(?:enum AvatarStyle|struct ExpiryCountdown: View) \{.*?^\}', components, re.M | re.S)
    if len(blocks) != 2:
        raise RuntimeError('incomplete unchanged display helper extraction')
    (path / 'Sources/DisplayHelpers.swift').write_text('import SwiftUI\n' + '\n\n'.join(blocks))
    model_tests = re.findall(r'^final class ModelTests: XCTestCase \{.*?^\}', original, re.M | re.S)
    if len(model_tests) != 1:
        raise RuntimeError('missing original model tests')
    (path / 'Tests/OriginalDisplayTests.swift').write_text('import XCTest\n@testable import Meow\n' + model_tests[0])
    manifest = path / 'Package.swift'
    manifest.write_text(manifest.read_text().replace('path: "Sources")', 'path: "Sources", resources: [.copy("meow.wav")])'))
    shutil.copy2(ROOT / 'ios/Meow/Resources/Sounds/meow.wav', path / 'Sources/meow.wav')
    sound = (ROOT / 'ios/MeowTests/NotificationSoundTests.swift').read_text().replace('Bundle.main.url', 'Bundle.module.url')
    (path / 'Tests/NotificationSoundTests.swift').write_text(sound)


def coverage_gate(records, manifest):
    result = {}
    for name, changed in manifest.items():
        if name not in records:
            raise RuntimeError('missing coverage: ' + name)
        source = (ROOT / name).read_text().splitlines()
        for line, expected in changed.items():
            if source[int(line) - 1] != expected:
                raise RuntimeError('stale changed-line manifest: ' + name)
        executable = {int(n): records[name][int(n)] for n in changed if int(n) in records[name]}
        if not executable or any(value == 0 for value in executable.values()):
            raise RuntimeError(f'uncovered changed lines: {name}: {[n for n, v in executable.items() if v == 0]}')
        result[name] = {'covered': len(executable), 'executable': len(executable),
                        'nonexecutable_changed_lines': len(changed) - len(executable)}
    if len(result) != 2:
        raise RuntimeError('incomplete AppModel/ReplyQueue coverage scope')
    return result


def negative_controls(records, manifest, gate=coverage_gate):
    for bad, expected in [({}, 'missing coverage'),
                          ({n: {line: 0 for line in rows} for n, rows in records.items()}, 'uncovered changed')]:
        try:
            gate(bad, manifest)
        except RuntimeError as error:
            if expected not in str(error):
                raise
        else:
            raise RuntimeError('coverage negative control accepted')


def coverage(output):
    build = output / 'package/.build/arm64-apple-macosx/debug'
    binary = build / 'MeowFailuresPackageTests.xctest/Contents/MacOS/MeowFailuresPackageTests'
    base.run(['xcrun', 'llvm-cov', 'export', '-format=lcov',
              '-instr-profile=' + str(build / 'codecov/default.profdata'), str(binary)], output / 'coverage.lcov')
    records = {}
    names = ['ios/Meow/App/AppModel.swift', 'ios/Meow/App/ReplyQueue.swift']
    for record in (output / 'coverage.lcov').read_text().split('end_of_record'):
        paths = re.findall(r'^SF:(.+)$', record, re.M)
        if not paths:
            continue
        for name in names:
            if Path(paths[0]).name == Path(name).name:
                records[name] = {int(line): int(count) for line, count in re.findall(r'^DA:(\d+),(\d+)', record, re.M)}
    manifest = json.loads((ROOT / 'docs/verification/frontend-recovery-changed-lines.json').read_text())
    result = coverage_gate(records, manifest)
    negative_controls(records, manifest)
    try:
        negative_controls(records, manifest, gate=lambda *_: {})
    except RuntimeError as error:
        if 'negative control accepted' not in str(error):
            raise
    else:
        raise RuntimeError('negative controls are vacuous with their defense removed')
    return {'files': result, 'negative_controls_rejected': 2, 'negative_control_defense_removed_rejected': True,
            'unmeasured': ['SwiftUI view bodies: real iOS SDK compile only', 'UIKit/notification-center/keychain boundary adapters']}


def mutations(output):
    mutants = [
        ('failure-feedback', 'AppModel.swift', 'alert = reasons.joined(separator: "\\n")', 'alert = nil'),
        ('durable-failure', 'ReplyQueue.swift',
         'failed.failure = .init(status: error.status, code: error.code, message: error.message)', 'failed.failure = nil'),
        ('same-server-recovery', 'AppModel.swift',
         'guard !isRegistering, !isChangingServer else { return false }',
         'guard !isRegistering, !isChangingServer, server != newServer else { return false }'),
        ('read-rollback', 'AppModel.swift', 'current.readAt == readAt', 'current.readAt == Date.distantPast'),
        ('read-new-session', 'AppModel.swift',
         'guard generation == sessionGeneration, readRequests[id] == request else { return false }', 'guard true else { return false }'),
        ('read-new-counts', 'AppModel.swift', 'if revision == countsRevision { counts.unread += unreadDelta }',
         'if true { counts.unread += unreadDelta }'),
        ('read-mark-all', 'AppModel.swift', '            readRequests = [:]\n', '            // no read invalidation\n'),
    ]
    results = []
    for name, file, old, new in mutants:
        path = output / 'package/Sources' / file
        original = path.read_bytes()
        if original.decode().count(old) != 1:
            raise RuntimeError('ambiguous mutant: ' + name)
        changed = original.decode().replace(old, new).encode()
        try:
            path.write_bytes(changed)
            assert path.read_bytes() == changed and changed != original
            shutil.rmtree(output / 'package/.build')
            result = base.native(output, 'mutant-' + name, test_filter='FrontendRecoveryTests', check=False)
            log = (output / f'mutant-{name}.log').read_text()
            if not result['exit'] or not result['failures'] or 'Compiling Meow ' + file not in log:
                raise RuntimeError('mutant survived or not freshly compiled: ' + name)
            results.append({'name': name, 'sha256': hashlib.sha256(changed).hexdigest(), 'failed_tests': result['failures']})
            print('Mutation rejected:', name, flush=True)
        finally:
            path.write_bytes(original)
        if path.read_bytes() != original:
            raise RuntimeError('mutant restoration failed')
    shutil.rmtree(output / 'package/.build')
    restored = base.native(output, 'restored')
    if restored['failures']:
        raise RuntimeError('restored suite not green')
    return {'killed': len(results), 'total': len(mutants), 'details': results, 'restored': restored}


def full(output, result):
    result['source_sha256'] = base.state_hash()
    result['tools'] = {name: subprocess.check_output(cmd, text=True).strip() for name, cmd in [
        ('swift', ['swift', '--version']), ('xcode', ['xcodebuild', '-version']), ('xcodegen', ['xcodegen', '--version'])]}
    result['coverage'] = coverage(output)
    result['mutations'] = mutations(output)
    base.run(['xcodegen', 'generate', '--spec', 'ios/project.yml'], output / 'xcodegen.log')
    base.run(['xcodebuild', 'build-for-testing', '-project', 'ios/Meow.xcodeproj', '-scheme', 'Meow',
              '-destination', 'generic/platform=iOS', '-derivedDataPath', str(output / 'ios-build'),
              'CODE_SIGNING_ALLOWED=NO'], output / 'ios-build.log')
    result['ios_compile'] = 'passed (main app, extension, all unit and UI test targets)'
    if base.state_hash() != result['source_sha256']:
        raise RuntimeError('source changed during verification')
    result['physical_runtime'] = 'not performed; no simulator boot, no phone app replacement'
    result['independent_verification'] = 'not performed (autonomous run)'
    return result


def native(output, test_filter=None):
    result = base.native(output, test_filter=test_filter, check=False)
    if not test_filter:
        expected = sum(len(re.findall(r'^\s+func test\w+\(', p.read_text(), re.M))
                       for p in (output / 'package/Tests').glob('*.swift'))
        if result['tests'] != expected:
            raise RuntimeError(f'incomplete XCTest run: expected {expected}, got {result["tests"]}')
    return result


def execute(output, args):
    result = native(output, args.filter)
    if args.expect_red:
        if not result['exit'] or not result['failures']:
            raise RuntimeError('RED requires actual assertion failures')
    elif result['exit'] or result['failures']:
        raise RuntimeError(f'tests failed: {result}')
    if args.full:
        result = full(output, result)
        result['live_integration'] = 'passed against isolated real Go server, ephemeral local port/database'
    (output / 'results.json').write_text(json.dumps(result, indent=2) + '\n')
    print(json.dumps(result, indent=2))


def integration_server(output, args):
    base.run(['go', 'build', '-o', str(output / 'meow-server'), './server/cmd/meow-server'], output / 'go-build.log')
    with socket.socket() as listener:
        listener.bind(('127.0.0.1', 0))
        port = listener.getsockname()[1]
    env = {key: value for key, value in os.environ.items() if not key.startswith('MEOW_')}
    env.update(MEOW_ADDR=f'127.0.0.1:{port}', MEOW_DB=str(output / 'integration.db'))
    with (output / 'integration-server.log').open('w') as log:
        process = subprocess.Popen([str(output / 'meow-server')], cwd=ROOT, env=env, stdout=log, stderr=subprocess.STDOUT)
        try:
            origin = f'http://127.0.0.1:{port}'
            for _ in range(80):
                if process.poll() is not None:
                    raise RuntimeError('integration server stopped unexpectedly')
                try:
                    with urllib.request.urlopen(origin + '/healthz', timeout=1) as response:
                        if response.status != 200:
                            raise RuntimeError('integration health check failed')
                    break
                except OSError:
                    time.sleep(.1)
            else:
                raise RuntimeError('integration server never became ready')
            shutil.copy2(ROOT / 'ios/scripts/frontend_recovery/LiveIntegrationTests.swift', output / 'package/Tests/LiveIntegrationTests.swift')
            os.environ['MEOW_RECOVERY_INTEGRATION_URL'] = origin
            execute(output, args)
        finally:
            process.terminate()
            try:
                process.wait(timeout=5)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait(timeout=5)
            os.environ.pop('MEOW_RECOVERY_INTEGRATION_URL', None)


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--output', required=True, type=Path)
    p.add_argument('--expect-red', action='store_true')
    p.add_argument('--filter')
    p.add_argument('--full', action='store_true')
    args = p.parse_args()
    output = args.output.resolve()
    output.mkdir(parents=True, exist_ok=False)
    package(output)
    if args.full:
        if args.expect_red or args.filter:
            raise RuntimeError('full verification needs an unfiltered green suite')
        integration_server(output, args)
    else:
        execute(output, args)


if __name__ == '__main__':
    main()
