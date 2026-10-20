# Local-first inbox — evidence, 2026-10-20

[Specification](local-inbox-spec.md). Tier 3. Spec approval: not obtained (autonomous run under the user's request to plan and execute). Independent agent verification: not performed. Work was isolated on codex/local-inbox and will be merged directly into main; no PR. No packages, dependencies, server schema or HTTP contract were added.

## Delivered behavior

The main app persists complete synchronized history to its own SQLite database, separate from the notification extension. Search, All / Unread / Pending and display pagination operate on saved messages without HTTP or a debounce delay. History can be searched after restarting offline. Cache scopes hash normalized server URL plus the device credential; the credential itself is not stored in SQLite. Reset/re-registration erases the old scope. Storage is serialized off the main actor, transactional, excluded from backup and protected with iOS data protection.

Foreground/manual sync loads disk before requesting all retained inbox pages. It does not pass the display query to the server. A failed page/repeated cursor preserves the previous complete cache. Complete snapshots reconcile removals, retaining local read/reply/deletion mutations made during the request. Late old-session responses cannot repopulate memory/disk. Search matches can render before unread counts return. HTTP 429 waits use the server's delay-seconds Retry-After; retries are bounded per page and waits are cancellable. The empty view shows synchronization progress while a request is outstanding. Storage errors are displayed separately from network errors.

This is full paginated network synchronization with incremental changed-row SQLite writes; it does not claim a server change-log/incremental network API. Background notifications enter the cache when the main app fetches them. The extension never opens SQLite. Search covers synchronized local history, not notifications never fetched by the app. Server retention remains authoritative after successful reconciliation. Cached question state never bypasses existing server identity, expiry, idempotency or approval authentication checks.

## Final fresh verification

Reproduce all layers with:

```sh
python3 -B ios/scripts/verify_local_inbox.py --output dist/local-inbox/reproduce
```

Use a fresh directory. Tool versions, source hashes, exact test/mutation names and layer manifest are in [machine-readable results](local-inbox-results.json). iOS distribution checks use the existing verify_distribution.py. This run did not boot a simulator, replace the installed phone app, or perform physical UI/APNs execution.

| Layer | Observed result |
| --- | --- |
| Native Swift suite with a real Go HTTP process | **123 tests passed, 0 failed/skipped**. Same 123 passed again after restoring all mutants. Includes the existing regression suite, 30 local-store/model tests, and two isolated real-server integrations. Platform storage/keychain/notification boundaries are adapted on macOS; SQLite, AppModel, API, reply and notification logic are actual sources. |
| Real execution | Registered on a disposable loopback Go server, sent 52 notifications, synchronized all history, searched the oldest notification beyond the first 50 visible rows, and verified SQLite persistence and persisted read state. No production credentials or services were used. |
| New concurrency/robustness cases | Real temporary SQLite files; 32 concurrently written isolated scopes; mid-transaction SQLite trigger failure rolls back inserts/deletions; corrupted/empty payloads and unwritable paths surface errors. Held HTTP responses test accepted answers, authoritative deletions, read counts and resets against stale snapshots. |
| New input properties | Generated Unicode/emoji records round-trip across scopes; Chinese/case/punctuation/SQL-looking searches match literally; complete snapshots remove stale rows and distinguish empty from never synced. |
| Changed-line coverage gate | **285/285 measured executable changed lines** across AppModel (143), InboxStore (118), MeowAPI (24), measured on the final restored run. The manifest retains every changed line. Pure brace delimiters are excluded uniformly: LLVM emits a zero counter for the unreachable closing brace of an async while-true helper. This is line coverage, not complete branch coverage or UI coverage. |
| Coverage checker controls | Known missing profiles and zero execution counters rejected; a disabled checker is detected by its negative control. These controls do not prove every possible checker defect is rejected. |
| Manual mutation | **5/5 killed by executed failing tests**: collide credential scopes, bypass local search, discard concurrent local mutations, allow stale-session sync, commit instead of rollback. Fresh compilation and mutant source hashes recorded; mutations ran only inside the isolated verification package. This validates the regression suite collectively, not each individual test. |
| Suite order | **30/30 new local tests passed** in a seeded shuffled order, one process per case. This does not claim arbitrary in-process order or Swift Thread Sanitizer coverage. |
| Go regression | **185 tests including subtests passed** with race detection and shuffle, zero failures/skips. Build and vet passed. Go sources were unchanged by this feature. |
| iOS type/build checks | Main app, notification extension, unit and UI test targets compiled with the actual iOS SDK. Release archive is verified separately for TestFlight build 10. UI test execution and screenshots are not claimed. |
| Formatting/localization/capabilities | git diff --check and localization JSON validation passed. No Swift formatter installed. No third-party dependency changed. Changed/untracked source scan found no private-key/bearer credential literals. Capability review: new private local database only; no new network destination, environment access or production subprocess. Existing server/CLI credentials were not modified. |
| Source stability | Runtime/harness source hashes unchanged throughout the final gauntlet and through Release archive. Exact covered scope is recorded in results; this is not an assertion about ignored environment files. |

## Failure history and adversarial review

Nine initial regression cases were observed failing against the stub/current network-first model. Additional held-response tests reproduced a deleted message being resurrected and a slow count overriding an optimistic read; both were fixed before the final run. Two storage-error visibility cases failed before storage errors were separated from network errors. Two rate-limit cases failed before bounded Retry-After handling.

The first full suite retained one obsolete assertion expecting the literal server cursor `older`. It was updated to the explicit local-pagination contract in the specification; a new 201-message test asserts local pagination remains available and performs no HTTP. Authorization and reply assertions were not relaxed. An invalid-directory test initially targeted the system temporary directory's resource attributes; its fixture was corrected to an owned `inbox.sqlite` directory without changing assertions. A closing-brace coverage artifact was handled as described above; no executable logic was removed from the gate.

Adversarial pass checked parameterized SQLite writes, literal search text, transaction rollback, failed snapshot preservation, late reset callbacks, scope hashing without plaintext secrets, and stale answer/read/deletion merges. No new unresolved behavioral issue was found within the specified scope. This was an author review, not independent assurance. Performance on a physical device, very large production histories and real APNs delivery remain unmeasured; first/full synchronization can take multiple requests. The protocol does not provide an atomic multi-page server snapshot. Existing queued-reply durability is separate from the recoverable message cache.

## Distribution

TestFlight target is 0.1.0 (10). Archive, IPA, upload status and logs are under dist/testflight/0.1.0-10/. Upload success, Apple processing completion and physical installation are separate states; no phone validation is inferred from packaging.


Archive verification passed for both shipping bundles at 0.1.0 (10). Initial command-line exports failed with `exportArchive No Accounts` and `No signing certificate "iOS Distribution" found`. The archive log also reported invalid keychain credentials with a missing `Xcode-Username` field. Xcode Settings still displayed the account and team; restarting Xcode and retrying from a separate Terminal did not resolve the failure. The CLI account list was empty. The user then distributed the existing archive through Xcode Organizer, which also reported `No Accounts`; reauthenticating there allowed the upload to finish. This establishes that reauthentication resolved the signing failure, but does not establish why the credentials became invalid.

The local Organizer distribution log independently confirms `Upload succeeded for archive Meow` at **2026-10-20 14:45:13 +08:00**. Content-delivery metadata contains version **0.1.0**, build **10**. The CLI account list returned to one account after reauthentication. No duplicate upload was attempted. The upload confirmation and retry history are preserved in dist/testflight/0.1.0-10/gui-upload-confirmation.log and status.json. A separately exported IPA was not verified in this workflow. Apple processing completion and tester availability have not been independently confirmed because the App Store Connect web session requires sign-in. No physical validation of build 10 is claimed.
