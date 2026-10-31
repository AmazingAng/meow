#!/usr/bin/env python3
"""Recheck notification-only launch hardening with disposable state only."""
import argparse
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import tempfile

ROOT = Path(__file__).resolve().parents[1]
BASE = '6c79fa1'
REPORT = 'docs/engineering/launch-hardening-evidence.md'
EXPECTED = {'go-tests', 'build', 'vet', 'format', 'installer', 'vulnerabilities',
            'retired-execution', 'coverage', 'mutation', 'compose', 'secrets', 'ios', 'source'}
spec = importlib.util.spec_from_file_location('gateway_checks', ROOT/'scripts/verify_gateway_limits.py')
helpers = importlib.util.module_from_spec(spec)
spec.loader.exec_module(helpers)


def run(args, log, *, cwd=ROOT, env=None):
    with log.open('w') as stream:
        result = subprocess.run(args, cwd=cwd, env=env, stdout=stream, stderr=subprocess.STDOUT)
    if result.returncode != 0:
        raise RuntimeError(f'{args} failed: {log}')
    return log.read_text()


def source_hash():
    digest = hashlib.sha256()
    for name in source_files():
        if name == REPORT:
            continue
        path = ROOT/name
        if not path.is_file():
            raise RuntimeError('missing source: '+name)
        digest.update(name.encode()+b'\0'+path.read_bytes())
    return digest.hexdigest()


def source_files():
    deleted = subprocess.check_output(['git', 'ls-files', '--deleted', '-z'], cwd=ROOT).decode().split('\0')
    return [name for name in helpers.inputs() if name not in set(deleted)]


def retired_gate(files):
    patterns = {
        'server/internal/store/': r'func \(.*\) (CreateQuestion|Respond|CancelQuestion)\(',
        'server/internal/api/': r'func \(.*\) (createQuestion|respond|waitQuestion|cancelQuestion)\(',
        'client/': r'func \(.*\) (Ask|Wait|WaitOnce|Respond|Cancel)\(',
        'cli/cmd/meow/': r'func (cmdAsk|cmdWait|waitAndPrint)\(',
    }
    for name, text in files.items():
        for prefix, pattern in patterns.items():
            if name.startswith(prefix) and re.search(pattern, text):
                raise RuntimeError('retired executable function: '+name)
        if name in ('ios/Meow/App/AppModel.swift', 'ios/Shared/MeowAPI.swift') and re.search(r'func (answer|answerFromNotification|flushReplies|respond|pendingInbox)\(', text):
            raise RuntimeError('retired Swift sender: '+name)
        if name == 'ios/Meow/App/ReplyQueue.swift' and any(word in text for word in ['URLSession', 'MeowAPI', 'makeSender', 'func submit(', 'func flush(']):
            raise RuntimeError('saved archive can still send')
    return 'no compiled Go/Swift execution chain; legacy writer fixtures are test-only'


def security_coverage(profile):
    targets = {'server/internal/store/private_files.go': [0, 0],
               'server/internal/store/gateway.go': [0, 0],
               'server/internal/api/gateway.go': [0, 0],
               'server/internal/api/gateway_limits.go': [0, 0]}
    lines = profile.read_text().splitlines()
    if not lines or lines[0] != 'mode: atomic': raise RuntimeError('missing atomic coverage')
    for line in lines[1:]:
        location, statements, count = line.split()
        name = location.split(':')[0].removeprefix('meow.blue/meow/')
        if name in targets:
            n = int(statements)
            targets[name][1] += n
            if int(count): targets[name][0] += n
    for name, (covered, total) in targets.items():
        if total <= 0 or covered / total < 0.80:
            raise RuntimeError(f'{name} security coverage {covered}/{total} below 80%')
    return targets


def mutations(output):
    cases = [
        ('legacy-key-history', 'server/internal/api/api.go', '"GET /v1/questions/{id}", s.withDevice(s.getQuestion)', '"GET /v1/questions/{id}", s.withKey(store.ScopeNotify, s.getQuestion)', './server/internal/api', 'TestLaunchLegacyHistoryRequiresOwningPhone'),
        ('grant-ask', 'server/internal/store/store.go', 'var DefaultScopes = []string{ScopeNotify}', 'var DefaultScopes = []string{ScopeNotify, "ask"}', './server/internal/api', 'TestLaunchSenderKeysAreNotifyOnly'),
        ('world-readable-files', 'server/internal/store/private_files.go', 'f.Chmod(0600)', 'f.Chmod(0644)', './server/internal/store', 'TestPrivateExistingFilesAndSymlinks'),
        ('reinstate-default-32', 'server/internal/api/gateway_limits.go', 'DefaultGatewayMaxConcurrentPushes = 0', 'DefaultGatewayMaxConcurrentPushes = 32', './server/internal/api', 'TestGatewayLimitsDefaultHasNoFixedConcurrencyCap'),
        ('erase-rotated-route', 'server/internal/store/gateway.go', 'DELETE FROM gateway_routes WHERE id=? AND apns_token=? AND apns_env=?', 'DELETE FROM gateway_routes WHERE id=? AND ? IS NOT NULL AND ? IS NOT NULL', './server/internal/api', 'TestGatewayInvalidationPreservesConcurrentTokenRotation'),
        ('retired-false-success', 'server/internal/api/api.go', 'http.StatusGone, "feature_retired"', 'http.StatusOK, "feature_retired"', './server/internal/api', 'TestLaunchQuestionExecutionRetiredWithoutSideEffects'),
        ('route-quota-bypass', 'server/internal/store/gateway.go', 'strings.ToLower(route.APNsToken), route.APNsEnv, now', 'strings.ToLower(route.ID), route.APNsEnv, now', './server/internal/store', 'TestGatewayQuotaSharedAcrossRoutesAndTokenAliases'),
        ('erase-upgrade-quota', 'server/internal/store/store.go', 'MAX(quota_at),SUM(quota_count)', 'MAX(quota_at),0', './server/internal/store', 'TestGatewayQuotaMigrationPreservesActiveUsage'),
    ]
    killed = []
    with tempfile.TemporaryDirectory(prefix='meow-launch-mutants-') as directory:
        root = Path(directory)
        for name in source_files():
            target = root/name; target.parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(ROOT/name, target)
        for name, file, old, new, package, test in cases:
            target = root/file; original = target.read_text()
            if original.count(old) != 1 or old == new:
                raise RuntimeError('missing/ambiguous mutant anchor: '+name)
            target.write_text(original.replace(old, new))
            if target.read_text() == original:
                raise RuntimeError('mutant not applied: '+name)
            args = ['go', 'test', '-json', package, '-run', '^'+test+'$', '-count=1', '-timeout=20s']
            result = subprocess.run(args, cwd=root, capture_output=True, text=True)
            (output/(name+'.log')).write_text(result.stdout+result.stderr)
            target.write_text(original)
            events = [json.loads(line) for line in result.stdout.splitlines() if line.startswith('{')]
            if result.returncode != 1 or not any(e.get('Action') == 'fail' and e.get('Test') == test for e in events):
                raise RuntimeError('mutant survived or failed outside intended test: '+name)
            killed.append(name)
    return killed


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--output', type=Path, required=True)
    parser.add_argument('--ios', action='store_true', help='Run fresh isolated unit + affected UI cases')
    args = parser.parse_args()
    output = args.output.resolve(); output.mkdir(parents=True, exist_ok=True)
    if any(output.iterdir()): parser.error('use a fresh empty output directory')
    state = source_hash()
    report = {'source_hash': state, 'base': BASE, 'layers': {}}
    for bad in [{ 'client/client.go': 'func (c *Client) Respond() {}' },
                {'ios/Meow/App/ReplyQueue.swift': 'func submit() {}'}]:
        try: retired_gate(bad)
        except RuntimeError: continue
        raise RuntimeError('retirement checker accepted negative control')
    with tempfile.TemporaryDirectory() as directory:
        empty = Path(directory)/'empty.out'; empty.write_text('mode: atomic\n')
        try: security_coverage(empty)
        except RuntimeError: pass
        else: raise RuntimeError('security coverage accepted empty profile')
    report['checker_controls'] = 3 + helpers.checker_controls()
    log = run(['go', 'test', '-race', '-shuffle=20261101', '-count=1', '-coverprofile='+str(output/'coverage.out'), '-json', './...'], output/'go-tests.jsonl')
    report['layers']['go-tests'] = helpers.go_results(log)
    version = subprocess.check_output(['go', 'version'], text=True).strip()
    if 'go1.27.3 ' not in version: raise RuntimeError('wrong Go patch toolchain: '+version)
    report['toolchain'] = version
    for binary, package in [('meow', './cli/cmd/meow'), ('meow-server', './server/cmd/meow-server')]:
        run(['go', 'build', '-trimpath', '-ldflags=-X main.version=launch-hardening-dev', '-o', str(output/binary), package], output/(binary+'-build.log'))
        info = subprocess.check_output(['go', 'version', '-m', str(output/binary)], text=True)
        if 'go1.27.3' not in info: raise RuntimeError('binary toolchain mismatch')
    report['layers']['build'] = 'CLI/server rebuilt with Go 1.27.3; no install/release/deploy'
    run(['go', 'vet', './...'], output/'vet.log'); report['layers']['vet'] = 'pass'
    changed = subprocess.check_output(['git', 'diff', '--name-only', BASE], text=True).splitlines()
    new = subprocess.check_output(['git', 'ls-files', '--others', '--exclude-standard'], text=True).splitlines()
    go_files = [name for name in sorted(set(changed+new)) if name.endswith('.go') and (ROOT/name).is_file()]
    if run(['gofmt', '-l', *go_files], output/'gofmt.log').strip(): raise RuntimeError('formatting differs')
    run(['git', 'diff', '--check'], output/'diff-check.log'); report['layers']['format'] = 'pass'
    run(['python3', '-B', 'scripts/test_install.py'], output/'installer.log'); report['layers']['installer'] = 'pass'
    report['layers']['vulnerabilities'] = run(['go', 'run', 'golang.org/x/vuln/cmd/govulncheck@v1.9.0', './...'], output/'vulnerabilities.log').strip()
    files = {name: (ROOT/name).read_text() for name in source_files()
             if name.endswith(('.go', '.swift')) and not name.endswith('_test.go') and '/MeowTests/' not in name and '/scripts/' not in name and not name.startswith('scripts/')}
    report['layers']['retired-execution'] = retired_gate(files)
    report['layers']['coverage'] = security_coverage(output/'coverage.out')
    run(['go', 'tool', 'cover', '-func='+str(output/'coverage.out')], output/'coverage-functions.log')
    report['layers']['mutation'] = mutations(output)
    env = {k:v for k,v in os.environ.items() if not k.startswith('MEOW_')}
    env.update(MEOW_DOMAIN='meow.example.invalid', MEOW_APNS_KEY_PATH='/tmp/disposable-key-not-read.p8', MEOW_APNS_KEY_ID='TEST', MEOW_APNS_TEAM_ID='TEST')
    with (output/'compose.json').open('w') as stream:
        subprocess.run(['docker', 'compose', '--env-file', '/dev/null', '-f', 'deploy/compose.yaml', '-f', 'deploy/compose.gateway.yaml', 'config', '--format', 'json'], cwd=ROOT, env=env, stdout=stream, check=True)
    config = json.loads((output/'compose.json').read_text())
    if str(config['services']['meow']['environment']['MEOW_GATEWAY_MAX_CONCURRENT_PUSHES']) != '0': raise RuntimeError('Compose default is not unlimited')
    report['layers']['compose'] = 'default cap 0; no secrets read'
    diff = subprocess.check_output(['git', 'diff', BASE], text=True)
    for name in new:
        if name != REPORT: diff += (ROOT/name).read_text()
    scanned = subprocess.run(['gitleaks', 'stdin', '--redact', '--no-banner'], input=diff, text=True, capture_output=True)
    (output/'secrets.log').write_text(scanned.stdout+scanned.stderr)
    if scanned.returncode != 0: raise RuntimeError('diff secret scan failed')
    report['layers']['secrets'] = 'no findings'
    if args.ios:
        command = ['python3', '-B', 'ios/scripts/verify_ui_optimization.py', '--output', str(output/'ios'), '--coverage']
        run(command, output/'ios.log')
        report['layers']['ios'] = json.loads((output/'ios/results.json').read_text())
    else:
        report['layers']['ios'] = 'not run; pass --ios for isolated unit/UI verification'
    if source_hash() != state: raise RuntimeError('source changed during verification')
    report['layers']['source'] = 'unchanged'
    if set(report['layers']) != EXPECTED: raise RuntimeError('required layer missing')
    (output/'report.json').write_text(json.dumps(report, indent=2)+'\n')
    print(json.dumps(report, indent=2))


if __name__ == '__main__': main()
