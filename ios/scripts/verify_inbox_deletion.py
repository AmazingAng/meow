#!/usr/bin/env python3
"""Verify inbox deletion without running a simulator or touching a phone.

python3 -B ios/scripts/verify_inbox_deletion.py --output dist/inbox-deletion-final
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
import subprocess
import socket
import time
import urllib.error
import urllib.request
import sys

sys.dont_write_bytecode = True
ROOT = Path(__file__).resolve().parents[2]
spec = importlib.util.spec_from_file_location('recovery', ROOT / 'ios/scripts/verify_frontend_recovery.py')
recovery = importlib.util.module_from_spec(spec)
spec.loader.exec_module(recovery)


def run(args, log, cwd=ROOT, expected=0):
    with log.open('w') as stream:
        result = subprocess.run(args, cwd=cwd, stdout=stream, stderr=subprocess.STDOUT, env={**os.environ, 'PYTHONDONTWRITEBYTECODE': '1'})
    if result.returncode != expected:
        raise RuntimeError(f'command failed ({result.returncode}): {args}; see {log}')



BASELINE = 'e5f9b7f5d5fd54e269a8e49f6226baf4bffb8dd4'


def changed_lines(filename):
    text = subprocess.check_output(['git', 'diff', '--no-ext-diff', '--unified=0', BASELINE, '--', filename], cwd=ROOT, text=True)
    tracked = subprocess.run(['git', 'ls-files', '--error-unmatch', filename], cwd=ROOT, capture_output=True).returncode == 0
    if not tracked:
        return set(range(1, len((ROOT / filename).read_text().splitlines()) + 1))
    numbers = set()
    for line in text.splitlines():
        match = re.match(r'@@ -\d+(?:,\d+)? \+(\d+)(?:,(\d+))? @@', line)
        if match:
            start, length = int(match[1]), int(match[2] or '1')
            numbers.update(range(start, start + length))
    return numbers


def coverage_gate(records, changed, exclusions):
    result = {}
    for filename, numbers in changed.items():
        if filename not in records:
            raise RuntimeError('missing coverage: ' + filename)
        lines = (ROOT / filename).read_text().splitlines()
        measured = {n: records[filename][n] for n in numbers if n in records[filename]
                    and lines[n-1].strip() not in ('', '{', '}', '})', ')') and n not in exclusions.get(filename, set())}
        if not measured or any(value == 0 for value in measured.values()):
            raise RuntimeError('uncovered changed executable lines: ' + filename + ': ' + str([n for n,v in measured.items() if v == 0]))
        result[filename] = {'covered': len(measured), 'measured': len(measured)}
    return result


def check_coverage(output, package):
    build = package / '.build/arm64-apple-macosx/debug'
    export = subprocess.check_output(['xcrun', 'llvm-cov', 'export', '-format=lcov',
        '-instr-profile=' + str(build / 'codecov/default.profdata'),
        str(build / 'MeowFailuresPackageTests.xctest/Contents/MacOS/MeowFailuresPackageTests')], text=True)
    (output / 'coverage.lcov').write_text(export)
    swift = ['ios/Meow/App/AppModel.swift', 'ios/Shared/MeowAPI.swift', 'ios/Shared/InboxDeletion.swift']
    go = ['server/internal/store/deletion.go', 'server/internal/api/deletion.go', 'server/internal/api/api.go']
    records = {}
    for block in export.split('end_of_record'):
        names = re.findall(r'^SF:(.+)$', block, re.M)
        for name in swift:
            if names and Path(names[0]).name == Path(name).name:
                records[name] = {int(n): int(count) for n,count in re.findall(r'^DA:(\d+),(\d+)', block, re.M)}
    for row in (output / 'go.cover').read_text().splitlines()[1:]:
        match = re.fullmatch(r'meow.blue/meow/(.+):(\d+)\.\d+,(\d+)\.\d+ \d+ (\d+)', row)
        if not match:
            raise RuntimeError('unrecognized Go coverage row')
        name, start, end, count = match[1], int(match[2]), int(match[3]), int(match[4])
        if name in go:
            record = records.setdefault(name, {})
            for n in range(start, end + 1):
                record[n] = max(record.get(n, 0), count)
    # Narrow declared exception: SQLite fault injection exercises statement and
    # transaction failures, but not a driver Scan/type error or interrupted row
    # iterator. These defensive failure returns are not claimed as covered.
    source = (ROOT / go[0]).read_text().splitlines()
    scan = source.index('\t\t\tif err := rows.Scan(&question); err != nil {') + 1
    iterator = source.index('\t\terr = rows.Err()') + 1
    exclusions = {go[0]: {scan + 1, scan + 2, iterator + 3}}
    changed = {name: changed_lines(name) for name in swift + go}
    result = coverage_gate(records, changed, exclusions)
    def controls(gate):
        for bad in [{}, {name: {n: 0 for n in values} for name,values in records.items()}]:
            try:
                gate(bad, changed, exclusions)
            except RuntimeError:
                pass
            else:
                raise RuntimeError('coverage negative control was accepted')
    controls(coverage_gate)
    try:
        controls(lambda *_: {})
    except RuntimeError as error:
        if str(error) != 'coverage negative control was accepted': raise
    else:
        raise RuntimeError('coverage controls are vacuous with the defense removed')
    return {'files': result, 'negative_controls_rejected': 2, 'controls_with_defense_removed_rejected': True,
            'excluded': {go[0]: sorted(exclusions[go[0]])},
            'exclusion_reason': 'SQLite driver Scan/type error and mid-iteration I/O failure not fault-injected',
            'unmeasured': ['SwiftUI view rendering and UIKit platform boundaries: iOS SDK compile only']}


def mutations(output, package):
    mutations = [
        ('local-removal', package / 'Sources/AppModel.swift',
         'savedNotifications.removeAll(where: matches)', 'savedNotifications.removeAll { _ in false }',
         ['swift', 'test', '--package-path', str(package), '--filter', 'InboxDeletionModelTests']),
        ('stale-detail', package / 'Sources/AppModel.swift',
         'guard revision == deletionRevision else { return self.notification(id: id) != nil }',
         'guard true else { return self.notification(id: id) != nil }',
         ['swift', 'test', '--package-path', str(package), '--filter', 'InboxDeletionModelTests']),
        ('swift-upper-bound', package / 'Sources/InboxDeletion.swift',
         'milliseconds < before', 'milliseconds <= before',
         ['swift', 'test', '--package-path', str(package), '--filter', 'InboxDeletionRangeTests']),
        ('sql-upper-bound', ROOT / 'server/internal/store/deletion.go',
         'where = `device_id = ? AND created_at >= ? AND created_at < ?`',
         'where = `device_id = ? AND created_at >= ? AND created_at <= ?`',
         ['go', 'test', '-count=1', './server/internal/store', '-run', 'TestDeleteGeneratedHalfOpenIntervals']),
        ('sql-device-isolation', ROOT / 'server/internal/store/deletion.go',
         'where = `device_id = ? AND created_at >= ? AND created_at < ?`',
         'where = `? IS NOT NULL AND created_at >= ? AND created_at < ?`',
         ['go', 'test', '-count=1', './server/internal/store', '-run', 'TestDeleteGeneratedHalfOpenIntervals']),
    ]
    results = []
    for name,path,old,new,args in mutations:
        original = path.read_bytes()
        if original.decode().count(old) != 1:
            raise RuntimeError('ambiguous mutant: ' + name)
        modified = original.decode().replace(old,new).encode()
        try:
            path.write_bytes(modified)
            if path.read_bytes() != modified or modified == original:
                raise RuntimeError('mutation not applied')
            log = output / ('mutant-' + name + '.log')
            with log.open('w') as stream:
                result = subprocess.run(args, cwd=ROOT, stdout=stream, stderr=subprocess.STDOUT)
            text = log.read_text()
            if path.suffix == '.swift':
                executed = 'Compiling Meow ' + path.name in text
                assertion = 'XCTAssert' in text and 'failed' in text
            else:
                executed = '--- FAIL: TestDeleteGeneratedHalfOpenIntervals' in text
                assertion = executed
            if result.returncode != 1 or not executed or not assertion:
                raise RuntimeError('mutant survived or did not fail a behavioral assertion: ' + name)
            results.append({'name': name, 'killed': True, 'recompiled_or_uncached_test': True,
                            'property_suite_only': name in ['swift-upper-bound', 'sql-upper-bound', 'sql-device-isolation']})
        finally:
            path.write_bytes(original)
            if path.read_bytes() != original:
                raise RuntimeError('mutation restore failed')
    return results


def live_server(output):
    binary = output / 'meow-server'
    run(['go', 'build', '-o', str(binary), './server/cmd/meow-server'], output / 'server-build.log')
    with socket.socket() as sock:
        sock.bind(('127.0.0.1', 0))
        port = sock.getsockname()[1]
    base = f'http://127.0.0.1:{port}'
    env = {key:value for key,value in os.environ.items() if not key.startswith('MEOW_')}
    env.update(MEOW_ADDR=f'127.0.0.1:{port}', MEOW_DB=str(output / 'live.sqlite'))
    with (output / 'live-server.log').open('w') as log:
        process = subprocess.Popen([str(binary)], env=env, stdout=log, stderr=subprocess.STDOUT)
        try:
            for _ in range(100):
                try:
                    with urllib.request.urlopen(base + '/healthz', timeout=1) as res:
                        if res.status == 200: break
                except (OSError, urllib.error.URLError):
                    if process.poll() is not None:
                        raise RuntimeError('isolated server exited early')
                    time.sleep(.05)
            else: raise RuntimeError('server readiness timed out')
            def request(method,path,token=None,body=None,want=200):
                headers = {'Content-Type':'application/json'}
                if token: headers['Authorization'] = 'Bearer ' + token
                req = urllib.request.Request(base+path, method=method, headers=headers,
                    data=json.dumps(body).encode() if body is not None else None)
                try:
                    res = urllib.request.urlopen(req, timeout=3)
                except urllib.error.HTTPError as error:
                    res = error
                with res:
                    if res.status != want: raise RuntimeError(f'live {method} {path}: unexpected status {res.status}')
                    data = res.read()
                    return json.loads(data) if data else None
            device = request('POST','/v1/devices',body={'name':'Deletion verification'},want=201)
            token, key = device['device_token'], device['key']['secret']
            message = request('POST','/v1/notifications',key,{'title':'Live deletion 😺'},201)
            request('DELETE','/v1/notifications/'+message['id'],key,want=403)
            request('DELETE','/v1/notifications/'+message['id'],token,want=204)
            request('GET','/v1/notifications/'+message['id'],token,want=404)
            for i in range(3): request('POST','/v1/notifications',key,{'title':f'Bulk {i}'},201)
            before = int(time.time()*1000)+1000
            result = request('DELETE',f'/v1/inbox?from=0&before={before}',token)
            if result != {'deleted':3}: raise RuntimeError('live bulk count mismatch')
            if request('GET','/v1/inbox',token)['notifications'] != []: raise RuntimeError('live history resurrected')
            if request('GET','/v1/inbox/counts',token)['unread'] != 0: raise RuntimeError('live badge mismatch')
        finally:
            process.terminate()
            try: process.wait(timeout=10)
            except subprocess.TimeoutExpired:
                process.kill();process.wait();raise RuntimeError('server failed to stop')
    return 'real binary + HTTP + isolated SQLite; single, bulk, auth, missing detail, empty history and badge verified'

def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--output', type=Path, required=True)
    parser.add_argument('--native-only', action='store_true')
    args = parser.parse_args()
    output = args.output.resolve()
    output.mkdir(parents=True, exist_ok=False)
    source_hash = recovery.base.state_hash()
    recovery.package(output)
    package = output / 'package'
    shutil.copy2(ROOT / 'ios/MeowTests/InboxDeletionTests.swift', package / 'Tests/InboxDeletionTests.swift')
    run(['swift', 'test', '--package-path', str(package), '--enable-code-coverage'], output / 'swift.log')
    if args.native_only:
        print('Native tests passed: ' + str(output / 'swift.log'))
        return
    run(['go', 'test', '-race', '-shuffle=on', '-count=1', '-coverpkg=./server/internal/store,./server/internal/api', '-coverprofile=' + str(output / 'go.cover'), '-json', './...'], output / 'go.jsonl')
    coverage = check_coverage(output, package)
    mutant_results = mutations(output, package)
    run(['swift', 'test', '--package-path', str(package), '--enable-code-coverage'], output / 'swift-restored.log')
    run(['go', 'test', '-count=1', './server/internal/store', './server/internal/api', '-run', 'TestDelete'], output / 'go-restored.log')
    live = live_server(output)
    run(['go', 'vet', './...'], output / 'go-vet.log')
    run(['go', 'build', './...'], output / 'go-build.log')
    run(['xcodegen', 'generate', '--spec', 'ios/project.yml'], output / 'xcodegen.log')
    run(['xcodebuild', 'build-for-testing', '-project', 'ios/Meow.xcodeproj', '-scheme', 'Meow',
         '-destination', 'generic/platform=iOS', '-derivedDataPath', str(output / 'ios-build'),
         'CODE_SIGNING_ALLOWED=NO'], output / 'ios-build.log')
    run(['git', 'diff', '--check'], output / 'diff-check.log')
    json.loads((ROOT / 'ios/Meow/Resources/Localizable.xcstrings').read_text())
    # Fresh native process per case in seeded random dispatch order: verifies
    # independent test setup. Full Swift suite above uses shared-process order.
    binary = package / '.build/arm64-apple-macosx/debug/MeowFailuresPackageTests.xctest'
    listing = subprocess.check_output(['swift', 'test', 'list', '--package-path', str(package)], text=True, stderr=subprocess.STDOUT)
    names = re.findall(r'^MeowFailureTests\.[A-Za-z0-9_]+/[A-Za-z0-9_]+$', listing, re.M)
    if not names:
        raise RuntimeError('missing XCTest inventory')
    random.Random(20261021).shuffle(names)
    with (output / 'swift-shuffled.log').open('w') as stream:
        for name in names:
            # XCTest prints its entire environment on invalid arguments. Pass
            # only runtime essentials; no developer/API credentials.
            safe_env = {key: os.environ[key] for key in ['PATH', 'HOME', 'TMPDIR'] if key in os.environ}
            result = subprocess.run(['xcrun', 'xctest', '-XCTest', name, str(binary)],
                                    stdout=subprocess.PIPE, stderr=subprocess.STDOUT, env=safe_env, text=True)
            stream.write(result.stdout)
            if result.returncode != 0 or 'Executed 1 test, with 0 failures' not in result.stdout:
                raise RuntimeError('shuffled native test failed or selected no test: ' + name)
    events = [json.loads(line) for line in (output / 'go.jsonl').read_text().splitlines() if line.startswith('{')]
    if any(event.get('Action') == 'fail' or (event.get('Action') == 'skip' and event.get('Test')) for event in events):
        raise RuntimeError('Go test failed or unexpectedly skipped')
    result = {'native_tests': len(names), 'go_passed_including_subtests': sum(
        1 for line in (output / 'go.jsonl').read_text().splitlines()
        if line.startswith('{') and (event := json.loads(line)).get('Action') == 'pass' and event.get('Test')),
        'packages_without_test_files': [event['Package'] for event in events if event.get('Action') == 'skip' and not event.get('Test')],
        'ios_sdk_build': 'passed; main app, extension, unit and UI test targets',
        'simulator': 'not started', 'phone_install': 'not performed',
        'spec_approval': 'not obtained (autonomous run)', 'independent_verification': 'not performed'}
    if recovery.base.state_hash() != source_hash:
        raise RuntimeError('source changed during verification')
    result.update(source_sha256=source_hash, coverage=coverage, mutations=mutant_results, live_endpoint=live,
        tools={name: subprocess.check_output(command, text=True).strip() for name,command in [('go',['go','version']), ('swift',['swift','--version']), ('xcode',['xcodebuild','-version'])]})
    (output / 'results.json').write_text(json.dumps(result, indent=2) + '\n')
    print('Checks passed: ' + str(output / 'results.json'))


if __name__ == '__main__': main()
