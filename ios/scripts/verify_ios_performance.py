#!/usr/bin/env python3
"""Verify iOS performance fixes with isolated credentials, storage and HTTP.
Run: python3 -B ios/scripts/verify_ios_performance.py --output dist/ios-performance/final
"""
import argparse
import hashlib
import importlib.util
import json
from pathlib import Path
import plistlib
import re
import shutil
import subprocess

ROOT = Path(__file__).resolve().parents[2]
SWIFT_FILTER = 'InboxDeltaTests|InboxPerformanceTests|NotificationLatencyTests'


def require(ok, message):
    if not ok:
        raise RuntimeError(message)


def package(out):
    spec = importlib.util.spec_from_file_location('performance_native', ROOT / 'ios/scripts/verify_frontend_recovery.py')
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    module.package(out)
    for name in ['InboxPerformanceTests.swift', 'InboxDeltaTests.swift', 'NotificationLatencyTests.swift', 'InboxDeletionTests.swift', 'CacheRetentionTests.swift']:
        shutil.copy2(ROOT / 'ios/MeowTests' / name, out / 'package/Tests' / name)
    for name in ['InboxProjection.swift']:
        path = ROOT / 'ios/Meow/Models' / name
        if path.exists():
            shutil.copy2(path, out / 'package/Sources' / name)
    # SwiftPM's executable has no iOS Info.plist. Adapt only that metadata
    # boundary to the actual app configuration; do not rewrite test assertions.
    info = ROOT / 'ios/Meow/Info.plist'
    plistlib.loads(info.read_bytes())['MeowDefaultServer']
    shutil.copy2(info, out / 'AppInfo.plist')
    path = out / 'package/Sources/Shared.swift'
    text = path.read_text()
    old = 'Bundle.main.object(forInfoDictionaryKey: "MeowDefaultServer")'
    require(text.count(old) == 1, 'missing app metadata boundary')
    path.write_text(text.replace(old, 'PerformanceBundle.object(forInfoDictionaryKey: "MeowDefaultServer")'))
    (out / 'package/Sources/PerformanceBundle.swift').write_text('''import Foundation
enum PerformanceBundle {
 static let info = try! PropertyListSerialization.propertyList(from: Data(contentsOf: URL(fileURLWithPath: ''' + json.dumps(str(out / 'AppInfo.plist')) + ''')), format: nil) as! [String: Any]
 static func object(forInfoDictionaryKey key: String) -> Any? { info[key] }
}
''')


def native(out, test_filter=None):
    command = ['swift', 'test', '-c', 'release', '--package-path', str(out / 'package'), '--enable-code-coverage']
    if test_filter:
        command += ['--filter', test_filter]
    with (out / 'native.log').open('w') as stream:
        result = subprocess.run(command, cwd=ROOT, stdout=stream, stderr=subprocess.STDOUT)
    text = (out / 'native.log').read_text()
    cases = re.findall(r"Test Case '-\[([^]]+)\]' (passed|failed|skipped) ", text)
    summary = {'exit': result.returncode, 'tests': len(cases), 'failed': [n for n, state in cases if state == 'failed']}
    (out / 'native-summary.json').write_text(json.dumps(summary, indent=2) + '\n')
    require(cases and not any(state == 'skipped' for _, state in cases), 'missing or skipped native tests')
    require(result.returncode == 0 and not summary['failed'], 'native regressions failed: ' + str(out / 'native.log'))
    return summary


def run(command, out, name, cwd=ROOT, timeout=1200):
    print('Running:', name, flush=True)
    result = subprocess.run(command, cwd=cwd, capture_output=True, text=True, timeout=timeout)
    text = result.stdout + result.stderr
    (out / (name + '.log')).write_text(text)
    require(result.returncode == 0, name + ' failed; see ' + str(out))
    return text


def state():
    paths = subprocess.check_output(['git', 'ls-files', '-co', '--exclude-standard', 'ios', 'server',
                                     'message', 'adapters', 'client', 'cli', 'go.mod', 'go.sum', 'contracts'], cwd=ROOT, text=True).splitlines()
    return {name: hashlib.sha256((ROOT / name).read_bytes()).hexdigest()
            for name in sorted(set(paths)) if (ROOT / name).is_file()}


def checked_coverage(records):
    require(set(records) == {'InboxProjection.swift', 'InboxStore.writeChanges', 'selected_read.go:api',
                             'selected_read.go:store'}, 'missing coverage scope')
    require(all(rows for rows in records.values()), 'empty coverage scope')
    require(all(n > 0 for rows in records.values() for n in rows), 'unexecuted scoped logic')
    return {name: {'covered': len(rows), 'executable': len(rows)} for name, rows in records.items()}


def coverage(out):
    build = out / 'package/.build/arm64-apple-macosx/release'
    raw = run(['xcrun', 'llvm-cov', 'export', '-format=lcov',
               '-instr-profile=' + str(build / 'codecov/default.profdata'),
               str(build / 'MeowFailuresPackageTests.xctest/Contents/MacOS/MeowFailuresPackageTests')], out, 'coverage')
    records = {}
    for block in raw.split('end_of_record'):
        names = re.findall(r'^SF:(.+)$', block, re.M)
        if not names:
            continue
        name = Path(names[0]).name
        counts = {int(n): int(c) for n, c in re.findall(r'^DA:(\d+),(\d+)', block, re.M)}
        if name == 'InboxProjection.swift':
            records[name] = list(counts.values())
        elif name == 'InboxStore.swift':
            lines = Path(names[0]).read_text().splitlines()
            start = next(i + 1 for i, line in enumerate(lines) if 'private func writeChanges(' in line)
            end = next(i + 1 for i, line in enumerate(lines) if 'private func write(' in line)
            records['InboxStore.writeChanges'] = [c for n, c in counts.items() if start <= n < end]
    for line in (out / 'go.cover').read_text().splitlines():
        match = re.match(r'.*/server/internal/(api|store)/selected_read.go:\S+ (\d+) (\d+)$', line)
        if match:
            kind, statements, count = match.groups()
            records.setdefault('selected_read.go:' + kind, []).extend([int(count)] * int(statements))
    result = checked_coverage(records)
    for corrupt in [{}, {name: [0] * len(rows) for name, rows in records.items()}]:
        try:
            checked_coverage(corrupt)
        except RuntimeError:
            pass
        else:
            raise RuntimeError('coverage checker accepted a known-bad control')
    # Removing the defense must itself be detected by the same control pattern.
    def rejected(checker):
        try:
            checker({})
        except RuntimeError:
            return True
        return False
    require(rejected(checked_coverage) and not rejected(lambda _: {}), 'coverage controls are vacuous')
    return {'scopes': result, 'controls': 3,
            'limits': 'Executable line counts for new Swift projection/delta logic and Go statements. Not branch coverage or complete AppModel/UI coverage.'}


def mutations(out):
    cases = [
        ('snapshot-loss', 'InboxStore.swift',
         'try self.writeChanges(items, deleting: deleting, scope: scope)', 'try self.write(items, scope: scope)', 'InboxDeltaTests'),
        ('failed-dirty-ack', 'AppModel.swift', 'if case .success = result {', 'if true {',
         'testFailedDeltaIsRetriedWithTheNextChange'),
        ('local-only-network', 'AppModel.swift',
         'let remote = serverMembershipKnown ? targets.filter { serverNotificationIDs.contains($0) } : targets',
         'let remote = targets', 'testBatchReadSkipsLocalOnlyHistoryAndChunksSelectedIDs'),
        ('metadata-delay', 'NotificationDelivery.swift', 'Task.sleep(for: .milliseconds(250))',
         'Task.sleep(for: .seconds(2))', 'testVerifiedBodyDoesNotWaitForCancellationIgnoringAuxiliaries'),
        ('lost-rollback', 'InboxStore.swift', 'do { try execute("ROLLBACK") }', 'do { try execute("COMMIT") }',
         'testDeltaRollbackAndEmptyScope'),
        ('lost-badge', 'NotificationDelivery.swift', 'updated.badge = NSNumber(value: unread)',
         'updated.badge = nil', 'testFastAuxiliariesApplyBadgeAndReport'),
    ]
    results = []
    for name, file, old, new, test_filter in cases:
        path = out / 'package/Sources' / file
        original = path.read_text()
        # applyChanges and enqueueChanges intentionally share this operation.
        count = original.count(old)
        require(count == (2 if name == 'snapshot-loss' else 1), 'ambiguous mutant: ' + name)
        try:
            changed = original.replace(old, new)
            path.write_text(changed)
            require(path.read_text() == changed and changed != original, 'mutant was not written')
            command = ['swift', 'test', '-c', 'release', '--package-path', str(out / 'package'), '--filter', test_filter]
            result = subprocess.run(command, cwd=ROOT, capture_output=True, text=True, timeout=300)
            log = result.stdout + result.stderr
            (out / ('mutant-' + name + '.log')).write_text(log)
            failed = re.findall(r"Test Case '-\[([^]]+)\]' failed", log)
            require(result.returncode != 0 and failed and 'Compiling Meow ' in log, 'mutant survived or did not execute: ' + name)
            results.append({'name': name, 'sha256': hashlib.sha256(changed.encode()).hexdigest(), 'failed': failed})
            print('Killed mutant:', name, flush=True)
        finally:
            path.write_text(original)
        require(path.read_text() == original, 'mutant restore failed')
    # Public authorization must be tested through the real HTTP/store boundary.
    folder = out / 'go-mutant'
    folder.mkdir()
    for name in ['go.mod', 'go.sum']:
        shutil.copy2(ROOT / name, folder / name)
    for name in ['server', 'client', 'message', 'adapters']:
        shutil.copytree(ROOT / name, folder / name)
    path = folder / 'server/internal/store/selected_read.go'
    original = path.read_text()
    require(original.count('WHERE device_id=? AND') == 1, 'missing scope mutant')
    changed = original.replace('WHERE device_id=? AND', 'WHERE device_id!=? AND')
    path.write_text(changed)
    command = ['go', 'test', './server/internal/api', '-run', '^TestSelectedReadAuthorizationIsolationAndReplay$', '-count=1']
    result = subprocess.run(command, cwd=folder, capture_output=True, text=True, timeout=180)
    log = result.stdout + result.stderr
    (out / 'mutant-device-isolation.log').write_text(log)
    require(result.returncode != 0 and '--- FAIL: TestSelectedReadAuthorizationIsolationAndReplay' in log,
            'device-isolation mutant survived or failed to run')
    results.append({'name': 'device-isolation', 'sha256': hashlib.sha256(changed.encode()).hexdigest()})
    return {'killed': len(results), 'total': len(cases) + 1, 'details': results}


def full(out, native_result):
    result = {'native': native_result}
    result['toolchains'] = {name: run(command, out, 'version-' + name) for name, command in [
        ('swift', ['swift', '--version']), ('xcode', ['xcodebuild', '-version']),
        ('go', ['go', 'version']), ('python', ['python3', '--version'])]}
    before = state()
    raw = run(['go', 'test', '-race', '-shuffle=20261029', '-count=1', '-json',
               '-coverprofile=' + str(out / 'go.cover'), './...'], out, 'go-tests')
    events = [json.loads(line) for line in raw.splitlines() if line.startswith('{')]
    require(not any(e.get('Action') == 'fail' for e in events), 'Go failure events')
    result['go'] = {'test_cases': sum(e.get('Action') == 'pass' and 'Test' in e for e in events),
                    'top_level': sum(e.get('Action') == 'pass' and 'Test' in e and '/' not in e['Test'] for e in events)}
    result['coverage'] = coverage(out)
    run(['go', 'vet', './...'], out, 'go-vet')
    run(['go', 'build', '-o', str(out / 'meow-server'), './server/cmd/meow-server'], out, 'go-build')
    run(['python3', '-B', 'scripts/test_install.py'], out, 'installer')
    run(['git', 'diff', '--check'], out, 'diff-check')
    result['mutations'] = mutations(out)
    result['restored_native'] = native(out)
    tests = ['test08CompactHeaderSearchAndViewMenu',
             'test21GroupSearchReadsOnlyMatchingMessagesAndModeSurvivesRestart',
             'test27InboxReadHeaderAndGroupSwipeActions', 'test30ReadActionsLargeChinese']
    result['simulator'] = []
    # Each UI case needs a fresh app database/keychain, because server reset
    # intentionally cannot erase the phone's durable read state/history.
    for i, test in enumerate(tests):
        destination = out / ('simulator-' + str(i + 1))
        command = ['python3', '-B', 'ios/scripts/verify_ui_optimization.py', '--output', str(destination),
                   '--coverage', '--test-case', test]
        if i:
            command += ['--ui-only']
        run(command, out, 'simulator-' + str(i + 1), timeout=1800)
        result['simulator'].append(json.loads((destination / 'results.json').read_text()))
    require(state() == before, 'source changed during final verification')
    result['source_sha256'] = before
    result['limitations'] = ['No real APNs/phone test, TestFlight upload or deployment.',
                            'macOS native harness adapts UI/Keychain/AppGroup/Bundle boundaries; simulator uses real iOS SDK.',
                            'Independent review not performed; spec approval not obtained (autonomous run).',
                            'No dependency change; no new supply-chain audit. No standalone Swift property-test library; deterministic generated cases used.']
    (out / 'report.json').write_text(json.dumps(result, indent=2) + '\n')
    print('Verification passed:', out / 'report.json', flush=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--output', required=True, type=Path)
    parser.add_argument('--native-only', action='store_true')
    parser.add_argument('--test-filter')
    args = parser.parse_args()
    out = args.output.resolve()
    out.mkdir(parents=True, exist_ok=False)
    package(out)
    result = native(out, args.test_filter)
    print(json.dumps(result), flush=True)
    if not args.native_only:
        require(not args.test_filter, 'a filtered native run cannot be full verification')
        full(out, result)


if __name__ == '__main__':
    main()
