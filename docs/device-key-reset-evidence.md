# Device Key reset verification

Spec: [device-key-reset-spec.md](device-key-reset-spec.md). Spec approval: not obtained (autonomous run). Independent verification: not performed. The checks below were authored and executed in the same task; they are evidence within the stated contract, not independent assurance.

## Reproduce

From the repository root, with the installed Xcode, XcodeGen and iOS simulator runtime:

```sh
python3 -B ios/scripts/verify_device_key_reset.py --output /tmp/meow-key-reset-check
git diff --check
```

Use a new output directory. The verifier records tool versions and SHA-256 hashes of the seven application/test/verifier inputs in `results.json`. It creates and deletes disposable simulators. HTTP traffic uses disposable local fixtures or `.invalid` hosts intercepted by URLProtocol; no production key is rotated and no physical-device push is sent. No dependencies were added.

## Contract mapping

| Behavior | Executed check |
| --- | --- |
| Separate red Settings row, native confirmation, Cancel sends no requests | `testSettingsDeviceKeyResetConfirmationAndRecovery`; exported screenshots |
| Chinese translation and maximum Dynamic Type, both confirmation buttons usable | `testSettingsDeviceKeyResetChineseConfirmation`; large Chinese Settings navigation; compact dark simulator |
| New key is saved before old key is revoked; registration and local history stay intact | `testSuccessReplacesOnlyTheSenderKeyAndPreservesHistory` |
| Creation failure preserves current key and exposes an error | `testCreationFailureKeepsCurrentKey`; failure/recovery UI flow |
| Missing, empty or whitespace secret cannot revoke current key | `testMissingOrEmptySecretNeverRevokesCurrentKey` |
| Keychain failure preserves old key; only the unused replacement is cleaned up, including cleanup failure | `testKeychainFailureKeepsOldKeyAndOnlyCleansUpReplacement` |
| Failed old-key revocation survives model restart and retries without creating another key | `testFailedRevocationRetriesAcrossModelRestartWithoutCreatingAnotherKey` |
| Duplicate taps create one replacement | `testConcurrentResetCreatesOnlyOneKey` |
| Already-absent old key completes successfully | `testAlreadyAbsentOldKeyCompletesReset` |
| Pending work from another server/device token is ignored | `testForeignPendingRevocationIsNeverUsed` |
| Missing old-key ID or absent registration cannot create an unsafe replacement | `testMissingOldKeyIDDoesNotCreateAnUnrevokableReplacement`; `testUnregisteredDeviceCannotRotate` |
| Delayed creation and revocation failure cannot overwrite a new registration | `testLateCreationAndRevocationFailureCannotAffectNewRegistration` |
| New credential actually authenticates requests after reset and application restart | Recovery UI test against fixture that rejects revoked credentials |
| Existing Settings navigation/server cancellation remains usable | `test05SettingsAndServerCancellation`; `testSettingsLargeChineseNavigation` |

## Results

- Final fresh run: **244 passed, 0 failed, 0 skipped** (240 unit tests, including 11 reset tests; four focused Settings UI tests). A further **one UI test passed** on iPhone SE 3 in dark appearance at maximum Chinese accessibility text size.
- Four deliberately broken implementations were each rejected by their named behavioral test: removing duplicate protection, ignoring Keychain failure, forgetting pending revocation, and accepting stale registration callbacks. The mutation baseline passed all 11 reset tests first; source was restored before final unit/UI execution.
- Coverage gate: 80% minimum per matching reset function/closure; actual **60/60 executable lines**, including **57/57** in the two named functions. The known-zero-coverage negative control was rejected.
- Xcode **26.6 (17F113)**; iOS simulator **26.5 (23F77)**. `git diff --check`, Python AST parsing and localization JSON parsing passed. No production source changed after this run.
- Inspected normal English row and confirmation screenshots and maximum-size Chinese dark-mode screenshots. The reset row is red and independent; native large-text alerts keep both actions visible and provide a scrolling body rather than a custom alert layout.
- Compact local artifacts are retained under `dist/device-key-reset/`: source hashes/results, logs, test summaries and screenshots. Full temporary DerivedData and result bundles are removed after extraction to avoid keeping another large build tree.

## Failures encountered

- Before implementation, the RED run recorded 231 passing tests and four failing test cases: the missing Settings row and model failures for duplicate calls, missing secrets and revocation retry. Additional adversarial cases were then added.
- An initial UI check tried to observe the key through the native clipboard. Simulator/host clipboard synchronization returned unrelated clipboard data; a retry timed out in the native paste menu. The reset-specific check now verifies actual authenticated HTTP requests before/after reset and after restart. Exact Keychain contents remain asserted separately in model tests. The existing dedicated clipboard test was not weakened or changed; it is outside this focused verifier.
- The first complete gauntlet passed the model/UI cases but its coverage checker incorrectly expected exactly two Swift function records. Xcode also reports implicit closures. The checker now requires both named reset functions and checks every matching closure against the same coverage threshold. A zero-coverage negative control must be rejected.

## Scope and limits

- Verification ran in an isolated worktree based on `9f43a61647b8648c9760890f339ccecd9aee4914`, without the primary checkout's ignored build outputs or unrelated untracked `web/`. Landed application/test/verifier sources must match the recorded hashes. No new release, TestFlight upload or deployment is part of this change.
- Coverage enforcement applies to `rotateKey`, its implicit closures and `keyRotationIsCurrent`; it is not a claim of full Settings/app line or branch coverage. Swift/Xcode compilation provides type checks. No new Swift compiler warnings were introduced; existing unused-result warnings in unrelated tests and platform AppIntents metadata warnings remain.
- There is no parser or new serialization contract requiring a new property-testing library. Adversarial examples cover bad secrets, storage/cleanup failures, duplicate calls, retries and stale scopes. The four manual mutants exercise behavioral failures, not compilation errors. No independent reviewer, randomized-order full suite or physical-device credential reset was performed.
- Keychain and UserDefaults are separate stores. Abrupt process termination between their writes is not an atomic transaction and is not proved safe by these tests. A lost creation response can also leave an unused server key; failed cleanup is best effort. Pending old-key revocation is persisted once the new secret has been saved. Only the currently displayed sender key is rotated; this does not revoke unrelated API-created keys.
- The API contract, backend, gateway and CLI are unchanged. A focused existing Go API test was also run against disposable state: `go test -race ./server/internal/api -run TestKeyScopesAndRevocation -count=1` passed. This is supplementary endpoint evidence, not a fresh full Go suite.
