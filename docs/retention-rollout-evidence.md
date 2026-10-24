# Retention rollout evidence

Base: 978368dc5cdaf502581dcf2cd19e13c5f16c63b6. Contract: [retention-rollout-spec.md](retention-rollout-spec.md). Tier 1 configuration correction; spec approval not obtained (autonomous run). No new dependencies, schema change, iOS/CLI production change or deployment performed.

## Findings and result

The startup maintenance calls Prune immediately. Before build 17, refresh constructs its saved inbox from the remote list; current clients merge into existing local history. The 12h defaults therefore exposed older clients to history loss on refresh. Server parsing, direct/relay Compose defaults and .env.example now use 720h. No compose.shared-caddy.yaml exists on this main checkout. Explicit short windows still work. Existing explicit 12h configurations must be changed before restarting; this correction cannot restore pruned data or preserve older clients beyond the finite 30-day window.

## Checks

- RED: TestCacheConfiguration empty input failed with `got 12h0m0s, <nil> want 720h0m0s` before the production default changed. A sandbox cache access failure preceded the real assertion run; it was retried with cache access.
- `go test -race -shuffle=on -count=1 ./...`: all 9 test-bearing packages passed (2 further packages have no tests). Covers the 720h default, explicit 12h/720h/24h/1h and existing invalid inputs, maintenance scheduling and store cleanup semantics.
- `go vet ./...`: passed. `gofmt -l` on both modified Go files: empty. `git diff --check`: passed. Python ast parsing: passed.
- `verify_bounded_cache.compose_check`: direct and relay Compose each rendered with absent, empty, explicit 12h and explicit 24h values: 8 combinations passed.
- Real loopback Go server and native Swift cache suite: 12 tests passed. A 13h-old message survives restart with no retention override; restarting with explicit 12h prunes it. Downloaded local history remains readable after refresh/restart, searchable, markable as read and explicitly deletable. The existing backup and independent receipt checks passed.
- The first full native wrapper run executed 215 tests: 214 passed and `OnboardingCommandTests.testFreshInstallSuggestsOfficialServer` failed (`nil` versus the expected official URL). The wrapper does not embed the app Info.plist used by that setting; no app configuration code was changed here. This full-wrapper failure remains recorded in dist/retention-rollout/rollout-live.log. Final cache verification deliberately selects the 12 cache tests; it does not claim the full native wrapper passed. The helper's default remains the full suite, and the filtered runner checks the actual `Selected tests` suite result.
- Mutation/property/changed-line coverage layers: not added for this Tier 1 constant/configuration change; default regression was observed red, explicit overrides and invalid inputs are tested, and no parser or cleanup algorithm changed. No phone/simulator test performed. Independent review not performed.

## Reproduce

Existing Go, Python, Swift and Docker Compose tools are sufficient. Integration output directory must be new; no live service or real device credentials are used.

```sh
go test -race -shuffle=on -count=1 ./...
go vet ./...
python3 -B - <<'PYTHON'
import importlib.util
from pathlib import Path
spec = importlib.util.spec_from_file_location('cache', 'scripts/verify_bounded_cache.py')
m = importlib.util.module_from_spec(spec)
spec.loader.exec_module(m)
out = Path('dist/retention-rollout-recheck').resolve()
out.mkdir()
(out / 'runtime-coverage').mkdir()
m.run(['go', 'build', '-cover', '-covermode=atomic', '-o', str(out / 'meow-server'), './server/cmd/meow-server'], out, 'server-build')
m.native_package(out)
print(m.compose_check(out))
print(m.native_live(out, 'live', test_filter='CacheRetentionTests|CacheLiveIntegrationTests'))
PYTHON
git diff --check
gofmt -l server/cmd/meow-server/main.go server/cmd/meow-server/cache_test.go
```
