#!/usr/bin/env python3
"""Reproduce session-ID regression, native coverage/mutation gates and simulator UI checks.

Uses isolated native storage and a disposable HTTP fixture/simulator; no phone,
production server, credentials or installed agent configuration is changed.
"""
import argparse
import difflib
import hashlib
import importlib.util
import json
from pathlib import Path
import re
import subprocess

ROOT = Path(__file__).resolve().parents[2]
BASE = '635a5f82d77c2f8f0a3b09d4e72ee0bc7ad07e21'
CHANGED = ['ios/Meow/Models/InboxGrouping.swift', 'ios/Meow/App/AppModel.swift']
spec = importlib.util.spec_from_file_location('recovery', ROOT / 'ios/scripts/verify_frontend_recovery.py')
recovery = importlib.util.module_from_spec(spec)
spec.loader.exec_module(recovery)


def run(args, log, *, expected=0):
    with log.open('w') as stream:
        result = subprocess.run(args, cwd=ROOT, stdout=stream, stderr=subprocess.STDOUT)
    if expected == 0 and result.returncode != 0:
        raise RuntimeError(f'Command failed ({result.returncode}): {args}; see {log}')
    return result.returncode


def native(folder, *, selector=None, expect_failure=False):
    args = ['swift', 'test', '--package-path', str(folder / 'package'), '--enable-code-coverage']
    if selector:
        args += ['--filter', selector]
    code = run(args, folder / 'tests.log', expected=None if expect_failure else 0)
    log = (folder / 'tests.log').read_text()
    cases = re.findall(r"Test Case '-\[([^]]+)\]' (passed|failed|skipped) ", log)
    failures = [name for name, status in cases if status == 'failed']
    if not cases or any(status == 'skipped' for _, status in cases) or not re.search(r"Test Suite '(All|Selected) tests' (passed|failed)", log):
        raise RuntimeError('Incomplete, empty or skipped native tests: ' + str(folder))
    if expect_failure and (code == 0 or not failures):
        raise RuntimeError('Expected behavioral failure did not occur: ' + str(folder))
    return {'tests': len(cases), 'failures': failures, 'exit': code}


def make_package(folder):
    folder.mkdir()
    recovery.package(folder)


def red(folder):
    make_package(folder)
    for relative in CHANGED:
        data = subprocess.check_output(['git', 'show', BASE + ':' + relative], cwd=ROOT).decode()
        if relative.endswith('AppModel.swift'):
            data = data.replace('import UIKit\n', '// UIKit is adapted by Platform.swift.\n')
            data = data.replace('UNUserNotificationCenter.current()', 'RecoveryNotificationCenter.current()')
            data = data.replace('UNAuthorizationStatus', 'RecoveryAuthorizationStatus')
        (folder / 'package/Sources' / Path(relative).name).write_text(data)
    baseline_tests = subprocess.check_output(['git', 'show', BASE + ':ios/MeowTests/InboxGroupingModelTests.swift'], cwd=ROOT)
    (folder / 'package/Tests/InboxGroupingModelTests.swift').write_bytes(baseline_tests)
    path = folder / 'package/Tests/InboxSessionIdentityTests.swift'
    # The first five scenarios use only the build-21 interface, so RED must be
    # an assertion failure, never a missing new migration type/API.
    path.write_text(path.read_text().split('    func testLegacyMergeMigrationWaitsForMissingHistory')[0] + '}\n')
    result = native(folder, selector='InboxSessionIdentityTests', expect_failure=True)
    if result['tests'] != 5 or len(result['failures']) != 5:
        raise RuntimeError('Not all five original regression scenarios failed: ' + str(result))
    return result


def mutations(folder):
    make_package(folder)
    edits = [
        ('name-first', 'InboxGrouping.swift', [('if let sessionID, preferSessionID || session == nil', 'if let sessionID, session == nil')]),
        ('lose-current-title', 'InboxGrouping.swift', [('for (id, n) in latestNamed { descriptions[id]?.member.title = NotificationOrigin.nonempty(n.context?.session)! }', 'for (id, n) in latestNamed { _ = id; _ = n }')]),
        ('skip-migration', 'InboxGrouping.swift', [(r'.filter { $0.sessionIdentityVersion != 2 }.map(\.id)', r'.filter { _ in false }.map(\.id)')]),
        ('forget-persisted-identities', 'AppModel.swift', [('if result.rules != rules { groupingStore.save(result.rules, scope: scope) }', 'if result.rules != result.rules { groupingStore.save(result.rules, scope: scope) }')]),
        ('ignore-cancellation', 'InboxGrouping.swift', [
            ('if index % 256 == 0 && Task.isCancelled { return [] }', 'if false { return [] }'),
            ('if index % 256 == 0 && Task.isCancelled { return rules }', 'if false { return rules }')]),
    ]
    results = []
    for name, source, changes in edits:
        path = folder / 'package/Sources' / source
        original = path.read_bytes()
        data = original.decode()
        for old, new in changes:
            if data.count(old) != 1:
                raise RuntimeError('Ambiguous or stale mutation: ' + name + ' / ' + old)
            data = data.replace(old, new)
        try:
            path.write_text(data)
            assert path.read_bytes() != original
            result = native(folder, selector='InboxSessionIdentityTests|InboxGroupingModelTests/testMigratedMergeSurvivesDeletionRenameAndRestart', expect_failure=True)
            log = (folder / 'tests.log').read_text()
            if 'Compiling Meow ' + source not in log:
                raise RuntimeError('Mutant was not freshly compiled: ' + name)
            (folder / (name + '.log')).write_text(log)
            results.append({'name': name, 'source_sha256': hashlib.sha256(data.encode()).hexdigest(), **result})
        finally:
            path.write_bytes(original)
            if path.read_bytes() != original:
                raise RuntimeError('Failed to restore mutant: ' + name)
    return results


def changed_lines(name):
    old = subprocess.check_output(['git', 'show', BASE + ':' + name], cwd=ROOT).decode().splitlines()
    new = (ROOT / name).read_text().splitlines()
    return {n for tag, _, _, start, end in difflib.SequenceMatcher(a=old, b=new).get_opcodes()
            if tag in ['insert', 'replace'] for n in range(start + 1, end + 1)}


def coverage(folder):
    build = folder / 'package/.build/debug'
    raw = subprocess.check_output(['xcrun', 'llvm-cov', 'export', '-format=lcov',
          '-instr-profile=' + str(build / 'codecov/default.profdata'),
          str(build / 'MeowFailuresPackageTests.xctest/Contents/MacOS/MeowFailuresPackageTests')], cwd=ROOT).decode()
    (folder / 'coverage.lcov').write_text(raw)
    records = {}
    for name in CHANGED:
        found = [record for record in raw.split('end_of_record') if re.search(r'^SF:.*/' + Path(name).name + '$', record, re.M)]
        if len(found) != 1:
            raise RuntimeError('Missing or ambiguous coverage: ' + name)
        records[name] = {int(n): int(count) for n, count in re.findall(r'^DA:(\d+),(\d+)', found[0], re.M)}
    def gate(data):
        result = {}
        for name in CHANGED:
            if name not in data:
                raise RuntimeError('Missing coverage: ' + name)
            measured = {n: data[name][n] for n in changed_lines(name) if n in data[name]}
            if not measured or any(count == 0 for count in measured.values()):
                raise RuntimeError('Uncovered changed lines: ' + name + ': ' + str([n for n, c in measured.items() if c == 0]))
            result[name] = {'covered_changed_executable_lines': len(measured), 'changed_nonexecutable_lines': len(changed_lines(name) - measured.keys())}
        return result
    def controls(check):
        for bad in [{}, {name: {n: 0 for n in rows} for name, rows in records.items()}]:
            try:
                check(bad)
            except RuntimeError:
                continue
            raise RuntimeError('Coverage negative control accepted')
    controls(gate)
    try:
        controls(lambda _: {})
    except RuntimeError as error:
        if str(error) != 'Coverage negative control accepted':
            raise
    else:
        raise RuntimeError('Negative control was vacuous when defense was removed')
    return {'files': gate(records), 'negative_controls_rejected': 2, 'defense_removal_rejected': True,
            'scope': 'Changed executable LLVM line records in the real grouping/model code; no claim of branch coverage or UIKit boundary coverage.'}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--output', type=Path, required=True)
    parser.add_argument('--native-only', action='store_true')
    args = parser.parse_args()
    out = args.output.resolve();out.mkdir(parents=True, exist_ok=False)
    result = {'spec_approval': 'not obtained (autonomous run)', 'independent_verification': 'not performed', 'baseline_commit': BASE}
    source_paths = sorted([*CHANGED, 'ios/MeowTests/InboxSessionIdentityTests.swift', 'ios/MeowTests/InboxGroupingModelTests.swift', 'ios/MeowTests/InboxGroupingTests.swift', 'ios/MeowUITests/UIOptimizationTests.swift', 'ios/scripts/verify_session_identity.py', 'ios/scripts/verify_ui_optimization.py', 'ios/scripts/verify_frontend_recovery.py'])
    result['source_hashes'] = {p: hashlib.sha256((ROOT / p).read_bytes()).hexdigest() for p in source_paths}
    result['tools'] = {'swift': subprocess.check_output(['swift', '--version'], text=True).strip(),
                       'xcode': subprocess.check_output(['xcodebuild', '-version'], text=True).strip()}
    try:
        result['red'] = red(out / 'red')
        result['mutations'] = mutations(out / 'mutations')
        make_package(out / 'native')
        result['native'] = native(out / 'native')
        result['coverage'] = coverage(out / 'native')
        run(['git', 'diff', '--check'], out / 'diff-check.log')
        if args.native_only:
            result['simulator'] = 'not performed (--native-only)'
        else:
            run(['python3', '-B', 'ios/scripts/verify_ui_optimization.py', '--output', str(out / 'simulator'), '--appearance', 'dark'], out / 'simulator.log')
            result['simulator'] = json.loads((out / 'simulator/results.json').read_text())
        result['result'] = 'passed'
    except Exception as error:
        result['result'] = 'failed';result['error'] = str(error)
        raise
    finally:
        (out / 'results.json').write_text(json.dumps(result, indent=2) + '\n')
    print(json.dumps({k: result[k] for k in ['result', 'native', 'coverage']}, indent=2))


if __name__ == '__main__':
    main()
