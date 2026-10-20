# Codex notification coexistence

Tier 3: existing user configuration and concurrent notification subprocesses.
Spec approval: not obtained (autonomous run under the installation bug report).
Isolation: branch `codex/notify-coexistence`; existing localization edits remain
untouched. Direct merge to main, no PR, per the user's standing instruction.
No new dependencies or tools. Independent agent verification: not performed.

## Contract and failure model

1. A Codex config containing Computer Use's executable and `turn-ended` must
   install without `--force`, after the normal preview/confirmation. Both its
   original argv and the Meow skill must survive. Existing backups still apply.
2. The installed notify command runs Meow and the original handler concurrently.
   Each receives the original JSON payload unchanged as one argument, with no
   shell evaluation. Spaces, quotes, Unicode and shell-looking text remain data.
3. Reinstalling, changing the Meow executable or toggling summary must preserve
   exactly one copy of the original handler. Uninstall restores the original
   argv and preserves unrelated TOML content. A later foreign replacement is
   left alone. Explicit `--force` retains its documented replacement behavior.
4. `MEOW_HOOK_DISABLED=1` disables only Meow. Missing credentials, invalid input,
   unsupported Meow events and failed Meow HTTP delivery do not suppress the
   original handler. Hook stdout/stderr remain empty and exit code remains zero.
5. A missing, failed or hanging original handler does not prevent Meow delivery.
   Meow keeps its five-second HTTP deadline. The original handler has a separate
   thirty-second deadline and a bounded process wait; a slow original command
   can be terminated at that deadline. This limit is part of the contract, not
   a claim to preserve arbitrarily long-running custom notification programs.
6. Malformed forwarded-command JSON, missing executable names and duplicate
   forwarding options fail safely. Unknown notifications are forwarded without
   requiring Meow to recognize them. Logs do not contain notification payloads.
7. Setup prints all detected agents before requesting individual changes, and
   identifies undetected supported agents with their explicit setup command.
8. CLI behavior, existing tests and Go dependencies remain compatible. One old
   plan-level test asserting that foreign notify must conflict is superseded
   by coexistence; the low-level editor's conflict safeguards remain unchanged.

Risk coverage: config loss -> install/reinstall/uninstall tests and property
round-trips; argument corruption/injection -> real executable fixture and exact
argv assertions; notifications suppressing each other -> failure and ordering
tests; hanging children -> timeout test; overbroad uninstall -> ownership tests;
onboarding ambiguity -> wizard output tests.

## Files and verification

Implementation: cli/internal/setup/codex.go, cli/cmd/meow/setup.go,
cli/cmd/meow/hook.go, cli/cmd/meow/wizard.go, README.md.
Tests: cli/cmd/meow/codex_forward_test.go, existing setup/wizard regressions,
cli/internal/setup/coexistence_test.go. Reproducible gauntlet:
scripts/verify_codex_coexistence.py. Evidence:
docs/verification/codex-notify-coexistence-results.json.

Run reproducing RED tests, then full race/shuffled Go suite, build, vet, gofmt,
changed executable-line coverage with failing controls, 3-5 isolated manual
mutants, generated argv round-trips and a real built CLI helper invocation.
No live Computer Use process, colleague credentials or production API is used.
Release CLI v0.1.1 and update the Homebrew formula after verification; no iOS
binary or server change is required.
