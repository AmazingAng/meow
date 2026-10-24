# Retention rollout defaults

Scope: Tier 1 configuration correction. Work directly in the clean checkout; no new dependencies, schema, cleanup algorithm, iOS or CLI changes. Spec approval: not obtained (autonomous run within requested correction).

Failure model: a server restarted with the implicit 12h retention deletes bodies before older apps can safely retain local history. A configured 12h override remains intentional and must not be silently overridden. Previously deleted bodies cannot be recovered by changing retention.

Acceptance:
1. Unset or empty MEOW_RETENTION resolves to 720h; explicit 12h, 24h and 1h remain accepted. Invalid/sub-hour/disabled settings remain rejected.
2. Every existing Compose entry point defaults to 720h and preserves explicit overrides. The environment example uses 720h.
3. Real startup with no override preserves a 13-hour-old notification; explicit 12h startup still prunes it. Existing integration continues verifying that downloaded iPhone history survives server expiry.
4. Docs describe 720h as the rollout default, require build 17+ before an operator explicitly shortens retention, and explain that an existing explicit 12h setting is not automatically migrated.

Setup and checks: modify server/cmd/meow-server/{main.go,cache_test.go}, deploy/{compose.yaml,compose.relay.yaml,.env.example,README.md}, README.md, docs/architecture.md, scripts/verify_bounded_cache.py. compose.shared-caddy.yaml is absent on main. Use existing Go, Python and Docker tools; no installation. Run the configuration regression before changing production defaults, then go test ./..., go vet ./..., gofmt/diff checks, Compose rendering and a real loopback server startup rehearsal. Commit the correction directly to main per the existing user preference. Record actual results and skipped layers in docs/retention-rollout-evidence.md.
