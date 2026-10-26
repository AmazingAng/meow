#!/usr/bin/env python3
"""Verify Cursor generation snapshots using isolated state and local HTTP only."""
import argparse
import hashlib
import difflib
import json
import os
from pathlib import Path
import subprocess
import tempfile
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

ROOT = Path(__file__).resolve().parents[1]
CORE = 'cli/cmd/meow/cursor_snapshot.go'


def run(argv, output, name, **kwargs):
    result = subprocess.run(argv, cwd=ROOT, text=True, capture_output=True, **kwargs)
    (output / (name + '.log')).write_text(result.stdout + result.stderr)
    if result.returncode:
        raise RuntimeError(f'{name}: exit {result.returncode}; see {output / (name + ".log")}')
    return result.stdout


def coverage(profile):
    covered = total = 0
    for line in profile.splitlines()[1:]:
        if '/cursor_snapshot.go:' not in line:
            continue
        _, statements, count = line.split()
        total += int(statements)
        if int(count):
            covered += int(statements)
    if not total or covered != total:
        raise RuntimeError(f'Cursor snapshot coverage gate: {covered}/{total}, requires 100%')
    return dict(covered_statements=covered, total_statements=total, threshold=100)


def changed_coverage(profile):
    files = [CORE, 'cli/cmd/meow/cursor_setup.go', 'cli/cmd/meow/hook.go', 'cli/internal/setup/cursor.go']
    changes = {}
    for name in files:
        before = subprocess.run(['git', 'show', 'f7ae005:' + name], cwd=ROOT, capture_output=True, text=True)
        old = before.stdout.splitlines() if before.returncode == 0 else []
        new = (ROOT / name).read_text().splitlines()
        touched = set()
        for tag, _, _, start, end in difflib.SequenceMatcher(a=old, b=new).get_opcodes():
            if tag != 'equal':
                touched.update(range(start + 1, end + 1))
        changes[name] = touched
    checked = total = 0
    for line in profile.splitlines()[1:]:
        location, count, hits = line.split()
        name, span = location.rsplit(':', 1)
        name = name.removeprefix('meow.blue/meow/')
        if name not in changes:
            continue
        first, last = span.split(',')
        start = int(first.split('.')[0])
        end, column = map(int, last.split('.'))
        if column == 1:
            end -= 1
        if changes[name].intersection(range(start, end + 1)):
            total += int(count)
            if int(hits):
                checked += int(count)
    if not total or checked != total:
        raise RuntimeError(f'changed-statement gate: {checked}/{total}, requires 100%')
    return dict(covered=checked, total=total, baseline='f7ae005')


def mutation_checks(output):
    cases = [
        ('generation', 'cfg.Key, conversation, generation, kind', 'cfg.Key, conversation, "same-generation", kind', 'TestCursorGenerationSnapshotsAndReplay'),
        ('registration', 'cfg.Server, cfg.Key, conversation, generation, kind', '"same-server", "same-key", conversation, generation, kind', 'TestCursorSnapshotIsolatedByConversationAndRegistration'),
        ('first-write', 'if replace {', 'if true {', 'TestCursorSnapshotFirstWriterWins'),
        ('body', 'ResolveBody: func(adapters.Origin) string { return body }', 'ResolveBody: func(adapters.Origin) string { _ = body; return "" }', 'TestCursorGenerationSnapshotsAndReplay'),
    ]
    original = (ROOT / CORE).read_text()
    results = []
    with tempfile.TemporaryDirectory(prefix='meow-cursor-mutants-') as tmp:
        tmp = Path(tmp)
        for name, before, after, selector in cases:
            if original.count(before) != 1:
                raise RuntimeError('mutation target changed: ' + name)
            changed = tmp / (name + '.go')
            changed.write_text(original.replace(before, after))
            overlay = tmp / (name + '.json')
            overlay.write_text(json.dumps({'Replace': {str(ROOT / CORE): str(changed)}}))
            result = subprocess.run(['go', 'test', '-overlay', str(overlay), './cli/cmd/meow', '-run', '^' + selector + '$', '-count=1', '-json'], cwd=ROOT, text=True, capture_output=True)
            (output / ('mutant-' + name + '.log')).write_text(result.stdout + result.stderr)
            events = [json.loads(line) for line in result.stdout.splitlines() if line.startswith('{')]
            executed = [e['Test'] for e in events if e.get('Test') and e['Action'] == 'run']
            failed = [e['Test'] for e in events if e.get('Test') and e['Action'] == 'fail']
            if result.returncode != 1 or selector not in executed or selector not in failed:
                raise RuntimeError('mutant survived or failed before its behavioral assertion: ' + name)
            # Attribute property-only coverage honestly; not every invariant is
            # exercised by the pure identity property.
            properties = subprocess.run(['go', 'test', '-overlay', str(overlay), './cli/cmd/meow', '-run', '^FuzzCursorSnapshotIdentity$', '-count=1', '-json'], cwd=ROOT, text=True, capture_output=True)
            pe = [json.loads(line) for line in properties.stdout.splitlines() if line.startswith('{')]
            pf = [e['Test'] for e in pe if e.get('Test') and e['Action'] == 'fail']
            if properties.returncode not in (0, 1) or (properties.returncode == 1 and not pf):
                raise RuntimeError('property mutant did not execute: ' + name)
            results.append(dict(name=name, executed=executed, failed=failed, property_failed=pf))
    return results


def real_cli(output):
    requests = []
    records = {}
    lock = threading.Lock()

    class Origin(BaseHTTPRequestHandler):
        def log_message(self, *_):
            pass

        def do_POST(self):
            data = json.loads(self.rfile.read(int(self.headers['Content-Length'])))
            key = self.headers.get('Idempotency-Key')
            with lock:
                if key in records and records[key] != data:
                    self.send_response(409)
                    self.end_headers()
                    return
                records[key] = data
                requests.append((key, data))
            self.send_response(201)
            self.send_header('Content-Type', 'application/json')
            self.end_headers()
            self.wfile.write(b'{"id":"ntf_disposable"}')

    server = ThreadingHTTPServer(('127.0.0.1', 0), Origin)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    try:
        with tempfile.TemporaryDirectory(prefix='meow-cursor-processes-') as folder:
            home = Path(folder)
            binary = home / 'meow'
            run(['go', 'build', '-o', str(binary), './cli/cmd/meow'], output, 'cli-build')
            env = {k: v for k, v in os.environ.items() if not k.startswith(('MEOW_', 'CODEX_', 'CLAUDE_'))}
            env.update(HOME=str(home), MEOW_CONFIG=str(home / 'config.json'), MEOW_SERVER='http://127.0.0.1:' + str(server.server_port), MEOW_KEY='meow_disposable_cursor_rehearsal')
            transcript = home / 'transcript.jsonl'
            transcript.write_text('{"role":"assistant","message":{"content":"must not read"}}\n')

            def event(kind, generation, text='', summary=True):
                value = dict(conversation_id='chat', generation_id=generation, transcript_path=str(transcript), workspace_roots=['/example/project'])
                value.update(dict(hook_event_name='afterAgentResponse', text=text) if kind == 'cursor-response' else dict(hook_event_name='stop', status='completed'))
                result = subprocess.run([str(binary), 'hook', kind] + ([] if summary else ['--no-summary']), input=json.dumps(value), cwd=home, env=env, capture_output=True, text=True, timeout=10)
                if result.returncode or result.stdout or result.stderr:
                    raise RuntimeError('hook did not remain silent/successful')

            event('cursor-response', 'g1', 'First answer 😺')
            if requests:
                raise RuntimeError('capture sent a notification')
            event('cursor-stop', 'g1')
            event('cursor-response', 'g2', 'Second answer')
            event('cursor-response', 'g1', 'Late changed answer')
            event('cursor-stop', 'g1')
            event('cursor-stop', 'g2')
            event('cursor-stop', 'missing')
            event('cursor-response', 'missing', 'Late missing answer')
            event('cursor-stop', 'missing')
            event('cursor-response', 'no-summary', 'Not saved', summary=False)
            event('cursor-stop', 'no-summary', summary=False)
            bodies = [value.get('body', '') for _, value in requests]
            if bodies != ['First answer 😺', 'First answer 😺', 'Second answer', '', '', '']:
                raise RuntimeError('generation replay failed: ' + repr(bodies))
            if len(records) != 4 or requests[0][0] != requests[1][0] or requests[3][0] != requests[4][0]:
                raise RuntimeError('replay delivery identity changed')
            return dict(processes=10, http_requests=len(requests), unique_notifications=len(records), replay_frozen=True, cross_generation_body=False, production_pushes=False)
    finally:
        server.shutdown()
        server.server_close()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--output', required=True, type=Path)
    args = parser.parse_args()
    out = args.output.resolve()
    out.mkdir(parents=True, exist_ok=False)
    result = run(['go', 'test', '-race', '-shuffle=on', '-count=1', '-json', '-coverprofile=' + str(out / 'coverage.out'), './...'], out, 'tests')
    events = [json.loads(line) for line in result.splitlines() if line.startswith('{')]
    passed = [e for e in events if e.get('Test') and e['Action'] == 'pass']
    if not passed or any(e['Action'] in ('skip', 'fail') for e in events):
        raise RuntimeError('empty, failed or skipped test suite')
    run(['go', 'vet', './...'], out, 'vet')
    run(['go', 'build', './...'], out, 'build')
    go_files = [str(p.relative_to(ROOT)) for folder in ('cli', 'adapters') for p in (ROOT / folder).rglob('*.go')]
    if run(['gofmt', '-l', *go_files], out, 'format').strip():
        raise RuntimeError('Go formatting differs')
    run(['python3', '-B', 'scripts/test_install.py'], out, 'installer')
    profile = (out / 'coverage.out').read_text()
    measured = coverage(profile)
    changed = changed_coverage(profile)
    bad = '\n'.join(' '.join(line.split()[:-1] + ['0']) if '/cursor_snapshot.go:' in line else line for line in profile.splitlines())
    try:
        coverage(bad)
    except RuntimeError:
        negative_control = 'rejected known-zero-coverage profile'
    else:
        raise RuntimeError('coverage gate accepted its negative control')
    try:
        changed_coverage(bad)
    except RuntimeError:
        changed_negative_control = 'rejected zero-coverage changed statements'
    else:
        raise RuntimeError('changed-statement gate accepted its negative control')
    run(['go', 'test', './cli/cmd/meow', '-run', '^$', '-fuzz', '^FuzzCursorSnapshotIdentity$', '-fuzztime=3s', '-parallel=2'], out, 'fuzz-identity')
    run(['go', 'test', '-race', './cli/cmd/meow', '-run', '^TestCursorConcurrentStopsFreezeOnePayload$', '-count=20'], out, 'stress')
    report = dict(tests_including_subtests=len(passed), coverage=measured, changed_statement_coverage=changed, coverage_gate_negative_control=negative_control, changed_gate_negative_control=changed_negative_control, mutants=mutation_checks(out), real_cli=real_cli(out), go_version=run(['go', 'version'], out, 'go-version').strip(), spec_approval='not obtained (autonomous run)', independent_verification='not performed', supply_chain='no dependency changes; no new audit required')
    sources = ['go.mod', 'go.sum', 'scripts/verify_cursor_generations.py'] + go_files
    report['source_sha256'] = {name: hashlib.sha256((ROOT / name).read_bytes()).hexdigest() for name in sources}
    (out / 'results.json').write_text(json.dumps(report, indent=2) + '\n')
    print(json.dumps({k: report[k] for k in ['tests_including_subtests', 'coverage', 'changed_statement_coverage', 'real_cli']}, indent=2))


if __name__ == '__main__':
    main()
