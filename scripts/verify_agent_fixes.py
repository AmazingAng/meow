#!/usr/bin/env python3
"""Verify the scoped CLI fixes without editing installed agent configurations.

Run: python3 scripts/verify_agent_fixes.py --output /tmp/meow-agent-check-new
Go 1.27.2; govulncheck pinned to v1.9.0. Output must not already exist.
"""
import argparse
import difflib
import hashlib
import http.server
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import threading

ROOT = Path(__file__).resolve().parents[1]
BASE = '2749aed'
FILES = ['cli/internal/setup/codex.go', 'cli/internal/setup/skill.go',
         'cli/cmd/meow/hook.go', 'cli/cmd/meow/setup.go']


def source_hash():
    names = ['go.mod', 'go.sum', 'scripts/verify_agent_fixes.py']
    for directory in ('cli', 'client', 'message', 'adapters', 'contracts', 'server'):
        names.extend(str(p.relative_to(ROOT)) for p in (ROOT / directory).rglob('*.go'))
    digest = hashlib.sha256()
    for name in sorted(names):
        digest.update(name.encode())
        digest.update((ROOT / name).read_bytes())
    return digest.hexdigest()


def run(args, output, name, *, root=ROOT, env=None):
    result = subprocess.run(args, cwd=root, env=env, text=True, capture_output=True)
    (output / (name + '.log')).write_text(result.stdout + result.stderr)
    if result.returncode:
        raise RuntimeError(f'{name}: exit {result.returncode}; see {output / (name + ".log")}')
    return result.stdout


def check_coverage(profile, root=ROOT):
    uncovered, checked = [], set()
    changes = {}
    for name in FILES:
        before = subprocess.run(['git', 'show', BASE + ':' + name], cwd=root,
                                capture_output=True, text=True, check=True).stdout.splitlines()
        after = (root / name).read_text().splitlines()
        changed = set()
        for tag, _, _, start, end in difflib.SequenceMatcher(a=before, b=after).get_opcodes():
            if tag != 'equal':
                changed.update(range(start + 1, end + 1))
        changes[name] = changed
    blocks = {}
    for line in profile.read_text().splitlines()[1:]:
        location, statements, count = line.split()
        blocks[location] = blocks.get(location, 0) + int(count)
    for location, count in blocks.items():
        name, span = location.rsplit(':', 1)
        name = name.removeprefix('meow.blue/meow/')
        if name not in changes:
            continue
        start, end = span.split(',')
        first = int(start.split('.')[0])
        last, column = map(int, end.split('.'))
        if column == 1:
            last -= 1
        touched = changes[name].intersection(range(first, last + 1))
        for number in touched:
            text = (root / name).read_text().splitlines()[number - 1].strip()
            if text and not text.startswith('//') and text not in ('}', '})', ')'):
                checked.add((name, number))
                if count == 0:
                    uncovered.append((name, number))
    if not checked:
        raise RuntimeError('changed-line coverage checked no executable lines')
    if uncovered:
        raise RuntimeError('uncovered changed lines: ' + repr(uncovered))
    return {'covered_changed_lines': len(checked), 'missed_changed_lines': 0}


def mutation_tests(output):
    mutations = [
        ('ownership', 'cli/internal/setup/codex.go',
         'return known &&', 'return (known || true) &&'),
        ('continuation', 'adapters/claude.go',
         'e.Notification.Body = in.LastAssistantMessage',
         'e.Notification.Body = ""'),
        ('idempotency', 'adapters/adapter.go',
         'e.EventID, string(b)', 'e.EventID, string(b[:0])+"event"'),
        ('skill-untouched', 'cli/cmd/meow/setup.go', '!exists || !setup.IsMeowSkill(old)', '!exists || len(old) < 0'),
        ('quoted-key', 'cli/internal/setup/codex.go',
         'string(key.Node().Data) == "notify"', 'string(parser.Raw(key.Node().Raw)) == "notify"'),
    ]
    results = []
    with tempfile.TemporaryDirectory(prefix='meow-agent-mutants-') as folder:
        copy = Path(folder)
        for name in ('cli', 'client', 'message', 'adapters', 'contracts', 'server'):
            shutil.copytree(ROOT / name, copy / name)
        for name in ('go.mod', 'go.sum'):
            shutil.copy2(ROOT / name, copy / name)
        for name, file, old, new in mutations:
            path = copy / file
            source = path.read_text()
            if source.count(old) != 1:
                raise RuntimeError(f'mutant {name}: replacement is not unique')
            path.write_text(source.replace(old, new))
            try:
                selector = ('FuzzCodexConfigPreservesSettings' if name == 'quoted-key' else
                            'TestCodexForeign|TestBuildClaudeStop|TestClaudeCompletion|TestSetupLeavesSkills')
                result = subprocess.run(['go', 'test', './cli/internal/setup', './cli/cmd/meow',
                                         '-run', selector,
                                         '-count=1', '-json'], cwd=copy, capture_output=True, text=True)
                (output / ('mutant-' + name + '.log')).write_text(result.stdout + result.stderr)
                events = [json.loads(line) for line in result.stdout.splitlines() if line.startswith('{')]
                failures = [event['Test'] for event in events if event.get('Action') == 'fail' and event.get('Test')]
                if result.returncode != 1 or not failures:
                    raise RuntimeError(f'mutant {name} survived or failed without an executed failing test')
                results.append({'name': name, 'killed_by': failures})
            finally:
                path.write_text(source)
            if path.read_text() != source:
                raise RuntimeError(f'mutant {name} was not restored')
    return results


def real_cli(output):
    binary = output / "tools with spaces'quote" / 'meow'
    binary.parent.mkdir()
    run(['go', 'build', '-o', str(binary), './cli/cmd/meow'], output, 'cli-build')
    received, committed = [], {}

    class Handler(http.server.BaseHTTPRequestHandler):
        def do_POST(self):
            body = json.loads(self.rfile.read(int(self.headers['Content-Length'])))
            key = self.headers.get('Idempotency-Key')
            if self.path != '/v1/notifications' or self.headers.get('Authorization') != 'Bearer meow_test':
                self.send_response(401)
                self.end_headers()
                return
            received.append((key, body))
            if key in committed and committed[key] != body:
                self.send_response(422)
            else:
                committed[key] = body
                self.send_response(201)
            self.end_headers()
            self.wfile.write(b'{"id":"ntf_test"}')

        def log_message(self, *_):
            pass

    server = http.server.ThreadingHTTPServer(('127.0.0.1', 0), Handler)
    thread = threading.Thread(target=server.serve_forever)
    thread.start()
    config = output / 'config.json'
    config.write_text(json.dumps({'server': 'http://127.0.0.1:' + str(server.server_port), 'key': 'meow_test'}))
    config.chmod(0o600)
    env = os.environ.copy()
    for key in ('MEOW_KEY', 'MEOW_SERVER', 'MEOW_HOOK_DISABLED', 'MEOW_EXECUTABLE'):
        env.pop(key, None)
    env.update(MEOW_CONFIG=str(config), CLAUDE_CONFIG_DIR=str(output / 'claude-config'))
    try:
        for index, (active, message) in enumerate([(False, 'Initial'), (True, 'Final'), (True, 'Final')]):
            payload = {'session_id': 'session', 'prompt_id': 'prompt',
                       'stop_hook_active': active, 'last_assistant_message': message}
            result = subprocess.run([str(binary), 'hook', 'claude-stop'], input=json.dumps(payload),
                                    capture_output=True, text=True, env=env, timeout=10)
            if result.returncode or result.stdout or result.stderr:
                raise RuntimeError(f'real hook {index} was not silent and successful')
        if len(received) != 3 or len(committed) != 2 or received[1][0] != received[2][0]:
            raise RuntimeError('real hook continuation/idempotency check failed')
        run([str(binary), 'setup', 'claude', '--yes', '--no-test'], output, 'setup-install', env=env)
        if (output / 'claude-config/skills/meow/SKILL.md').exists():
            raise RuntimeError('setup installed a skill; skills.sh owns it now')
        run([str(binary), 'setup', 'claude', '--yes', '--no-test'], output, 'setup-reinstall', env=env)
        run([str(binary), 'setup', 'claude', '--uninstall', '--yes'], output, 'setup-uninstall', env=env)
        if (output / 'claude-config/skills/meow/SKILL.md').exists():
            raise RuntimeError('installed skill survived uninstall')
        return {'hook_requests': len(received), 'distinct_notifications': len(committed),
                'setup_install_reinstall_uninstall': 'passed'}
    finally:
        server.shutdown()
        server.server_close()
        thread.join()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--output', required=True, type=Path)
    args = parser.parse_args()
    out = args.output.resolve()
    out.mkdir(parents=True, exist_ok=False)
    before_hash = source_hash()
    result = {'go': run(['go', 'version'], out, 'go-version').strip(), 'layers': {}}
    test_json = run(['go', 'test', '-race', '-shuffle=20261018', '-count=1', '-coverpkg=./...', '-coverprofile=' + str(out / 'coverage.out'),
                     '-json', './...'], out, 'tests')
    events = [json.loads(line) for line in test_json.splitlines() if line.startswith('{')]
    skipped = [e.get('Test') for e in events if e.get('Action') == 'skip']
    if skipped:
        raise RuntimeError('test suite skipped cases: ' + repr(skipped))
    result['layers']['tests'] = {'passed': sum(e.get('Action') == 'pass' and bool(e.get('Test')) for e in events), 'skipped': 0}
    result['layers']['coverage'] = check_coverage(out / 'coverage.out')
    # Negative control: zeroing coverage must make this gate reject its input.
    fake = out / 'coverage-negative.out'
    fake.write_text('\n'.join(line.rsplit(' ', 1)[0] + ' 0' if i else line
                              for i, line in enumerate((out / 'coverage.out').read_text().splitlines())) + '\n')
    try:
        check_coverage(fake)
    except RuntimeError as error:
        if 'uncovered changed lines' not in str(error):
            raise
        result['layers']['coverage_negative_control'] = 'rejected zeroed counters'
    else:
        raise RuntimeError('coverage gate accepted its negative control')
    run(['go', 'build', './...'], out, 'build')
    result['layers']['build'] = 'passed'
    run(['go', 'vet', './...'], out, 'vet')
    result['layers']['vet'] = 'passed'
    files = [str(p.relative_to(ROOT)) for p in (ROOT / 'cli').rglob('*.go')]
    formatted = run(['gofmt', '-l', *files], out, 'gofmt')
    if formatted.strip():
        raise RuntimeError('unformatted files: ' + formatted)
    result['layers']['gofmt'] = 'passed'
    run(['go', 'test', './cli/internal/setup', '-run', '^$', '-fuzz', '^FuzzCodexConfigPreservesSettings$',
         '-fuzztime=5s', '-parallel=2'], out, 'fuzz')
    result['layers']['fuzz'] = '5-second native Go fuzz run passed; 200 deterministic round trips also run in the suite'
    result['layers']['mutation'] = mutation_tests(out)
    result['layers']['real_cli'] = real_cli(out)
    run(['go', 'run', 'golang.org/x/vuln/cmd/govulncheck@v1.9.0', './...'], out, 'vulnerabilities')
    result['layers']['vulnerabilities'] = 'no reachable vulnerabilities reported'
    expected = {'tests', 'coverage', 'coverage_negative_control', 'build', 'vet', 'gofmt', 'fuzz', 'mutation', 'real_cli', 'vulnerabilities'}
    if set(result['layers']) != expected:
        raise RuntimeError('verification layer manifest is incomplete')
    result['source_hash'] = source_hash()
    if before_hash != result['source_hash']:
        raise RuntimeError('Go source or verifier changed during verification')
    result['limits'] = ['independent verification not performed', 'no real agent session or APNs repeated',
                        'staticcheck unavailable; go vet used', 'Go coverage reports statement blocks, not full branch coverage']
    (out / 'results.json').write_text(json.dumps(result, ensure_ascii=False, indent=2) + '\n')
    print(json.dumps(result, ensure_ascii=False, indent=2))


if __name__ == '__main__':
    main()
