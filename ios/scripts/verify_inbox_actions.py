#!/usr/bin/env python3
"""Verify inbox read/delete snapshots with disposable simulators and HTTP state."""
import argparse
import hashlib
import json
from pathlib import Path
import subprocess
import sys

ROOT = Path(__file__).resolve().parents[2]
FUNCTIONS = {
    'AppModel.inboxSelection(_:)', 'AppModel.unreadInboxSelection.getter', 'AppModel.markInboxRead(_:)',
    'AppModel.deleteNotifications(_:)', 'static AppModel.deleteRemoteMessage(_:api:)',
    'AppModel.markMatchingRead(_:)', 'static AppModel.markRemoteRead(_:api:)',
    'AppModel.setBatchRead(_:at:)', 'AppModel.removeDeletedMessages(_:)',
    'static AppModel.performInboxMutation(_:)',
}


def output(command):
    return subprocess.check_output(command, cwd=ROOT, text=True).strip()


def run(command, log):
    with log.open('w') as stream:
        return subprocess.run(command, cwd=ROOT, stdout=stream, stderr=subprocess.STDOUT).returncode


def summary(bundle):
    return json.loads(output(['xcrun', 'xcresulttool', 'get', 'test-results', 'summary', '--path', str(bundle)]))


def boot_device(device):
    inventory = json.loads(output(['xcrun', 'simctl', 'list', 'devices', '--json']))
    state = next(d['state'] for group in inventory['devices'].values() for d in group if d['udid'] == device)
    if state == 'Booted':
        output(['xcrun', 'simctl', 'shutdown', device])
    elif state != 'Shutdown':
        raise RuntimeError('unexpected mutation simulator state: ' + state)
    output(['xcrun', 'simctl', 'boot', device])
    output(['xcrun', 'simctl', 'bootstatus', device, '-b'])


def coverage_gate(report):
    found = {f['name']: f for t in report['targets'] for file in t.get('files', [])
             if file['name'] == 'AppModel.swift' for f in file.get('functions', []) if f['name'] in FUNCTIONS}
    if set(found) != FUNCTIONS:
        raise RuntimeError('missing inbox action coverage functions: ' + repr(FUNCTIONS - set(found)))
    for name, f in found.items():
        if f['executableLines'] <= 0 or f['coveredLines'] / f['executableLines'] < 0.8:
            raise RuntimeError('inbox action coverage below 80%: ' + name)
    return [{k: f[k] for k in ['name', 'coveredLines', 'executableLines']} for f in found.values()]


def mutations(out):
    source = ROOT / 'ios/Meow/App/AppModel.swift'
    original = source.read_text()
    cases = [
        ('scope', 'guard selection.scope == inboxScope, isRegistered', 'guard isRegistered',
         'InboxDeletionModelTests/testSelectedDeletionRejectsForeignScopeAndUnsupportedEndpoint'),
        ('selection', 'let targets = selection.ids.intersection(available).sorted()', 'let targets = available.sorted()',
         'InboxDeletionModelTests/testSelectedDeletionIncludesAllPagesAndRetainsFailuresAndNewArrivals'),
        ('failed-delete', 'if success { removed.insert(id) } else { failed.insert(id) }',
         'if success { removed.insert(id) } else { removed.insert(id); failed.insert(id) }',
         'InboxDeletionModelTests/testSelectedDeletionIncludesAllPagesAndRetainsFailuresAndNewArrivals'),
        ('batch', 'setBatchRead(Set(targets), at: readAt)', 'setBatchRead(Set(targets.prefix(4)), at: readAt)',
         'InboxGroupingModelTests/testThousandsOfReadTargetsPersistTogetherBeforeNetworkFinishes'),
        ('retry-after', 'try await Task.sleep(for: error.retryAfter ?? .seconds(1))', 'try Task.checkCancellation()',
         'InboxDeletionModelTests/testSelectedReadAndDeleteRespectRetryAfter'),
    ]
    output(['xcodegen', 'generate', '--spec', 'ios/project.yml'])
    device = output(['xcrun', 'simctl', 'create', 'Meow Inbox Action Mutation Temporary',
                     'com.apple.CoreSimulator.SimDeviceType.iPhone-17-Pro', 'com.apple.CoreSimulator.SimRuntime.iOS-26-5'])
    results = []
    try:
        boot_device(device)
        base = ['xcodebuild', 'test', '-project', 'ios/Meow.xcodeproj', '-scheme', 'Meow',
                '-destination', 'platform=iOS Simulator,id=' + device, '-derivedDataPath', str(out / 'DerivedData'),
                '-parallel-testing-enabled', 'NO', 'CODE_SIGN_IDENTITY=-', 'CODE_SIGNING_ALLOWED=YES']
        bundle = out / 'baseline.xcresult'
        if run(base + ['-only-testing:MeowTests', '-resultBundlePath', str(bundle)], out / 'baseline.log'):
            raise RuntimeError('full model baseline failed')
        baseline = summary(bundle)
        if baseline['failedTests'] or baseline['skippedTests'] or baseline['passedTests'] != 248:
            raise RuntimeError('expected all 248 model tests before mutation')
        for name, before, after, test in cases:
            print('Mutation: ' + name, flush=True)
            # Repeated test-host installs can leave SpringBoard's launch preflight
            # busy after xcodebuild exits. Reboot only this disposable device.
            boot_device(device)
            if original.count(before) != 1:
                raise RuntimeError('ambiguous mutation anchor: ' + name)
            bundle = out / (name + '.xcresult')
            try:
                source.write_text(original.replace(before, after))
                code = run(base + ['-only-testing:MeowTests/' + test, '-resultBundlePath', str(bundle)], out / (name + '.log'))
                result = summary(bundle)
                if code == 0 or result['failedTests'] < 1 or test.split('/')[-1] not in json.dumps(result['testFailures']):
                    raise RuntimeError('mutation was not killed by its behavior test: ' + name)
                results.append(dict(name=name, test=test, failed_tests=result['failedTests']))
            finally:
                source.write_text(original)
    finally:
        source.write_text(original)
        subprocess.run(['xcrun', 'simctl', 'shutdown', device], capture_output=True)
        subprocess.run(['xcrun', 'simctl', 'delete', device], check=True, capture_output=True)
    return results


def ui(out, case, *, unit=False, compact=False):
    print('UI: ' + case + (' (compact dark)' if compact else ''), flush=True)
    command = [sys.executable, '-B', 'ios/scripts/verify_ui_optimization.py', '--output', str(out), '--test-case', case]
    if unit:
        command += ['--coverage']
    else:
        command += ['--ui-only']
    if compact:
        command += ['--device-type', 'com.apple.CoreSimulator.SimDeviceType.iPhone-SE-3rd-generation', '--appearance', 'dark']
    if run(command, out.parent / (out.name + '.log')):
        raise RuntimeError('UI verification failed: ' + case)
    return summary(out / 'ui.xcresult')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--output', required=True, type=Path)
    out = parser.parse_args().output.resolve()
    out.mkdir(parents=True, exist_ok=False)
    killed = mutations(out)
    # Each UI scenario owns a fresh device, origin and inbox. Resetting HTTP data
    # while retaining the previous test's durable local read states is not isolation.
    cases = [
        'test30ReadActionsLargeChinese', 'test29MessageSwipeAndSearchGroupDeletionKeepUnmatchedHistory',
        'test28GroupVisitDoesNotReadNewArrivalOnRefreshOrDetailReturn',
        'test26GroupEntryMarksItsMessagesRead', 'test27InboxReadHeaderAndGroupSwipeActions',
        'test08CompactHeaderSearchAndViewMenu', 'test21GroupSearchReadsOnlyMatchingMessagesAndModeSurvivesRestart',
        'test25PlainHeaderKeepsViewNamesAndLeadingAlignment',
    ]
    runs = [ui(out / ('ui-' + str(i)), case, unit=i == 0) for i, case in enumerate(cases)]
    report = json.loads(output(['xcrun', 'xccov', 'view', '--report', '--json', str(out / 'ui-0/ui.xcresult')]))
    covered = coverage_gate(report)
    for t in report['targets']:
        for file in t.get('files', []):
            for f in file.get('functions', []):
                f['coveredLines'] = 0
    try:
        coverage_gate(report)
    except RuntimeError:
        pass
    else:
        raise RuntimeError('coverage gate accepted the zero-coverage negative control')
    dark = ui(out / 'compact-dark', 'test30ReadActionsLargeChinese', compact=True)
    paths = ['ios/Meow/App/AppModel.swift', 'ios/Meow/Models/InboxGrouping.swift', 'ios/Meow/Views/InboxView.swift',
             'ios/Meow/Views/InboxGroupView.swift', 'ios/Meow/Resources/Localizable.xcstrings',
             'ios/MeowTests/InboxDeletionTests.swift', 'ios/MeowTests/InboxGroupingModelTests.swift',
             'ios/MeowUITests/UIOptimizationTests.swift', 'ios/scripts/verify_ui_optimization.py', 'ios/scripts/verify_inbox_actions.py']
    result = dict(unit_tests=runs[0]['passedTests'] - 1, ui_tests=sum(r['passedTests'] for r in runs) - (runs[0]['passedTests'] - 1),
                  failed_tests=sum(r['failedTests'] for r in runs), skipped_tests=sum(r['skippedTests'] for r in runs),
                  compact_dark=dark['passedTests'], mutations=killed, function_coverage=covered,
                  coverage_negative_control='rejected', source_sha256={p: hashlib.sha256((ROOT / p).read_bytes()).hexdigest() for p in paths},
                  xcode=output(['xcodebuild', '-version']), spec_approval='not obtained (autonomous run)',
                  independent_verification='not performed', production_requests=0)
    (out / 'results.json').write_text(json.dumps(result, indent=2) + '\n')
    print(json.dumps(result, indent=2), flush=True)


if __name__ == '__main__':
    main()
