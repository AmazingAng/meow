# TestFlight 0.1.0 (2) — build and upload status

2026-10-19. This record distinguishes a distribution-signed IPA from a build uploaded to TestFlight. Current status: **TestFlight 0.1.0 (2) installed and the scoped real gateway production/LAN smoke test passed.**

## Source changes

- `ios/Shared/PrivacyInfo.xcprivacy` is packaged into both the main app and notification extension through their existing Shared source membership. UserDefaults reasons: `1C8F.1` for App Group settings and `CA92.1` for app-local fallback storage. No file timestamp, disk space, system boot time or active keyboard API usage was found in app source.
- The manifest declares device identifiers, linked to the device/user, used for app functionality (APNs delivery/routing), with no tracking or tracking domains. The gateway persists APNs tokens and device routing IDs. Notification bodies and replies go directly to the selected Meow API; they are not relayed through the gateway. There is no analytics SDK. App Store privacy questionnaire and public policy remain separate from this binary manifest; hosting a public Meow API that collects content would require revisiting those disclosures.
- Build number changed from 1 to 2; version remains 0.1.0.
- XcodeGen's target defaults overrode the project-level iPhone setting and produced UIDeviceFamily [1,2]. Both shipping targets now specify iPhone explicitly. The final archive is [1] and builds without the earlier orientation warning; no iPad functionality was added.
- `ios/ExportOptions.plist` exports with app-store-connect automatic signing; build numbering is explicit. `ios/scripts/verify_distribution.py` checks the actual archive or IPA: bundle/version alignment, packaged manifests, code signatures, App Group and (for IPA) production APNs/non-debuggable App Store provisioning.

## Executed checks

- Xcode 26.6 (17F113), XcodeGen 2.46.0; no dependency added.
- Both new plist files passed plutil validation.
- Final Release archive `Meow-iPhone.xcarchive` succeeded without warnings.
- App Store Connect export succeeded using the existing Xcode account/cloud signing. A new local distribution identity was not required.
- Final IPA inspection passed for app and extension: matching version/build, iPhone-only, manifests present, signatures valid, shared App Group present, get-task-allow false. App entitlement aps-environment is production. Embedded profiles have beta-reports-active true; neither lists individual devices.
- No .p8, .pem, .key or .env files were present in the IPA.
- Existing Swift/Go runtime source was not changed during this packaging task. Earlier regression suite results are in remove-temporary-gates-evidence.md; they are not presented as a new full-suite run.

## Initial blockers and continuation

1. Upload via Xcode exportArchive destination=upload failed with exit 70, Error Downloading App Information. The logged App Store Connect request filtered bundleId=blue.meow.app, returned HTTP 200 and **zero matching app records**. No binary was uploaded. Create the iOS app record in App Store Connect, then rerun the upload below. This does not submit an App Store release for review.
2. Using the existing private APNs configuration, the server's real Apple credential check accepted sandbox but production returned **403 BadEnvironmentKeyInToken**. The existing key cannot validate TestFlight production pushes. A production-capable key for blue.meow.app is needed; do not change the app back to sandbox to work around it.

The original checks above remain historical evidence. On continuation, the user created App Store Connect app `6824443742`, named **Meow: AI Agent Notifications**, for `blue.meow.app`. Xcode returned one matching app and began uploading the same Release archive using the existing destination=upload configuration. Retry log: `dist/testflight/0.1.0-2/upload-retry.log`. At **17:15:39 +08:00**, the uploader reported **Upload succeeded**, followed by **Uploaded Meow / EXPORT SUCCEEDED**, and xcodebuild returned exit 0. Apple reported the package had begun processing. Processing completion/tester availability is a separate state and is not inferred from upload success.

The user supplied a new APNs key, ID `QXK2SA2Q55`. A mode-600 working copy lives under the private test directory; the original key was not modified. The existing server binary's real Apple credential check now returns exit 0 and **accepted** for both sandbox and production. This establishes provider authentication, not phone delivery.

A separate real gateway/origin pair was started for phone testing using the new server binary, isolated databases and logs under `~/.config/meow/testflight-gateway/`:

- API: `http://192.168.31.235:8182`, gateway: `http://192.168.31.235:8181`.
- Both `/healthz` endpoints passed. Only the gateway process receives APNs credentials; the API process receives the gateway URL and no APNs settings.
- These are LAN test endpoints, not a public deployment. The existing API on port 8080, Docker services and DNS were not changed.
- Native processes, PID files, mode-600 run environment files and databases remain in the private directory for continuation. No fake APNs token or synthetic phone route was registered.

The paired iPhone was reachable and still had Meow 0.1.0 (1). Its app/data were not replaced or reset. No real gateway phone delivery, approval, offline retry or revocation result is claimed yet.

## Local artifacts and continuation

Artifacts are ignored under `dist/testflight/0.1.0-2/`:

- `Meow-iPhone.xcarchive`, `archive-iphone.xcresult`, `archive-iphone.log`
- `export-iphone/Meow.ipa`, export metadata and `export-iphone.log`
- `archive-inspection.json`, `ipa-inspection.json`, `status.json`
- `UploadOptions.plist`, `upload.log` (initial failed record lookup), `upload-retry.log` (current upload attempt)

Apple distribution logs stay in the local temporary directory; they are not committed because they may contain account session details. Existing APNs credentials/logs remain under the private `~/.config/meow/` directory.

```sh
python3 ios/scripts/verify_distribution.py \
  dist/testflight/0.1.0-2/export-iphone/Meow.ipa --version 0.1.0 --build 2

# After creating the app record; reuses the already prepared destination=upload options.
xcodebuild -exportArchive \
  -archivePath dist/testflight/0.1.0-2/Meow-iPhone.xcarchive \
  -exportPath dist/testflight/0.1.0-2/upload-next \
  -exportOptionsPlist dist/testflight/0.1.0-2/UploadOptions.plist \
  -allowProvisioningUpdates
```

Once uploaded and processed, install this exact build through TestFlight. The replacement APNs key is checked and the separate gateway/origin processes are ready. Verify automatic binding, generic fallback/content hydration, lock-screen identity-verified approval, offline retry, expiry rejection and route revocation on server changes. Use the existing LAN first; Internet/mobile-data reachability is a separate deployment check and was previously deferred.

Apple references: [required API reasons](https://developer.apple.com/documentation/bundleresources/app-privacy-configuration/nsprivacyaccessedapitypes/nsprivacyaccessedapitypereasons), [manifest collection categories](https://developer.apple.com/documentation/bundleresources/app-privacy-configuration/nsprivacycollecteddatatypes/nsprivacycollecteddatatype), [upload builds](https://developer.apple.com/help/app-store-connect/manage-builds/upload-builds).

## Browser verification after upload

Read the user-requested App Store Connect page in their existing Chrome session. The Distribution draft is version 1.0, Prepare for Submission, with store metadata still blank. Navigated read-only to TestFlight: version 0.1.0, Build 2 is listed as Ready to Submit, with 90 days remaining. The build details show Groups (0), and the Internal Testing group list is empty. No store metadata, testers, groups, review submissions or legal agreements were changed. The next installation prerequisite is creating/assigning an internal testing group and selecting the user’s own account. The store draft version can be aligned before a later public submission; it does not prevent this TestFlight build from existing.

After the user reported Waiting for Review, refreshed the browser read-only. The build is waiting for TestFlight App Review, Groups (0) remains, and Individual Tester (1) shows No Builds Available. No internal group appears in the sidebar. Apple documents this review state as the external-testing approval queue; an internal group containing an eligible App Store Connect user is the path for immediate internal testing. No review was cancelled and no invitations were sent by the agent.

## Real gateway phone verification — completed

The user installed Meow via TestFlight; the paired iPhone reports version **0.1.0**, build **2**. The app registered a production APNs route automatically. The origin holds a gateway binding and no direct APNs token. Real provider credentials are present only in the gateway process; this test uses Apple's production APNs endpoint, not a mock server or simulator push injection.

Machine-readable results: [testflight-real-gateway-results.json](testflight-real-gateway-results.json). The private original, test CLI config, queue/wait outputs and service logs remain under `~/.config/meow/testflight-gateway/`. Published results contain no mobile tokens, Device Keys, APNs tokens, management/wake secrets or private key material.

| Scenario | Observed result |
| --- | --- |
| Automatic gateway enrollment | One production route bound successfully; origin has no direct APNs token. |
| Real CLI notification | APNs accepted; extension receipt recorded; user saw the full `TF2-PRODUCTION-01` body. |
| Lock-screen approval | User approved; CLI wait returned exit 0 and answer approve; one committed response. |
| Controlled locked-device check | Initial user observation was no visible authentication prompt. Repeated with front camera covered and lock icon closed: CoreDevice reported passcodeRequired=true, question remained pending with zero responses; user saw concealed notification content and an unlock/password requirement. After authentication, user approved; CLI returned 0, one response. This confirms locked-device protection, not a fresh biometric challenge for every tap on an already-unlocked device. Preview privacy and action-only authentication were not isolated. |
| Offline free-text reply | User loaded the question, test API was stopped while gateway stayed healthy, user submitted tf2-offline-ok and saw the offline queue. Before recovery the server remained pending with zero responses. User force-quit and reopened the app while API was still down. After API restart, it automatically committed the original answer once; CLI retried network failures and returned exit 0. |
| Expiry | A 30-second question became expired with no answer and zero responses; CLI returned exit 4. User confirmed expired UI and no approval available. |

Totals: **5 notifications accepted by APNs, 5 extension receipts, 3 answered questions, 1 expired question, 3 committed responses**. An extension receipt is not itself proof of visual presentation; visual observations above come from the user. Final health checks passed for both services, including the restored origin.

No runtime source change or replacement build was needed during these tests. Existing services on port 8080 and ordinary CLI configuration were retained; test CLI login is isolated in the private test directory. Meow on the phone now uses the test origin at http://192.168.31.235:8182, with gateway http://192.168.31.235:8181. Both services remain running for continued LAN use; private run scripts and PID files support restarting them. The Mac must stay awake and reachable on that LAN.

Limitations: this session does not establish public HTTPS/mobile-data reachability, a live retry-after-commit idempotent replay, real fallback while the origin is unreachable during extension fetch, token rotation, cancellation/rejection, or live server-switch revocation. Earlier automated tests cover these separately where documented; they are not relabelled as physical-device checks. No physical-phone screenshots were taken. App Store metadata/public release preparation remains a separate task.
