# Docker deployment

The base `compose.yaml` runs Meow as UID 65532 with a persistent SQLite volume.
Caddy is the only published service. It obtains a public TLS certificate for
`MEOW_DOMAIN` and redirects HTTP to HTTPS.

## Prepare credentials

Keep the APNs key outside the checkout and Docker build context:

```sh
install -d -m 700 "$HOME/.config/meow/deploy"
install -m 444 /path/to/AuthKey_XXXXXXXXXX.p8 "$HOME/.config/meow/deploy/apns.p8"
cp deploy/.env.example deploy/.env
chmod 600 deploy/.env
```

Set `MEOW_APNS_KEY_PATH` in `deploy/.env` to the **absolute** path of that copy,
and fill in the domain, APNs Key ID and Team ID. Keep the parent directory mode
700. The key copy must be readable by the nonroot container user; a host-owned
mode-600 file fails on Linux. The original download can remain mode 600. The
bind mount is read-only, and a missing key fails rather than creating a directory.

## Start on a publicly reachable host

Point the domain at the host and allow inbound TCP 80/443 (UDP 443 enables HTTP/3).
Then, from this repository:

```sh
docker compose --env-file deploy/.env -f deploy/compose.yaml up -d --build --wait
curl --fail --show-error https://YOUR_DOMAIN/healthz
docker compose --env-file deploy/.env -f deploy/compose.yaml ps
docker compose --env-file deploy/.env -f deploy/compose.yaml logs --tail 50
```

At startup the server checks its APNs credentials against Apple, in the
background, and logs `APNs credentials accepted` or `APNs credentials rejected`
for each environment, with the likely cause (for example, Key ID, Team ID and
key that don't belong together). To run the check on demand:

```sh
docker compose --env-file deploy/.env -f deploy/compose.yaml exec meow /meow-server check-apns
```

Builds installed from Xcode use the sandbox environment; TestFlight and App
Store builds use production. A key limited to one environment fails the other
check, which only matters if you install builds that use it.

A machine behind a home router needs port forwarding or a tunnel. DNS pointing
at a LAN address does not make it reachable from the Internet. Cloudflare's
public edge certificate alone does not prove Caddy has obtained its certificate.

## Exercise the deployed API

The verifier creates a separate test device without an APNs token. It checks
registration, credential separation, notifications, read/delivery metadata,
idempotent creates/replies, long polling, conflict handling, expiry and cancellation.
Its state directory holds device credentials; keep that directory private.

```sh
python3 scripts/verify_deployment.py exercise \
  --base-url https://YOUR_DOMAIN --state-dir "$HOME/.config/meow/deploy/check"
docker compose --env-file deploy/.env -f deploy/compose.yaml up -d --force-recreate --wait
python3 scripts/verify_deployment.py persistence \
  --base-url https://YOUR_DOMAIN --state-dir "$HOME/.config/meow/deploy/check"
```

For a local TLS test, a separate Caddy config can use `tls internal`. Export its
public root certificate and supply `--ca /path/to/root.crt --connect-ip 127.0.0.1`
to the verifier. Hostname verification and SNI are preserved. This verifies local
TLS, **not** public ACME issuance; it does not require adding a CA to system trust.

## Back up and restore

```sh
docker compose --env-file deploy/.env -f deploy/compose.yaml exec -T meow \
  /meow-server backup /data/backup-YYYY-MM-DD.db
docker compose --env-file deploy/.env -f deploy/compose.yaml cp \
  meow:/data/backup-YYYY-MM-DD.db "$HOME/.config/meow/deploy/backup.db"
chmod 600 "$HOME/.config/meow/deploy/backup.db"
```

The backup is an online SQLite snapshot; do not copy the active database file.
Test a restore against a **different database path or volume**, keeping the live
database untouched. Start a second server with `MEOW_DB` pointing at the snapshot,
then run the verifier's `persistence` phase against it using the original test
credentials. Remove the restore-test container afterward.

`docker compose down` preserves data. `docker compose down -v` deletes the SQLite
and Caddy certificate volumes. Export backups before intentionally deleting them.

## Delivery cache and rollout

Both direct-APNs and relay Compose files pass `MEOW_RETENTION` from `.env`, defaulting to `12h`. Content is pruned on startup and every five minutes. Downloaded iPhone history has its own lifetime and is not removed by server expiry. A phone offline longer than the server window cannot backfill expired messages.

Update iOS clients to the version that preserves independent local history **before** shortening the server window. Keep `MEOW_RETENTION=720h` during a staged rollout if older clients remain in use. This server version automatically upgrades the SQLite schema to add a content-free 24-hour send-receipt ledger; make an online backup before upgrading. Older server binaries do not understand the new schema: rollback requires the pre-upgrade backup, not just swapping the binary. TTL does not remove old backup files or guarantee byte erasure from SQLite/WAL.

### Semantic event rollout (schema v5)

Upgrade the server, then the CLI/adapters, then iOS to show event icons. Old clients ignore the additive field; old messages keep their title and remain generic. New CLI/adapters explicitly emit `result`, `attention` or `failure`; custom integrations may omit `event`. Do not infer or backfill events from titles. Existing cache TTL and the App's independent local history are unchanged.

Before the server upgrade, take the online backup described above using the currently running server binary. Schema v5 adds a constrained `event` column and preserves old rows with an empty value. An older server binary refuses v5 databases; rollback requires stopping the new server and restoring the pre-upgrade snapshot (new messages after that snapshot are not included). Keep the live upgraded database separately until the rollback has been checked.
