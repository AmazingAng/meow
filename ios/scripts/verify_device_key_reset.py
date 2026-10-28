#!/usr/bin/env python3
"""Verify the Settings key-reset contract using disposable devices and credentials."""
import argparse
import hashlib
import json
from pathlib import Path
import subprocess
import sys

ROOT = Path(__file__).resolve().parents[2]


def output(command):
    return subprocess.check_output(command, cwd=ROOT, text=True).strip()


def summary(bundle):
    return json.loads(output(['xcrun', 'xcresulttool', 'get', 'test-results', 'summary', '--path', str(bundle)]))


def run(command, log):
    with log.open('w') as stream:
        return subprocess.run(command, cwd=ROOT, stdout=stream, stderr=subprocess.STDOUT).returncode


def coverage_gate(report, threshold=0.8):
    functions = [f for t in report['targets'] for file in t.get('files', [])
                 if file['name'] == 'AppModel.swift' for f in file.get('functions', [])
                 if 'rotateKey()' in f['name'] or 'keyRotationIsCurrent' in f['name']]
    primary = {'AppModel.rotateKey()', 'AppModel.keyRotationIsCurrent(_:api:)'}
    if not primary.issubset({f['name'] for f in functions}):
        raise RuntimeError('expected coverage for both reset functions')
    for f in functions:
        if f['executableLines'] <= 0 or f['coveredLines'] / f['executableLines'] < threshold:
            raise RuntimeError('key reset coverage below 80%: ' + f['name'])
    return [{k: f[k] for k in ['name', 'coveredLines', 'executableLines']} for f in functions]


def mutants(out):
    source = ROOT / 'ios/Meow/App/AppModel.swift'
    original = source.read_text()
    cases = [
        ('duplicate', '!isRotatingKey, !isRegistering', '!isRegistering', 'testConcurrentResetCreatesOnlyOneKey'),
        ('storage', 'guard saveDeviceKey(secret) else', 'guard true else', 'testKeychainFailureKeepsOldKeyAndOnlyCleansUpReplacement'),
        ('retry', 'pending?["scope"] as? String == scope ? pending?["id"] as? String : nil', 'nil as String?',
         'testFailedRevocationRetriesAcrossModelRestartWithoutCreatingAnotherKey'),
        ('scope', 'generation == sessionGeneration && server == api.server && MeowShared.deviceToken == api.token', 'true',
         'testLateCreationAndRevocationFailureCannotAffectNewRegistration'),
    ]
    output(['xcodegen', 'generate', '--spec', 'ios/project.yml'])
    device = output(['xcrun', 'simctl', 'create', 'Meow Key Reset Mutation Temporary',
                     'com.apple.CoreSimulator.SimDeviceType.iPhone-17-Pro', 'com.apple.CoreSimulator.SimRuntime.iOS-26-5'])
    results = []
    try:
        base = ['xcodebuild', 'test', '-project', 'ios/Meow.xcodeproj', '-scheme', 'Meow',
                '-destination', 'platform=iOS Simulator,id=' + device, '-derivedDataPath', str(out / 'DerivedData'),
                '-parallel-testing-enabled', 'NO', 'CODE_SIGN_IDENTITY=-', 'CODE_SIGNING_ALLOWED=YES']
        baseline_bundle = out / 'baseline.xcresult'
        code = run(base + ['-only-testing:MeowTests/DeviceKeyResetTests', '-resultBundlePath', str(baseline_bundle)], out / 'baseline.log')
        baseline = summary(baseline_bundle)
        if code or baseline['failedTests'] or baseline['passedTests'] != 11:
            raise RuntimeError('mutation baseline did not pass all 11 key reset tests')
        for name, before, after, test in cases:
            if original.count(before) != 1:
                raise RuntimeError('mutation anchor is ambiguous: ' + name)
            bundle = out / (name + '.xcresult')
            try:
                source.write_text(original.replace(before, after))
                code = run(base + ['-only-testing:MeowTests/DeviceKeyResetTests/' + test,
                                   '-resultBundlePath', str(bundle)], out / (name + '.log'))
                result = summary(bundle)
                if code == 0 or result['failedTests'] < 1 or test not in json.dumps(result['testFailures']):
                    raise RuntimeError('mutant not killed by its behavioral test: ' + name)
                results.append(dict(name=name, test=test, failed_tests=result['failedTests']))
            finally:
                source.write_text(original)
    finally:
        source.write_text(original)
        subprocess.run(['xcrun', 'simctl', 'shutdown', device], check=False, capture_output=True)
        subprocess.run(['xcrun', 'simctl', 'delete', device], check=True, capture_output=True)
    return results


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--output', required=True, type=Path)
    args = parser.parse_args()
    out = args.output.resolve()
    out.mkdir(parents=True, exist_ok=False)
    killed = mutants(out)
    command = [sys.executable, '-B', 'ios/scripts/verify_ui_optimization.py', '--output', str(out / 'final-ui'), '--coverage']
    for test in ['testSettingsDeviceKeyResetConfirmationAndRecovery', 'testSettingsDeviceKeyResetChineseConfirmation',
                 'test05SettingsAndServerCancellation', 'testSettingsLargeChineseNavigation']:
        command += ['--test-case', test]
    if run(command, out / 'final-ui.log'):
        raise RuntimeError('final unit/UI verification failed')
    final = summary(out / 'final-ui/ui.xcresult')
    report = json.loads(output(['xcrun', 'xccov', 'view', '--report', '--json', str(out / 'final-ui/ui.xcresult')]))
    (out / 'coverage.json').write_text(json.dumps(report, indent=2))
    covered = coverage_gate(report)
    bad = json.loads(json.dumps(report))
    for target in bad['targets']:
        for file in target.get('files', []):
            for function in file.get('functions', []):
                function['coveredLines'] = 0
    try:
        coverage_gate(bad)
    except RuntimeError:
        pass
    else:
        raise RuntimeError('coverage gate accepted its zero-coverage negative control')
    dark = [sys.executable, '-B', 'ios/scripts/verify_ui_optimization.py', '--output', str(out / 'compact-dark'),
            '--device-type', 'com.apple.CoreSimulator.SimDeviceType.iPhone-SE-3rd-generation', '--appearance', 'dark', '--ui-only',
            '--test-case', 'testSettingsDeviceKeyResetChineseConfirmation']
    if run(dark, out / 'compact-dark.log'):
        raise RuntimeError('compact dark Chinese verification failed')
    paths = ['ios/Meow/App/AppModel.swift', 'ios/Meow/Views/SettingsView.swift', 'ios/Meow/Resources/Localizable.xcstrings',
             'ios/MeowTests/DeviceKeyResetTests.swift', 'ios/MeowUITests/UIOptimizationTests.swift',
             'ios/scripts/verify_ui_optimization.py', 'ios/scripts/verify_device_key_reset.py']
    result = dict(final_tests=final['passedTests'], failed_tests=final['failedTests'], mutations=killed,
                  reset_function_coverage=covered, coverage_gate_negative_control='rejected',
                  compact_dark=summary(out / 'compact-dark/ui.xcresult')['passedTests'],
                  source_sha256={p: hashlib.sha256((ROOT / p).read_bytes()).hexdigest() for p in paths},
                  xcode=output(['xcodebuild', '-version']), independent_verification='not performed',
                  spec_approval='not obtained (autonomous run)', real_key_rotations=0)
    (out / 'results.json').write_text(json.dumps(result, indent=2) + '\n')
    print(json.dumps(result, indent=2))


if __name__ == '__main__':
    main()
