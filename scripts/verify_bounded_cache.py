#!/usr/bin/env python3
"""Verify bounded server caching and durable iPhone history without a simulator.

python3 -B scripts/verify_bounded_cache.py --output dist/bounded-cache-final [--docker]
Uses only existing Go/Swift/Xcode tools and Python's standard library. Test servers
bind to loopback; credentials, SQLite files and fault-injection copies are isolated.
"""
import argparse
import difflib
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import re
import shutil
import socket
import sqlite3
import subprocess
import tempfile
import time
import urllib.error
import urllib.request

ROOT = Path(__file__).resolve().parents[1]
BASE = '4caa7bdbfad7dee2f9ea0cda9b778673cd41c374'
GO_FILES = ['server/cmd/meow-server/main.go', 'server/internal/store/store.go', 'server/internal/api/agent.go']
SWIFT_FILE = 'ios/Meow/App/AppModel.swift'


def require(value, message):
    if not value:
        raise RuntimeError(message)


def run(args, out, name, *, cwd=ROOT, env=None, check=True):
    with (out / (name + '.log')).open('w') as log:
        result = subprocess.run(args, cwd=cwd, env=env, stdout=log, stderr=subprocess.STDOUT)
    require(not check or result.returncode == 0, f'{name} failed: {out / (name + ".log")}')
    return result.returncode


def module(name):
    spec = importlib.util.spec_from_file_location(name, ROOT / 'scripts' / (name + '.py'))
    result = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(result)
    return result


def native_package(out):
    module('verify_session_context').native_package(out)
    package = out / 'package'
    for name in ['ProtocolContractTests.swift', 'CacheRetentionTests.swift']:
        shutil.copy2(ROOT / 'ios/MeowTests' / name, package / 'Tests' / name)
    shutil.copy2(ROOT / 'ios/scripts/frontend_recovery/CacheLiveIntegrationTests.swift', package / 'Tests/CacheLiveIntegrationTests.swift')
    shutil.copy2(ROOT / 'contracts/fixtures/notifications.json', package / 'Tests/notifications.json')
    shutil.copy2(ROOT / 'ios/Meow/Resources/Sounds/meow.wav', package / 'Tests/meow.wav')
    manifest = package / 'Package.swift'
    text = manifest.read_text()
    require(text.count('path: "Tests")') == 1, 'native test resource target changed')
    manifest.write_text(text.replace('path: "Tests")', 'path: "Tests", resources: [.copy("notifications.json"), .copy("meow.wav")])'))


def request(url, path, *, token='', data=None, key='', expected=200):
    req = urllib.request.Request(url + path, data=None if data is None else json.dumps(data).encode(),
                                 headers={'Authorization': 'Bearer ' + token, 'Content-Type': 'application/json', 'Idempotency-Key': key})
    try:
        response = urllib.request.urlopen(req, timeout=3)
    except urllib.error.HTTPError as error:
        response = error
    with response:
        raw = response.read()
        require(response.status == expected, f'{path}: expected {expected}, got {response.status}')
        return json.loads(raw) if raw and path != '/healthz' else raw


def stop(process):
    process.terminate()
    try:
        process.wait(timeout=10)
    except subprocess.TimeoutExpired:
        process.kill()
        process.wait()
        raise RuntimeError('test process did not stop cleanly')


def native_live(out, name):
    folder = out / name
    folder.mkdir()
    with socket.socket() as sock:
        sock.bind(('127.0.0.1', 0))
        port = sock.getsockname()[1]
    url = f'http://127.0.0.1:{port}'
    env = {k: v for k, v in os.environ.items() if not k.startswith('MEOW_')}
    env.update(MEOW_ADDR=f'127.0.0.1:{port}', MEOW_DB=str(folder / 'server.db'), GOCOVERDIR=str(out / 'runtime-coverage'))
    server = None
    native = None
    logs = []
    def start(retention):
        current = dict(env)
        if retention:
            current['MEOW_RETENTION'] = retention
        stream = (folder / ('server-' + (retention or 'default') + '.log')).open('w')
        logs.append(stream)
        process = subprocess.Popen([str(out / 'meow-server')], cwd=ROOT, env=current, stdout=stream, stderr=subprocess.STDOUT)
        for _ in range(100):
            require(process.poll() is None, 'real server exited before readiness')
            try:
                request(url, '/healthz')
                return process
            except OSError:
                time.sleep(.05)
        stop(process)
        raise RuntimeError('real server startup timeout')
    try:
        server = start('720h')
        reg = request(url, '/v1/devices', data={'name': 'cache integration fixture'}, expected=201)
        token, key = reg['device_token'], reg['key']['secret']
        payload = {'title': '旧消息 local history', 'body': '**完整历史** 😺\n```go\nrun()\n```',
                   'context': {'workspace': 'cache test', 'session': 'retention test', 'session_id': 'run-old'}}
        old = request(url, '/v1/notifications', token=key, data=payload, key='old-event', expected=201)
        recent = request(url, '/v1/notifications', token=key, data={'title': 'Recent message'}, key='recent-event', expected=201)
        with sqlite3.connect(folder / 'server.db') as db:
            created = int(time.time() * 1000) - 13 * 3_600_000
            for table, id_column in [('notifications', 'id'), ('notification_receipts', 'notification_id'), ('idempotency', 'resource_id')]:
                db.execute(f'UPDATE {table} SET created_at=? WHERE {id_column}=?', (created, old['id']))
        require(run([str(out / 'meow-server'), 'backup', str(folder / 'before-prune.db')], folder, 'backup', env=env) == 0, 'backup failed')
        fixture = folder / 'fixture.json'
        fixture.write_text(json.dumps({'url': url, 'token': token, 'device': reg['device']['id'],
            'oldID': old['id'], 'recentID': recent['id'], 'folder': folder.as_uri()}))
        fixture.chmod(0o600)
        native_env = dict(env, MEOW_CACHE_INTEGRATION_FIXTURE=str(fixture))
        log = (out / (name + '.log')).open('w')
        logs.append(log)
        native = subprocess.Popen(['swift', 'test', '--package-path', str(out / 'package'), '--enable-code-coverage'],
                                  cwd=ROOT, env=native_env, stdout=log, stderr=subprocess.STDOUT)
        deadline = time.monotonic() + 120
        while not (folder / 'history-saved').exists():
            require(native.poll() is None, 'native tests exited before saving real-server history; see ' + str(out / (name + '.log')))
            require(time.monotonic() < deadline, 'native history save timeout')
            time.sleep(.05)
        stop(server)
        server = start(None)  # No env override: exercise the real 12h default.
        deadline = time.monotonic() + 5
        while True:
            try:
                request(url, '/v1/notifications/' + old['id'], token=token, expected=404)
                break
            except RuntimeError:
                require(time.monotonic() < deadline, 'startup cleanup failed to expire old content')
                time.sleep(.05)
        rows = request(url, '/v1/inbox', token=token)['notifications']
        require([n['id'] for n in rows] == [recent['id']], 'startup prune removed wrong records')
        request(url, '/v1/notifications', token=key, data=payload, key='old-event', expected=404)
        with sqlite3.connect(folder / 'server.db') as db:
            require(db.execute('SELECT count(*) FROM notification_receipts').fetchone()[0] == 2, 'prune lost quota receipts')
        (folder / 'server-pruned').touch()
        require(native.wait(timeout=120) == 0, 'native/live tests failed; see ' + str(out / (name + '.log')))
        text = (out / (name + '.log')).read_text()
        passed = re.findall(r"Test Case '.+' passed", text)
        require(passed and "Test Suite 'All tests' passed" in text and 'CacheLiveIntegrationTests' in text, 'missing native execution')
        require(' skipped ' not in text, 'native suite skipped a test')
        require(request(url, '/v1/inbox', token=token)['notifications'] == [], 'explicit local/server deletion not synchronized')
        with sqlite3.connect(folder / 'server.db') as db:
            require(db.execute('SELECT count(*) FROM notification_receipts').fetchone()[0] == 2, 'explicit delete reset quota ledger')
        with sqlite3.connect(folder / 'before-prune.db') as db:
            require(db.execute('SELECT count(*) FROM notifications').fetchone()[0] == 2, 'online pre-prune backup incomplete')
        return {'native_tests': len(passed), 'real_startup_prune': True, 'local_restart_search_read_delete': True,
                'metadata_independent': True, 'online_backup': True, 'simulator': False, 'phone_install': False}
    finally:
        if native and native.poll() is None:
            stop(native)
        if server and server.poll() is None:
            stop(server)
        for stream in logs:
            stream.close()


def mutated_tests(out):
    results = []
    cases = [
        ('quota-after-expiry', 'server/internal/store/store.go',
         'SELECT COUNT(*) FROM notification_receipts WHERE device_id = ? AND created_at > ?',
         'SELECT COUNT(*) FROM notifications WHERE device_id = ? AND created_at > ?',
         './server/internal/store', 'TestCachePrunedQuotaIsSerialized'),
        ('retry-window', 'server/internal/store/store.go',
         '{`DELETE FROM idempotency WHERE created_at <= ?`, ms(now.Add(-24 * time.Hour))}',
         '{`DELETE FROM idempotency WHERE created_at <= ?`, ms(now.Add(-retention))}',
         './server/internal/store', 'TestCacheExpiryKeepsIdempotencyAndQuota'),
        ('default-window', 'server/cmd/meow-server/main.go', 'env("MEOW_RETENTION", "12h")',
         'env("MEOW_RETENTION", "720h")', './server/cmd/meow-server', 'TestCacheConfiguration'),
    ]
    with tempfile.TemporaryDirectory(prefix='meow-cache-mutants-') as tmp:
        root = Path(tmp)
        for folder in ['server', 'client', 'message', 'adapters', 'contracts']:
            shutil.copytree(ROOT / folder, root / folder)
        for file in ['go.mod', 'go.sum']:
            shutil.copy2(ROOT / file, root / file)
        for name, path, old, new, package, selector in cases:
            file = root / path
            original = file.read_text()
            require(original.count(old) == 1, 'missing/ambiguous mutant target: ' + name)
            file.write_text(original.replace(old, new))
            try:
                status = run(['go', 'test', package, '-run', selector, '-count=1', '-json'], out, 'mutant-' + name, cwd=root, check=False)
                events = [json.loads(line) for line in (out / ('mutant-' + name + '.log')).read_text().splitlines() if line.startswith('{')]
                failures = [e['Test'] for e in events if e.get('Test') and e.get('Action') == 'fail']
                require(status == 1 and failures, 'mutant survived or never executed: ' + name)
                results.append({'mutant': name, 'failed_tests': failures})
            finally:
                file.write_text(original)
    source = out / 'package/Sources/AppModel.swift'
    original = source.read_text()
    for name, old, new in [
        ('local-history', 'var merged = current', 'var merged: [String: MeowNotification] = [:]'),
        ('failed-local-read', 'if hydratedScope == scope { persistInbox() }', 'hydratedScope = scope; persistInbox()'),
    ]:
        require(original.count(old) == 1, 'missing Swift mutant target')
        source.write_text(original.replace(old, new))
        try:
            status = run(['swift', 'test', '--package-path', str(out / 'package'), '--filter', 'CacheRetentionTests'], out, 'mutant-' + name, check=False)
            text = (out / ('mutant-' + name + '.log')).read_text()
            require(status == 1 and re.search(r"Test Case '.+' failed", text) and 'Compiling Meow AppModel.swift' in text,
                    'Swift mutant survived or failed compilation instead of assertions')
            results.append({'mutant': name, 'behavioral_failure': True})
        finally:
            source.write_text(original)
    return results


def changed_lines(path):
    original = subprocess.check_output(['git', 'show', BASE + ':' + path], cwd=ROOT, text=True).splitlines()
    current = (ROOT / path).read_text().splitlines()
    changed = set()
    for tag, _, _, start, end in difflib.SequenceMatcher(a=original, b=current, autojunk=False).get_opcodes():
        if tag in ('insert', 'replace'):
            changed.update(range(start + 1, end + 1))
    return current, changed


def coverage(out):
    run(['go', 'tool', 'covdata', 'textfmt', '-i=' + str(out / 'runtime-coverage'), '-o=' + str(out / 'runtime.cover')], out, 'runtime-profile')
    records = {}
    for name in ['go.cover', 'runtime.cover']:
        for row in (out / name).read_text().splitlines()[1:]:
            match = re.match(r'meow.blue/meow/(.+):(\d+)\.(\d+),(\d+)\.(\d+) (\d+) (\d+)$', row)
            require(match, 'malformed Go coverage row')
            path, start, col, end, endcol, statements, count = match.groups()
            key = (path, int(start), int(col), int(end), int(endcol), int(statements))
            records[key] = records.get(key, 0) + int(count)
    result = {}
    for path in GO_FILES:
        _, changed = changed_lines(path)
        blocks = [(key, count) for key, count in records.items() if key[0] == path and changed.intersection(range(key[1], key[3] + 1))]
        require(blocks, 'missing changed Go coverage: ' + path)
        uncovered = [(key[1], key[3]) for key, count in blocks if not count]
        require(not uncovered, f'uncovered changed Go blocks: {path}: {uncovered}')
        result[path] = {'covered_changed_blocks': len(blocks), 'changed_blocks': len(blocks)}
    build = out / 'package/.build/arm64-apple-macosx/debug'
    binary = build / 'MeowFailuresPackageTests.xctest/Contents/MacOS/MeowFailuresPackageTests'
    run(['xcrun', 'llvm-cov', 'export', '-format=lcov', '-instr-profile=' + str(build / 'codecov/default.profdata'), str(binary)], out, 'swift-coverage')
    text = (out / 'swift-coverage.log').read_text()
    swift = None
    for record in text.split('end_of_record'):
        if re.search(r'^SF:.*/AppModel.swift$', record, re.M):
            swift = {int(n): int(count) for n, count in re.findall(r'^DA:(\d+),(\d+)', record, re.M)}
    require(swift is not None, 'missing AppModel coverage')
    lines, changed = changed_lines(SWIFT_FILE)
    measured = {n: swift[n] for n in changed if n in swift and lines[n - 1].strip() not in ('{', '}')}
    require(measured, 'missing changed Swift executable lines')
    require(all(measured.values()), 'uncovered changed Swift lines: ' + str([n for n, count in measured.items() if not count]))
    result[SWIFT_FILE] = {'covered_changed_lines': len(measured), 'changed_executable_lines': len(measured)}
    # A zero profile must fail the same executable-block criterion.
    def gate(rows):
        require(rows and all(rows), 'missing/uncovered executable coverage')
    gate([count for _, count in blocks])
    for rows in [[], [0] * len(blocks)]:
        try:
            gate(rows)
        except RuntimeError:
            pass
        else:
            raise RuntimeError('coverage gate accepted a negative control')
    result['negative_controls_rejected'] = 2
    return result


def compose_check(out):
    env = {k: v for k, v in os.environ.items() if not k.startswith('MEOW_')}
    env.update(MEOW_DOMAIN='cache-test.example.com', MEOW_APNS_KEY_ID='TEST', MEOW_APNS_TEAM_ID='TEST',
               MEOW_APNS_KEY_PATH=str(out / 'unused.p8'), MEOW_PUSH_GATEWAY_URL='https://gateway.example.com')
    for name in ['compose.yaml', 'compose.relay.yaml']:
        command = ['docker', 'compose', '--env-file', '/dev/null', '-f', 'deploy/' + name, 'config', '--format', 'json']
        for value in [None, '24h']:
            current = dict(env)
            if value:
                current['MEOW_RETENTION'] = value
            config = json.loads(subprocess.check_output(command, cwd=ROOT, env=current, text=True))
            require(config['services']['meow']['environment']['MEOW_RETENTION'] == (value or '12h'), 'Compose dropped retention setting')
    return {'direct_and_relay': True, 'default_and_override': True}


def source_state():
    files = [ROOT / "go.mod", ROOT / "go.sum"]
    for name in ['message', 'adapters', 'cli', 'client', 'server', 'ios', 'scripts', 'deploy']:
        files.extend(p for p in (ROOT / name).rglob('*') if p.is_file()
            and p.suffix in ('.go', '.swift', '.sql', '.py', '.yml', '.yaml', '.md', '.example')
            and '.xcodeproj' not in str(p) and '__pycache__' not in str(p))
    return {str(p.relative_to(ROOT)): hashlib.sha256(p.read_bytes()).hexdigest() for p in files}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--output', required=True, type=Path)
    parser.add_argument('--docker', action='store_true')
    args = parser.parse_args()
    out = args.output.resolve()
    out.mkdir(parents=True, exist_ok=False)
    (out / 'runtime-coverage').mkdir()
    initial_source = source_state()
    result = {'spec_approval': 'not obtained (autonomous run)', 'independent_verification': 'not performed', 'tools': {}}
    def done(name, value):
        result[name] = value
        (out / 'results.json').write_text(json.dumps(result, indent=2) + '\n')
        print(name + ': passed', flush=True)
    for name, command in [('go', ['go', 'version']), ('swift', ['swift', '--version']), ('xcode', ['xcodebuild', '-version'])]:
        result['tools'][name] = subprocess.check_output(command, text=True).strip()
    run(['go', 'test', '-race', '-shuffle=on', '-count=1', '-covermode=atomic', '-coverprofile=' + str(out / 'go.cover'), '-json', './...'], out, 'go-tests')
    events = [json.loads(line) for line in (out / 'go-tests.log').read_text().splitlines() if line.startswith('{')]
    cases = [e for e in events if e.get('Test') and e.get('Action') in ('pass', 'fail', 'skip')]
    require(cases and all(e['Action'] == 'pass' for e in cases), 'missing/failed/skipped Go tests')
    done('go', {'passed_including_subtests': len(cases), 'race': True, 'shuffle': True})
    run(['go', 'vet', './...'], out, 'go-vet')
    done('go_vet', True)
    run(['go', 'test', './server/internal/store', '-run=^$', '-fuzz=FuzzCacheRetentionBoundary', '-fuzztime=2s', '-parallel=2'], out, 'fuzz')
    done('fuzz', (out / 'fuzz.log').read_text().strip())
    run(['go', 'build', '-cover', '-covermode=atomic', '-o', str(out / 'meow-server'), './server/cmd/meow-server'], out, 'server-build')
    invalid_env = {k: v for k, v in os.environ.items() if not k.startswith('MEOW_')}
    invalid_env.update(MEOW_RETENTION='0', GOCOVERDIR=str(out / 'runtime-coverage'))
    require(run([str(out / 'meow-server')], out, 'invalid-cache-config', env=invalid_env, check=False) == 1, 'server accepted disabled cache')
    native_package(out)
    done('native_live_initial', native_live(out, 'native-initial'))
    done('mutations', mutated_tests(out))
    done('native_live_final', native_live(out, 'native-final'))
    done('coverage', coverage(out))
    if args.docker:
        done('compose', compose_check(out))
        image = 'meow-server:bounded-cache-verification'
        run(['docker', 'build', '-t', image, '.'], out, 'docker-build')
        done('docker_runtime', module('verify_notification_modules').docker_runtime(out, image))
    else:
        result['docker'] = 'skipped: --docker not requested'
    run(['xcodegen', 'generate', '--spec', 'ios/project.yml'], out, 'xcodegen')
    run(['xcodebuild', 'build-for-testing', '-project', 'ios/Meow.xcodeproj', '-scheme', 'Meow',
         '-destination', 'generic/platform=iOS', '-derivedDataPath', str(out / 'ios-build'), 'CODE_SIGNING_ALLOWED=NO'], out, 'ios-build')
    done('ios_sdk_compile', True)
    run(['git', 'diff', '--check'], out, 'diff-check')
    require(not subprocess.check_output(['gofmt', '-l', 'server/cmd/meow-server', 'server/internal/store', 'server/internal/api'], cwd=ROOT, text=True).strip(), 'Go formatting failed')
    done('format', True)
    files = GO_FILES + [SWIFT_FILE, 'server/internal/store/cache.go', 'server/internal/store/cache_test.go',
            'server/internal/api/cache_test.go', 'server/cmd/meow-server/cache_test.go', 'ios/MeowTests/CacheRetentionTests.swift']
    result['source_sha256'] = {name: hashlib.sha256((ROOT / name).read_bytes()).hexdigest() for name in files}
    require(initial_source == source_state(), 'source changed during final verification')
    done('source_stability', True)
    done('completed', True)
    print('Evidence: ' + str(out / 'results.json'), flush=True)


if __name__ == '__main__':
    main()
