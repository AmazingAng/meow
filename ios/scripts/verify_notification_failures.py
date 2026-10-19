#!/usr/bin/env python3
"""Run real shared Swift sources on macOS; never install an app or boot a simulator."""
import argparse
import hashlib
import json
from pathlib import Path
import re
import shutil
import subprocess

ROOT = Path(__file__).resolve().parents[2]
SOURCES = list((ROOT / 'ios/Shared').glob('*.swift')) + [ROOT / 'ios/Meow/App/ReplyQueue.swift']
TESTS = ['GatewayTests.swift', 'NotificationFailureTests.swift', 'ReplyFailureTests.swift', 'NotificationCallbackTests.swift']
EXPECTED_LAYERS = {'native', 'coverage', 'mutations', 'go', 'go_vet', 'ios_compile'}


def run(args, log, *, cwd=ROOT, check=True):
    with log.open('w') as stream:
        result = subprocess.run(args, cwd=cwd, stdout=stream, stderr=subprocess.STDOUT)
    if check and result.returncode:
        raise RuntimeError(f'{args[0]} failed ({result.returncode}); see {log}')
    return result.returncode


def package(path):
    (path / 'Sources').mkdir(parents=True)
    (path / 'Tests').mkdir()
    (path / 'Package.swift').write_text('''// swift-tools-version: 6.0
import PackageDescription
let package = Package(name: "MeowFailures", platforms: [.macOS(.v14)], targets: [
    .target(name: "Meow", path: "Sources"),
    .testTarget(name: "MeowFailureTests", dependencies: ["Meow"], path: "Tests")
], swiftLanguageModes: [.v5])
''')
    for source in SOURCES:
        shutil.copy2(source, path / 'Sources' / source.name)
    for name in TESTS:
        source = ROOT / 'ios/MeowTests' / name
        if source.exists():
            shutil.copy2(source, path / 'Tests' / name)
    # Reuse the existing queue regressions verbatim. Only these top-level Swift
    # declarations are platform independent; the other classes require UIKit.
    original = (ROOT / 'ios/MeowTests/MeowTests.swift').read_text()
    header = r'^(?:final class ReplyQueueTests: XCTestCase|extension ReplyQueueTests(?:\.HeldServer)?) \{'
    blocks = re.findall(header + r'.*?^\}', original, re.M | re.S)
    if not blocks or len(blocks) != len(re.findall(header, original, re.M)):
        raise RuntimeError('incomplete original queue test extraction')
    (path / 'Tests/OriginalReplyQueueTests.swift').write_text(
        'import XCTest\n@testable import Meow\n\n' + '\n\n'.join(blocks) + '\n')


def native(path, name='native', test_filter=None, check=True):
    args = ['swift', 'test', '--package-path', str(path / 'package'), '--enable-code-coverage',
            '--xunit-output', str(path / f'{name}.xml')]
    if test_filter:
        args += ['--filter', test_filter]
    code = run(args, path / f'{name}.log', check=check)
    # SwiftPM's --xunit-output covers Swift Testing, not Darwin XCTest. Require
    # actual completed XCTest cases and the enclosing suite completion instead.
    log = (path / f'{name}.log').read_text()
    cases = re.findall(r"Test Case '-\[([^]]+)\]' (passed|failed|skipped) ", log)
    failures = [name for name, status in cases if status == 'failed']
    if not cases or any(status == 'skipped' for _, status in cases) or not re.search(r"Test Suite '(All|Selected) tests' (passed|failed)", log):
        raise RuntimeError('empty, incomplete or skipped XCTest report')
    return {'exit': code, 'tests': len(cases), 'failures': failures}


def coverage_gate(records, manifest):
    measured = {}
    for name, lines in manifest.items():
        if not name.endswith(('NotificationContent.swift', 'NotificationDelivery.swift', 'ReplyQueue.swift', 'NotificationCallback.swift')):
            continue  # AppModel/delegate require an iOS host, compiled separately.
        if name not in records:
            raise RuntimeError(f'missing coverage file: {name}')
        source = (ROOT / name).read_text().splitlines()
        for line, text in lines.items():
            if source[int(line) - 1] != text:
                raise RuntimeError(f'stale changed-line manifest: {name}:{line}')
        executable = {int(n): records[name][int(n)] for n in lines if int(n) in records[name]}
        if not executable or any(n == 0 for n in executable.values()):
            raise RuntimeError(f'uncovered changed executable lines: {name}: {executable}')
        measured[name] = {'covered': len(executable), 'executable': len(executable),
                          'nonexecutable_changed_lines': len(lines) - len(executable)}
    if len(measured) != 4:
        raise RuntimeError('incomplete coverage scope')
    return measured


def coverage(output):
    build = output / 'package/.build/arm64-apple-macosx/debug'
    binary = build / 'MeowFailuresPackageTests.xctest/Contents/MacOS/MeowFailuresPackageTests'
    run(['xcrun', 'llvm-cov', 'export', '-format=lcov',
         '-instr-profile=' + str(build / 'codecov/default.profdata'), str(binary)], output / 'coverage.lcov')
    records = {}
    for record in (output / 'coverage.lcov').read_text().split('end_of_record'):
        files = re.findall(r'^SF:(.+)$', record, re.M)
        if not files:
            continue
        names = [p for p in SOURCES if p.name == Path(files[0]).name]
        if names:
            records[str(names[0].relative_to(ROOT))] = {
                int(line): int(count) for line, count in re.findall(r'^DA:(\d+),(\d+)', record, re.M)}
    manifest = json.loads((ROOT / 'docs/verification/notification-failure-changed-lines.json').read_text())
    result = coverage_gate(records, manifest)
    # Negative controls: missing input and a known uncovered executable line
    # must each be rejected for the intended reason.
    for corrupt, reason in [({}, 'missing coverage'),
                            ({name: {line: 0 for line in values} for name, values in records.items()}, 'uncovered changed')]:
        try:
            coverage_gate(corrupt, manifest)
        except RuntimeError as error:
            if reason not in str(error):
                raise
        else:
            raise RuntimeError('coverage gate accepted a known-bad control')
    return {'files': result, 'negative_controls_rejected': 2,
            'unmeasured': ['AppModel.swift', 'MeowApp.swift (iOS SDK compile only)']}


def mutations(output):
    source = output / 'package/Sources'
    mutants = [
        ('direct-device', 'NotificationContent.swift',
         'guard let deviceID, metadata["device_id"] as? String == deviceID else { return false }',
         'guard metadata["device_id"] == nil || metadata["device_id"] as? String == deviceID else { return false }'),
        ('relay-binding', 'NotificationContent.swift', 'metadata["binding_id"] as? String == bindingID', 'metadata["binding_id"] is String'),
        ('stale-display', 'NotificationContent.swift', 'if !relay && accepted { return content }', 'if !relay { return content }'),
        ('expiry', 'NotificationContent.swift', 'expiry > now.timeIntervalSince1970', 'expiry > now.timeIntervalSince1970 - 3600'),
        ('queue-identity', 'ReplyQueue.swift', 'if let identity, currentIdentity() != identity { return .discarded }', 'if false { return .discarded }'),
        ('late-result', 'ReplyQueue.swift', 'guard deliveryEpoch == epoch else { return .discarded }', 'guard true else { return .discarded }'),
        ('extension-identity', 'NotificationDelivery.swift', 'currentIdentity() == identity &&', 'true &&'),
        ('completion-thread', 'NotificationCallback.swift', 'Task { @MainActor in', 'Task {'),
    ]
    results = []
    for name, filename, old, new in mutants:
        path = source / filename
        original = path.read_bytes()
        expected = 3 if name == 'late-result' else 1
        if original.decode().count(old) != expected:
            raise RuntimeError(f'{name}: mutation target not unique/as declared')
        changed = original.decode().replace(old, new).encode()
        try:
            path.write_bytes(changed)
            if path.read_bytes() != changed or changed == original:
                raise RuntimeError('mutant was not applied')
            # Rebuild from scratch, avoiding any timestamp/incremental-cache
            # ambiguity between similar mutations.
            shutil.rmtree(output / 'package/.build')
            result = native(output, 'mutant-' + name, check=False)
            log = (output / f'mutant-{name}.log').read_text()
            if not result['exit'] or not result['failures'] or 'Compiling Meow ' + filename not in log:
                raise RuntimeError(f'{name}: mutant survived or was not freshly compiled')
            results.append({'name': name, 'sha256': hashlib.sha256(changed).hexdigest(),
                            'failed_tests': result['failures']})
            print('Mutation rejected:', name, flush=True)
        finally:
            path.write_bytes(original)
        if path.read_bytes() != original:
            raise RuntimeError('mutation restoration failed')
    shutil.rmtree(output / 'package/.build')
    restored = native(output, 'restored')
    if restored['failures']:
        raise RuntimeError('restored source did not return to green')
    return {'killed': len(results), 'total': len(mutants), 'details': results, 'restored': restored}


def state_hash():
    names = subprocess.check_output(['git', 'ls-files', '--cached', '--others', '--exclude-standard', '-z'], cwd=ROOT).decode().split('\0')
    digest = hashlib.sha256()
    for name in sorted(set(names)):
        if not name or name.startswith('docs/verification/'):
            continue
        path = ROOT / name
        digest.update(name.encode() + b'\0' + (path.read_bytes() if path.is_file() else b'deleted'))
    return digest.hexdigest()


def full(output, result):
    completed = {'native'}
    result['source_sha256'] = state_hash()
    result['tools'] = {name: subprocess.check_output(args, text=True).strip() for name, args in [
        ('swift', ['swift', '--version']), ('xcode', ['xcodebuild', '-version']), ('go', ['go', 'version'])]}
    result['coverage'] = coverage(output)
    completed.add('coverage')
    result['mutations'] = mutations(output)
    completed.add('mutations')
    run(['go', 'test', '-race', '-shuffle=on', '-count=1', '-json', './...'], output / 'go-tests.jsonl')
    events = [json.loads(line) for line in (output / 'go-tests.jsonl').read_text().splitlines() if line.startswith('{')]
    tests = [e for e in events if e.get('Test') and e.get('Action') in ('pass', 'fail', 'skip')]
    if not tests or any(e['Action'] != 'pass' for e in tests):
        raise RuntimeError('Go tests missing, skipped or failed')
    result['go'] = {'passed': len(tests), 'race': True, 'shuffle': True}
    completed.add('go')
    run(['go', 'vet', './...'], output / 'go-vet.log')
    completed.add('go_vet')
    run(['xcodegen', 'generate', '--spec', 'ios/project.yml'], output / 'xcodegen.log')
    run(['xcodebuild', 'build-for-testing', '-project', 'ios/Meow.xcodeproj', '-scheme', 'Meow',
         '-destination', 'generic/platform=iOS', '-derivedDataPath', str(output / 'ios-build'),
         'CODE_SIGNING_ALLOWED=NO'], output / 'ios-build.log')
    completed.add('ios_compile')
    if state_hash() != result['source_sha256']:
        raise RuntimeError('source changed during verification')
    if completed != EXPECTED_LAYERS:
        raise RuntimeError('verification layer manifest incomplete')
    result['completed_layers'] = sorted(completed)
    result['physical_checks'] = 'pending; no simulator boot or phone app replacement'
    result['independent_verification'] = 'not performed'
    return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--output', type=Path, default=ROOT / 'dist/notification-failures/final')
    parser.add_argument('--filter')
    parser.add_argument('--expect-red', action='store_true')
    parser.add_argument('--full', action='store_true')
    args = parser.parse_args()
    output = args.output.resolve()
    if output.exists():
        raise RuntimeError(f'use a fresh output directory: {output}')
    output.mkdir(parents=True)
    package(output / 'package')
    result = native(output, test_filter=args.filter, check=False)
    if args.expect_red:
        if result['exit'] == 0 or not result['failures']:
            raise RuntimeError('RED requires behavioral XCTest failures')
    elif result['exit'] or result['failures']:
        raise RuntimeError('native test failures')
    if args.full:
        if args.expect_red or args.filter:
            raise RuntimeError('full verification requires an unfiltered green suite')
        result = full(output, result)
    (output / 'results.json').write_text(json.dumps(result, indent=2) + '\n')
    print(json.dumps(result, indent=2), flush=True)


if __name__ == '__main__':
    main()
