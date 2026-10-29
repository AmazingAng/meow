#!/usr/bin/env python3
"""Reproduce server performance fixes. Go from go.mod; Python standard library.
Run: python3 -B scripts/verify_server_performance.py
Each run gets a fresh dist/server-performance/run-* directory. No live service
or user credentials are used; mutants run only in a temporary source copy.
"""
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import socket
import subprocess
import tempfile
import time
import urllib.error
import urllib.request

ROOT = Path(__file__).resolve().parents[1]
EXPECTED = ['tests', 'coverage', 'negative-controls', 'build', 'vet', 'format',
            'installer', 'benchmarks', 'mutations', 'live-http', 'source-stability']
CODE = ['cli', 'client', 'message', 'adapters', 'contracts', 'server']


def require(ok, message):
    if not ok:
        raise RuntimeError(message)


def source_state():
    names = [ROOT / 'go.mod', ROOT / 'go.sum', Path(__file__).resolve()]
    for folder in CODE:
        names += [p for p in (ROOT / folder).rglob('*') if p.is_file()]
    return {str(p.relative_to(ROOT)): hashlib.sha256(p.read_bytes()).hexdigest()
            for p in sorted(names)}


def run(args, out, name, cwd=ROOT, timeout=300):
    p = subprocess.run(args, cwd=cwd, capture_output=True, text=True, timeout=timeout)
    (out / (name + '.log')).write_text(p.stdout + p.stderr)
    require(p.returncode == 0, f'{name}: exit {p.returncode}; see {out / (name + ".log")}')
    return p.stdout


def coverage(profile):
    full = ['server/internal/api/limit.go', 'server/internal/push/scheduler.go']
    store = 'server/internal/store/store.go'
    lines = (ROOT / store).read_text().splitlines()
    ranges = []
    for function in ['inboxQuery', 'pruneBatch']:
        start = next(i + 1 for i, s in enumerate(lines) if s.startswith('func ') and ') ' + function + '(' in s)
        end = next((i + 1 for i, s in enumerate(lines) if i + 1 > start and s.startswith('func ')), len(lines) + 1)
        ranges.append((start, end))
    raw = profile.read_text().splitlines()
    require(raw and raw[0].startswith('mode:'), 'invalid coverage profile')
    blocks, seen = [], set()
    for row in raw[1:]:
        location, statements, count = row.split()
        name, span = location.rsplit(':', 1)
        name = name.removeprefix('meow.blue/meow/')
        start, end = span.split(',')
        first, last = int(start.split('.')[0]), int(end.split('.')[0])
        if name in full or name == store and any(first < b and last >= a for a, b in ranges):
            if int(statements):
                blocks.append({'location': location, 'statements': int(statements), 'count': int(count)})
                seen.add(name)
    require(seen == set(full + [store]) and blocks, 'coverage omitted required logic')
    require(all(b['count'] > 0 for b in blocks), 'uncovered performance logic')
    return blocks


def negative_controls(profile, out, checker=coverage):
    bad = out / 'zero.cover'
    rows = profile.read_text().splitlines()
    bad.write_text(rows[0] + '\n' + '\n'.join(r.rsplit(' ', 1)[0] + ' 0' for r in rows[1:]) + '\n')
    try:
        checker(bad)
    except RuntimeError as e:
        require(str(e) == 'uncovered performance logic', 'unexpected coverage control failure')
    else:
        raise RuntimeError('coverage checker accepted zero execution')
    return 'zero-execution coverage rejected'


def mutations(out):
    cases = [
        ('cursor', 'server/internal/store/store.go', [
            ('where := ` WHERE n.device_id = ? AND (n.created_at, n.id) < (?, ?)`',
             'where := ` WHERE n.device_id = ? AND (n.created_at < ? OR (n.created_at = ? AND n.id < ?))`'),
            ('args := []any{deviceID, at, id}', 'args := []any{deviceID, at, at, id}')],
         './server/internal/store', 'TestInboxCursorUsesIndexRangeWithoutSort'),
        ('expiry-index', 'server/internal/store/performance.go', [
            ('CREATE INDEX IF NOT EXISTS notifications_expiry ON notifications(question_id, created_at);', '')],
         './server/internal/store', 'TestPruneUsesExpiryIndexes'),
        ('batch', 'server/internal/store/store.go', [
            ("question_id IS NULL AND created_at <= ? ORDER BY created_at, rowid LIMIT 1000)",
             "question_id IS NULL AND created_at <= ? ORDER BY created_at, rowid LIMIT 1000000)")],
         './server/internal/store', 'TestPruneCommitsBoundedBatchesAndRollsBackFailingBatch'),
        ('early-retry', 'server/internal/push/scheduler.go', [
            ('job.readyAt = time.Now().Add(delay)', 'job.readyAt = time.Now().Add(0 * delay)')],
         './server/internal/push', 'TestScheduledRetryHonorsRetryAfter'),
        ('missing-timer', 'server/internal/push/scheduler.go', [('wake = timer.C', 'wake = nil')],
         './server/internal/push', 'TestScheduledRetryHonorsRetryAfter'),
        ('capacity', 'server/internal/api/limit.go', [
            ('if len(l.buckets) >= maxBuckets {\n\t\t\t\treturn false', 'if false {\n\t\t\t\treturn false')],
         './server/internal/api', 'TestLimiterHardCapacityAndRefill'),
        ('token-property', 'server/internal/api/limit.go', [('b.tokens--', 'b.tokens -= 0')],
         './server/internal/api', 'TestLimiterGeneratedTokenAccounting'),
    ]
    result = []
    with tempfile.TemporaryDirectory(prefix='meow-performance-mutants-') as directory:
        root = Path(directory)
        for name in CODE:
            shutil.copytree(ROOT / name, root / name)
        for name in ['go.mod', 'go.sum']:
            shutil.copy2(ROOT / name, root / name)
        for name, filename, replacements, package, test in cases:
            path = root / filename
            original = path.read_text()
            changed = original
            for old, new in replacements:
                require(changed.count(old) == 1, name + ': missing or ambiguous mutation')
                changed = changed.replace(old, new)
            path.write_text(changed)
            require(path.read_text() == changed and changed != original, 'mutation was not applied')
            try:
                p = subprocess.run(['go', 'test', package, '-run', '^' + test + '$', '-count=1', '-json', '-timeout=30s'],
                                   cwd=root, capture_output=True, text=True, timeout=60)
                (out / ('mutant-' + name + '.log')).write_text(p.stdout + p.stderr)
                events = [json.loads(line) for line in p.stdout.splitlines() if line.startswith('{')]
                failed = [e['Test'] for e in events if e.get('Action') == 'fail' and e.get('Test')]
                require(p.returncode == 1 and test in failed, name + ': survived or did not execute the expected failing test')
                result.append({'name': name, 'failed': failed, 'mutated_sha256': hashlib.sha256(changed.encode()).hexdigest()})
            finally:
                path.write_text(original)
            require(path.read_text() == original, 'mutant restoration failed')
    return result


def live_http(out):
    binary = out / 'meow-server'
    run(['go', 'build', '-o', str(binary), './server/cmd/meow-server'], out, 'build-server')
    with socket.socket() as sock:
        sock.bind(('127.0.0.1', 0))
        port = sock.getsockname()[1]
    env = {k: v for k, v in os.environ.items() if not k.startswith('MEOW_')}
    env.update(MEOW_ADDR=f'127.0.0.1:{port}', MEOW_DB=str(out / 'live.db'))
    base = f'http://127.0.0.1:{port}'

    def request(method, path, token='', body=None, expected=200):
        data = None if body is None else json.dumps(body).encode()
        req = urllib.request.Request(base + path, data=data, method=method,
                                     headers={'Content-Type': 'application/json', 'Authorization': 'Bearer ' + token})
        try:
            response = urllib.request.urlopen(req, timeout=5)
        except urllib.error.HTTPError as e:
            response = e
        with response:
            raw = response.read()
            require(response.status == expected, f'live {method} {path}: status {response.status}')
            return json.loads(raw) if raw else None

    with (out / 'server.log').open('w') as log:
        process = subprocess.Popen([str(binary)], cwd=out, env=env, stdout=log, stderr=log)
        try:
            for _ in range(100):
                require(process.poll() is None, 'server exited on startup')
                try:
                    request('GET', '/healthz')
                    break
                except urllib.error.URLError:
                    time.sleep(.05)
            else:
                raise RuntimeError('server startup timeout')
            reg = request('POST', '/v1/devices', body={'name': 'disposable performance check'}, expected=201)
            phone, sender = reg['device_token'], reg['key']['secret']
            ids = set()
            for i in range(20):
                row = request('POST', '/v1/notifications', sender,
                              {'title': 'fixture ' + str(i), 'body': 'Disposable **Markdown** content', 'event': 'result',
                               'context': {'workspace': 'fixture', 'session_id': 'fixture-session'}}, 201)
                ids.add(row['id'])
            seen, cursor = [], None
            while True:
                path = '/v1/inbox?limit=3' + ('&cursor=' + cursor if cursor else '')
                page = request('GET', path, phone)
                seen += [n['id'] for n in page['notifications']]
                cursor = page['next_cursor']
                if not cursor:
                    break
                require(len(seen) <= 20, 'live pagination failed to advance')
            require(len(seen) == 20 and set(seen) == ids, 'live cursor missed or duplicated records')
            request('POST', '/v1/inbox/read', phone, {})
            require(request('GET', '/v1/inbox/counts', phone)['unread'] == 0, 'read counts regressed')
            request('POST', '/v1/notifications', phone, {'title': 'forbidden'}, 403)
            request('GET', '/v1/inbox', sender, expected=403)
            snapshot = out / 'snapshot.db'
            p = subprocess.run([str(binary), 'backup', str(snapshot)], env=env, cwd=out, capture_output=True, text=True, timeout=30)
            require(p.returncode == 0 and snapshot.is_file(), 'online snapshot failed')
            return {'created': 20, 'paged': len(seen), 'unread_after_read': 0, 'role_separation': '403/403', 'online_snapshot': 'created'}
        finally:
            process.terminate()
            try:
                process.wait(timeout=10)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait()
                raise RuntimeError('server shutdown timeout')


def main():
    parent = ROOT / 'dist/server-performance'
    parent.mkdir(parents=True, exist_ok=True)
    out = Path(tempfile.mkdtemp(prefix='run-', dir=parent))
    print('Evidence: ' + str(out), flush=True)
    before = source_state()
    summary = {'source_sha256': before, 'completed': [], 'spec_approval': 'not obtained (autonomous run)', 'independent_verification': 'not performed'}

    def complete(name, value):
        require(name in EXPECTED and name not in summary['completed'], 'unexpected or duplicate layer')
        summary[name] = value
        summary['completed'].append(name)
        (out / 'summary.json').write_text(json.dumps(summary, indent=2) + '\n')
        print(name + ': passed', flush=True)

    summary['go'] = run(['go', 'version'], out, 'go-version').strip()
    raw = run(['go', 'test', '-race', '-shuffle=20261029', '-count=1', '-coverprofile=' + str(out / 'coverage.out'), '-json', './...'], out, 'tests')
    events = [json.loads(s) for s in raw.splitlines() if s.startswith('{')]
    tests = [e for e in events if e.get('Action') == 'pass' and e.get('Test')]
    require(tests and not any(e.get('Action') == 'fail' for e in events), 'no tests or failures in test stream')
    complete('tests', {'top_level_passed': sum('/' not in e['Test'] for e in tests), 'including_subtests_passed': len(tests), 'failed': 0, 'shuffle_seed': 20261029})
    blocks = coverage(out / 'coverage.out')
    complete('coverage', {'scope': ['api/limit.go', 'push/scheduler.go', 'Store.inboxQuery', 'Store.pruneBatch'],
                          'executed_statements': sum(b['statements'] for b in blocks), 'blocks': blocks})
    control = negative_controls(out / 'coverage.out', out)
    try:
        negative_controls(out / 'coverage.out', out, checker=lambda _: [])
    except RuntimeError as e:
        require(str(e) == 'coverage checker accepted zero execution', 'unexpected disabled-checker control failure')
    else:
        raise RuntimeError('negative control failed to detect disabled coverage checker')
    (out / 'zero.cover').unlink()
    complete('negative-controls', [control, 'negative control detects a disabled coverage checker'])
    complete('build', run(['go', 'build', './...'], out, 'build').strip())
    complete('vet', run(['go', 'vet', './...'], out, 'vet').strip())
    files = [str(p.relative_to(ROOT)) for p in (ROOT / 'server').rglob('*.go')]
    formatted = run(['gofmt', '-l', *files], out, 'format')
    require(not formatted.strip(), 'unformatted Go files: ' + formatted)
    complete('format', 'all server Go files formatted')
    complete('installer', run(['python3', '-B', 'scripts/test_install.py'], out, 'installer').strip())
    complete('benchmarks', run(['go', 'test', './server/internal/store', '-run', '^$', '-bench', '^BenchmarkServerPerformance$', '-benchtime=5x', '-count=1'], out, 'benchmarks').strip())
    complete('mutations', mutations(out))
    complete('live-http', live_http(out))
    require(source_state() == before, 'source changed during verification')
    complete('source-stability', 'source hashes unchanged')
    require(summary['completed'] == EXPECTED, 'missing/reordered verification layers')
    print('All expected layers completed. ' + str(out / 'summary.json'), flush=True)


if __name__ == '__main__':
    main()
