# Wake gateway

A self-hosted Meow server can use an operator-run gateway without installing
Apple credentials. The phone registers automatically after receiving its APNs
token. Only routing metadata crosses the gateway; the phone fetches notification
content directly from its saved Meow API. Replies stay in the agent on the computer. FCM is not involved.

```text
Wake:    Meow API → gateway → APNs → iPhone
Content: iPhone → Meow API
```

## Gateway operator

Run `meow-server gateway` as a separate process with its own persistent database.
Only this host receives `MEOW_APNS_KEY_FILE`, `MEOW_APNS_KEY_ID`,
`MEOW_APNS_TEAM_ID` and `MEOW_APNS_TOPIC` for the app's Apple team/bundle.

```sh
MEOW_DB=/private/data/gateway.db meow-server gateway
```

Alternatively, use the existing APNs/domain configuration with the gateway
Compose override. No enrollment secret or user invitation is needed:

```sh
cd deploy
docker compose -p meow-gateway -f compose.yaml -f compose.gateway.yaml up -d --build
```

Use the gateway hostname as `MEOW_DOMAIN`. This role exposes gateway endpoints,
not the mobile/agent API. Run the originating API on a separate host or configure
a reverse proxy for both; two Caddy instances cannot bind the same host's ports.

## Self-hosted API

Set `MEOW_PUSH_GATEWAY_URL` to the gateway's HTTP/HTTPS base URL, then start the
API as usual. The API host needs no `.p8` key, Key ID or Apple team account.

```sh
MEOW_PUSH_GATEWAY_URL=https://push.example.com meow-server
```

For Compose, set `MEOW_DOMAIN` and `MEOW_PUSH_GATEWAY_URL`, then run:

```sh
cd deploy
docker compose -p meow-relay -f compose.relay.yaml up -d --build
```

In the app, choose your API and allow notifications. Gateway registration and
binding happen automatically; there is no gateway setup screen. Ordinary
notification settings show the gateway address and a retry action if needed.

HTTP addresses work for self-hosted/LAN setups just as they do for the main API.
TLS certificate validation remains enabled for HTTPS, and clients refuse
redirects with credentials. The phone must be able to reach both the gateway
and its own API. A gateway does not expose a LAN-only API to mobile data; use
a reachable address or your VPN when outside that LAN. iOS transport and local
network permission rules still apply.

## Protocol and credentials

| Endpoint | Credential | Request/result |
| --- | --- | --- |
| `POST /v1/gateway/devices` | None | `apns_token`, `apns_env`, random `binding_id`; returns `id`, `management_token`, `push_token`, `binding_id` once |
| `PATCH /v1/gateway/devices/{id}` | Route's management token | Update `apns_token`, `apns_env`; binding remains immutable |
| `DELETE /v1/gateway/devices/{id}` | Route's management token | Revoke future requests; in-flight pushes may finish |
| `POST /v1/gateway/push` | Route's push token | `device_id`, `notification_id`, `priority`, RFC3339 `expires_at`; HTTP 200 `accepted:true` follows actual APNs acceptance |
| `GET /v1/push-config` on API | Mobile `mdt_` | `mode:direct` or `mode:gateway` with `gateway_url` |
| `PUT /v1/device/push-gateway` on API | Mobile `mdt_` | `gateway_device_id`, `push_token`, `binding_id` |

Registration is anonymous, as with the existing mobile device registration.
It validates APNs token/environment/binding input and applies a per-client-IP
limit of 10 registrations/hour (burst 10). Behind a trusted reverse proxy, set
`MEOW_TRUST_PROXY=true` so unrelated clients do not share the proxy's address.
There is no installation-wide device count or global registration quota.
Anonymous registration is not cryptographic proof of phone ownership.

All gateway endpoints additionally share a per-client-IP request bucket: a burst
of 200 requests, replenished at 20 requests/second. Invalid credentials, malformed
requests and unknown paths consume the same allowance before database access.
Changing routes does not create a fresh IP allowance. IPv4-mapped addresses are
canonicalized; IPv6 addresses in the same /64 share allowance. Clients behind
one NAT share a bucket. Only enable proxy trust behind an ingress that overwrites
or appends the actual connecting client's address; do not expose that trusted
listener directly to untrusted clients.

After registration, management and wake operations require distinct,
device-scoped credentials. `gwm_` manages one route and `gwp_` only wakes that
route. Neither reads content or executes a decision. The gateway stores only
hashes of those credentials. The phone keeps them in Keychain and passes only
the wake capability to its own API. The API must store that outbound capability
recoverably, so protect its database and backups; inbound `mdt_` and `meow_`
credentials remain hash-only.

A gateway wake is a generic mutable APNs alert with no approval/reply category.
It contains only event ID, binding ID and `relay:true`. The extension checks its
current binding, fetches `GET /v1/notifications/{id}` from the locally saved API,
then builds the displayed text. No URL from the push is followed. Failed fetches
show a generic alert without actions. V1 never offers approval/reply actions;
old saved reply text remains copyable on the phone and is never transmitted.

Server changes clear the local binding and attempt to revoke the old route.
Offline revocation is best-effort; old-binding notifications cannot act on the new
server identity. Ambiguous network failures can still duplicate an alert.

## Delivery and operations

- Wake rate limiting remains 60 attempts/minute, now shared by APNs token and
  environment across routes, case aliases and client IPs. Restart, revocation
  and re-enrollment do not reset active usage. Failed APNs attempts count.
- Gateway delivery has no fixed concurrency cap by default:
  `MEOW_GATEWAY_MAX_CONCURRENT_PUSHES=0`. Set a positive whole number only when
  you want an explicit operator cap; negative/malformed settings fail startup.
  Existing deployments with an explicit `32` keep it until that setting is
  removed or changed to `0`. IP limits and any opted-in cap apply per process.
- IP exhaustion returns HTTP 429 `rate_limited` and a positive `Retry-After`.
  When an explicit cap is configured, full APNs capacity returns the same error with `Retry-After: 1`, immediately,
  without queuing another push or spending the route's durable quota. Existing
  Meow API push retries already respect this delay. Health and management do not
  need an APNs slot, though their own IP/authentication limits still apply.
- The gateway sees APNs tokens, routing/event IDs, priority, expiry, network
  metadata and timing. Notification content stays on the API path; replies remain on the computer.
- `accepted` means APNs accepted the push; extension receipt and `read_at` are
  separate and do not prove that iOS displayed it to the user.
- Agent `GET /v1/whoami` exposes transport-neutral `push_registered` for setup
  and doctor; a gateway device does not need a direct APNs token on the API.
- APNs `Unregistered`, invalid-token and token/topic errors remove the matching
  route. Conditional deletion preserves a token rotated while a push was in
  flight. Transient network/provider failures do not delete registrations.
- SQLite upgrades to v7 transactionally, preserving active legacy quota usage.
  Old binaries cannot read v7 databases;
  keep a pre-upgrade snapshot if you need to roll back.

Current verification: `python3 -B scripts/verify_launch_hardening.py --output dist/launch-check`.
It uses disposable SQLite, loopback HTTP and simulated APNs boundaries. Physical
phone delivery remains a separate check. Older gateway verification artifacts
record their original source states, including the retired approval workflow.
