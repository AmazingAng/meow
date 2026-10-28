# Device Key reset in Settings

Spec approval: not obtained (autonomous run). The user chose a separate red row and a confirmation alert. Work in `/tmp/meow-device-key-reset-20261028`, then integrate only this change; preserve the unrelated `web/` directory. No new dependencies, server API changes, real credentials, pushes or releases.

## Contract and failure model

- Settings has a separate destructive `Reset Device Key…` row after the setup command. Tapping it only opens a native confirmation; Cancel makes no key requests. English and Simplified Chinese explain that senders must sign in again and history stays saved.
- Confirmation creates and securely saves a new key before revoking the old key. Success preserves mobile identity, saved history, read state and onboarding completion, and tells the user to update their CLI.
- Creation failure keeps the current key. Missing/empty response secrets and Keychain write failure never revoke the current key or claim success.
- A revocation failure keeps the new working key, shows the failure and retains a registration-scoped pending revocation so retry finishes it without creating more keys. Success clears that pending state.
- Concurrent reset calls cannot create duplicate keys. Server switches invalidate late results; old operations cannot overwrite the new registration's credentials or UI messages.
- The in-app row and onboarding share the same operation. No new reply/approval behavior, periodic key policy or key-management screen.

## Verification plan

Use the existing Swift/XCTest, XcodeGen and disposable fixture/simulator runner. Add `DeviceKeyResetTests.swift` for model and failure behavior and UI cases for cancellation, failure, success, Chinese and large text. Observe new tests fail against the previous implementation, then pass with the change. Keep existing assertions unless the user's new row changes their contract explicitly. Run all unit tests plus focused Settings UI checks, inspect screenshots, enable code coverage for the new tests, and apply behavioral mutation controls for duplicate guarding, saving-before-revocation, retry and scope validation. Document limitations, including no physical-device key rotation and existing cross-store crash consistency. Clean temporary worktree and DerivedData after integration; retain compact logs and screenshots.

Verification refinement: a reset-specific UI test initially reused the native clipboard helper, but simulator-to-host clipboard synchronization produced unrelated clipboard contents and a later paste-menu event timeout. The reset test now sends a test notification before and after reset and after restarting the app; the fixture validates the actual sender credential and rejects revoked credentials. The separate existing clipboard test is unchanged and is outside this focused reset verifier. Model tests independently check the exact persisted new secret. No new behavior or weaker credential assertion is introduced by this change of observation boundary.
