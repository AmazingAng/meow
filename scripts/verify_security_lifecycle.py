#!/usr/bin/env python3
"""Verify lifecycle hardening without production data, installs or releases."""
import argparse
import hashlib
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import importlib.util
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import tempfile
import threading

ROOT = Path(__file__).resolve().parents[1]
BASE = '14464b1'
EVIDENCE = 'docs/engineering/security-lifecycle-evidence.md'
TARGETS = ['cli/internal/config/config.go', 'server/internal/store/store.go',
           'server/internal/store/gateway.go', 'server/internal/store/lifecycle.go',
           'server/cmd/meow-server/gateway_maintenance.go']
LAYERS = {'race-tests', 'coverage', 'checker-controls', 'mutations', 'vet',
          'format', 'installer', 'source-vulnerabilities', 'artifacts',
          'cli-execution', 'secrets', 'source-unchanged'}


def require(condition, message):
    if not condition:
        raise RuntimeError(message)


def inputs():
    raw = subprocess.check_output(['git', 'ls-files', '--cached', '--others',
                                   '--exclude-standard', '-z'], cwd=ROOT)
    return sorted(set(filter(None, raw.decode().split('\0'))))


def source_hash():
    digest = hashlib.sha256()
    for name in inputs():
        if name != EVIDENCE:
            digest.update(name.encode() + b'\0' + (ROOT / name).read_bytes())
    return digest.hexdigest()


def run(args, log, **kwargs):
    with log.open('w') as stream:
        result = subprocess.run(args, cwd=ROOT, stdout=stream,
                                stderr=subprocess.STDOUT, **kwargs)
    require(result.returncode == 0, f'command failed ({result.returncode}): {args[0]}; {log}')
    return log.read_text()


def test_results(text):
    events = [json.loads(line) for line in text.splitlines() if line.startswith('{')]
    results = [e for e in events if e.get('Test') and e.get('Action') in ('pass', 'fail', 'skip')]
    require(results and all(e['Action'] == 'pass' for e in results), 'missing/failed/skipped test evidence')
    return {'passed_including_subtests': len(results)}


def coverage(profile):
    rows = profile.read_text().splitlines()
    require(rows and rows[0] == 'mode: atomic', 'missing atomic coverage')
    targets = {}
    for name in TARGETS:
        diff = subprocess.check_output(['git', 'diff', '--unified=0', BASE, '--', name], cwd=ROOT, text=True)
        changed = set()
        for first, count in re.findall(r'^@@ .* \+(\d+)(?:,(\d+))? @@', diff, re.M):
            changed.update(range(int(first), int(first) + int(count or 1)))
        if not subprocess.check_output(['git', 'ls-files', '--', name], cwd=ROOT):
            changed.update(range(1, len((ROOT / name).read_text().splitlines()) + 1))
        targets[name] = changed
    blocks = {}
    for line in rows[1:]:
        location, n, hits = line.split()
        name, span = location.split(':')
        name = name.removeprefix('meow.blue/meow/')
        first, last = [int(point.split('.')[0]) for point in span.split(',')]
        if name in targets and targets[name].intersection(range(first, last + 1)):
            key = name, span
            blocks[key] = int(n), int(hits) + blocks.get(key, (0, 0))[1]
    for name in TARGETS:
        require(any(path == name and n for (path, _), (n, _) in blocks.items()), 'unmeasured source: ' + name)
    total = sum(n for n, _ in blocks.values())
    covered = sum(n for n, hits in blocks.values() if hits)
    require(total > 0 and covered / total >= .85, f'changed coverage below 85%: {covered}/{total}')
    return {'covered_statements': covered, 'statements': total, 'threshold': .85,
            'uncovered': [f'{name}:{span}' for (name, span), (n, hits) in blocks.items() if n and not hits],
            'excluded': 'main.run APNs daemon wiring; no live Apple credentials in this verifier'}


def checker_controls(profile, out):
    for invalid in ['', '{"Test":"x","Action":"fail"}', '{"Test":"x","Action":"skip"}']:
        try:
            test_results(invalid)
        except RuntimeError:
            pass
        else:
            raise RuntimeError('test checker accepted invalid evidence')
    bad = out / 'zero.cover'
    bad.write_text('\n'.join(line.rsplit(' ', 1)[0] + ' 0' if i else line
                             for i, line in enumerate(profile.read_text().splitlines())) + '\n')
    try:
        coverage(bad)
    except RuntimeError as error:
        require('below 85%' in str(error), 'coverage control did not reach threshold gate')
    else:
        raise RuntimeError('coverage checker accepted zero hits')
    return 'three invalid test manifests and zero-hit coverage rejected'


def mutations(out):
    cases = [
        ('predictable-config', TARGETS[0], 'os.CreateTemp(filepath.Dir(p), ".meow-config-*")',
         'os.OpenFile(p+".tmp", os.O_CREATE|os.O_TRUNC|os.O_RDWR, 0600)',
         './cli/internal/config', 'TestSaveIgnoresPredictableTempAndReplacesDestinationSymlink'),
        ('retain-revoked-key', TARGETS[1], 'DELETE FROM api_keys WHERE id = ? AND device_id = ? AND revoked_at IS NULL',
         'UPDATE api_keys SET revoked_at = 1 WHERE id = ? AND device_id = ? AND revoked_at IS NULL',
         './server/internal/store', 'TestRevokedKeysAreRemovedWithoutLosingHistoryOrQuota'),
        ('expiry-boundary', TARGETS[2], 'column+`=? AND last_seen_at>?`', 'column+`=? AND last_seen_at>=?`',
         './server/internal/store', 'TestGatewayRouteExpiryBoundaryAndRenewal'),
        ('skip-wake-renewal', TARGETS[2], 'UPDATE gateway_routes SET last_seen_at=MAX(last_seen_at,?) WHERE id=?',
         'UPDATE gateway_routes SET last_seen_at=last_seen_at+0*? WHERE id=?',
         './server/internal/store', 'TestGatewayRouteExpiryBoundaryAndRenewal'),
        ('late-gateway-cleanup', TARGETS[4], 'time.NewTicker(5 * time.Minute)', 'time.NewTicker(10 * time.Minute)',
         './server/cmd/meow-server', 'TestGatewayMaintenanceStartsRetriesLogsAndStops'),
    ]
    killed = []
    with tempfile.TemporaryDirectory(prefix='meow-lifecycle-mutants-') as directory:
        copy = Path(directory)
        for name in inputs():
            target = copy / name
            target.parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(ROOT / name, target)
        for name, file, old, new, package, test in cases:
            target = copy / file
            original = target.read_bytes()
            require(original.decode().count(old) == 1, 'mutant anchor missing/ambiguous: ' + name)
            mutated = original.decode().replace(old, new).encode()
            try:
                target.write_bytes(mutated)
                require(target.read_bytes() == mutated and mutated != original, 'mutant not applied')
                result = subprocess.run(['go', 'test', '-json', package, '-run', '^' + test + '$',
                                         '-count=1', '-timeout=30s'], cwd=copy, text=True, capture_output=True)
                (out / ('mutant-' + name + '.log')).write_text(result.stdout + result.stderr)
                events = [json.loads(line) for line in result.stdout.splitlines() if line.startswith('{')]
                require(result.returncode == 1 and any(e.get('Action') == 'fail' and e.get('Test') == test for e in events),
                        'mutant survived or failed before its named assertion: ' + name)
                killed.append(name)
            finally:
                target.write_bytes(original)
                require(target.read_bytes() == original, 'mutant restoration failed')
    return {'killed': len(killed), 'executed': killed}


def cli_execution(binary, out):
    class Fixture(BaseHTTPRequestHandler):
        def log_message(self, *args):
            pass

        def do_GET(self):
            data = json.dumps({'key': {'hint': 'fixture', 'scopes': ['notify']},
                               'device': {'id': 'fixture-device'}}).encode()
            self.send_response(200); self.end_headers(); self.wfile.write(data)

        def do_POST(self):
            self.rfile.read(int(self.headers['Content-Length']))
            self.send_response(201); self.end_headers(); self.wfile.write(b'{"id":"ntf_fixture"}')

    server = ThreadingHTTPServer(('127.0.0.1', 0), Fixture)
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    try:
        with tempfile.TemporaryDirectory(prefix='meow-cli-private-') as directory:
            config = Path(directory) / 'config.json'
            victim = Path(directory) / 'unrelated'
            victim.write_text('preserve')
            Path(str(config) + '.tmp').symlink_to(victim)
            env = {k: v for k, v in os.environ.items() if not k.startswith('MEOW_')}
            env['MEOW_CONFIG'] = str(config)
            run([str(binary), 'login', '--server', f'http://127.0.0.1:{server.server_port}'],
                out / 'cli-login.log', env=env, input='meow_disposable_fixture\n', text=True)
            require(config.stat().st_mode & 0o777 == 0o600 and victim.read_text() == 'preserve', 'CLI saved unsafe credentials')
            run([str(binary), 'send', '--source', 'Fixture', '--event', 'result', '--title', 'Local check', 'Temporary fixture only'],
                out / 'cli-send.log', env=env)
    finally:
        server.shutdown(); server.server_close(); thread.join()
    return 'actual CLI login/save/send against loopback HTTP with disposable key'


def artifacts(out):
    results = []
    host = subprocess.check_output(['go', 'env', 'GOOS', 'GOARCH'], text=True).splitlines()
    for system, arch in [('darwin', 'arm64'), ('darwin', 'amd64'), ('linux', 'arm64'), ('linux', 'amd64')]:
        binary = out / ('meow-' + system + '-' + arch)
        env = dict(os.environ, GOOS=system, GOARCH=arch, CGO_ENABLED='0')
        run(['go', 'build', '-trimpath', '-ldflags', '-s -w -X main.version=0.1.10', '-o', str(binary), './cli/cmd/meow'],
            out / (binary.name + '-build.log'), env=env)
        metadata = run(['go', 'version', '-m', str(binary)], out / (binary.name + '-metadata.log'))
        require('go1.27.3' in metadata, 'unexpected release toolchain')
        run(['go', 'run', 'golang.org/x/vuln/cmd/govulncheck@v1.9.0', '-mode=binary', str(binary)],
            out / (binary.name + '-vulnerabilities.log'))
        results.append({'target': system + '/' + arch, 'sha256': hashlib.sha256(binary.read_bytes()).hexdigest()})
        if [system, arch] == host:
            cli_execution(binary, out)
    run(['go', 'build', '-o', str(out / 'meow-server'), './server/cmd/meow-server'], out / 'server-build.log')
    return results


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--output', type=Path, required=True)
    args = parser.parse_args()
    out = args.output.resolve()
    out.mkdir(parents=True, exist_ok=False)
    before = source_hash()
    report = {'source_sha256': before, 'comparison_base': BASE, 'spec_approval': 'not obtained (autonomous run)',
              'independent_verification': 'not performed', 'layers': {}}
    layers = report['layers']
    report['go'] = subprocess.check_output(['go', 'version'], text=True).strip()
    text = run(['go', 'test', '-race', '-shuffle=20261102', '-count=1', '-covermode=atomic',
                '-coverprofile=' + str(out / 'coverage.out'), '-json', './...'], out / 'tests.jsonl')
    layers['race-tests'] = test_results(text)
    layers['coverage'] = coverage(out / 'coverage.out')
    layers['checker-controls'] = checker_controls(out / 'coverage.out', out)
    layers['mutations'] = mutations(out)
    for name, command in [('vet', ['go', 'vet', './...']), ('installer', ['python3', '-B', 'scripts/test_install.py']),
                          ('source-vulnerabilities', ['go', 'run', 'golang.org/x/vuln/cmd/govulncheck@v1.9.0', './...'])]:
        run(command, out / (name + '.log')); layers[name] = 'pass'
    go_files = [name for name in inputs() if name.endswith('.go')]
    require(not run(['gofmt', '-l', *go_files], out / 'format.log').strip(), 'Go format differs')
    run(['git', 'diff', '--check'], out / 'diff.log'); layers['format'] = 'pass'
    layers['artifacts'] = artifacts(out)
    require((out / 'cli-send.log').is_file(), 'host CLI execution missing')
    layers['cli-execution'] = 'actual login, private config and notification send to localhost fixture'
    diff = subprocess.check_output(['git', 'diff', BASE], cwd=ROOT, text=True)
    for name in subprocess.check_output(['git', 'ls-files', '--others', '--exclude-standard'], cwd=ROOT, text=True).splitlines():
        if name != EVIDENCE and (ROOT / name).suffix not in ('.png', '.jpg'):
            diff += (ROOT / name).read_text()
    scan = subprocess.run(['gitleaks', 'stdin', '--redact', '--no-banner'], input=diff, text=True, capture_output=True)
    (out / 'secrets.log').write_text(scan.stdout + scan.stderr)
    require(scan.returncode == 0, 'diff secret scan failed'); layers['secrets'] = 'pass'
    require(source_hash() == before, 'source changed during verification'); layers['source-unchanged'] = True
    require(set(layers) == LAYERS, 'incomplete layer manifest')
    (out / 'report.json').write_text(json.dumps(report, ensure_ascii=False, indent=2) + '\n')
    print(json.dumps(report, ensure_ascii=False, indent=2))


if __name__ == '__main__':
    main()
