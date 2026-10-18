# Server switching with pending replies

2026-10-18. User authorized replacing the hard restriction with an explicit choice. Spec approval not obtained (autonomous run). Work is isolated in a temporary copy; no Git initialization, dependencies, or backend changes.

Acceptance:
1. Keeping replies leaves server, credentials, and durable replies unchanged.
2. Explicit discard durably clears local replies before switching identity. A storage failure preserves the old identity and replies.
3. Cancel in-flight sends without waiting indefinitely; their server and credentials are captured before sending.
4. Late results and old flush batches cannot recreate discarded replies or affect the next session.
5. Explain that discarding local replies cannot undo answers already processed by the old server.
6. Verify both choices in the simulator with real backend requests and screenshots; keeping replies still retries successfully after restart.

This supersedes the previous review's UI policy of blocking every switch with pending replies. Its storage and session isolation guarantees remain required.

Failure model and checks: lost replies on failed disk writes (durable-file failure fixture); requests sent with the next session's identity (capture sender before task creation, stop the previous flush batch); canceled requests completing late (held network continuations for success, temporary error, and permanent refusal); cancellation not reaching the sender (explicit cancellation expectation); UI hiding the safe choice (assert both visible choices on the actual simulator). Existing expiry, idempotency, registration locking, and stale inbox tests remain unchanged.

Implementation note from simulator verification: iOS 26's popover omits a cancel-role action. Keep waiting must be an explicit visible action, and the keyboard is dismissed before presenting the choice. Both paths have screenshot attachments. No system notification or backend behavior is changed.
