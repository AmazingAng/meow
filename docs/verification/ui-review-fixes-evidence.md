# UI review fixes — 2026-10-20

All three reported issues were valid and were fixed in the app and installation documentation.

## Changes

1. **Urgency across pagination:** the home refresh independently reads the complete pending-question set through the existing `filter=pending` endpoint before publishing the snapshot. It follows every pending cursor, deduplicates overlapping records, and preserves the ordinary inbox cursor. The pending filter also loads its complete matching set. A failed later page fails the refresh instead of presenting a partial urgency ranking as complete. Existing load-generation checks prevent an old response from repopulating a switched session.
2. **Pending membership:** the pending view renders only open questions. Answering/rejecting a question or reaching its deadline removes the row immediately and shows the empty state when appropriate. With the complete unsearched set loaded, the summary/filter count is derived locally so expiry also removes its stale badge.
3. **CLI installation:** authenticated GitHub API returned zero releases for `AmazingAng/meow`. The help sheet now gives source-build instructions, and README uses the same installation path. A fresh clone of the actual GitHub default branch was built successfully and its executable returned `meow dev` for `--version`. The commands were verified in a temporary directory, without overwriting the user's installed CLI.

Ordinary notifications remain paginated. No server protocol change or CLI release publication is required by this solution. Loading a very large pending set takes multiple requests; the app does not impose an arbitrary cutoff that could hide the earliest deadline.

## Regression coverage

- 51-message model/HTTP fixture: the older urgent question outside the first 50 records appears first; loading the normal next page does not duplicate it.
- More than 200 pending questions: all pages are collected, the search term is preserved, overlap is deduplicated, and the urgent question on the last page wins.
- A failed subsequent pending page is reported as a failure.
- Simulator: urgent item visible without scrolling; reject then return to an empty pending list; remain on screen until expiry and verify row/count removal.
- Simulator: installation help contains the build command and no obsolete download link.
- Existing recovery, queue, session, notification and UI flows remain in the verification run.

The native harness passed **91/91** tests at `/tmp/meow-ui-review-fixes-native-final-20261020/results.json`. iOS results, source hashes and selected screenshots are recorded alongside this report. The initial new pagination fixture accidentally encoded numeric dates; it was corrected to the API's RFC 3339 format. That fixture error is not an application failure and its failed run is not counted as a pass.

Final verification: **91 iOS unit tests + 7 UI flows passed**, with no failures or skips. After the last local-count display adjustment, the answer/expiry UI flow was rerun and passed, including the assertion that the expired question leaves the filter badge. [Results and source hashes](ui-review-fixes-results.json). Screenshots: [urgent item beyond page one](ui-review-fixes-screenshots/urgent-beyond-first-page.png), [pending after expiry](ui-review-fixes-screenshots/pending-after-expiry.png), [CLI help](ui-review-fixes-screenshots/setup-cli-help.png). `git diff --check` and localization JSON validation passed.

## Reproduce

```sh
python3 -B ios/scripts/verify_frontend_recovery.py --output /tmp/meow-ui-review-native-fresh
python3 -B ios/scripts/verify_ui_optimization.py --output dist/ui-review-fixes/reproduce
```

Use fresh output directories. Simulator tests use a disposable HTTP fixture; real APNs delivery and physical-device UI were not exercised in this follow-up. The native harness adapts platform storage/notification APIs; the iOS run executes the actual app. English and Simplified Chinese help strings are included. Existing unrelated changes were preserved.
