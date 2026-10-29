#!/usr/bin/env python3
"""Verify local unread authority using disposable native, process and iOS state.
Full run: python3 -B ios/scripts/verify_local_unread.py --output dist/local-unread/check-NEW --full
"""
import argparse
import hashlib
import importlib.util
import json
from pathlib import Path
import re
import shutil
import subprocess

ROOT = Path(__file__).resolve().parents[2]


def require(ok, message):
    if not ok:
        raise RuntimeError(message)


def run(command, out, name, cwd=ROOT, timeout=1200):
    print('Running:', name, flush=True)
    p = subprocess.run(command, cwd=cwd, capture_output=True, text=True, timeout=timeout)
    text = p.stdout + p.stderr
    (out / (name + '.log')).write_text(text)
    require(p.returncode == 0, name + ' failed: ' + str(out / (name + '.log')))
    return text


def module():
    spec = importlib.util.spec_from_file_location('performance', ROOT / 'ios/scripts/verify_ios_performance.py')
    m = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(m)
    return m


def package(out, m):
    m.package(out)
    shutil.copy2(ROOT / 'ios/MeowTests/LocalUnreadTests.swift', out / 'package/Tests/LocalUnreadTests.swift')
    # Public bridge exists only in this disposable package. Both child processes
    # run the real shared InboxStore/SQLite implementation, not a mocked store.
    (out / 'package/Sources/ProcessProbe.swift').write_text('''import Foundation
public func localInboxProcessProbe(_ args: [String]) async throws {
    let store = InboxStore(fileURL: URL(fileURLWithPath: args[1]))
    if args[2] == "activate" { try await store.activate(scope: "probe"); return }
    if args[2] == "check" {
        let rows = try await store.load(scope: "probe") ?? []
        guard rows.count == 100, rows.filter(\\.isUnread).count == 50,
              rows.allSatisfy({ $0.isUnread == (Int($0.id.dropFirst())! % 2 == 1) }) else { fatalError("shared process state diverged") }
        print("rows=100 unread=50")
        return
    }
    let ids = args[2] == "writer0" ? Array(0..<100) : Array((0..<100).reversed())
    for i in ids {
        let item = MeowNotification(id: "n\\(i)", type: .info, title: "fixture", body: "shared receipt", source: "fixture",
            priority: "normal", pushStatus: "accepted", createdAt: Date(timeIntervalSince1970: 1790000000))
        try await store.receiveVerified(item, scope: "probe")
        if args[2] == "writer0", i % 2 == 0 { try await store.markLocalRead([item.id], scope: "probe") }
        if i % 17 == 0 { try await Task.sleep(for: .milliseconds(2)) }
    }
    print(args[2] + " complete")
}
''')
    (out / 'package/Probe').mkdir()
    (out / 'package/Probe/Probe.swift').write_text('''import Foundation
import Meow
@main struct Probe { static func main() async throws { try await localInboxProcessProbe(CommandLine.arguments) } }
''')
    path = out / 'package/Package.swift'
    s = path.read_text()
    needle = '    .testTarget(name: "MeowFailureTests"'
    require(s.count(needle) == 1, 'probe manifest boundary missing')
    path.write_text(s.replace(needle, '    .executableTarget(name: "InboxStoreProbe", dependencies: ["Meow"], path: "Probe"),\n' + needle))


def coverage_gate(records):
    required = {'receiveVerified', 'markLocalRead', 'unreadCount', 'writeRows'}
    require(set(records) == required and all(records.values()), 'missing local authority coverage')
    require(all(n > 0 for counts in records.values() for n in counts), 'unexecuted critical local authority logic')
    return {name: {'covered': len(counts), 'executable': len(counts)} for name, counts in records.items()}


def coverage(out):
    build = out / 'package/.build/arm64-apple-macosx/release'
    text = run(['xcrun', 'llvm-cov', 'export', '-format=lcov', '-instr-profile=' + str(build / 'codecov/default.profdata'),
                str(build / 'MeowFailuresPackageTests.xctest/Contents/MacOS/MeowFailuresPackageTests')], out, 'coverage')
    blocks = [block for block in text.split('end_of_record') if '/Sources/InboxStore.swift' in block]
    require(len(blocks) == 1, 'missing shared store coverage')
    source = (out / 'package/Sources/InboxStore.swift').read_text().splitlines()
    counts = {int(line): int(count) for line, count in re.findall(r'^DA:(\d+),(\d+)', blocks[0], re.M)}
    records = {}
    for name in ['receiveVerified', 'markLocalRead', 'unreadCount', 'writeRows']:
        start = next(i + 1 for i, line in enumerate(source) if re.search(r'func ' + name + r'\(', line))
        end = next((i + 1 for i in range(start, len(source)) if re.match(r'    (?:private )?func |    deinit ', source[i])), len(source) + 1)
        records[name] = [n for line, n in counts.items() if start <= line < end]
    result = coverage_gate(records)
    for bad in [{}, {name: [0] * len(rows) for name, rows in records.items()}]:
        try:
            coverage_gate(bad)
        except RuntimeError:
            pass
        else:
            raise RuntimeError('coverage gate accepted negative control')
    def rejects(checker):
        try:
            checker({})
        except RuntimeError:
            return True
        return False
    require(rejects(coverage_gate) and not rejects(lambda _: {}), 'negative control defense removal not detected')
    return {'critical_functions': result, 'negative_controls': 3,
            'limits': 'Swift executable line coverage for critical transactions/writer/counts; not whole-migration/UI/branch coverage.'}


def processes(out):
    run(['swift', 'build', '-c', 'release', '--package-path', str(out / 'package'), '--product', 'InboxStoreProbe'], out, 'process-build')
    binary = out / 'package/.build/release/InboxStoreProbe'
    db = out / 'processes.sqlite'
    run([str(binary), str(db), 'activate'], out, 'process-activate')
    writers = []
    for name in ['writer0', 'writer1']:
        stream = (out / ('process-' + name + '.log')).open('w')
        p = subprocess.Popen([str(binary), str(db), name], stdout=stream, stderr=subprocess.STDOUT)
        writers.append((p, stream))
    try:
        require(len({p.pid for p, _ in writers}) == 2, 'writers did not run in separate processes')
        for p, stream in writers:
            require(p.wait(timeout=60) == 0, 'shared writer failed: ' + str(p.pid))
            stream.close()
    finally:
        for p, stream in writers:
            if p.poll() is None:
                p.kill(); p.wait()
            stream.close()
    text = run([str(binary), str(db), 'check'], out, 'process-check')
    require('rows=100 unread=50' in text, 'process check did not execute')
    return {'writer_pids': [p.pid for p, _ in writers], 'receipts': 200, 'rows': 100, 'unread': 50}


def mutations(out):
    cases = [
        ('pending-read', 'InboxStore.swift', 'try self.writeRows(pendingItems, encoded: pendingEncoded, scope: scope)',
         'try self.writeRows([], encoded: [], scope: scope)', 'testReadingUnsavedOnlineContentCommitsThePayloadAndReadFlagTogether'),
        ('read-regression', 'InboxStore.swift', 'COALESCE(inbox_messages.read_at,excluded.read_at)', 'excluded.read_at',
         'testTwoConnectionsDeduplicateAndPreserveLocalReadOverRemoteUpdates'),
        ('deleted-resurrection', 'InboxStore.swift', 'FROM inbox_deletions WHERE scope=? AND id=?) ON CONFLICT',
         'FROM inbox_deletions WHERE scope=? AND id=? AND 0) ON CONFLICT', 'testDeletedAndOldRegistrationReceiptsCannotResurrectMessages'),
        ('identity-rollback', 'InboxStore.swift', 'guard isCurrent() else', 'guard true else',
         'testReceiptIdentityChangeRollsBackAndInactiveScopeCannotWrite'),
        ('legacy-reimport', 'InboxStore.swift', 'guard try self.metadata("legacy_imported") == nil else', 'guard true else',
         'testLegacyMigrationPreservesReadStateAndDoesNotImportTwice'),
        ('recovered-storage-error', 'AppModel.swift', 'if pendingInboxChanges.isEmpty { inboxStorageError = nil }',
         'if false { inboxStorageError = nil }', 'testFailedLocalReadTransactionCanRetryWithoutPretendingSuccess'),
        ('old-badge', 'NotificationDelivery.swift', 'fallback.badge = nil', 'fallback.badge = original.badge',
         'testOldServerBadgeCannotOverrideLocalStateOnFailedFetch'),
    ]
    results = []
    for name, file, old, new, selected in cases:
        path = out / 'package/Sources' / file
        original = path.read_text()
        require(original.count(old) == 1, 'ambiguous mutant: ' + name)
        try:
            changed = original.replace(old, new); path.write_text(changed)
            require(path.read_text() == changed and original != changed, 'mutant not written')
            p = subprocess.run(['swift', 'test', '-c', 'release', '--package-path', str(out / 'package'), '--filter', selected],
                               cwd=ROOT, capture_output=True, text=True, timeout=300)
            text = p.stdout + p.stderr
            (out / ('mutant-' + name + '.log')).write_text(text)
            failed = re.findall(r"Test Case '-\[([^]]+)\]' failed", text)
            require(p.returncode != 0 and failed and 'Compiling Meow ' in text, 'mutant survived or did not execute: ' + name)
            results.append({'name': name, 'hash': hashlib.sha256(changed.encode()).hexdigest(), 'failed': failed})
            print('Killed mutant:', name, flush=True)
        finally:
            path.write_text(original)
        require(path.read_text() == original, 'mutation restore failed')
    folder = out / 'go-mutant'; folder.mkdir()
    for name in ['go.mod', 'go.sum']:
        shutil.copy2(ROOT / name, folder / name)
    for name in ['server', 'client', 'message', 'adapters', 'contracts']:
        shutil.copytree(ROOT / name, folder / name)
    path = folder / 'server/internal/push/payload.go'
    original = path.read_text(); needle = '\tif n.Source != "" {'
    require(original.count(needle) == 1, 'missing server badge mutant')
    changed = original.replace(needle, '\taps["badge"] = 0\n' + needle); path.write_text(changed)
    p = subprocess.run(['go', 'test', './server/internal/push', '-run', '^TestDirectPushNeverOverridesPhoneUnreadBadge$', '-count=1'],
                       cwd=folder, capture_output=True, text=True, timeout=180)
    text = p.stdout + p.stderr; (out / 'mutant-server-badge.log').write_text(text)
    require(p.returncode != 0 and '--- FAIL: TestDirectPushNeverOverridesPhoneUnreadBadge' in text, 'badge mutant did not fail an executed assertion')
    results.append({'name': 'server-badge', 'hash': hashlib.sha256(changed.encode()).hexdigest()})
    return {'killed': len(results), 'total': 8, 'details': results}


def full(out, m, before, native):
    result = {'native': native}
    result['toolchains'] = {name: run(command, out, 'version-' + name) for name, command in [
        ('swift', ['swift', '--version']), ('xcode', ['xcodebuild', '-version']),
        ('go', ['go', 'version']), ('python', ['python3', '--version'])]}
    result['coverage'] = coverage(out)
    result['processes'] = processes(out)
    text = run(['go', 'test', '-race', '-shuffle=20261029', '-count=1', '-json', './...'], out, 'go-tests')
    events = [json.loads(line) for line in text.splitlines() if line.startswith('{')]
    require(not any(e.get('Action') == 'fail' or (e.get('Action') == 'skip' and 'Test' in e) for e in events), 'Go failed/skipped tests')
    passed = [e for e in events if e.get('Action') == 'pass' and 'Test' in e]
    result['go'] = {'cases': len(passed), 'top_level': sum('/' not in e['Test'] for e in passed)}
    run(['go', 'vet', './...'], out, 'go-vet')
    run(['go', 'build', '-o', str(out / 'meow-server'), './server/cmd/meow-server'], out, 'go-build')
    run(['python3', '-B', 'scripts/test_install.py'], out, 'installer')
    formatted = run(['gofmt', '-l', 'server/internal/push/payload.go', 'server/internal/push/local_badge_test.go',
                     'server/internal/store/store.go'], out, 'gofmt')
    require(not formatted.strip(), 'Go formatting differs')
    run(['git', 'diff', '--check'], out, 'diff-check')
    result['mutations'] = mutations(out)
    result['restored_native'] = m.native(out)
    result['simulator'] = []
    for index, name in enumerate(['test31LocalUnreadBadgeDoesNotDependOnServerReads',
                                 'test21GroupSearchReadsOnlyMatchingMessagesAndModeSurvivesRestart',
                                 'test27InboxReadHeaderAndGroupSwipeActions', 'test30ReadActionsLargeChinese']):
        destination = out / ('simulator-' + str(index + 1))
        command = ['python3', '-B', 'ios/scripts/verify_ui_optimization.py', '--output', str(destination),
                   '--coverage', '--test-case', name]
        if index:
            command += ['--ui-only']
        run(command, out, 'simulator-' + str(index + 1), timeout=1800)
        result['simulator'].append(json.loads((destination / 'results.json').read_text()))
    require(m.state() == before, 'source changed during verification')
    result['source_sha256'] = before
    result['limits'] = ['Spec approval not obtained (autonomous run); independent review not performed.',
                       'No production deployment, phone APNs, TestFlight upload or Swift TSAN run.',
                       'Native harness adapts UI/Keychain/App Group/Bundle boundaries. iOS cases use real SDK.',
                       'Badge updates are best-effort; iOS can terminate the extension. App reconciles on open.',
                       'No dependency changed; deterministic generated cases used rather than new property-test dependency.']
    (out / 'report.json').write_text(json.dumps(result, indent=2) + '\n')
    print('Verification passed:', out / 'report.json', flush=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--output', type=Path, required=True)
    parser.add_argument('--filter')
    parser.add_argument('--full', action='store_true')
    args = parser.parse_args()
    require(not (args.full and args.filter), 'full verification cannot filter native tests')
    out = args.output.resolve(); out.mkdir(parents=True, exist_ok=False)
    m = module(); before = m.state()
    package(out, m)
    native = m.native(out, args.filter)
    print(json.dumps(native), flush=True)
    if args.full:
        full(out, m, before, native)


if __name__ == '__main__':
    main()
