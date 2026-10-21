#!/usr/bin/env python3
"""Verify session metadata end to end without a simulator, phone or real account.

Uses existing Go/Swift/Xcode tools and standard-library Python only. All credentials,
agent directories, databases and mutable fixtures are temporary and isolated.
"""
import argparse
import importlib.util
import json
import os
from pathlib import Path
import re
import shutil
import socket
import subprocess
import tempfile
import time
import urllib.request

ROOT = Path(__file__).resolve().parents[1]


def run(args, out, name, *, cwd=ROOT, env=None, check=True):
    with (out / (name + '.log')).open('w') as log:
        p = subprocess.run(args, cwd=cwd, env=env, stdout=log, stderr=subprocess.STDOUT)
    if check and p.returncode:
        raise RuntimeError(f'{name} failed; see {out / (name + ".log")}')
    return p.returncode


def native_package(out):
    spec = importlib.util.spec_from_file_location('recovery', ROOT / 'ios/scripts/verify_frontend_recovery.py')
    recovery = importlib.util.module_from_spec(spec); spec.loader.exec_module(recovery)
    recovery.package(out)
    for name in ['SessionContextTests.swift', 'InboxDeletionTests.swift', 'NotificationBodyTests.swift']:
        shutil.copy2(ROOT / 'ios/MeowTests' / name, out / 'package/Tests' / name)
    shutil.copy2(ROOT / 'ios/Meow/Models/NotificationBody.swift', out / 'package/Sources/NotificationBody.swift')


def mutations(out):
    results = []
    with tempfile.TemporaryDirectory(prefix='meow-context-mutants-') as tmp:
        root = Path(tmp)
        for name in ['cli', 'client', 'message', 'adapters', 'contracts', 'server']:
            shutil.copytree(ROOT / name, root / name)
        for name in ['go.mod', 'go.sum']:
            shutil.copy2(ROOT / name, root / name)
        go_cases = [
            ('latest-session-name', 'cli/cmd/meow/session_context.go',
             'for i := len(lines) - 1; i >= 0; i-- {', 'for i := 0; i < len(lines); i++ {',
             './cli/cmd/meow', 'TestHookSessionContext'),
            ('persist-context', 'server/internal/api/agent.go', 'Context: in.Context', 'Context: nil',
             './server/internal/api', 'TestNotificationContextHTTP'),
        ]
        for name, filename, old, new, package, selector in go_cases:
            file = root / filename; original = file.read_text()
            if original.count(old) != 1: raise RuntimeError('ambiguous mutant: ' + name)
            file.write_text(original.replace(old, new))
            try:
                code = run(['go', 'test', package, '-run', '^' + selector + '$', '-count=1', '-json'],
                           out, 'mutant-' + name, cwd=root, check=False)
                events = [json.loads(line) for line in (out / ('mutant-' + name + '.log')).read_text().splitlines() if line.startswith('{')]
                failed = [e['Test'] for e in events if e.get('Action') == 'fail' and e.get('Test')]
                if code != 1 or not any(t.startswith(selector) for t in failed):
                    raise RuntimeError('mutant survived or did not execute: ' + name)
                results.append({'name': name, 'failed_tests': failed})
            finally: file.write_text(original)
    for name, filename, old, new, selector in [
        ('stable-session-identity', 'Sources/NotificationOrigin.swift',
         'Self.nonempty(sessionId) ?? Self.nonempty(session) ?? ""',
         'Self.nonempty(session) ?? Self.nonempty(sessionId) ?? ""', 'SessionContextTests.testIdentityColors'),
        ('local-context-search', 'Sources/AppModel.swift',
         '|| notification.context?.searchText.localizedCaseInsensitiveContains(query) == true',
         '|| false', 'LocalInboxModelTests.testLocalSearchMatchesSessionWorkspace'),
    ]:
        file = out / 'package' / filename; original = file.read_text()
        if original.count(old) != 1: raise RuntimeError('ambiguous mutant: ' + name)
        file.write_text(original.replace(old, new))
        try:
            code = run(['swift', 'test', '--package-path', str(out / 'package'), '--filter', selector],
                       out, 'mutant-' + name, check=False)
            log = (out / ('mutant-' + name + '.log')).read_text()
            if code != 1 or not re.search(r"Test Case '.+' failed", log) or 'Executed 0 tests' in log:
                raise RuntimeError('mutant survived or did not execute: ' + name)
            results.append({'name': name, 'behavioral_failure': True})
        finally: file.write_text(original)
    return results


def live_cli(out):
    run(['go', 'build', '-o', str(out / 'meow'), './cli/cmd/meow'], out, 'cli-build')
    run(['go', 'build', '-o', str(out / 'meow-server'), './server/cmd/meow-server'], out, 'server-build')
    with tempfile.TemporaryDirectory(prefix='meow-context-live-') as tmp:
        d = Path(tmp)
        with socket.socket() as sock:
            sock.bind(('127.0.0.1', 0)); port = sock.getsockname()[1]
        server = f'http://127.0.0.1:{port}'
        env = dict(os.environ, HOME=str(d), CODEX_HOME=str(d / 'codex'), CLAUDE_CONFIG_DIR=str(d / 'claude'),
                   MEOW_ADDR=f'127.0.0.1:{port}', MEOW_DB=str(d / 'server.db'), MEOW_SERVER=server,
                   MEOW_CONFIG=str(d / 'meow.json'), MEOW_APNS_KEY_FILE='', MEOW_PUSH_GATEWAY_URL='',
                   MEOW_HOOK_DISABLED='0', MEOW_LANGUAGE='en')
        def request(path, body=None, token=''):
            req = urllib.request.Request(server + path, data=None if body is None else json.dumps(body).encode(),
                headers={'Authorization': 'Bearer ' + token, 'Content-Type': 'application/json'})
            with urllib.request.urlopen(req, timeout=3) as res:
                raw = res.read(); return raw if path == '/healthz' else json.loads(raw)
        with (out / 'live-server.log').open('w') as log:
            process = subprocess.Popen([str(out / 'meow-server')], env=env, stdout=log, stderr=log)
            try:
                for _ in range(100):
                    if process.poll() is not None: raise RuntimeError('server exited')
                    try: request('/healthz'); break
                    except OSError: time.sleep(.05)
                else: raise RuntimeError('server did not start')
                reg = request('/v1/devices', {'name': 'isolated context test'})
                env['MEOW_KEY'] = reg['key']['secret']
                home = Path(env['CODEX_HOME']); home.mkdir()
                (home / 'session_index.jsonl').write_text('{"id":"thread-1","thread_name":"部署审查 😺"}\n')
                payload = json.dumps({'type':'agent-turn-complete','thread-id':'thread-1','turn-id':'turn-1',
                                      'cwd':'/private/customer/project','last-assistant-message':'Completed'})
                run([str(out / 'meow'), 'hook', 'codex', payload], out, 'live-hook', env=env)
                run([str(out / 'meow'), 'send', '--source', 'Another agent', '--workspace', 'project',
                     '--workspace-id', 'w1', '--session', 'Session B', '--session-id', 'thread-2', 'Done'],
                     out, 'live-send', env=env)
                page = request('/v1/inbox', token=reg['device_token'])['notifications']
                by_session = {n['context']['session_id']: n for n in page}
                assert len(page) == 2 and by_session['thread-1']['context']['session'] == '部署审查 😺'
                assert by_session['thread-2']['context']['session'] == 'Session B'
                assert '/private/' not in json.dumps([n['context'] for n in page])
                detail = request('/v1/notifications/' + by_session['thread-1']['id'], token=reg['device_token'])
                assert detail['context'] == by_session['thread-1']['context']
                # Re-delivering the same hook stays idempotent on the real server.
                run([str(out / 'meow'), 'hook', 'codex', payload], out, 'live-replay', env=env)
                assert len(request('/v1/inbox', token=reg['device_token'])['notifications']) == 2
                return {'notifications': 2, 'hook_session': '部署审查 😺', 'manual_context': True, 'idempotent_replay': True}
            finally:
                process.terminate()
                try: process.wait(timeout=5)
                except subprocess.TimeoutExpired: process.kill(); process.wait()


def coverage_gate(go_records, swift_records):
    expected = {'cli/cmd/meow/session_context.go', 'message/normalize.go'}
    if set(go_records) != expected or not swift_records:
        raise RuntimeError('missing core coverage records')
    resolver = (ROOT / 'cli/cmd/meow/session_context.go').read_text().splitlines()
    exceptions = set()
    for anchor in ['info, err = f.Stat()', 'b, err := io.ReadAll(io.NewSectionReader(f, start, metadataTailLimit))']:
        index = next(i for i, line in enumerate(resolver) if line.strip() == anchor)
        if resolver[index + 2].strip() != 'return ""': raise RuntimeError('stale OS-failure exclusion')
        exceptions.add(index + 3)  # 1-based return in this exceptional branch.
    totals = {}
    for name, records in go_records.items():
        if not records: raise RuntimeError('missing executable Go blocks')
        excluded = exceptions if name.endswith('session_context.go') else set()
        eligible = [(line, count) for line, count in records if line not in excluded]
        if not eligible or any(count == 0 for _, count in eligible):
            raise RuntimeError('uncovered Go core blocks: ' + name)
        totals[name] = {'covered_blocks': len(eligible), 'eligible_blocks': len(eligible),
                        'OS_failure_returns_not_measured': len(excluded)}
    if any(count == 0 for count in swift_records.values()):
        raise RuntimeError('uncovered Swift origin lines')
    totals['NotificationOrigin.swift'] = {'covered_lines': len(swift_records), 'executable_lines':len(swift_records)}
    return totals


def coverage(out):
    go_records = {'cli/cmd/meow/session_context.go': [], 'message/normalize.go': []}
    for line in (out / 'go.cover').read_text().splitlines():
        match = re.match(r'meow.blue/meow/(.+):(\d+)\.\d+,\d+\.\d+ \d+ (\d+)$', line)
        if match and match[1] in go_records:
            go_records[match[1]].append((int(match[2]), int(match[3])))
    binary = out / 'package/.build/debug/MeowFailuresPackageTests.xctest/Contents/MacOS/MeowFailuresPackageTests'
    run(['xcrun', 'llvm-cov', 'export', '-format=lcov', '-instr-profile=' +
         str(out / 'package/.build/debug/codecov/default.profdata'), str(binary)], out, 'coverage')
    records = [record for record in (out / 'coverage.log').read_text().split('end_of_record')
               if re.search(r'^SF:.*?/NotificationOrigin.swift$', record, re.M)]
    if len(records) != 1: raise RuntimeError('missing/duplicate Swift coverage')
    source = out / 'package/Sources/NotificationOrigin.swift'
    if source.read_bytes() != (ROOT / 'ios/Shared/NotificationOrigin.swift').read_bytes():
        raise RuntimeError('stale measured Swift source')
    swift_records = {int(a):int(b) for a,b in re.findall(r'^DA:(\d+),(\d+)', records[0], re.M)}
    result = coverage_gate(go_records, swift_records)
    controls = [({}, swift_records), (go_records, {line:0 for line in swift_records}),
                ({name:[(line,0) for line,_ in blocks] for name,blocks in go_records.items()}, swift_records)]
    for bad_go, bad_swift in controls:
        try: coverage_gate(bad_go, bad_swift)
        except RuntimeError: pass
        else: raise RuntimeError('coverage gate accepted a known bad negative control')
    return {'core': result, 'negative_controls_rejected':len(controls),
            'limits':['OS descriptor fstat/read failure returns are not fault-injected',
                      'SwiftUI bodies are checked with the iOS SDK; physical layout is not verified']}


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--output', type=Path, required=True)
    args = p.parse_args(); out = args.output.resolve(); out.mkdir(parents=True, exist_ok=False)
    native_package(out)
    run(['swift', 'test', '--package-path', str(out / 'package'), '--enable-code-coverage'], out, 'native')
    native_log = (out / 'native.log').read_text()
    tests = re.findall(r"Test Case '.+' passed", native_log)
    if not tests or "Test Suite 'All tests' passed" not in native_log: raise RuntimeError('empty native suite')
    run(['go', 'test', '-race', '-shuffle=on', '-count=1', '-coverprofile=' + str(out / 'go.cover'), '-json', './...'], out, 'go')
    run(['go', 'vet', './...'], out, 'go-vet')
    run(['xcodegen', 'generate', '--spec', 'ios/project.yml'], out, 'xcodegen')
    run(['xcodebuild', 'build-for-testing', '-project', 'ios/Meow.xcodeproj', '-scheme', 'Meow',
         '-destination', 'generic/platform=iOS', '-derivedDataPath', str(out / 'ios-build'),
         'CODE_SIGNING_ALLOWED=NO'], out, 'ios-compile')
    results = {'native_tests':len(tests), 'ios_sdk_compile':True,
               'coverage':coverage(out), 'live':live_cli(out), 'mutations':mutations(out)}
    (out / 'results.json').write_text(json.dumps(results, ensure_ascii=False, indent=2) + '\n')
    print(json.dumps(results, ensure_ascii=False))


if __name__ == '__main__': main()
