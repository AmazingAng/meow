#!/usr/bin/env python3
"""Reproduce the wake-gateway checks using existing local toolchains.

The UI run erases ONLY a simulator whose name starts with 'Meow Gateway'.
All credentials/data are disposable; no configured APNs provider is contacted.
"""
import argparse
from collections import Counter
import hashlib
import http.server
import json
import os
from pathlib import Path
import socket
import sqlite3
import subprocess
import tempfile
import threading
import time
import urllib.error
import urllib.request

ROOT = Path(__file__).resolve().parents[1]


def run(args, output, *, env=None, expected=0):
    output = Path(output)
    with output.open('w') as stream:
        result = subprocess.run(args, cwd=ROOT, env=env, stdout=stream, stderr=subprocess.STDOUT)
    if result.returncode != expected:
        raise RuntimeError(f'{args[0]} failed ({result.returncode}), see {output}')
    return output.read_text()


def captured(args):
    return subprocess.check_output(args, cwd=ROOT, text=True)


def state_hash():
    names = captured(['git', 'ls-files', '--cached', '--others', '--exclude-standard', '-z']).split('\0')
    deleted = set(captured(['git', 'ls-files', '--deleted', '-z']).split('\0'))
    digest = hashlib.sha256()
    for name in sorted(set(names)):
        if not name or name.startswith('docs/verification/'):
            continue
        path = ROOT / name
        if name in deleted:
            digest.update(name.encode() + b'\0deleted\0')
            continue
        if not path.is_file():
            raise RuntimeError(f'source input is missing or not a regular file: {name}')
        digest.update(name.encode() + b'\0' + path.read_bytes())
    return digest.hexdigest()


def go_results(log):
    events = [json.loads(line) for line in log.splitlines() if line.startswith('{')]
    tests = [e for e in events if e.get('Test') and e.get('Action') in ('pass', 'fail', 'skip')]
    if not tests or any(e['Action'] in ('fail', 'skip') for e in tests):
        raise RuntimeError('Go test output is empty, failed or skipped')
    return len(tests)


def coverage_gate(metrics, required, minimum=0.80):
    if not required or set(metrics) != set(required):
        raise RuntimeError('coverage inputs missing or unexpected')
    for name, (covered, total) in metrics.items():
        if total <= 0 or covered < 0 or covered > total or covered / total < minimum:
            raise RuntimeError(f'{name}: coverage {covered}/{total} below {minimum:.0%}')
    return {name: {'covered': values[0], 'total': values[1]} for name, values in metrics.items()}


def core_go_coverage(profile):
    required = ['server/internal/api/gateway.go', 'server/internal/push/gateway.go', 'server/internal/store/gateway.go']
    blocks = {}
    lines = profile.read_text().splitlines()
    if not lines or lines[0] != 'mode: atomic':
        raise RuntimeError('missing atomic coverage profile')
    for line in lines[1:]:
        location, statements, count = line.split()
        previous = blocks.get(location, (int(statements), 0))
        if previous[0] != int(statements):
            raise RuntimeError('inconsistent coverage blocks')
        blocks[location] = (int(statements), previous[1] + int(count))
    metrics = {name: [0, 0] for name in required}
    for location, (statements, count) in blocks.items():
        name = location.split(':')[0].removeprefix('meow.blue/meow/')
        if name in metrics:
            metrics[name][0] += statements if count else 0
            metrics[name][1] += statements
    return coverage_gate(metrics, required)


def core_swift_coverage(report):
    required = ['NotificationContent.swift', 'NotificationDelivery.swift', 'Gateway.swift']
    targets = [target for target in report['targets'] if target['name'] == 'Meow.app']
    if len(targets) != 1:
        raise RuntimeError('missing app coverage target')
    metrics = {Path(f['path']).name: [f['coveredLines'], f['executableLines']]
               for f in targets[0]['files'] if Path(f['path']).name in required}
    return coverage_gate(metrics, required)


def checker_controls():
    # Each intentionally bad input must actually raise. This only demonstrates
    # these failure paths; it is not a proof of arbitrary checker correctness.
    controls = [lambda: go_results(''),
                lambda: go_results('{"Test":"x","Action":"fail"}'),
                lambda: go_results('{"Test":"x","Action":"skip"}'),
                lambda: coverage_gate({'x': [0, 1]}, ['x']),
                lambda: coverage_gate({}, ['x'])]
    for control in controls:
        try:
            control()
        except RuntimeError:
            continue
        raise RuntimeError('negative control did not reach a checker failure')
    return len(controls)


def compose_configs(output):
    env = {k: v for k, v in os.environ.items() if not k.startswith('MEOW_')}
    env.update(MEOW_DOMAIN='meow.example.invalid', MEOW_PUSH_GATEWAY_URL='https://push.example.invalid',
               MEOW_APNS_KEY_PATH='/tmp/non-secret-gateway-fixture.p8', MEOW_APNS_KEY_ID='TEST', MEOW_APNS_TEAM_ID='TEST')
    run(['docker', 'compose', '-f', 'deploy/compose.relay.yaml', 'config', '--quiet'], output / 'compose-relay.log', env=env)
    run(['docker', 'compose', '-f', 'deploy/compose.yaml', '-f', 'deploy/compose.gateway.yaml', 'config', '--quiet'], output / 'compose-gateway.log', env=env)
    return ['relay', 'gateway']


def mutations(output):
    cases = [
        ('global-registration-quota', 'server/internal/api/gateway.go',
         'g.enrollLimit.allow(clientIP(r, g.TrustProxy))', 'g.enrollLimit.allow("installation")',
         './server/internal/api', 'TestGatewayRegistrationIsAutomaticAndPerClient'),
        ('placeholder-destination', 'client/client.go',
         'return ErrNoServer', 'server = "https://placeholder.invalid"',
         './client', 'TestMissingServerNeverUsesAPlaceholder'),
        ('owner-can-wake', 'server/internal/api/gateway.go',
         'in.DeviceID, bearer(r), false', 'in.DeviceID, bearer(r), true',
         './server/internal/api', 'TestGatewayWakePrivacyAndIsolation'),
        ('allow-content-fields', 'server/internal/api/gateway.go',
         'd.DisallowUnknownFields()', '// mutation: accept unknown content fields',
         './server/internal/api', 'TestGatewayWakePrivacyAndIsolation'),
        ('accept-http-202', 'server/internal/push/gateway.go',
         'res.StatusCode != 200', 'res.StatusCode != 200 && res.StatusCode != 202',
         './server/internal/push', 'TestGatewayClientExplicitAcceptance'),
        ('quota-overrun', 'server/internal/store/gateway.go',
         'quota_count<60', 'quota_count<600',
         './server/internal/api', 'TestGatewayConcurrentQuota'),
        ('revocation-noop', 'server/internal/store/gateway.go',
         'DELETE FROM gateway_routes WHERE id=? AND management_hash=?',
         'UPDATE gateway_routes SET quota_count=quota_count WHERE id=? AND management_hash=?',
         './server/internal/api', 'TestGatewayManagementAndRevocation'),
    ]
    outcomes = []
    for name, filename, before, after, package, selector in cases:
        path = ROOT / filename
        original = path.read_bytes()
        source = original.decode()
        if source.count(before) != 1:
            raise RuntimeError(f'{name}: mutation does not match exactly once')
        try:
            path.write_text(source.replace(before, after))
            logfile = output / f'mutant-{name}.log'
            with logfile.open('w') as stream:
                result = subprocess.run(['go', 'test', '-json', package, '-run', '^' + selector + '$', '-count=1'],
                                        cwd=ROOT, stdout=stream, stderr=subprocess.STDOUT)
            events = [json.loads(line) for line in logfile.read_text().splitlines() if line.startswith('{')]
            killed = result.returncode == 1 and any(e.get('Action') == 'fail' and e.get('Test') == selector for e in events)
            if not killed:
                raise RuntimeError(f'{name}: no executed behavioral test killed this mutant; see {logfile}')
            outcomes.append({'mutant': name, 'test': selector, 'killed': True})
        finally:
            path.write_bytes(original)
            if path.read_bytes() != original:
                raise RuntimeError(f'{name}: source restoration failed')
    return outcomes


def port():
    with socket.socket() as s:
        s.bind(('127.0.0.1', 0))
        return s.getsockname()[1]


def xcode_test(output, simulator, name, selector, env=None):
    bundle = output / (name + '.xcresult')
    run(['xcodebuild', 'test', '-project', 'ios/Meow.xcodeproj', '-scheme', 'Meow',
         '-destination', 'platform=iOS Simulator,id=' + simulator,
         '-derivedDataPath', str(output / 'DerivedData'), '-resultBundlePath', str(bundle),
         '-enableCodeCoverage', 'YES', '-only-testing:' + selector, 'CODE_SIGN_IDENTITY=-',
         'CODE_SIGNING_ALLOWED=YES', '-quiet'], output / (name + '.log'), env=env)
    summary = json.loads(captured(['xcrun', 'xcresulttool', 'get', 'test-results', 'summary', '--path', str(bundle)]))
    if summary['failedTests'] or summary['skippedTests'] or summary['passedTests'] == 0:
        raise RuntimeError(f'{name}: empty, skipped or failing tests')
    # Treat new Swift compiler warnings as a failure, not a clean build.
    if 'warning:' in (output / (name + '.log')).read_text():
        raise RuntimeError(f'{name}: build warnings; see log')
    return summary, bundle


def ui_run(output, simulator):
    devices = json.loads(captured(['xcrun', 'simctl', 'list', 'devices', '-j']))['devices']
    device = next((d for values in devices.values() for d in values if d['udid'] == simulator), None)
    if not device or not device['name'].startswith('Meow Gateway'):
        raise RuntimeError('UI harness refuses to erase a simulator without a Meow Gateway name')
    if device['state'] == 'Booted':
        run(['xcrun', 'simctl', 'shutdown', simulator], output / 'sim-shutdown.log')
    run(['xcrun', 'simctl', 'erase', simulator], output / 'sim-erase.log')
    run(['xcrun', 'simctl', 'boot', simulator], output / 'sim-boot.log')

    push_token = 'gwp_' + 'b' * 39
    owner_token = 'gwm_' + 'c' * 39
    lock = threading.Lock()
    observations = []
    class Gateway(http.server.BaseHTTPRequestHandler):
        def log_message(self, *_):
            pass
        def respond(self, status, body=None):
            self.send_response(status)
            self.send_header('Content-Type', 'application/json')
            self.end_headers()
            if body is not None:
                self.wfile.write(json.dumps(body).encode())
        def do_POST(self):
            body = json.loads(self.rfile.read(int(self.headers.get('Content-Length', 0))))
            if self.path == '/v1/gateway/devices':
                if self.headers.get('Authorization') is not None:
                    return self.respond(400)
                if set(body) != {'apns_token', 'apns_env', 'binding_id'}:
                    return self.respond(400)
                with lock:
                    observations.append('enrolled')
                self.respond(201, {'id': 'gwd_uitest', 'management_token': owner_token,
                                  'push_token': push_token, 'binding_id': body['binding_id']})
            elif self.path == '/v1/gateway/push':
                if self.headers.get('Authorization') != 'Bearer ' + push_token or set(body) != {'device_id', 'notification_id', 'priority', 'expires_at'}:
                    return self.respond(400)
                with lock:
                    observations.append('wake')
                self.respond(200, {'accepted': True})
            else:
                self.respond(404)
        def do_PATCH(self):
            if self.headers.get('Authorization') != 'Bearer ' + owner_token:
                return self.respond(401)
            self.rfile.read(int(self.headers.get('Content-Length', 0)))
            with lock:
                observations.append('token-updated')
            self.respond(204)
        def do_DELETE(self):
            if self.path != '/v1/gateway/devices/gwd_uitest' or self.headers.get('Authorization') != 'Bearer ' + owner_token:
                return self.respond(401)
            with lock:
                observations.append('revoked')
            self.respond(204)

    gateway = http.server.ThreadingHTTPServer(('127.0.0.1', 0), Gateway)
    thread = threading.Thread(target=gateway.serve_forever, daemon=True)
    thread.start()
    origin_port = port()
    origin_url = f'http://127.0.0.1:{origin_port}'
    env = {k: v for k, v in os.environ.items() if not k.startswith('MEOW_')}
    env.update(MEOW_ADDR=f'127.0.0.1:{origin_port}', MEOW_DB=str(output / 'ui-origin.db'),
               MEOW_PUSH_GATEWAY_URL=f'http://127.0.0.1:{gateway.server_port}')
    log = (output / 'ui-origin.log').open('w')
    process = subprocess.Popen([str(output / 'meow-server')], cwd=ROOT, env=env, stdout=log, stderr=subprocess.STDOUT)
    try:
        deadline = time.monotonic() + 15
        while True:
            try:
                with urllib.request.urlopen(origin_url + '/healthz', timeout=1) as response:
                    if json.load(response)['status'] == 'ok':
                        break
            except (OSError, urllib.error.URLError):
                if time.monotonic() >= deadline or process.poll() is not None:
                    raise RuntimeError('disposable origin did not start')
                time.sleep(0.1)
        xenv = os.environ.copy()
        xenv.update(TEST_RUNNER_MEOW_GATEWAY_UITEST_SERVER=origin_url)
        summary, bundle = xcode_test(output, simulator, 'ios-ui', 'MeowUITests/GatewayUITests', xenv)
        deadline = time.monotonic() + 5
        while 'revoked' not in observations and time.monotonic() < deadline:
            time.sleep(0.05)
        if observations.count('enrolled') != 1 or 'revoked' not in observations:
            raise RuntimeError(f'enrollment/revocation not observed: {observations}')
        with sqlite3.connect(output / 'ui-origin.db') as db:
            row = db.execute('SELECT push_token FROM device_gateway_bindings').fetchone()
            if row != (push_token,):
                raise RuntimeError('app did not install its actual device binding')
        backup = output / 'ui-backup.db'
        run([str(output / 'meow-server'), 'backup', str(backup)], output / 'ui-backup.log', env=env)
        contents = backup.read_bytes()
        if owner_token.encode() in contents:
            raise RuntimeError('management credential leaked into origin database')
        run(['xcrun', 'xcresulttool', 'export', 'attachments', '--path', str(bundle), '--output-path', str(output / 'screenshots')], output / 'attachments.log')
        return {'summary': summary, 'gateway_events': observations, 'origin_contains_only_wake_capability': True}
    finally:
        process.terminate()
        try:
            process.wait(timeout=5)
        except subprocess.TimeoutExpired:
            process.kill()
            process.wait()
        log.close()
        gateway.shutdown()
        gateway.server_close()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--simulator-id', required=True)
    parser.add_argument('--output', type=Path)
    parser.add_argument('--mutations', action='store_true')
    args = parser.parse_args()
    output = args.output or Path(tempfile.mkdtemp(prefix='meow-gateway-verification-'))
    output.mkdir(parents=True, exist_ok=True)
    results = {'source_hash': state_hash(), 'output': str(output), 'spec_approval': 'not obtained (autonomous run)',
               'tools': {'go': captured(['go', 'version']).strip(), 'xcode': captured(['xcodebuild', '-version']).strip(),
                         'xcodegen': captured(['xcodegen', '--version']).strip()}}
    results['checker_negative_controls'] = checker_controls()
    results['compose_config_checks'] = compose_configs(output)
    print('Evidence directory:', output, flush=True)
    if args.mutations:
        results['mutations'] = mutations(output)
        if state_hash() != results['source_hash']:
            raise RuntimeError('mutations changed the source tree')
        print('Behavioral mutants killed:', len(results['mutations']), flush=True)
    run(['go', 'vet', './...'], output / 'go-vet.log')
    go_files = [name for name in captured(['git', 'ls-files', '--cached', '--others', '--exclude-standard', '*.go']).splitlines()
                if (ROOT / name).is_file()]
    if captured(['gofmt', '-l', *go_files]).strip():
        raise RuntimeError('Go files are not formatted')
    go_log = run(['go', 'test', '-json', '-race', '-shuffle=on', '-count=1', '-coverpkg=./...', '-coverprofile=' + str(output / 'go.cover'), './...'], output / 'go-tests.jsonl')
    results['go_passed'] = go_results(go_log)
    print('Go tests passed:', results['go_passed'], flush=True)
    run(['go', 'test', './server/internal/push', '-run', '^$', '-fuzz', 'FuzzGatewayIdentifiers', '-fuzztime=3s'], output / 'go-fuzz.log')
    results['go_core_coverage'] = core_go_coverage(output / 'go.cover')
    results['go_coverage'] = captured(['go', 'tool', 'cover', '-func', str(output / 'go.cover')])
    (output / 'go-coverage.txt').write_text(results['go_coverage'])
    run(['go', 'build', '-o', str(output / 'meow-server'), './server/cmd/meow-server'], output / 'go-build.log')
    run(['xcodegen', 'generate', '--spec', 'ios/project.yml'], output / 'xcodegen.log')
    summary, bundle = xcode_test(output, args.simulator_id, 'ios-unit', 'MeowTests')
    results['ios_unit'] = summary
    coverage = json.loads(captured(['xcrun', 'xccov', 'view', '--report', '--json', str(bundle)]))
    (output / 'ios-coverage.json').write_text(json.dumps(coverage, indent=2))
    results['swift_core_coverage'] = core_swift_coverage(coverage)
    print('iOS unit tests passed:', summary['passedTests'], flush=True)
    results['ui'] = ui_run(output, args.simulator_id)
    print('iOS UI tests passed:', results['ui']['summary']['passedTests'], flush=True)
    results['final_source_hash'] = state_hash()
    if results['final_source_hash'] != results['source_hash']:
        raise RuntimeError('source changed during verification')
    (output / 'results.json').write_text(json.dumps(results, indent=2))
    print('PASS:', output / 'results.json', flush=True)


if __name__ == '__main__':
    main()
