#!/usr/bin/env python3
"""Verify the real local inbox and AppModel without starting a simulator.

Run from the repository root:
python3 -B ios/scripts/verify_local_inbox.py --output dist/local-inbox/final
"""
import argparse
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import random
import re
import shutil
import socket
import subprocess
import time
import urllib.request

ROOT = Path(__file__).resolve().parents[2]
spec = importlib.util.spec_from_file_location('recovery', ROOT / 'ios/scripts/verify_frontend_recovery.py')
recovery = importlib.util.module_from_spec(spec)
spec.loader.exec_module(recovery)
SOURCE_FILES = ['ios/Meow/App/AppModel.swift', 'ios/Meow/App/InboxStore.swift', 'ios/Shared/MeowAPI.swift']


def require(condition, message):
    if not condition:
        raise RuntimeError(message)


def run(args, log, **kwargs):
    with log.open('w') as stream:
        result = subprocess.run(args, cwd=ROOT, text=True, stdout=stream, stderr=subprocess.STDOUT, **kwargs)
    require(result.returncode == 0, 'command failed: ' + str(log))
    return result


def sources():
    files = ['go.mod', 'go.sum', 'ios/project.yml']
    for folder in ['ios', 'server', 'client', 'cli']:
        files += [str(p.relative_to(ROOT)) for p in (ROOT / folder).rglob('*')
                  if p.is_file() and p.suffix in ('.swift', '.py', '.go', '.sql', '.xcstrings', '.xcprivacy', '.entitlements')
                  and '.xcodeproj' not in str(p)]
    return {name: hashlib.sha256((ROOT / name).read_bytes()).hexdigest() for name in sorted(set(files))}


def executable_coverage(output):
    build = output / 'package/.build/arm64-apple-macosx/debug'
    binary = build / 'MeowFailuresPackageTests.xctest/Contents/MacOS/MeowFailuresPackageTests'
    profile = output / 'coverage.lcov'
    run(['xcrun', 'llvm-cov', 'export', '-format=lcov',
         '-instr-profile=' + str(build / 'codecov/default.profdata'), str(binary)], profile)
    return profile


def coverage_gate(profile, manifest):
    records = {}
    for block in profile.read_text().split('end_of_record'):
        name = re.findall(r'^SF:(.+)$', block, re.M)
        if name:
            records[Path(name[0]).name] = {int(n): int(count) for n, count in re.findall(r'^DA:(\d+),(\d+)', block, re.M)}
    result = {}
    for filename, changed in manifest.items():
        rows = records.get(Path(filename).name)
        require(rows is not None, 'missing required coverage: ' + filename)
        code = (ROOT / filename).read_text().splitlines()
        for number, line in changed.items():
            require(code[int(number)-1] == line, 'stale changed-line manifest: ' + filename)
        # LLVM sometimes emits a zero-count line for the unreachable closing
        # brace of a while-true async function. Exclude pure delimiters uniformly,
        # including delimiters with nonzero counters; keep them in the manifest.
        measured = {int(n): rows[int(n)] for n in changed if int(n) in rows
                    and code[int(n)-1].strip() not in ('{', '}')}
        require(measured, 'no executable lines: ' + filename)
        require(all(count > 0 for count in measured.values()),
                'uncovered changed lines: ' + filename + ': ' + str([n for n, count in measured.items() if not count]))
        result[filename] = {'covered_executable_lines': len(measured), 'changed_executable_lines': len(measured)}
    return result


def coverage_controls(output, profile, manifest):
    bad = output / 'negative-coverage.lcov'
    bad.write_text('SF:missing.swift\nDA:1,0\nend_of_record\n')
    for target, label in [(bad, 'missing'), (profile, 'zero')]:
        if label == 'zero':
            bad.write_text(re.sub(r'^DA:(\d+),\d+', r'DA:\1,0', profile.read_text(), flags=re.M))
            target = bad
        try:
            coverage_gate(target, manifest)
        except RuntimeError as error:
            require(('missing required' if label == 'missing' else 'uncovered changed') in str(error), str(error))
        else:
            raise RuntimeError('coverage negative control accepted: ' + label)
    def prove_not_vacuous(checker):
        try:
            checker(bad, manifest)
        except RuntimeError:
            return
        raise RuntimeError('negative control detects a disabled coverage checker')
    try:
        prove_not_vacuous(lambda *_: {})
    except RuntimeError as error:
        require(str(error) == 'negative control detects a disabled coverage checker', str(error))
    else:
        raise RuntimeError('the checker control was vacuous')
    bad.unlink()
    return {'missing_profile_rejected': True, 'zero_counts_rejected': True, 'disabled_checker_detected': True}


def mutations(output):
    cases = [
        ('scope', 'InboxStore.swift', 'let bytes = Data((normalized + "\\n" + token).utf8)', 'let bytes = Data("same-device".utf8)'),
        ('search', 'AppModel.swift', 'return inFilter && (query.isEmpty || notification.title.localizedCaseInsensitiveContains(query)',
         'return inFilter && (true || notification.title.localizedCaseInsensitiveContains(query)'),
        ('local-mutations', 'AppModel.swift', 'for (id, changedAt) in itemRevisions where changedAt > revision {',
         'for (id, changedAt) in itemRevisions where changedAt > UInt64.max {'),
        ('late-session', 'AppModel.swift', 'let items = try await api.allInbox()\n            guard generation == loadGeneration, !isChangingServer else { return }',
         'let items = try await api.allInbox()\n            guard true else { return }'),
        ('rollback', 'InboxStore.swift', 'do { try execute("ROLLBACK") }', 'do { try execute("COMMIT") }'),
    ]
    evidence = []
    for name, filename, old, new in cases:
        source = output / 'package/Sources' / filename
        original = source.read_bytes()
        require(original.decode().count(old) == 1, 'missing/nonunique mutant target: ' + name)
        changed = original.decode().replace(old, new).encode()
        try:
            source.write_bytes(changed)
            require(source.read_bytes() == changed and original != changed, 'mutation not applied')
            shutil.rmtree(output / 'package/.build')
            result = recovery.base.native(output, name='mutant-' + name,
                                          test_filter='InboxStoreTests|LocalInboxModelTests', check=False)
            require(result['exit'] != 0 and result['failures'], 'mutant survived/no executed assertions: ' + name)
            require('Compiling Meow ' + filename in (output / ('mutant-' + name + '.log')).read_text(),
                    'mutant did not compile afresh: ' + name)
            evidence.append({'name': name, 'source_sha256': hashlib.sha256(changed).hexdigest(), 'failed_tests': result['failures']})
            print('mutation ' + name + ': killed', flush=True)
        finally:
            source.write_bytes(original)
        require(source.read_bytes() == original, 'mutation restoration failed')
    return evidence


def shuffle(output):
    source = (ROOT / 'ios/MeowTests/LocalInboxTests.swift').read_text()
    classes = re.split(r'(?:final class|extension) (InboxStoreTests|LocalInboxModelTests)(?:[^\n]*)\{', source)
    names = []
    for index in range(1, len(classes), 2):
        names += [classes[index] + '.' + name for name in re.findall(r'func (test\w+)\(', classes[index+1])]
    require(len(names) == len(set(names)) and len(names) >= 20, 'missing/duplicate local regression selection')
    random.Random(20261020).shuffle(names)
    for index, name in enumerate(names):
        log = output / f'shuffled-{index}.log'
        run(['swift', 'test', '--package-path', str(output / 'package'), '--skip-build', '--filter', name], log)
        text = log.read_text()
        require(len(re.findall(r"Test Case '-\[.*\]' passed ", text)) == 1 and 'Executed 1 test' in text,
                'missing/incomplete shuffled testcase: ' + name)
    return {'seed': 20261020, 'passed': len(names), 'scope': 'new local inbox tests, one XCTest process per case', 'order': names}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--output', required=True, type=Path)
    args = parser.parse_args()
    output = args.output.resolve()
    output.mkdir(parents=True, exist_ok=False)
    before = sources()
    result = {'source_sha256': before, 'completed': [], 'tools': {}}
    def complete(name, value):
        result[name] = value; result['completed'].append(name)
        print(name + ': passed', flush=True)
    for name, command in [('swift', ['swift', '--version']), ('xcode', ['xcodebuild', '-version']), ('go', ['go', 'version'])]:
        result['tools'][name] = subprocess.check_output(command, text=True).strip()
    recovery.package(output)
    for filename in ['LiveIntegrationTests.swift', 'LocalInboxIntegrationTests.swift']:
        shutil.copy2(ROOT / 'ios/scripts/frontend_recovery' / filename, output / 'package/Tests' / filename)
    run(['go', 'build', '-o', str(output / 'meow-server'), './server/cmd/meow-server'], output / 'server-build.log')
    with socket.socket() as listener:
        listener.bind(('127.0.0.1', 0)); port = listener.getsockname()[1]
    env = {k: v for k, v in os.environ.items() if not k.startswith('MEOW_')}
    env.update(MEOW_ADDR=f'127.0.0.1:{port}', MEOW_DB=str(output / 'integration.db'))
    with (output / 'server.log').open('w') as stream:
        process = subprocess.Popen([str(output / 'meow-server')], cwd=ROOT, env=env, stdout=stream, stderr=subprocess.STDOUT)
        try:
            for _ in range(100):
                require(process.poll() is None, 'real Go server exited')
                try:
                    with urllib.request.urlopen(f'http://127.0.0.1:{port}/healthz', timeout=1): break
                except OSError: time.sleep(.05)
            else: raise RuntimeError('real Go server startup timed out')
            os.environ['MEOW_RECOVERY_INTEGRATION_URL'] = f'http://127.0.0.1:{port}'
            complete('native_with_real_http', recovery.native(output))
            require(not result['native_with_real_http']['exit'] and not result['native_with_real_http']['failures'], 'native tests failed')
            manifest = json.loads((ROOT / 'docs/verification/local-inbox-changed-lines.json').read_text())
            profile = executable_coverage(output)
            complete('coverage', coverage_gate(profile, manifest))
            complete('coverage_controls', coverage_controls(output, profile, manifest))
            complete('shuffled', shuffle(output))
            complete('mutations', mutations(output))
            shutil.rmtree(output / 'package/.build')
            complete('restored_native', recovery.native(output))
            require(result['restored_native']['exit'] == 0 and not result['restored_native']['failures'], 'restored tests failed')
            complete('final_coverage', coverage_gate(executable_coverage(output), manifest))
        finally:
            process.terminate()
            try: process.wait(timeout=10)
            except subprocess.TimeoutExpired:
                process.kill(); process.wait(); raise RuntimeError('real Go server did not stop cleanly')
            os.environ.pop('MEOW_RECOVERY_INTEGRATION_URL', None)
    run(['go', 'test', '-race', '-shuffle=on', '-count=1', '-json', './...'], output / 'go-tests.jsonl')
    events = [json.loads(line) for line in (output / 'go-tests.jsonl').read_text().splitlines() if line.startswith('{')]
    tests = [e for e in events if e.get('Test') and e.get('Action') in ('pass', 'fail', 'skip')]
    require(tests and all(e['Action'] == 'pass' for e in tests), 'missing/failed/skipped Go tests')
    complete('go', {'passed_including_subtests': len(tests), 'race': True, 'shuffle': True})
    run(['go', 'vet', './...'], output / 'go-vet.log')
    complete('go_vet', 'passed')
    run(['go', 'build', './...'], output / 'go-build.log')
    complete('go_build', 'passed')
    run(['xcodegen', 'generate', '--spec', 'ios/project.yml'], output / 'xcodegen.log')
    run(['xcodebuild', 'build-for-testing', '-project', 'ios/Meow.xcodeproj', '-scheme', 'Meow',
         '-destination', 'generic/platform=iOS', '-derivedDataPath', str(output / 'ios-build'), 'CODE_SIGNING_ALLOWED=NO'], output / 'ios-build.log')
    complete('ios_compile', 'main app, notification extension, all unit and UI test targets')
    run(['git', 'diff', '--check'], output / 'diff-check.log')
    json.loads((ROOT / 'ios/Meow/Resources/Localizable.xcstrings').read_text())
    complete('format_and_localization', 'diff whitespace and JSON parsing passed; no Swift formatter installed')
    require(before == sources(), 'source changed during final verification')
    complete('source_stability', 'unchanged')
    require(result['completed'] == ['native_with_real_http', 'coverage', 'coverage_controls', 'shuffled', 'mutations',
        'restored_native', 'final_coverage', 'go', 'go_vet', 'go_build', 'ios_compile', 'format_and_localization', 'source_stability'],
        'verification layer manifest incomplete')
    result.update(physical_execution='not performed', simulator_started=False, independent_verification='not performed', spec_approval='not obtained (autonomous run)')
    (output / 'results.json').write_text(json.dumps(result, indent=2) + '\n')
    print('All checks passed: ' + str(output / 'results.json'))


if __name__ == '__main__': main()
