# Agent integration fixes

Scope: the four issues accepted for repair in the preceding review. Autonomous execution; spec approval: not obtained (autonomous run). No commits or changes to the user's installed agent configuration.

Isolation: a temporary copy of current Go sources, including the user's existing server changes; sync only the CLI changes, dependency manifests, tests and verification artifacts after checking their original hashes. The user's server, deployment and iOS edits remain outside this change.

Failure model and acceptance criteria:

1. Editing TOML can invalidate or misplace configuration. Parse valid quoted keys, comments, arrays, multiline strings, inline tables and table headers with a real TOML parser. Reject invalid input before writing. Keep unrelated bytes and values. Installing twice is stable; uninstall removes only Meow's assignment. Test quoted-key conflicts, comment delimiters, multiline values, CRLF, invalid documents and generated install/update/remove round trips.
2. Weak ownership checks can erase another notification program. Recognize the executable and exact hook argument structure; foreign commands containing `hook codex` must conflict on install and survive uninstall. Preserve compatibility with the existing standard Meow command, comments and multiline arrays.
3. A Stop-hook continuation can finish without a notification. Send the final response even when `stop_hook_active` is true; identical events deduplicate, different completion bodies within one prompt must not collide at the server's Idempotency-Key. The hook remains silent and returns zero.
4. A generated skill can fail to find the installed executable. Use the same shell-quoted executable path as installed hooks, including whitespace and apostrophes; exercise generated commands with a PATH that excludes the binary. Keep agent source labels and refusal/expiry rules.

Setup: add github.com/pelletier/go-toml/v2 v2.4.3 (MIT) for complete TOML validation and source positions, replacing the handwritten parser. No runtime Python dependency. Go standard testing/quick supplies generated invariants. Validation uses Go tests/race/shuffle, build, vet, gofmt, changed-line coverage, four manual mutations, actual CLI execution against a local fake HTTP boundary and a dependency vulnerability/license check. Scripts and a JSON result report are saved under scripts/ and docs/verification/.

Limits: this does not establish that sibling Stop hooks allow stopping, does not configure real agent accounts, and does not repeat unchanged iOS/APNs checks. Independent agent verification is not performed. New or changed implementation lines must be covered; any unavailable layer is stated explicitly in the evidence.

Verification refinement: retain support for a custom executable name by supplying the exact currently selected executable to ownership checks. The four-fix scope is unchanged. Validation now includes a fifth, quoted-key mutant run against the TOML property seeds alone, plus a five-second native Go fuzz run. Coverage counters from multiple instrumented test binaries are summed before the changed-line gate is applied.
