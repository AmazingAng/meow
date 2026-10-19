# Frontend recovery contract

Autonomous Tier 3 (credentials, durable replies, late results). Spec approval: not obtained (autonomous run); the user authorized fixing the three reviewed issues. Existing work is preserved on branch codex/frontend-recovery. No commits, pushes, new packages, public API changes, APNs or phone configuration changes. No simulator boot or replacement of the TestFlight app.

## Acceptance

1. An offline reply later refused with 401/404/409/410 moves from pending to a durable failed record, preserving answer, question ID and idempotency key. App reports the reason. Relaunch loads the failure and never retries it. Legacy JSON arrays still decode. An accepted later explicit submission clears an older failure for that question.
2. Persistence failure cannot erase the durable original answer or allow an unsafe server switch. Concurrent late results cannot erase an authoritative success. A server reset clears old failures together with the old identity; stale flush results cannot surface on the next server.
3. Settings offers re-registration at the current URL. Confirmation explains new Device Key and unavailable old registration/history. Pending replies require explicit discard; cancellation preserves the identity and reply. Saving the same URL ordinarily does not reset anything. Backend reset remains the common implementation.
4. Mark-read optimistically updates notification and count; a failed request rolls back only its own mutation, restores the count from the current local/server state, and shows a lightweight reason. Repeated taps do not duplicate pending requests. A concurrent successful mark-all/answer/read or newer refresh cannot be undone by a late failure. A failed old-session request never modifies the new session.
5. Existing identity, expiry, idempotency, queue durability and notification callback tests remain green. No extra authentication requirements or restrictions on changing server.

## Execution

Add ios/MeowTests/FrontendRecoveryTests.swift and ios/scripts/verify_frontend_recovery.py, with native macOS test boundary adapters under ios/scripts/frontend_recovery/ for UIKit, notification-center and credential storage only. Actual AppModel/ReplyQueue/API business logic executes; iOS-only UI and framework integration are compiled against the real iOS SDK. Run existing portable tests and relevant original model/session tests too. Add behavioral RED, coverage, mutation controls and fresh final results under dist/frontend-recovery/; durable reports under docs/verification/frontend-recovery-*. No framework installed. Native adapters are not phone runtime evidence. Record unmeasured layers and independent verification not performed.

Clarification: failure-record cleanup errors during re-registration must explain storage failure rather than falsely claim active replies are pending. Native test storage is explicitly confined to the run output directory, with an isolated credentials/defaults boundary.
