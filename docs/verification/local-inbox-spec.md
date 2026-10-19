# Local-first inbox specification — 2026-10-20

Tier 3: persistent private messages, session isolation, and concurrent synchronization. Spec approval: not obtained (autonomous run); the user explicitly requested planning and execution, and the spec is recorded before implementation. No independent agent verification is claimed.

## Setup and scope

Use branch codex/local-inbox in the existing checkout so the configured signing environment and ignored verification tools remain available. Merge directly into main and push after verification; do not create a PR. Install no packages. Use the OS SQLite3 library, Foundation and existing CryptoKit; no third-party dependency, server schema or HTTP contract change. Build and upload TestFlight 0.1.0 (10) after the checks pass. Do not boot a simulator or replace the installed phone app.

Add ios/Meow/App/InboxStore.swift, ios/MeowTests/LocalInboxTests.swift, ios/scripts/verify_local_inbox.py and evidence/results under docs/verification/. Update AppModel, InboxView, existing regression tests/harnesses as necessary, and localization/version configuration. All permanent verification commands will be reproducible through verify_local_inbox.py; scratch output belongs under ignored dist/local-inbox/ and dist/testflight/0.1.0-10/.

## Intended behavior

1. Successfully synchronized notifications are stored in the main app's local SQLite database. Restarting and losing the network does not remove saved messages. Titles, bodies, sources, question status/choices/answer/expiry and read state round-trip.
2. Typing Chinese, English or punctuation immediately filters all messages already synchronized locally, including records beyond the first display page. Searching and changing filters issue no HTTP requests. Search results do not wait for unread-count requests. Unread and pending filters work offline; pending membership honors expiry and resolved state.
3. On launch/foreground/manual refresh, load disk first and synchronize all server inbox pages in the background through the existing API. Search terms never restrict synchronization. Existing saved matches remain usable while syncing. An empty view while sync is outstanding displays progress rather than a definitive no-results claim.
4. A successful complete server snapshot reconciles removed/retained messages and writes only changed records where possible. A failed later page must not delete cached messages or mark a partial snapshot as complete. Detect repeated cursors instead of looping forever. Full pagination is the network synchronization contract for this implementation; there is no change-log API or claim of incremental network synchronization.
5. Scope records by a hash of the normalized server URL and device credential; never store the credential itself in the message database. Reset/re-registration isolates and clears the old scope. Late old-session responses cannot update new memory or disk state.
6. Local read/reply mutations made during a sync must survive older snapshots; read failure rollback, mark-all-read and accepted/rejected/expired question results are persisted. Server responses continue to decide whether a reply is accepted; local cached pending state cannot bypass identity checks, expiry, Face ID or idempotency.
7. Local database errors are surfaced. Writes are serialized, transactional and off the main thread. The notification extension does not open the message database; notifications are cached when the main app fetches/synchronizes them. Cache scope/clear and concurrent save/load behavior are tested with real temporary SQLite files.
8. Retain the compact navigation, accessible filter menu, on-demand search and urgency ordering. Display pagination is local and must not trigger additional network requests. Update the former server-cursor UI model regression to this explicit new contract; do not weaken authorization/reply tests.

## Failure model and executable checks

- Lost cache, partial writes or stale deletion: temporary-database persistence/restart tests, failure injection and complete/partial sync scenarios.
- Cross-server/credential disclosure: scope collision/rotation tests, reset plus held old-response tests, no plaintext credential serialization.
- Stale response overwriting a local answer/read: held HTTP responses plus real model operations and restart checks.
- Network searches/false emptiness: observe HTTP calls during local search/filter, delayed-count and delayed-sync tests, iOS SDK compile for the actual empty-state view.
- Hostile query/serialization/order: deterministic generated Unicode/punctuation queries, round-trips, idempotent writes and sorting properties.
- Checker mistakes: executable coverage/mutation gates with known-bad negative controls. Report exact measured scope; do not present UI compilation as UI execution.

## Verification

Observe failing assertions before implementing each new behavior. Run the complete native Swift regression harness, new SQLite/model tests, full Go race/shuffle tests and vet/build, iOS build-for-testing and Release archive. Collect changed executable-line coverage with a failing gate and run at least 3 meaningful manual mutants in an isolated verification copy. Use an isolated real HTTP/Go process for synchronization execution. Record source hashes, tool versions, secret/capability review, failures, skips and no fresh physical/simulator execution. Existing unrelated tests remain intact; any assertion change must map to the local-pagination contract above.

## Refinement: rate limits during complete history synchronization

Full pagination may hit the existing per-device API limiter, especially on fast LAN connections with large histories. Preserve the current page cursor and honor the API's delay-seconds Retry-After header on HTTP 429, with three retries per page and cancellable waits. Other errors retain the complete previous cache. This changes only internal Swift error metadata and retry handling, not the server HTTP contract. Add a measured delay/resume test and a bounded-exhaustion test. The user was informed before implementation; spec approval remains not obtained (autonomous execution).
