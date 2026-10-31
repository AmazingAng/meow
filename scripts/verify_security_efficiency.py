#!/usr/bin/env python3
"""Check security/efficiency fixes using disposable databases, HTTP and Swift state.

No production credentials, pushes, installations or releases. --ios-results may
reuse a completed verify_ui_optimization run made against unchanged iOS sources.
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
import sys
import tempfile
import time

sys.dont_write_bytecode = True

ROOT = Path(__file__).resolve().parents[1]
BASE = 'ae1eea2'
REPORT = 'docs/engineering/security-efficiency-evidence.md'


def module(name, path):
    spec = importlib.util.spec_from_file_location(name, ROOT / path)
    value = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(value)
    return value


helpers = module('security_gateway_helpers', 'scripts/verify_gateway_limits.py')
native = module('security_native_helpers', 'ios/scripts/verify_frontend_recovery.py')


def require(value, message):
    if not value:
        raise RuntimeError(message)


def run(args, log, cwd=ROOT):
    with log.open('w') as stream:
        result = subprocess.run(args, cwd=cwd, stdout=stream, stderr=subprocess.STDOUT)
    require(result.returncode == 0, f'{args[0]} failed; see {log}')
    return log.read_text()


def source_hash():
    digest = hashlib.sha256()
    for name in helpers.inputs():
        if name == REPORT:
            continue
        path = ROOT / name
        require(path.is_file(), 'missing source: ' + name)
        digest.update(name.encode() + b'\0' + path.read_bytes())
    return digest.hexdigest()


def native_package(out):
    native.package(out)
    package = out / 'package'
    for name in ['InboxProjection.swift', 'NotificationBody.swift', 'InboxPreviewCache.swift']:
        shutil.copyfile(ROOT / 'ios/Meow/Models' / name, package / 'Sources' / name)
    for name in ['InboxSyncTests.swift', 'InboxPreviewCacheTests.swift']:
        shutil.copyfile(ROOT / 'ios/MeowTests' / name, package / 'Tests' / name)
    # SwiftPM has no app Info.plist. Supply the real shipped default at this
    # bundle boundary, without changing production configuration parsing.
    metadata = plistlib.loads((ROOT / 'ios/Meow/Info.plist').read_bytes())
    shared = package / 'Sources/Shared.swift'
    original = shared.read_text()
    anchor = 'Bundle.main.object(forInfoDictionaryKey: "MeowDefaultServer") as? String'
    require(original.count(anchor) == 1, 'app bundle boundary changed')
    shared.write_text(original.replace(anchor, json.dumps(metadata['MeowDefaultServer'])))
    return package


def go_coverage(profile):
    # A gate, not a global coverage percentage: changed statement blocks in
    # modified production Go files, including all new sync helpers.
    targets = {}
    for name in helpers.inputs():
        if not name.endswith('.go') or name.endswith('_test.go'):
            continue
        diff = subprocess.check_output(['git', 'diff', '--unified=0', BASE, '--', name], cwd=ROOT, text=True)
        rows = set()
        for first, count in re.findall(r'^@@ .* \+(\d+)(?:,(\d+))? @@', diff, re.M):
            rows.update(range(int(first), int(first) + int(count or 1)))
        if not subprocess.check_output(['git', 'ls-files', '--', name], cwd=ROOT):
            rows.update(range(1, len((ROOT / name).read_text().splitlines()) + 1))
        if rows:
            targets[name] = rows
    blocks = {}
    lines = profile.read_text().splitlines()
    require(lines and lines[0] == 'mode: atomic', 'missing atomic coverage')
    for line in lines[1:]:
        location, statements, hits = line.split()
        name, span = location.split(':')
        name = name.removeprefix('meow.blue/meow/')
        if name not in targets:
            continue
        first, last = span.split(',')
        if targets[name].intersection(range(int(first.split('.')[0]), int(last.split('.')[0]) + 1)):
            n, previous = blocks.get((name, span), (int(statements), 0))
            blocks[name, span] = (n, previous + int(hits))
    for name in targets:
        require(any(path == name and n > 0 for (path, _), (n, _) in blocks.items()), 'unmeasured ' + name)
    total = sum(n for n, _ in blocks.values())
    covered = sum(n for n, hit in blocks.values() if hit > 0)
    require(total > 0 and covered / total >= .85, 'changed Go coverage below 85%')
    return {'covered_statements': covered, 'statements': total, 'threshold': .85,
            'uncovered': [f'{name}:{span}' for (name, span), (n, hit) in blocks.items() if n and not hit]}


def swift_coverage(out):
    build = out / 'package/.build/arm64-apple-macosx/debug'
    binary = build / 'MeowFailuresPackageTests.xctest/Contents/MacOS/MeowFailuresPackageTests'
    text = run(['xcrun', 'llvm-cov', 'export', '-format=lcov',
                '-instr-profile=' + str(build / 'codecov/default.profdata'), str(binary)], out / 'swift.lcov')
    targets = ['InboxPreviewCache.swift', 'InboxStore.swift', 'MeowAPI.swift', 'AppModel.swift']
    result = {}
    paths_by_name = {Path(n).name: n for n in ['ios/Meow/App/AppModel.swift', 'ios/Shared/InboxStore.swift',
                     'ios/Shared/MeowAPI.swift', 'ios/Meow/Models/InboxPreviewCache.swift']}
    for record in text.split('end_of_record'):
        paths = re.findall(r'^SF:(.+)$', record, re.M)
        if not paths or Path(paths[0]).name not in targets:
            continue
        name = Path(paths[0]).name
        rows = [(int(n), int(hit)) for n, hit in re.findall(r'^DA:(\d+),(\d+)', record, re.M)]
        # Whole-file gates are deliberately separate from changed-line Go
        # coverage; AppModel includes UI/account flows absent from native tests.
        threshold = .9 if name == 'InboxPreviewCache.swift' else .6
        require(rows and sum(hit > 0 for _, hit in rows) / len(rows) >= threshold,
                'native Swift coverage below threshold: ' + name)
        result[name] = {'covered_lines': sum(hit > 0 for _, hit in rows), 'lines': len(rows), 'threshold': threshold}
        source = paths_by_name[name]
        diff = subprocess.check_output(['git', 'diff', '--unified=0', BASE, '--', source], cwd=ROOT, text=True)
        changed = set()
        for first, count in re.findall(r'^@@ .* \+(\d+)(?:,(\d+))? @@', diff, re.M):
            changed.update(range(int(first), int(first) + int(count or 1)))
        if not subprocess.check_output(['git', 'ls-files', '--', source], cwd=ROOT):
            changed.update(range(1, len((ROOT / source).read_text().splitlines()) + 1))
        measured = [(line, hit) for line, hit in rows if line in changed]
        require(measured and sum(hit > 0 for _, hit in measured) / len(measured) >= .85,
                'changed Swift coverage below 85%: ' + source)
        result[name]['changed'] = {'covered_lines': sum(hit > 0 for _, hit in measured), 'lines': len(measured),
                                   'threshold': .85, 'uncovered': [line for line, hit in measured if not hit]}
    require(set(result) == set(targets), 'missing native coverage target')
    return result


def mutations(out, package):
    go_cases = [
        ('ipv6-budget', 'server/internal/api/api.go', 'if address.Is6() {', 'if false && address.Is6() {',
         './server/internal/api', 'TestAuditIPv6RegistrationAllowance'),
        ('body-log', 'server/internal/push/apns.go', '"notification", m.CollapseID)', '"payload", string(m.Payload))',
         './server/internal/push', 'TestAuditDisabledPusherDoesNotLogPrivateBody'),
        ('provider-delay', 'server/internal/push/apns.go', 'delay := retryAfter(resp.Header.Get("Retry-After"), a.Now())', 'delay := time.Duration(0)',
         './server/internal/push', 'TestAuditAPNsProviderRetryAfter'),
        ('gateway-delay', 'server/internal/api/gateway.go', 'if ae.RetryAfter > 0 {', 'if false && ae.RetryAfter > 0 {',
         './server/internal/api', 'TestAuditGatewayProviderRetryAfter'),
        ('cursor-device', 'server/internal/api/sync.go', 'err != nil || cursor.Device != p.device.ID', 'err != nil',
         './server/internal/api', 'TestInboxSyncSnapshotAndDeviceFence'),
        ('moving-ceiling', 'server/internal/api/sync.go', 'if cursor.Until == 0 {', 'if true {',
         './server/internal/api', 'TestInboxSyncSnapshotAndDeviceFence'),
        ('migration-error', 'server/internal/store/store.go', 'if _, err := tx.ExecContext(ctx, syncSQL); err != nil {\n\t\t\t\treturn err',
         'if _, err := tx.ExecContext(ctx, syncSQL); err != nil {\n\t\t\t\t_ = err',
         './server/internal/store', 'TestSyncMigrationFailureIsAtomic'),
    ]
    results = []
    with tempfile.TemporaryDirectory(prefix='meow-security-mutants-') as folder:
        copy = Path(folder)
        for name in helpers.inputs():
            if not (name.endswith(('.go', '.sql')) or name in ('go.mod', 'go.sum')):
                continue
            target = copy / name
            target.parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(ROOT / name, target)
        for name, file, old, new, target, test in go_cases:
            path = copy / file
            original = path.read_text()
            require(original.count(old) == 1, 'ambiguous mutant: ' + name)
            path.write_text(original.replace(old, new))
            result = subprocess.run(['go', 'test', '-json', target, '-run', '^' + test + '$', '-count=1'], cwd=copy, text=True, capture_output=True)
            (out / ('mutant-' + name + '.log')).write_text(result.stdout + result.stderr)
            path.write_text(original)
            rows = [json.loads(line) for line in result.stdout.splitlines() if line.startswith('{')]
            require(result.returncode == 1 and any(r.get('Action') == 'fail' and r.get('Test') == test for r in rows),
                    'mutant survived or failed to execute named test: ' + name)
            results.append(name)
    swift_cases = [
        ('checkpoint', 'InboxStore.swift', 'try self.setMetadata("sync_cursor." + scope, value: cursor)', '// checkpoint omitted',
         'InboxSyncStoreTests/testPageAndCheckpointCommitTogetherAndResumeAfterRestart'),
        ('repeat-parse', 'InboxPreviewCache.swift', 'if var entry = entries[body] {', 'if var entry = entries[body], false {',
         'InboxPreviewCacheTests/testConcurrentRequestsShareParsingIncludingEmptyResults'),
        ('repeat-local-decode', 'InboxStore.swift', 'guard revision != current else { return nil }', '// revision check omitted',
         'InboxSyncStoreTests/testUnchangedLocalHistoryAvoidsDecodeButOtherConnectionsAndReadsInvalidate'),
        ('legacy-resolution', 'InboxStore.swift', 'incoming[index].question = resolved', '_ = resolved',
         'InboxSyncStoreTests/testStalePendingSnapshotPreservesResolvedLegacyHistory'),
    ]
    for name, file, old, new, test in swift_cases:
        path = package / 'Sources' / file
        original = path.read_bytes()
        require(original.decode().count(old) == 1, 'ambiguous Swift mutant: ' + name)
        try:
            path.write_text(original.decode().replace(old, new))
            result = subprocess.run(['swift', 'test', '--package-path', str(package), '--filter', test], cwd=ROOT, text=True, capture_output=True)
            text = result.stdout + result.stderr
            (out / ('mutant-' + name + '.log')).write_text(text)
            method = test.split('/')[-1]
            require(result.returncode != 0 and re.search(r"Test Case '.* " + method + r"\]' failed", text),
                    'Swift mutant survived or did not execute named test: ' + name)
            results.append(name)
        finally:
            path.write_bytes(original)
    return results


def benchmark(out):
    source = out / 'preview-benchmark.swift'
    source.write_text('''import Foundation
@main struct Probe {
 static func main() async {
  let body = String(repeating: "**a** b ", count: 490)
  let start = Date(); var original = 0
  for _ in 0..<50 { original += NotificationBody(body).listPreview?.characters.count ?? 0 }
  let parsed = Date().timeIntervalSince(start)
  let cache = InboxPreviewCache(); let cachedStart = Date(); var cached = 0
  for _ in 0..<50 { cached += await cache.preview(body).text?.characters.count ?? 0 }
  precondition(original == cached && original > 0)
  print("input_chars=\\(body.count) iterations=50 original_ms=\\(parsed * 1000) cached_ms=\\(Date().timeIntervalSince(cachedStart) * 1000) output=\\(cached)")
 }
}
''')
    run(['swiftc', '-O', '-parse-as-library', str(ROOT / 'ios/Meow/Models/NotificationBody.swift'),
         str(ROOT / 'ios/Meow/Models/InboxPreviewCache.swift'), str(source), '-o', str(out / 'preview-benchmark')], out / 'benchmark-build.log')
    return run([str(out / 'preview-benchmark')], out / 'benchmark.log').strip()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--output', type=Path, required=True)
    parser.add_argument('--ios-results', type=Path)
    args = parser.parse_args()
    out = args.output.resolve()
    out.mkdir(parents=True, exist_ok=False)
    before = source_hash()
    report = {'source_sha256': before, 'spec_approval': 'not obtained (autonomous run)',
              'independent_verification': 'not performed', 'layers': {}}
    report['toolchain'] = subprocess.check_output(['go', 'version'], text=True).strip()
    package = native_package(out)
    run(['go', 'test', '-race', '-shuffle=20261101', '-count=1', '-covermode=atomic',
         '-coverprofile=' + str(out / 'go-coverage.out'), '-json', './...'], out / 'go-tests.jsonl')
    report['layers']['go'] = helpers.go_results((out / 'go-tests.jsonl').read_text())
    report['layers']['go-coverage'] = go_coverage(out / 'go-coverage.out')
    # Negative control: empty evidence must fail the coverage gate.
    empty = out / 'empty.cover'
    empty.write_text('mode: atomic\n')
    try:
        go_coverage(empty)
    except RuntimeError:
        report['layers']['coverage-negative-control'] = 'rejected'
    else:
        raise RuntimeError('coverage gate accepted empty evidence')
    report['layers']['native'] = native.base.native(out)
    report['layers']['swift-coverage'] = swift_coverage(out)
    report['layers']['mutations'] = mutations(out, package)
    report['layers']['restored-native'] = native.base.native(out, 'restored')
    for name, command in [
        ('vet', ['go', 'vet', './...']), ('installer', ['python3', '-B', 'scripts/test_install.py']),
        ('diff', ['git', 'diff', '--check']),
        ('vulnerabilities', ['go', 'run', 'golang.org/x/vuln/cmd/govulncheck@v1.9.0', './...']),
    ]:
        report['layers'][name] = run(command, out / (name + '.log')).strip() or 'pass'
    go_files = [n for n in helpers.inputs() if n.endswith('.go')]
    require(not run(['gofmt', '-l', *go_files], out / 'format.log').strip(), 'Go formatting differs')
    report['layers']['format'] = 'pass'
    for name, target in [('meow', './cli/cmd/meow'), ('meow-server', './server/cmd/meow-server')]:
        run(['go', 'build', '-trimpath', '-o', str(out / name), target], out / (name + '-build.log'))
    report['layers']['build'] = 'CLI/server compiled; not installed or released'
    report['layers']['benchmark'] = benchmark(out)
    diff = subprocess.check_output(['git', 'diff', BASE], cwd=ROOT, text=True)
    for name in subprocess.check_output(['git', 'ls-files', '--others', '--exclude-standard'], cwd=ROOT, text=True).splitlines():
        if name != REPORT:
            diff += (ROOT / name).read_text()
    scanned = subprocess.run(['gitleaks', 'stdin', '--redact', '--no-banner'], input=diff, text=True, capture_output=True)
    (out / 'secrets.log').write_text(scanned.stdout + scanned.stderr)
    require(scanned.returncode == 0, 'diff secret scan failed')
    report['layers']['secrets'] = 'pass'
    if args.ios_results:
        deadline = time.monotonic() + 1200
        # The UI runner writes its summary before exporting attachments.
        # Wait for both phases instead of treating that small interval as a
        # missing-screenshot failure.
        while not ((args.ios_results / 'results.json').exists()
                   and any((args.ios_results / 'screenshots').glob('*.png'))):
            require(time.monotonic() < deadline, 'timed out waiting for iOS regression results')
            time.sleep(1)
        summary = json.loads((args.ios_results / 'results.json').read_text())
        require(summary['passedTests'] > 0 and not summary['failedTests'] and not summary['skippedTests'], 'iOS regression failed')
        require(any((args.ios_results / 'screenshots').glob('*.png')), 'iOS screenshots missing')
        report['layers']['ios'] = {k: summary[k] for k in ['result', 'passedTests', 'failedTests', 'skippedTests']}
        report['layers']['ios']['results'] = str(args.ios_results)
    else:
        run(['python3', '-B', 'ios/scripts/verify_ui_optimization.py', '--output', str(out / 'ios/ui'), '--coverage'], out / 'ios-run.log')
        report['layers']['ios'] = json.loads((out / 'ios/ui/results.json').read_text())
    require(source_hash() == before, 'source changed during verification')
    report['layers']['source-unchanged'] = True
    (out / 'report.json').write_text(json.dumps(report, indent=2, ensure_ascii=False) + '\n')
    print(json.dumps(report, indent=2, ensure_ascii=False))


if __name__ == '__main__':
    main()
