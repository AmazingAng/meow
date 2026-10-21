#!/usr/bin/env python3
"""Verify Codex notify coexistence without touching installed agent settings."""
import argparse
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile

ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location('agent_checks', ROOT / 'scripts/verify_agent_fixes.py')
base = importlib.util.module_from_spec(spec)
spec.loader.exec_module(base)
base.BASE = '9c73f28'
base.FILES = ['cli/internal/setup/codex.go', 'cli/cmd/meow/setup.go',
              'cli/cmd/meow/hook.go', 'cli/cmd/meow/wizard.go']


def reject_control(path, gate):
    try:
        gate(path)
    except RuntimeError:
        return
    raise AssertionError('coverage checker accepted known-bad input')


def mutants(output):
    mutations = [
        ('preservation', 'cli/cmd/meow/setup.go',
         'if o.install && !o.force && len(original) > 0 {',
         'if o.install && !o.force && len(original) > 999 {'),
        ('restoration', 'cli/cmd/meow/setup.go',
         'if !o.install && ours && len(original) > 0 {\n\t\tdata, err = setup.CodexConfig',
         'if !o.install && ours && len(original) > 999 {\n\t\tdata, err = setup.CodexConfig'),
        ('raw-payload', 'cli/cmd/meow/hook.go',
         'forwardNotify(ctx, forwarded, payload)', 'forwardNotify(ctx, forwarded, "{}")'),
        ('disabled-meow', 'cli/cmd/meow/hook.go',
         'if len(forwarded) > 0 {', 'if len(forwarded) > 0 && os.Getenv("MEOW_HOOK_DISABLED") != "1" {'),
        ('invalid-config', 'cli/cmd/meow/setup.go',
         'original, err := setup.CodexOriginalNotify(old, o.executables()...)\n\tif err != nil {\n\t\treturn nil, nil, fmt.Errorf("%s: %w", tilde(path), err)\n\t}',
         'original, _ := setup.CodexOriginalNotify(old, o.executables()...)'),
    ]
    result = []
    with tempfile.TemporaryDirectory(prefix='meow-notify-mutants-') as directory:
        root = Path(directory)
        for folder in ['cli', 'client', 'message', 'adapters', 'contracts', 'server']:
            shutil.copytree(ROOT / folder, root / folder)
        for name in ['go.mod', 'go.sum']:
            shutil.copy2(ROOT / name, root / name)
        for name, file, old, new in mutations:
            path = root / file
            original = path.read_text()
            if original.count(old) != 1:
                raise RuntimeError('ambiguous mutant: ' + name)
            try:
                path.write_text(original.replace(old, new))
                digest = hashlib.sha256(path.read_bytes()).hexdigest()
                runs = {}
                for label, selector in [('full', 'TestCodex(Coexistence|Forward|Malformed)'),
                                        ('property', 'TestCodexCoexistenceGeneratedArgumentRoundTrips')]:
                    run = subprocess.run(['go', 'test', './cli/cmd/meow', '-run', selector,
                                          '-count=1', '-json', '-timeout=20s'], cwd=root,
                                         capture_output=True, text=True)
                    (output / f'mutant-{name}-{label}.log').write_text(run.stdout + run.stderr)
                    events = [json.loads(line) for line in run.stdout.splitlines() if line.startswith('{')]
                    failures = [e['Test'] for e in events if e.get('Action') == 'fail' and e.get('Test')]
                    if run.returncode not in [0, 1] or (run.returncode and not failures):
                        raise RuntimeError('mutant failed without executed test: ' + name)
                    runs[label] = failures
                if not runs['full']:
                    raise RuntimeError('mutant survived full suite: ' + name)
                result.append({'name': name, 'sha256': digest, 'failing_tests': runs})
            finally:
                path.write_text(original)
            if path.read_text() != original:
                raise RuntimeError('mutant restoration failed')
    return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--output', type=Path, required=True)
    args = parser.parse_args()
    out = args.output.resolve()
    out.mkdir(parents=True, exist_ok=False)
    profile = out / 'coverage.out'
    test_json = base.run(['go', 'test', '-race', '-shuffle=on', '-count=1', '-json',
                          '-coverprofile=' + str(profile), './...'], out, 'tests')
    events = [json.loads(line) for line in test_json.splitlines() if line.startswith('{')]
    tests = [e for e in events if e.get('Action') == 'pass' and e.get('Test')]
    if not tests or any(e.get('Action') in ['fail', 'skip'] for e in events):
        raise RuntimeError('empty, failed or skipped test suite')
    coverage = base.check_coverage(profile)
    missing = out / 'missing.out'; missing.write_text('mode: set\n')
    zero = out / 'zero.out'
    zero.write_text('\n'.join(line.rsplit(' ', 1)[0] + ' 0' if index else line
                              for index, line in enumerate(profile.read_text().splitlines())) + '\n')
    reject_control(missing, base.check_coverage); reject_control(zero, base.check_coverage)
    try:
        reject_control(zero, lambda _: {})
    except AssertionError:
        pass
    else:
        raise RuntimeError('negative control passed with checker disabled')
    base.run(['go', 'build', './...'], out, 'build')
    base.run(['go', 'vet', './...'], out, 'vet')
    formatted = base.run(['gofmt', '-l', *base.FILES, 'cli/cmd/meow/codex_forward_test.go',
                          'cli/internal/setup/coexistence_test.go'], out, 'format')
    if formatted.strip():
        raise RuntimeError('Go source needs formatting')
    mutation_result = mutants(out)
    existing_live = base.real_cli(out)
    binary = out / "tools with spaces'quote/meow"
    helper = out / 'Computer Use helper'
    capture = out / 'forwarded-argv'
    helper.write_text('#!/bin/sh\nprintf "%s\\000" "$@" > "$MEOW_TEST_CAPTURE"\n')
    helper.chmod(0o700)
    original = [str(helper), 'turn-ended', "quotes ' 中文 $(not-a-command)"]
    payload = '{"type":"agent-turn-complete","last-assistant-message":"exact Unicode 猫"}'
    env = dict(os.environ, MEOW_HOOK_DISABLED='1', MEOW_TEST_CAPTURE=str(capture),
               MEOW_CONFIG=str(out / 'isolated-config.json'))
    response = base.run([str(binary), 'hook', 'codex', '--forward-notify=' + json.dumps(original), payload],
                        out, 'real-forward', env=env)
    if response or capture.read_bytes().split(b'\0')[:-1] != [s.encode() for s in original[1:] + [payload]]:
        raise RuntimeError('real built CLI did not preserve forwarded argv')
    files = base.FILES + ['scripts/verify_codex_coexistence.py', 'cli/cmd/meow/codex_forward_test.go',
                          'cli/internal/setup/coexistence_test.go', 'go.mod', 'go.sum']
    result = {'go_version': subprocess.check_output(['go', 'version']).decode().strip(),
              'tests_including_subtests': len(tests), 'race_shuffle': 'passed',
              'coverage': coverage, 'coverage_negative_controls': 2,
              'disabled_checker_detected': True, 'mutants': mutation_result,
              'property_scope': 'argv configuration round-trip only; runtime mutations can survive property-only tests',
              'real_forwarded_argv': 'passed', 'existing_live_cli': existing_live,
              'source_sha256': {p: hashlib.sha256((ROOT / p).read_bytes()).hexdigest() for p in files},
              'spec_approval': 'not obtained (autonomous run)', 'independent_review': 'not performed',
              'limits': ['No real Computer Use process or physical app execution.',
                         'Forwarded direct child is bounded at 30s; independently spawned descendants are not managed.',
                         'staticcheck unavailable; Go compiler, vet and gofmt used.',
                         'No dependency changes; no additional dependency audit.',
                         'Existing localization metadata edits are outside this CLI verification scope.']}
    (out / 'results.json').write_text(json.dumps(result, ensure_ascii=False, indent=2) + '\n')
    print(json.dumps({'tests': len(tests), 'coverage': coverage, 'mutants_killed': len(mutation_result)}))


if __name__ == '__main__':
    main()
