#!/usr/bin/env python3
"""Verify gateway admission using disposable SQLite and simulated APNs only."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import tempfile

ROOT = Path(__file__).resolve().parents[1]
API = 'server/internal/api/gateway.go'
HELPER = 'server/internal/api/gateway_limits.go'
MAIN = 'server/cmd/meow-server/main.go'
BASE = '63199e1a499aab1a406f7c523a3084ebf56b544f'
LAYERS = {'tests', 'build', 'vet', 'format', 'installer', 'coverage',
          'mutations', 'compose', 'secrets', 'source-unchanged'}


def run(args, log, cwd=ROOT):
    with log.open('w') as stream:
        result = subprocess.run(args, cwd=cwd, stdout=stream, stderr=subprocess.STDOUT)
    if result.returncode != 0:
        raise RuntimeError(f'{args} exited {result.returncode}: {log}')
    return log.read_text()


def inputs():
    data = subprocess.check_output(['git', 'ls-files', '--cached', '--others', '--exclude-standard', '-z'], cwd=ROOT)
    return sorted({p for p in data.decode().split('\0') if p})


def source_state():
    digest = hashlib.sha256()
    for name in inputs():
        if name == 'docs/engineering/gateway-resource-limits-evidence.md':
            continue  # report contains this hash; it is not executable input
        path = ROOT / name
        if not path.is_file():
            raise RuntimeError(f'source input unavailable: {name}')
        digest.update(name.encode() + b'\0' + path.read_bytes())
    return digest.hexdigest()


def go_results(log):
    rows = [json.loads(line) for line in log.splitlines() if line.startswith('{')]
    tests = [r for r in rows if r.get('Test') and r.get('Action') in ('pass', 'fail', 'skip')]
    if not tests or any(r['Action'] != 'pass' for r in tests):
        raise RuntimeError('test evidence missing, failed or skipped')
    return {'passed_including_subtests': len(tests),
            'gateway_limits_cases': sum(r['Test'].startswith(('TestGatewayLimits', 'TestGatewayConcurrency', 'TestGatewayInvalidConcurrency')) for r in tests)}


def coverage(profile):
    lines = profile.read_text().splitlines()
    if not lines or lines[0] != 'mode: atomic':
        raise RuntimeError('missing atomic coverage profile')
    targets = {API: set(), HELPER: set(), MAIN: set()}
    # Changed statement blocks in the gateway, plus the new configuration parser.
    for name in (API,):
        diff = subprocess.check_output(['git', 'diff', '--unified=0', BASE, '--', name], cwd=ROOT, text=True)
        for match in re.finditer(r'^@@ .* \+(\d+)(?:,(\d+))? @@', diff, re.MULTILINE):
            first, count = int(match[1]), int(match[2] or 1)
            targets[name].update(range(first, first + count))
    targets[HELPER] = set(range(1, len((ROOT / HELPER).read_text().splitlines()) + 1))
    main = (ROOT / MAIN).read_text().splitlines()
    start = next(i for i, line in enumerate(main, 1) if line.startswith('func gatewayConcurrencyFromEnv('))
    end = next(i for i, line in enumerate(main[start:], start + 1) if line == '}')
    targets[MAIN] = set(range(start, end + 1))
    blocks = {}
    for line in lines[1:]:
        location, statements, count = line.split()
        name, span = location.split(':')
        name = name.removeprefix('meow.blue/meow/')
        if name not in targets:
            continue
        first, last = span.split(',')
        first_line, last_line = int(first.split('.')[0]), int(last.split('.')[0])
        if not targets[name].intersection(range(first_line, last_line + 1)):
            continue
        key = (name, span)
        n, hit = int(statements), int(count)
        if key in blocks and blocks[key][0] != n:
            raise RuntimeError('inconsistent coverage records')
        blocks[key] = (n, hit + blocks.get(key, (0, 0))[1])
    missing = [f'{name}:{span}' for (name, span), (n, hit) in blocks.items() if n > 0 and hit == 0]
    for name in targets:
        if not any(path == name and n > 0 for (path, _), (n, _) in blocks.items()):
            raise RuntimeError(f'coverage absent for {name}')
    if missing:
        raise RuntimeError(f'uncovered gateway admission/config blocks: {missing}')
    return {'covered_statements': sum(n for n, _ in blocks.values()),
            'scope': 'changed gateway statement blocks, IP/admission helper and env parser',
            'excluded': 'main.run daemon wiring requires live APNs startup; Compose wiring checked separately'}


def mutations(output):
    quota = '''\tif err := g.Store.TakeGatewayQuota(r.Context(), route.ID); err != nil {
\t\tgatewayStoreError(w, err)
\t\treturn
\t}
'''
    admission = '''\tif !g.admitPush(w, r) {
\t\treturn
\t}
\tdefer func() { <-g.pushSlots }()
'''
    cases = [
        ('ignore-ip-budget', API, 'if ok, retry := g.ipLimit.allow(gatewayClientIP(r, g.TrustProxy)); !ok {',
         'if ok, retry := g.ipLimit.allow(gatewayClientIP(r, g.TrustProxy)); false && !ok {', 'TestGatewayLimitsInvalidCredentialsShareBudget'),
        ('overspend-push-slots', API, 'make(chan struct{}, c.MaxConcurrentPushes)',
         'make(chan struct{}, c.MaxConcurrentPushes+1)', 'TestGatewayLimitsGlobalPushAdmission'),
        ('leak-push-slot', API, '\tdefer func() { <-g.pushSlots }()', '', 'TestGatewayLimitsReleasePushSlot'),
        ('busy-burns-quota', API, admission + quota, quota + admission, 'TestGatewayLimitsGlobalPushAdmission'),
        ('trust-spoofed-header', HELPER, 'raw := clientIP(r, trustProxy)', 'raw := clientIP(r, true)', 'TestGatewayLimitsTrustedProxyAndAddressAliases'),
        ('rotate-ipv6-interface', HELPER, 'if address.Is6() {', 'if false && address.Is6() {', 'TestGatewayLimitsAddressProperties'),
    ]
    result = []
    with tempfile.TemporaryDirectory(prefix='meow-gateway-limits-mutants-') as directory:
        root = Path(directory)
        for name in inputs():
            target = root / name
            target.parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(ROOT / name, target)
        for name, path, old, new, test in cases:
            target = root / path
            original = target.read_text()
            if original.count(old) != 1 or old == new:
                raise RuntimeError(f'mutant anchor ambiguous or missing: {name}')
            mutated = original.replace(old, new)
            target.write_text(mutated)
            if target.read_text() == original:
                raise RuntimeError(f'mutant not applied: {name}')
            args = ['go', 'test', '-json', './server/internal/api', '-run', '^'+test+'$', '-count=1', '-timeout=15s']
            completed = subprocess.run(args, cwd=root, capture_output=True, text=True)
            (output / (name + '.log')).write_text(completed.stdout + completed.stderr)
            target.write_text(original)
            events = [json.loads(line) for line in completed.stdout.splitlines() if line.startswith('{')]
            if completed.returncode != 1 or not any(e.get('Action') == 'fail' and e.get('Test', '').split('/')[0] == test for e in events):
                raise RuntimeError(f'mutant survived or did not reach its behavioral test: {name}')
            result.append(name)
    return {'killed': len(result), 'executed': result}


def checker_controls():
    for log in ['', '{"Test":"x","Action":"skip"}', '{"Test":"x","Action":"fail"}']:
        try:
            go_results(log)
        except RuntimeError:
            continue
        raise RuntimeError('test checker accepted a negative control')
    with tempfile.TemporaryDirectory() as directory:
        profile = Path(directory) / 'coverage.out'
        profile.write_text('mode: atomic\n')
        try:
            coverage(profile)
        except RuntimeError:
            return 4
        raise RuntimeError('coverage checker accepted empty evidence')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--output', type=Path, required=True)
    args = parser.parse_args()
    output = args.output.resolve()
    output.mkdir(parents=True, exist_ok=True)
    if any(output.iterdir()):
        parser.error('--output must be a fresh empty directory')
    before = source_state()
    report = {'source_hash': before, 'comparison_base': BASE, 'go': subprocess.check_output(['go','version'], text=True).strip(),
              'checker_negative_controls': checker_controls(), 'layers': {}}
    log = run(['go','test','-race','-shuffle=20261101','-count=1','-covermode=atomic',
               '-coverprofile='+str(output/'coverage.out'),'-json','./...'], output/'tests.jsonl')
    report['layers']['tests'] = go_results(log)
    run(['go','build','./...'], output/'build.log'); report['layers']['build'] = 'pass'
    run(['go','vet','./...'], output/'vet.log'); report['layers']['vet'] = 'pass'
    formatted = run(['gofmt','-l',API,HELPER,MAIN,'server/internal/api/gateway_limits_test.go',
                     'server/internal/api/gateway_test.go','server/cmd/meow-server/gateway_limits_test.go'], output/'format.log')
    if formatted.strip(): raise RuntimeError('Go formatting differs')
    run(['git','diff','--check'],output/'diff-check.log'); report['layers']['format'] = 'pass'
    run(['python3','-B','scripts/test_install.py'], output/'installer.log'); report['layers']['installer'] = 'pass'
    report['layers']['coverage'] = coverage(output/'coverage.out')
    report['layers']['mutations'] = mutations(output)
    env = {k:v for k,v in os.environ.items() if not k.startswith('MEOW_')}
    env.update(MEOW_DOMAIN='meow.example.invalid',MEOW_APNS_KEY_PATH='/tmp/non-secret-fixture.p8',
               MEOW_APNS_KEY_ID='TEST',MEOW_APNS_TEAM_ID='TEST',MEOW_GATEWAY_MAX_CONCURRENT_PUSHES='7')
    with (output/'compose.json').open('w') as stream:
        subprocess.run(['docker','compose','-f','deploy/compose.yaml','-f','deploy/compose.gateway.yaml','config','--format','json'],
                       cwd=ROOT,env=env,stdout=stream,stderr=subprocess.PIPE,check=True)
    compose = json.loads((output/'compose.json').read_text())
    if str(compose['services']['meow']['environment']['MEOW_GATEWAY_MAX_CONCURRENT_PUSHES']) != '7':
        raise RuntimeError('Compose does not pass concurrency setting')
    report['layers']['compose'] = 'explicit concurrency override reaches gateway'
    diff = subprocess.check_output(['git','diff',BASE],cwd=ROOT,text=True)
    for name in inputs():
        tracked = subprocess.run(['git','ls-files','--error-unmatch',name],cwd=ROOT,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL).returncode
        if tracked == 1:
            diff += (ROOT/name).read_text()
        elif tracked != 0:
            raise RuntimeError('could not determine tracked source files')
    scan = subprocess.run(['gitleaks','stdin','--redact','--no-banner'],cwd=ROOT,input=diff,capture_output=True,text=True)
    (output/'secrets.log').write_text(scan.stdout+scan.stderr)
    if scan.returncode != 0: raise RuntimeError('diff secret scan failed')
    report['layers']['secrets'] = 'no findings in diff/new files; dependency set unchanged'
    if source_state() != before: raise RuntimeError('source changed during verification')
    report['layers']['source-unchanged'] = 'pass'
    if set(report['layers']) != LAYERS: raise RuntimeError('required verification layer missing')
    (output/'report.json').write_text(json.dumps(report,indent=2)+'\n')
    print(json.dumps(report,indent=2))


if __name__ == '__main__':
    main()
