#!/usr/bin/env python3
"""Reproduce agent-hook checks without changing installed configurations.

python3 scripts/verify_agent_hooks.py --output /tmp/meow-hooks-check
Optional --ios-device UUID runs the iOS unit suite on that simulator.
Uses the existing Go, Python, Xcode/xcodegen installations; adds no dependencies.
"""
import argparse
import hashlib
import difflib
import json
import os
from pathlib import Path
import shutil
import socket
import subprocess
import tempfile
import time
import urllib.request

ROOT = Path(__file__).resolve().parents[1]


def run(argv, out, name, *, cwd=ROOT, env=None, stdin=None):
    p = subprocess.run(argv, cwd=cwd, env=env, input=stdin, text=True, capture_output=True)
    (out / (name + '.log')).write_text(p.stdout + p.stderr)
    if p.returncode:
        raise RuntimeError(f'{name} failed: {out / (name + ".log")}')
    return p.stdout


def mutants(out):
    cases = [
        ('truncation', 'adapters/format.go', 'message.MaxBody)', '200)', './cli/cmd/meow', 'TestHookFullBodyAndNewEvents'),
        ('retry', 'cli/cmd/meow/hook.go', 'attempt < 3', 'attempt < 1', './cli/cmd/meow', 'TestHookRetriesFrozenPayload'),
        ('retired-notify', 'cli/cmd/meow/hook.go', 'if kind == "codex" {\n\t\treturn', 'if false {\n\t\treturn', './cli/cmd/meow', 'TestCodexRetiredNotifyNeverSends'),
        ('backup', 'cli/internal/setup/files.go', 'return backup, RemoveFile(path)', 'os.Remove(backup); return backup, RemoveFile(path)', './cli/cmd/meow', 'TestSetupBacksUpCustomizedLegacySkill'),
        ('preview', 'message/preview.go', 'return ""', 'return text', './server/internal/push', 'TestNotificationPreviewContract'),
    ]
    results = []
    with tempfile.TemporaryDirectory(prefix='meow-hooks-mutants-') as tmp:
        root = Path(tmp)
        for name in ('cli', 'client', 'message', 'adapters', 'contracts', 'server'):
            shutil.copytree(ROOT / name, root / name)
        shutil.copytree(ROOT / 'ios/MeowTests/Fixtures', root / 'ios/MeowTests/Fixtures')
        for name in ('go.mod', 'go.sum'):
            shutil.copy2(ROOT / name, root / name)
        for name, filename, before, after, package, selector in cases:
            path = root / filename
            original = path.read_text()
            expected = 4 if name == 'preview' else 1
            if original.count(before) != expected:
                raise RuntimeError('mutation target changed: ' + name)
            path.write_text(original.replace(before, after))
            try:
                runs = {}
                for label, tests in [('contract', selector), ('properties', 'FuzzPreviewBounded|FuzzLifecycleRoundTrip|TestLifecyclePreservesForeignAndEmptyGroups')]:
                    cmd = ['go', 'test', package, '-run', tests, '-json', '-count=1']
                    p = subprocess.run(cmd, cwd=root, capture_output=True, text=True)
                    (out / f'mutant-{name}-{label}.log').write_text(p.stdout + p.stderr)
                    events = [json.loads(s) for s in p.stdout.splitlines() if s.startswith('{')]
                    failed = [e['Test'] for e in events if e.get('Test') and e.get('Action') == 'fail']
                    executed = [e['Test'] for e in events if e.get('Test') and e.get('Action') == 'run']
                    if p.returncode not in (0, 1) or (p.returncode and not failed):
                        raise RuntimeError('mutant did not reach a behavioral failure: ' + name)
                    if label == 'contract' and (not executed or not failed or p.returncode != 1):
                        raise RuntimeError('mutant survived: ' + name)
                    runs[label] = dict(executed=executed, failed=failed)
                results.append(dict(name=name, changed_sha256=hashlib.sha256(path.read_bytes()).hexdigest(), runs=runs))
            finally:
                path.write_text(original)
    return results


def real_cli(out):
    with tempfile.TemporaryDirectory(prefix='meow-hooks-live-') as tmp:
        d = Path(tmp)
        binary = d / "tools with space'quote" / 'meow'
        binary.parent.mkdir()
        origin = d / 'meow-server'
        run(['go', 'build', '-o', str(binary), './cli/cmd/meow'], out, 'cli-build')
        run(['go', 'build', '-o', str(origin), './server/cmd/meow-server'], out, 'server-build')
        with socket.socket() as sock:
            sock.bind(('127.0.0.1', 0)); port = sock.getsockname()[1]
        env = dict(os.environ, HOME=str(d), CODEX_HOME=str(d / 'codex'), CLAUDE_CONFIG_DIR=str(d / 'claude'),
                   GROK_HOME=str(d / 'grok'),
                   MEOW_CONFIG=str(d / 'meow.json'), MEOW_EXECUTABLE=str(binary), MEOW_LANGUAGE='en', MEOW_HOOK_DISABLED='0',
                   MEOW_DB=str(d / 'origin.db'), MEOW_ADDR=f'127.0.0.1:{port}', MEOW_SERVER=f'http://127.0.0.1:{port}',
                   MEOW_APNS_KEY_FILE='', MEOW_PUSH_GATEWAY_URL='')
        log = open(out / 'origin.log', 'w')
        process = subprocess.Popen([str(origin)], env=env, stdout=log, stderr=log)
        def request(path, value=None, token=''):
            data = None if value is None else json.dumps(value).encode()
            req = urllib.request.Request(env['MEOW_SERVER'] + path, data=data, headers={'Content-Type': 'application/json', 'Authorization': 'Bearer ' + token})
            with urllib.request.urlopen(req, timeout=5) as response:
                raw = response.read()
                return json.loads(raw) if path != '/healthz' else raw
        try:
            for _ in range(100):
                if process.poll() is not None:
                    raise RuntimeError('origin stopped before ready')
                try:
                    request('/healthz'); break
                except OSError:
                    time.sleep(.05)
            else:
                raise RuntimeError('origin not ready')
            registration = request('/v1/devices', {'name': 'hook verification'})
            env['MEOW_KEY'] = registration['key']['secret']
            for agent in ('claude', 'codex', 'cursor', 'grokbot'):
                run([str(binary), 'setup', agent, '--yes', '--no-test'], out, 'live-setup-' + agent, env=env)
            def hook(kind, event, name):
                raw = json.dumps(event, ensure_ascii=False)
                args = [str(binary), 'hook', kind]
                if kind == 'codex':
                    args.append(raw); raw = None
                output = run(args, out, name, env=env, stdin=raw)
                if output or (out / (name + '.log')).read_text():
                    raise RuntimeError('hook was not silent')
            body = ('完整正文😺\n' * 70) + 'IMPORTANT TAIL'
            stop = dict(session_id='s', turn_id='t', hook_event_name='Stop', last_assistant_message=body)
            if request('/v1/inbox', token=registration['device_token'])['notifications']:
                raise RuntimeError('no-test setup sent a notification before a hook ran')
            hook('codex', {'type': 'agent-turn-complete', 'last-assistant-message': '{"suggestion":"Next"}'}, 'live-retired-notify')
            hook('codex-stop', stop, 'live-stop')
            hook('codex-stop', stop, 'live-stop-replay')
            hook('codex-stop', dict(session_id='suggestion', turn_id='suggestion-turn', hook_event_name='Stop', transcript_path=None, last_assistant_message='{"suggestion":"Next"}'), 'live-suggestion')
            hook('codex-permission', dict(session_id='s', turn_id='t', hook_event_name='PermissionRequest', tool_name='Bash', tool_input={'command': 'git push'}), 'live-permission')
            hook('claude-failure', dict(session_id='s', prompt_id='p', error='rate_limit', error_details='Try later'), 'live-failure')
            hook('claude-stop', dict(session_id='s', prompt_id='p2', last_assistant_message='{"suggestions":[]}'), 'live-structured')
            hook('cursor-stop', dict(hook_event_name='stop', conversation_id='c', generation_id='g', status='completed', workspace_roots=['/work/meow']), 'live-cursor')
            hook('cursor-stop', dict(hook_event_name='stop', conversation_id='c', generation_id='g2', status='aborted'), 'live-cursor-aborted')
            hook('cursor-stop', dict(hook_event_name='Stop', sessionId='s', lastAssistantMessage='from grok', reason='end_turn'), 'live-cursor-ignores-grok')
            hook('grokbot-stop', dict(hook_event_name='Stop', sessionId='s', promptId='p', cwd='/work/meow', reason='end_turn', lastAssistantMessage='Grok finished'), 'live-grok-stop')
            hook('grokbot-stop', dict(hook_event_name='Stop', sessionId='s', promptId='end', reason='shutdown', lastAssistantMessage='bye'), 'live-grok-shutdown')
            hook('grokbot-failure', dict(hook_event_name='StopFailure', sessionId='s', promptId='err', error='rate_limit', errorDetails='Try later'), 'live-grok-failure')
            hook('grokbot-notify', dict(hook_event_name='Notification', sessionId='s', notificationType='permission_prompt', message='Allow deploy?'), 'live-grok-permission')
            hook('grokbot-notify', dict(hook_event_name='Notification', sessionId='s', notificationType='idle_prompt', message='waiting'), 'live-grok-idle')
            inbox = request('/v1/inbox', token=registration['device_token'])['notifications']
            if len(inbox) != 8:
                raise RuntimeError('wrong persisted count: ' + str(len(inbox)))
            results = [n for n in inbox if n['source'] == 'Codex' and n['title'] == 'New result']
            if len(results) != 1 or results[0]['body'] != body:
                raise RuntimeError('body was lost or replay created duplicates')
            if sum(n['priority'] == 'high' for n in inbox) != 4:
                raise RuntimeError('missing permission/error priority')
            for agent in ('claude', 'codex', 'cursor', 'grokbot'):
                run([str(binary), 'setup', agent, '--uninstall', '--yes'], out, 'live-uninstall-' + agent, env=env)
            return dict(stored=8, replay_deduplicated=True, full_body=True, hooks_only=True)
        finally:
            process.terminate()
            try:
                process.wait(timeout=10)
            except subprocess.TimeoutExpired:
                process.kill(); process.wait()
            log.close()


def coverage_report(profile):
    # Diagnostic only: error-injection/host integration gaps are declared, not
    # hidden behind a lowered pass threshold.
    changed = {}
    for line in profile.read_text().splitlines()[1:]:
        location, _, count = line.split()
        name, span = location.rsplit(':', 1)
        name = name.removeprefix('meow.blue/meow/')
        if name not in changed:
            before = subprocess.run(['git', 'show', '4028203:' + name], cwd=ROOT, text=True, capture_output=True)
            if before.returncode not in (0, 128):
                raise RuntimeError('cannot load coverage baseline')
            after = (ROOT / name).read_text().splitlines()
            touched = set()
            for tag, _, _, lo, hi in difflib.SequenceMatcher(a=before.stdout.splitlines(), b=after).get_opcodes():
                if tag != 'equal':
                    touched.update(range(lo + 1, hi + 1))
            changed[name] = dict(touched=touched, lines=after, executed={})
        entry = changed[name]
        start, end = span.split(',')
        lo = int(start.split('.')[0]); hi, col = map(int, end.split('.'))
        if col == 1:
            hi -= 1
        for n in entry['touched'].intersection(range(lo, hi + 1)):
            text = entry['lines'][n - 1].strip()
            if text and not text.startswith('//') and text not in ('}', ')', '})'):
                entry['executed'][n] = entry['executed'].get(n, False) or int(count) > 0
    return {name: {'covered': sum(e['executed'].values()), 'total': len(e['executed']),
                   'missed_lines': [n for n, hit in sorted(e['executed'].items()) if not hit]}
            for name, e in changed.items() if e['executed']}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--output', type=Path, required=True)
    parser.add_argument('--ios-device')
    args = parser.parse_args()
    out = args.output.resolve(); out.mkdir(parents=True, exist_ok=False)
    go_files = sorted(str(p.relative_to(ROOT)) for folder in ('cli', 'client', 'message', 'adapters', 'contracts', 'server') for p in (ROOT / folder).rglob('*.go'))
    if run(['gofmt', '-l', *go_files], out, 'format').strip():
        raise RuntimeError('unformatted Go files')
    run(['go', 'vet', './...'], out, 'vet')
    tests = run(['go', 'test', '-race', '-shuffle=on', '-count=1', '-json', '-coverprofile=' + str(out / 'coverage.out'), './...'], out, 'tests')
    events = [json.loads(s) for s in tests.splitlines() if s.startswith('{')]
    passed = [e for e in events if e.get('Test') and e.get('Action') == 'pass']
    if not passed or any(e.get('Action') in ('fail', 'skip') for e in events):
        raise RuntimeError('empty/failed/skipped suite')
    run(['go', 'test', './cli/internal/setup', '-run', '^$', '-fuzz', 'FuzzLifecycleRoundTrip', '-fuzztime=3s', '-parallel=2'], out, 'fuzz-lifecycle')
    run(['go', 'test', './server/internal/push', '-run', '^$', '-fuzz', 'FuzzPreviewBounded', '-fuzztime=3s', '-parallel=2'], out, 'fuzz-preview')
    mutation = mutants(out)
    live = real_cli(out)
    if args.ios_device:
        run(['xcodegen', 'generate'], out, 'xcodegen', cwd=ROOT / 'ios')
        run(['xcodebuild', '-project', 'ios/Meow.xcodeproj', '-scheme', 'Meow', '-destination', 'platform=iOS Simulator,id=' + args.ios_device, '-only-testing:MeowTests', '-parallel-testing-enabled', 'NO', 'CODE_SIGN_IDENTITY=-', 'CODE_SIGNING_ALLOWED=YES', 'test'], out, 'ios')
    files = go_files + ['go.mod', 'go.sum', 'ios/Shared/NotificationContent.swift', 'ios/Meow/Views/InboxView.swift', 'ios/MeowTests/NotificationPreviewTests.swift', 'ios/MeowTests/Fixtures/notification-preview.json', 'scripts/verify_agent_hooks.py']
    report = dict(tests_including_subtests=len(passed), mutants=mutation, real_cli=live,
                  go_version=subprocess.check_output(['go', 'version'], text=True).strip(),
                  python_version=subprocess.check_output(['python3', '--version'], text=True).strip(),
                  source_sha256={f: hashlib.sha256((ROOT / f).read_bytes()).hexdigest() for f in files},
                  coverage=coverage_report(out / 'coverage.out'),
                  coverage_gate='Not enforced: filesystem failures and host trust not fully covered. Diagnostic counts are not an exhaustive coverage pass.',
                  independent_verification='not performed', ios=bool(args.ios_device))
    (out / 'results.json').write_text(json.dumps(report, ensure_ascii=False, indent=2) + '\n')
    print(json.dumps(dict(tests=len(passed), mutants_killed=len(mutation), real_cli=live, ios=bool(args.ios_device))))

if __name__ == '__main__':
    main()
