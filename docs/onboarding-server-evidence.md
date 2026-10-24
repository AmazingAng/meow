# Onboarding verification evidence

Scope: [server selection contract](onboarding-server-spec.md), onboarding usability and the PR #5 review follow-up. The stronger mutation/coverage checks remain limited to the saved-server boundary; UI refinements use ordinary integration tests. The follow-up was explicitly requested by the user. No independent verifier or new runtime dependency.

Source: base 980c4629159205860162026aac2dde86a5dc9f39; SHA-256 over sorted changed/new paths relative to that base (each path, NUL, contents, NUL): `ce932d846655eafd923a6ad4838e2be2eab9241dbbe43a165e1d5304cefa40ee`. This evidence file and the server-selection spec are excluded.

## Reproduction

Use fresh output directories:

```sh
python3 ios/scripts/verify_onboarding_server.py --full --output /tmp/meow-onboarding-verification
```

This now runs all `UIOptimizationTests` and all iOS unit tests, rather than selecting only the guided setup case. The real-server onboarding tests use a separate disposable simulator named `Meow Fix …`:

```sh
python3 ios/scripts/verify_review_fixes.py --simulator-id <disposable-simulator-id> --output /tmp/meow-onboarding-real --onboarding-only
```

That command verifies invalid/unreachable server errors, registration, deferred notification permission, computer-vs-phone test detection, relaunch recovery and bash/zsh script sending. It excludes retired reply UI scenarios.

## Results (2026-10-24)

Tools: Apple Swift 6.3.3, Go 1.27.2, Python 3.14.3, iOS 26.5 simulator; script example also verified with the host's curl 7.71.1.

- 225 iOS unit tests and all 17 current notification `UIOptimizationTests`: 242 passed, zero failures/skips. Includes key masking, missing-key retry, notification-permission guidance, formatting, read-only historical questions, settings, grouping and session identity.
- After replacing the curl example with the older-compatible `-fsS` option, all 225 unit tests and the affected script-help UI test passed again (226). Other app behavior was unchanged.
- Updated `OnboardingRegressionUITests` against a real disposable Meow server: 2 passed, zero failures/skips. These are additional tests, for 244 distinct iOS tests in total; reruns are not counted twice.
- The final bash and zsh examples both delivered `Hello from curl` as the title with an empty body, matching the plain-text API contract. This final script-only check used the same `verify_script_example` helper and a fresh local server; already-passing UI tests were not repeated after correcting the helper's assertion.
- `go test -race ./...`, `go vet ./...`, the focused CLI suite and all 9 installer tests passed. Masked keys fail locally with actionable copy instructions before any HTTP request or credential save.
- Native Shared.swift checks: 128 saved-server round trips passed. The changed getter executed; a zero-execution negative control was rejected. Four behavioral mutants were killed: default fallback, discarding saved URL, ignoring save and ignoring clear. Source restored.
- Screenshots inspected for masked-key setup and settings script help. Formatting and whitespace checks passed.

Local evidence: `/tmp/meow-onboarding-review/full`, `/tmp/meow-onboarding-review/script-help-final`, `/tmp/meow-onboarding-review/real-server-compatible-curl/onboarding.xcresult`, `/tmp/meow-onboarding-review/script-example.json`.

## Behavior and coverage

- The official URL is a UI suggestion only. Saved self-hosted addresses win, and orphaned credentials cannot silently bind to the official server.
- Hidden Device Key text cannot be selected; the explicit copy button still copies the full key. Revealed text remains selectable.
- Key-bearing onboarding login/curl examples were removed intentionally. Unused builders are deleted. `meow login` and the HTTP API remain available; Settings → Script integration provides examples using `$MEOW_KEY` rather than the saved secret.
- The server help explains that notification content passes through and is stored on the chosen server.
- A new PR/main workflow runs Go race tests, vet and installer tests. iOS results above were obtained locally; they are not GitHub-hosted iOS CI results.

Validation issues corrected: the old UI test assumed an empty server field and an exposed key; it also treated a clipped Show Device Key button as tappable. The updated test replaces the prefilled address, scrolls above the fixed footer and explicitly reveals the key. An initial curl option was unsupported by curl 7.71.1 and was replaced. The script assertion was corrected to the existing API rule that the first plain-text line is the title, not the body.

Limits: no physical-device/APNs end-to-end run, no email/AirDrop delivery test, no pairing API or short-URL deployment, no App Store privacy submission, and no independent verification. The full historical reply/gateway/focus UI suites were not run. Their relevant model behavior remains covered by the complete unit target. Mutation and coverage claims concern only the saved-server boundary, not whole-app coverage.
