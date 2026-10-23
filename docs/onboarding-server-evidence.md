# Onboarding verification evidence

Scope: [server selection contract](onboarding-server-spec.md). Tier 3 treatment is limited to the server/credential isolation boundary; UI refinements use normal integration checks.

Spec approval: not obtained (autonomous run). Independent verification: not performed. No new dependencies.

Source: base 635a5f8; SHA-256 over sorted modified tracked paths plus ios/scripts/verify_onboarding_server.py (each path, NUL, contents, NUL): `46bb120d1b2f636e93103316f908bf597127ca8e3f8de17f0674a28368576c15`. Documentation evidence/spec files are excluded from this source fingerprint.

Reproduce from the repository root with a fresh output directory:

```sh
python3 ios/scripts/verify_onboarding_server.py --full --output /tmp/meow-onboarding-verification
```

Tools: Apple Swift 6.3.3, Go 1.27.2, Python 3.14.3, iOS 26.5 simulator.

Final run (2026-10-23):
- 212 iOS unit tests plus 1 guided onboarding UI test: 213 passed, 0 failed, 0 skipped.
- CLI suite: `go test -count=1 ./cli/cmd/meow` passed; `go vet ./cli/cmd/meow`, gofmt and git diff whitespace checks passed.
- Native Shared.swift checks: 128 generated self-hosted URL save/read/remove cases passed. They exercise the actual getter/setter; only bundle configuration, isolated preferences and unused external type declarations are adapted.
- Changed server getter: executed, coverage gate passed; zero-execution negative control rejected. This is a one-line getter coverage claim, not whole-app/UI coverage.
- Four disposable native mutants were compiled, run and killed by behavioral failures: default fallback, discarding saved URL, ignoring save, ignoring clear. Restored source passed and repository source stayed unchanged.

Acceptance mapping:
- Official URL prefill before registration: UI test first launches without a fixture-server override and asserts the prefilled field without registering against production.
- Existing self-hosted URL wins: SessionRegressionTests.testSavedServerOverridesOfficialDefault plus generated native cases.
- Orphaned credential cannot use the official default: unchanged GatewaySessionTests.testMissingServerReturnsToOnboarding.
- Key absent → creation fails → retry succeeds: guided UI fixture, with Next disabled until a key exists; key reveal/hide and copy/share entry points checked. Rotation keeps its existing explicit replacement confirmation.
- Notification permission deferred: guided UI test verifies warning and enable action on the test page, including when a computer notification has arrived.
- Commands exclude Device Key and quote server addresses: existing OnboardingCommandTests retained. Key text is masked by default and revealable for manual transfer.

Failures found: initially adding the default to the old shared getter broke the missing-server/credential test. The native check independently reproduced `CONTRACT FAILED: a suggestion must not become a saved server`. The fix confines defaults to onboarding field prefill. Initial sandbox attempts could not bind local HTTP ports; final local test runs used approved execution access.

Limits and skipped layers: no physical-device/APNs end-to-end run, no email/AirDrop delivery test, no pairing API, no short installer URL deployment, no independent verifier. Full UI suite and legacy native verification harnesses were not run; the focused flow and complete iOS unit target were run. No dedicated Swift formatter configured; compilation and whitespace checks used. No supply-chain audit needed because dependencies did not change. Deterministic generated cases cover 128 valid host/path combinations, not a general URL fuzzer. Mutation/coverage claims concern the saved-server boundary only. Existing OS notification permission APIs and key rotation protocol are reused, not redesigned.
