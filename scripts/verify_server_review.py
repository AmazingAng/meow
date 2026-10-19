#!/usr/bin/env python3
"""Reproduce the three server review fixes. Go 1.27.2, Python standard library.
Run: python3 scripts/verify_server_review.py
Writes only dist/server-review-fixes/final; mutants run in a temporary copy.
"""
import hashlib
import json
import os
from pathlib import Path
import shutil
import socket
import subprocess
import tempfile
import time
import urllib.error
import urllib.request

ROOT = Path(__file__).resolve().parents[1]
OUT = ROOT / 'dist/server-review-fixes/final'
API = 'server/internal/api/agent.go'
GATEWAY = 'server/internal/push/gateway.go'
DISPATCH = 'server/internal/push/dispatcher.go'
SOURCE_FILES = [API, GATEWAY, DISPATCH, 'server/internal/push/apns.go']
TEST_FILES = ['server/internal/api/question_scope_test.go', 'server/internal/push/review_regression_test.go']
EXPECTED = ['tests', 'coverage', 'negative-controls', 'build', 'vet', 'format', 'fuzz', 'mutations', 'live-http', 'source-stability']


def require(condition, message):
    if not condition:
        raise RuntimeError(message)


def run(args, name, cwd=ROOT):
    p = subprocess.run(args, cwd=cwd, capture_output=True, text=True)
    (OUT / (name + '.log')).write_text(p.stdout + p.stderr)
    require(p.returncode == 0, f'{name}: exit {p.returncode}; see {OUT / (name + ".log")}')
    return p.stdout


def source_state():
    names = ['go.mod', 'go.sum', 'scripts/verify_server_review.py']
    for folder in ['cli', 'client', 'server']:
        names += [str(p.relative_to(ROOT)) for p in (ROOT / folder).rglob('*') if p.is_file() and p.suffix in ('.go', '.sql')]
    return {name: hashlib.sha256((ROOT / name).read_bytes()).hexdigest() for name in sorted(names)}


def coverage(profile):
    # Scope is the new executable logic. Existing target/message loading was
    # moved inside the attempt loop without changing its existing error branches.
    snippets = {
        API: [('if p.key != nil && !p.key.Has(store.ScopeAsk)', '\n\tif q, ok := s.loadQuestion')],
        GATEWAY: [('return &Error{Status: res.StatusCode', '\n\t}'), ('func retryAfter(', None)],
        DISPATCH: [('if ctx.Err() != nil {\n\t\t\treturn // leave queued', '\n\t\tt, err := '),
                   ('case n.Question != nil && !d.store.Now()', '\n\t\t}'),
                   ('delay := d.Backoff << attempt', '\n\t\tselect {'),
                   ('case <-time.After(delay):', '\n\t\t}')],
    }
    selected = {}
    for name, sections in snippets.items():
        text = (ROOT / name).read_text()
        lines = set()
        for start, end in sections:
            require(text.count(start) == 1, 'coverage start missing/nonunique: ' + start)
            a = text.index(start)
            b = len(text) if end is None else text.index(end, a)
            lines.update(range(text.count('\n', 0, a) + 1, text.count('\n', 0, b) + 1))
        selected[name] = lines
    rows = profile.read_text().splitlines()
    require(rows and rows[0].startswith('mode:'), 'invalid coverage profile')
    blocks, seen = [], set()
    for row in rows[1:]:
        loc, statements, count = row.split()
        name, span = loc.rsplit(':', 1)
        name = name.removeprefix('meow.blue/meow/')
        if name not in selected:
            continue
        first, last = span.split(',')
        a = int(first.split('.')[0])
        b, column = map(int, last.split('.'))
        if column == 1:
            b -= 1
        if selected[name].intersection(range(a, b + 1)) and int(statements) > 0:
            blocks.append({'location': loc, 'statements': int(statements), 'count': int(count)})
            seen.add(name)
    require(seen == set(selected), 'coverage omitted a required source file')
    require(blocks and all(b['count'] > 0 for b in blocks), 'uncovered changed executable block')
    return blocks


def negative_controls(profile, checker=coverage):
    bad = OUT / 'bad-coverage.out'
    bad.write_text('\n'.join([profile.read_text().splitlines()[0]] + [r.rsplit(' ', 1)[0] + ' 0' for r in profile.read_text().splitlines()[1:]]) + '\n')
    checks = []
    for file, expected in [(bad, 'uncovered changed executable block')]:
        try:
            checker(file)
        except RuntimeError as e:
            require(str(e) == expected, 'wrong negative-control failure: ' + str(e))
            checks.append(expected)
        else:
            raise RuntimeError('coverage gate accepted uncovered code')
    bad.write_text('mode: atomic\n')
    try:
        checker(bad)
    except RuntimeError as e:
        require(str(e) == 'coverage omitted a required source file', str(e))
        checks.append(str(e))
    else:
        raise RuntimeError('coverage gate accepted an empty profile')
    bad.unlink()
    return checks


def mutations():
    cases = [
        ('scope', API, 'if p.key != nil && !p.key.Has(store.ScopeAsk)', 'if false'),
        ('header', GATEWAY, 'RetryAfter: retryAfter(res.Header.Get("Retry-After"), time.Now())', 'RetryAfter: 0'),
        ('delay', DISPATCH, 'delay = max(delay, ae.RetryAfter)', 'delay = d.Backoff << attempt'),
        ('resolved', DISPATCH, 'case n.Question != nil && n.Question.Status != store.StatusPending:', 'case false:'),
        ('expiry', DISPATCH, 'case n.Question != nil && !d.store.Now().Before(n.Question.ExpiresAt):', 'case false:'),
    ]
    results = []
    with tempfile.TemporaryDirectory(prefix='meow-server-mutants-') as folder:
        root = Path(folder)
        for name in ('cli', 'client', 'server'):
            shutil.copytree(ROOT / name, root / name)
        for name in ('go.mod', 'go.sum'):
            shutil.copy2(ROOT / name, root / name)
        for name, file, old, new in cases:
            path = root / file
            original = path.read_text()
            require(original.count(old) == 1, f'{name}: ambiguous/missing mutation target')
            changed = original.replace(old, new)
            path.write_text(changed)
            try:
                require(path.read_text() == changed and changed != original, 'mutation not applied')
                p = subprocess.run(['go', 'test', './server/internal/api', './server/internal/push', '-count=1', '-json', '-run', 'TestQuestionReadRequiresAskScope|TestGatewayRetryAfter|TestDispatcherReloadsBeforeRetry'], cwd=root, capture_output=True, text=True)
                (OUT / ('mutant-' + name + '.log')).write_text(p.stdout + p.stderr)
                events = [json.loads(line) for line in p.stdout.splitlines() if line.startswith('{')]
                failed = [e['Test'] for e in events if e.get('Action') == 'fail' and e.get('Test')]
                require(p.returncode == 1 and failed, name + ': survived or failed without an executed failing test')
                results.append({'name': name, 'source_sha256': hashlib.sha256(changed.encode()).hexdigest(), 'failed_tests': failed})
            finally:
                path.write_text(original)
            require(path.read_text() == original, 'mutant restoration failed')
    return results


def live_http():
    binary = OUT / 'meow-server'
    run(['go', 'build', '-o', str(binary), './server/cmd/meow-server'], 'build-server')
    with socket.socket() as sock:
        sock.bind(('127.0.0.1', 0))
        port = sock.getsockname()[1]
    env = {k: v for k, v in os.environ.items() if not k.startswith('MEOW_')}
    env.update(MEOW_ADDR=f'127.0.0.1:{port}', MEOW_DB=str(OUT / 'live.db'))
    base = f'http://127.0.0.1:{port}'
    def request(method, path, token='', body=None, expected=200):
        data = None if body is None else json.dumps(body).encode()
        req = urllib.request.Request(base + path, data=data, method=method, headers={'Content-Type': 'application/json', 'Authorization': 'Bearer ' + token})
        if path.endswith('/responses'):
            req.add_header('Idempotency-Key', 'review-answer')
        try:
            res = urllib.request.urlopen(req, timeout=5)
        except urllib.error.HTTPError as e:
            res = e
        with res:
            payload = json.load(res)
            require(res.status == expected, f'live HTTP {method} {path}: {res.status}, expected {expected}')
            return payload
    with (OUT / 'server.log').open('w') as log:
        proc = subprocess.Popen([str(binary)], cwd=OUT, env=env, stdout=log, stderr=log)
        try:
            for _ in range(100):
                require(proc.poll() is None, 'server exited during startup')
                try:
                    request('GET', '/healthz')
                    break
                except urllib.error.URLError:
                    time.sleep(.05)
            else:
                raise RuntimeError('server startup timed out')
            reg = request('POST', '/v1/devices', body={'name': 'review'}, expected=201)
            phone, agent = reg['device_token'], reg['key']['secret']
            key = request('POST', '/v1/keys', phone, {'name': 'notify only', 'scopes': ['notify']}, 201)
            q = request('POST', '/v1/questions', agent, {'question': 'private question'}, 201)
            request('POST', f'/v1/questions/{q["id"]}/responses', phone, {'answer': 'private answer'}, expected=201)
            denied = request('GET', f'/v1/questions/{q["id"]}', key['secret'], expected=403)
            require(denied == {'error': {'code': 'forbidden', 'message': 'Device Key lacks the ask scope'}}, 'denial leaked content')
            allowed = request('GET', f'/v1/questions/{q["id"]}', agent)
            require(allowed['answer'] == 'private answer', 'authorized answer missing')
            return {'notify_only': 403, 'ask_key': 200, 'answer': 'verified', 'server': 'built binary, loopback, disposable database'}
        finally:
            proc.terminate()
            try:
                proc.wait(timeout=10)
            except subprocess.TimeoutExpired:
                proc.kill()
                proc.wait()
                raise RuntimeError('server did not shut down gracefully')


def main():
    if OUT.exists():
        shutil.rmtree(OUT)
    OUT.mkdir(parents=True)
    before = source_state()
    result = {'go': run(['go', 'version'], 'go-version').strip(), 'source_sha256': before, 'completed': []}
    def complete(name, value):
        result[name] = value
        result['completed'].append(name)
        print(name + ': passed', flush=True)
    profile = OUT / 'coverage.out'
    raw = run(['go', 'test', './...', '-race', '-shuffle=on', '-count=1', '-coverprofile=' + str(profile), '-json'], 'tests')
    events = [json.loads(line) for line in raw.splitlines() if line.startswith('{')]
    passed = [e for e in events if e.get('Action') == 'pass' and e.get('Test')]
    complete('tests', {'top_level_passed': sum('/' not in e['Test'] for e in passed), 'including_subtests_passed': len(passed), 'failed': 0})
    complete('coverage', coverage(profile))
    controls = negative_controls(profile)
    try:
        negative_controls(profile, checker=lambda _: [])
    except RuntimeError as e:
        require(str(e) == 'coverage gate accepted uncovered code', 'unexpected disabled-gate control error')
        controls.append('negative control detects disabled coverage gate')
    else:
        raise RuntimeError('negative control failed to detect disabled coverage gate')
    complete('negative-controls', controls)
    complete('build', run(['go', 'build', './...'], 'build'))
    complete('vet', run(['go', 'vet', './...'], 'vet'))
    formatted = run(['gofmt', '-l', *SOURCE_FILES, *TEST_FILES], 'format')
    require(not formatted.strip(), 'gofmt reported unformatted files')
    complete('format', 'all six Go files formatted')
    complete('fuzz', run(['go', 'test', './server/internal/push', '-run=^$', '-fuzz=^FuzzRetryAfter$', '-fuzztime=5s', '-parallel=2'], 'fuzz'))
    complete('mutations', mutations())
    complete('live-http', live_http())
    require(before == source_state(), 'source changed during verification')
    complete('source-stability', 'verified')
    require(result['completed'] == EXPECTED, 'missing/out-of-order verification layer')
    (OUT / 'results.json').write_text(json.dumps(result, indent=2) + '\n')
    print('All layers passed: ' + str(OUT / 'results.json'))


if __name__ == '__main__':
    main()
