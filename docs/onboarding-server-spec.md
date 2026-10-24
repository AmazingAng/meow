# Onboarding server selection contract

Scope: the official default is a UI suggestion, never an authenticated session fallback.

Spec approval: not obtained (autonomous run). The user authorized onboarding improvements; this narrow guard preserves existing credential isolation. Existing UI work predates this focused evidence-first check.

Failure model and acceptance checks:
- A new install displays https://api.meow.blue, but does not register until Get started. UI fixture checks the prefilled field without contacting production.
- Missing saved server plus an existing credential must return to onboarding, never route that credential to the official default. Preserve GatewaySessionTests.testMissingServerReturnsToOnboarding unchanged.
- Saving any valid self-hosted URL must preserve it exactly; changing or removing it must not reuse an earlier/default URL. Native generated cases check the actual Shared.swift getter and setter.
- Setup commands remain credential-free; recovery reuses the existing explicit key-rotation confirmation and API. No new auth protocol, pairing API, dependencies or real credential mutations.

Setup: isolated branch codex/onboarding-friction; existing Python, Swift/Xcode and Go tooling. Add ios/scripts/verify_onboarding_server.py and docs/onboarding-server-evidence.md. The native harness copies Shared.swift to a temporary directory, substitutes only the bundle default and isolated UserDefaults suite, and mutates disposable copies. No dependency installation. Commit/push final changes after verification, following the user's delivery preference.

Checks: full iOS unit suite plus guided onboarding UI; CLI suite; native server-selection properties, changed-getter coverage with negative control, four behavior mutants, format/diff checks. Pairing design and broad UI coverage are outside the narrow server-binding assurance claim.
