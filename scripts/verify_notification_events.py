#!/usr/bin/env python3
"""Reproduce explicit-event contract, migration, CLI, native iOS model and SDK checks.

No real accounts, APNs calls, simulator or production configuration are used.
Python stdlib + existing Go, Swift and Xcode toolchains only.
"""
import argparse
import copy
import importlib.util
import json
import os
from pathlib import Path
import re
import shutil
import socket
import sqlite3
import subprocess
import tarfile
import tempfile
import time
import urllib.error
import urllib.request

ROOT = Path(__file__).resolve().parents[1]
BASE = 'be3db816c1aaea70956052c576b44be6d83af11a'
spec = importlib.util.spec_from_file_location('session', ROOT / 'scripts/verify_session_context.py')
helper = importlib.util.module_from_spec(spec)
spec.loader.exec_module(helper)
run = helper.run


def live(out):
    """Use both real server versions, online backup, CLI and real HTTP requests."""
    run(['go', 'build', '-o', str(out / 'meow'), './cli/cmd/meow'], out, 'build-cli')
    run(['go', 'build', '-o', str(out / 'server'), './server/cmd/meow-server'], out, 'build-server')
    with tempfile.TemporaryDirectory(prefix='meow-event-live-') as tmp:
        folder = Path(tmp)
        archive = folder / 'old.tar'
        with archive.open('wb') as dest:
            subprocess.run(['git', 'archive', BASE], cwd=ROOT, stdout=dest, check=True)
        old = folder / 'old'; old.mkdir()
        with tarfile.open(archive) as tar:
            tar.extractall(old, filter='data')
        run(['go', 'build', '-o', str(out / 'server-v4'), './server/cmd/meow-server'], out, 'build-v4', cwd=old)
        with socket.socket() as sock:
            sock.bind(('127.0.0.1', 0)); port = sock.getsockname()[1]
        url = f'http://127.0.0.1:{port}'
        # Build a minimal env instead of inheriting credentials or production hooks.
        env = {key: value for key, value in os.environ.items() if not key.startswith(('MEOW_', 'CODEX_', 'CLAUDE_'))}
        env.update(MEOW_ADDR=f'127.0.0.1:{port}', MEOW_DB=str(folder / 'server.db'),
                   MEOW_CONFIG=str(folder / 'cli.json'), MEOW_SERVER=url)
        def request(path, body=None, token='', idem='', expected=200):
            req = urllib.request.Request(url + path, data=None if body is None else json.dumps(body).encode(),
                headers={'Authorization': 'Bearer ' + token, 'Content-Type': 'application/json', 'Idempotency-Key': idem})
            try:
                with urllib.request.urlopen(req, timeout=3) as res: code, raw = res.status, res.read()
            except urllib.error.HTTPError as error: code, raw = error.code, error.read()
            if code != expected: raise RuntimeError(f'live status {code}, expected {expected} at {path}')
            return json.loads(raw)
        def start(binary, name):
            log = (out / (name + '.log')).open('w')
            proc = subprocess.Popen([str(binary)], env=env, stdout=log, stderr=log)
            for _ in range(100):
                if proc.poll() is not None: log.close(); raise RuntimeError('server exited: ' + name)
                try: request('/healthz'); return proc, log
                except OSError: time.sleep(.05)
            stop(proc, log); raise RuntimeError('server did not start')
        def stop(proc, log):
            proc.terminate()
            try: proc.wait(timeout=5)
            except subprocess.TimeoutExpired: proc.kill(); proc.wait()
            log.close()
        def schema(path):
            with sqlite3.connect(path) as db: return db.execute('PRAGMA user_version').fetchone()[0]
        proc, log = start(out / 'server-v4', 'live-v4')
        try:
            reg = request('/v1/devices', {'name': 'isolated event rehearsal'}, expected=201)
            key, token = reg['key']['secret'], reg['device_token']
            legacy = request('/v1/notifications', {'title': 'New result'}, key, 'old-retry', 201)
            backup = folder / 'before-v5.db'
            run([str(out / 'server-v4'), 'backup', str(backup)], out, 'live-online-backup', env=env)
            if schema(backup) != 4: raise RuntimeError('backup was not v4')
        finally: stop(proc, log)
        proc, log = start(out / 'server', 'live-v5')
        try:
            if schema(folder / 'server.db') != 5: raise RuntimeError('migration did not produce v5')
            retry = request('/v1/notifications', {'title': 'New result'}, key, 'old-retry')
            if retry['id'] != legacy['id'] or 'event' in retry: raise RuntimeError('old retry/backfill changed')
            env['MEOW_KEY'] = key
            for index, event in enumerate(['result', 'attention', 'failure']):
                args = [str(out / 'meow'), 'send', '--idempotency-key', 'new-' + event, '--quiet']
                payload = {'title': '自定义标题', 'event': event, 'source': 'CI', 'priority': 'low',
                           'context': {'workspace': 'meow', 'session': '部署审查'}}
                if index == 1: args += ['--json', json.dumps(payload, ensure_ascii=False)]
                else: args += ['--event', event, '--priority', 'low', '--source', 'CI', '--workspace', 'meow', '--session', '部署审查', '自定义标题']
                run(args, out, 'live-cli-' + event, env=env)
                run(args, out, 'live-cli-retry-' + event, env=env)
                changed = dict(payload, event='failure' if event != 'failure' else 'result')
                request('/v1/notifications', changed, key, 'new-' + event, 422)
            code = run([str(out / 'meow'), 'send', '--event', 'success', 'bad'], out, 'live-invalid-event', env=env, check=False)
            if code != 2: raise RuntimeError('CLI did not reject unknown event')
            for bad in [[], {}, 42, True, 'RESULT', ' result', 'x' * 1000]:
                request('/v1/notifications', {'title': 'hostile', 'event': bad}, key, expected=422 if isinstance(bad, str) else 400)
        finally: stop(proc, log)
        proc, log = start(out / 'server', 'live-restart')
        try:
            records = request('/v1/inbox', token=token)['notifications']
            if len(records) != 4: raise RuntimeError('retries created duplicate messages')
            for n in records:
                if request('/v1/notifications/' + n['id'], token=token) != n: raise RuntimeError('detail/inbox drift')
            (out / 'live-notifications.json').write_text(json.dumps(records, ensure_ascii=False))
        finally: stop(proc, log)
        code = run([str(out / 'server-v4')], out, 'live-downgrade-refusal', env=env, check=False)
        if code != 1 or 'newer than this server supports' not in (out / 'live-downgrade-refusal.log').read_text():
            raise RuntimeError('old binary accepted v5')
        env['MEOW_DB'] = str(folder / 'restored.db'); shutil.copy2(backup, env['MEOW_DB'])
        proc, log = start(out / 'server-v4', 'live-rollback')
        try:
            restored = request('/v1/inbox', token=token)['notifications']
            if len(restored) != 1 or restored[0]['id'] != legacy['id']: raise RuntimeError('rollback lost original record')
        finally: stop(proc, log)
    return {'records': 4, 'v4_to_v5': True, 'old_retry_unchanged': True, 'cli_and_json': True,
            'restart': True, 'online_backup_and_rollback': True, 'hostile_inputs_rejected': 7}


def native(out):
    helper.native_package(out)
    tests = out / 'package/Tests'
    for name in ['NotificationEventTests.swift', 'ProtocolContractTests.swift']:
        shutil.copy2(ROOT / 'ios/MeowTests' / name, tests / name)
    shutil.copy2(ROOT / 'scripts/notification_events/LiveEventTests.swift', tests / 'LiveEventTests.swift')
    for src, name in [(ROOT / 'contracts/fixtures/notifications.json', 'notifications.json'),
                      (ROOT / 'ios/Meow/Resources/Sounds/meow.wav', 'meow.wav'),
                      (out / 'live-notifications.json', 'live-notifications.json')]: shutil.copy2(src, tests / name)
    manifest = out / 'package/Package.swift'
    text = manifest.read_text()
    old = 'path: "Tests")'
    if text.count(old) != 1: raise RuntimeError('test manifest shape changed')
    manifest.write_text(text.replace(old, 'path: "Tests", resources: [.copy("notifications.json"), .copy("meow.wav"), .copy("live-notifications.json")])'))
    run(['swift', 'test', '--package-path', str(out / 'package'), '--enable-code-coverage'], out, 'native')
    log = (out / 'native.log').read_text()
    count = len(re.findall(r"Test Case '.+' passed", log))
    if not count or "Test Suite 'All tests' passed" not in log: raise RuntimeError('empty native suite')
    return count


def mutations(out):
    results = []
    with tempfile.TemporaryDirectory(prefix='meow-event-mutants-') as tmp:
        root = Path(tmp)
        for name in ['message', 'adapters', 'client', 'cli', 'server', 'contracts', 'ios']:
            shutil.copytree(ROOT / name, root / name, ignore=shutil.ignore_patterns('Meow.xcodeproj'))
        for name in ['go.mod', 'go.sum']: shutil.copy2(ROOT / name, root / name)
        cases = [
            ('canonical-event', 'message/normalize.go', 'n.Context, n.Event, n.Type', 'n.Context, "", n.Type', './message', 'TestExplicitEventContract'),
            ('canonical-event-property', 'message/normalize.go', 'n.Context, n.Event, n.Type', 'n.Context, "", n.Type', './message', 'TestEventRoundTripProperties'),
            ('canonical-absent', 'message/normalize.go', '`json:"event,omitempty"`', '`json:"event"`', './message', 'TestUnspecifiedEventPreservesCanonicalBytes'),
            ('api-event', 'server/internal/api/json.go', 'Event: n.Event,', 'Event: "",', './server/internal/api', 'TestNotificationEventHTTP'),
            ('query-event', 'server/internal/api/agent.go', 'message.EventKind(q.Get("event"))', '""', './server/internal/api', 'TestPlainTextNotificationEvent'),
            ('adapter-event', 'adapters/claude.go', 'e.Notification.Event = message.EventResult', 'e.Notification.Event = ""', './adapters', 'TestAgentEventsAreExplicit'),
            ('migration-event', 'server/internal/store/store.go', 'if v < 5 {', 'if v < 4 {', './server/internal/store', 'TestEventMigrationPersistenceAndBackup'),
        ]
        for name, filename, old, new, package, selector in cases:
            file = root / filename; original = file.read_text()
            if original.count(old) != 1: raise RuntimeError('ambiguous mutant ' + name)
            try:
                file.write_text(original.replace(old, new))
                code = run(['go', 'test', package, '-run', '^' + selector + '$', '-count=1', '-json'], out, 'mutant-' + name, cwd=root, check=False)
                events = [json.loads(line) for line in (out / ('mutant-' + name + '.log')).read_text().splitlines() if line.startswith('{')]
                failed = [e['Test'] for e in events if e.get('Action') == 'fail' and e.get('Test')]
                if code != 1 or not any(t.startswith(selector) for t in failed): raise RuntimeError('mutant survived/not executed ' + name)
                results.append({'name': name, 'failed_tests': failed})
            finally: file.write_text(original)
    file = out / 'package/Sources/InboxPresentation.swift'; original = file.read_text()
    old = 'var inboxCategory: InboxCategory? { event }'
    if original.count(old) != 1: raise RuntimeError('ambiguous Swift mutant')
    try:
        file.write_text(original.replace(old, 'var inboxCategory: InboxCategory? { nil }'))
        code = run(['swift', 'test', '--package-path', str(out / 'package'), '--filter', 'NotificationEventTests'], out, 'mutant-swift-event', check=False)
        log = (out / 'mutant-swift-event.log').read_text()
        if code != 1 or not re.search(r"Test Case '.+' failed", log) or 'Executed 0 tests' in log: raise RuntimeError('Swift mutant survived/not executed')
        results.append({'name': 'swift-event', 'behavioral_failure': True})
    finally: file.write_text(original)
    # Restore measured build and coverage after mutating the isolated native copy.
    run(['swift', 'test', '--package-path', str(out / 'package'), '--enable-code-coverage'], out, 'native-final')
    return results


def changed_lines():
    diff = subprocess.check_output(['git', 'diff', '--unified=0', BASE, '--', '*.go', '*.swift'], cwd=ROOT, text=True)
    result = {}; name = None
    for line in diff.splitlines():
        if line.startswith('+++ b/'):
            name = line[6:]
            if name.endswith('_test.go') or '/MeowTests/' in name or '/MeowUITests/' in name: name = None
        elif name and line.startswith('@@'):
            m = re.search(r'\+(\d+)(?:,(\d+))?', line)
            start, count = int(m[1]), int(m[2]) if m[2] else 1
            result.setdefault(name, set()).update(range(start, start + count))
    return {n: lines for n, lines in result.items() if lines}


def coverage_gate(records, expected):
    if not expected or set(records) != set(expected): raise RuntimeError('missing changed-file coverage')
    totals = {}
    for name, lines in expected.items():
        measured = records[name]
        if not measured: raise RuntimeError('no executable changed lines: ' + name)
        uncovered = [line for line, count in measured.items() if count == 0]
        if uncovered: raise RuntimeError('uncovered changed lines: ' + name + ': ' + str(uncovered))
        totals[name] = len(measured)
    return totals


def coverage(out):
    changed = changed_lines()
    # SwiftUI bodies need an iOS runtime; this run compiles them with the SDK.
    declarations = {'message/notification.go', 'message/record.go'}
    for name in declarations:
        if re.search(r'^func ', (ROOT / name).read_text(), re.M): raise RuntimeError('declaration-only coverage exclusion became executable')
    expected = {n: lines for n, lines in changed.items() if n not in declarations and (n.endswith('.go') or n.endswith(('Models.swift', 'InboxPresentation.swift')))}
    records = {n: {} for n in expected}
    for line in (out / 'go.cover').read_text().splitlines():
        m = re.match(r'meow.blue/meow/(.+):(\d+)\.\d+,(\d+)\.\d+ (\d+) (\d+)$', line)
        if m and m[1] in records and int(m[4]) > 0:
            for number in range(int(m[2]), int(m[3]) + 1):
                if number in expected[m[1]]: records[m[1]][number] = min(records[m[1]].get(number, int(m[5])), int(m[5]))
    binary = out / 'package/.build/debug/MeowFailuresPackageTests.xctest/Contents/MacOS/MeowFailuresPackageTests'
    run(['xcrun', 'llvm-cov', 'export', '-format=lcov', '-instr-profile=' + str(out / 'package/.build/debug/codecov/default.profdata'), str(binary)], out, 'coverage')
    for record in (out / 'coverage.log').read_text().split('end_of_record'):
        names = re.findall(r'^SF:(.+)$', record, re.M)
        if not names: continue
        for name in expected:
            if not name.endswith('.swift') or Path(names[0]).name != Path(name).name: continue
            if Path(names[0]).read_bytes() != (ROOT / name).read_bytes(): raise RuntimeError('stale native source')
            records[name] = {int(n): int(v) for n, v in re.findall(r'^DA:(\d+),(\d+)', record, re.M) if int(n) in expected[name]}
    totals = coverage_gate(records, expected)
    bad_cases = [({}, expected), ({n: {line: 0 for line in rows} for n, rows in records.items()}, expected)]
    def controls(gate):
        for bad, manifest in bad_cases:
            try: gate(bad, manifest)
            except RuntimeError: pass
            else: raise AssertionError('coverage accepted negative control')
    controls(coverage_gate)
    try: controls(lambda *_: {})
    except AssertionError: pass
    else: raise RuntimeError('negative controls vacuous without defense')
    return {'executed_changed_lines': totals, 'negative_controls_rejected': 2,
            'declarations_verified_by_roundtrip': sorted(declarations),
            'limit': 'SwiftUI view layout is SDK-compiled, not runtime-measured'}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--output', type=Path, required=True)
    args = parser.parse_args(); out = args.output.resolve(); out.mkdir(parents=True, exist_ok=False)
    results = {'base': BASE}
    results['live'] = live(out)
    results['native_tests'] = native(out)
    run(['go', 'test', '-race', '-shuffle=on', '-count=1', '-coverprofile=' + str(out / 'go.cover'), '-json', './...'], out, 'go')
    run(['go', 'vet', './...'], out, 'go-vet')
    results['mutants'] = mutations(out)
    results['coverage'] = coverage(out)
    run(['xcodegen', 'generate', '--spec', 'ios/project.yml'], out, 'xcodegen')
    run(['xcodebuild', 'build-for-testing', '-project', 'ios/Meow.xcodeproj', '-scheme', 'Meow', '-destination', 'generic/platform=iOS', '-derivedDataPath', str(out / 'ios-build'), 'CODE_SIGNING_ALLOWED=NO'], out, 'ios-sdk')
    run(['git', 'diff', '--check'], out, 'diff-check')
    results['ios_sdk'] = True
    results['toolchains'] = {tool: subprocess.check_output(command, cwd=ROOT, text=True).strip() for tool, command in [('go', ['go', 'version']), ('swift', ['swift', '--version']), ('xcode', ['xcodebuild', '-version'])]}
    (out / 'results.json').write_text(json.dumps(results, ensure_ascii=False, indent=2) + '\n')
    print(json.dumps(results, ensure_ascii=False))


if __name__ == '__main__': main()
