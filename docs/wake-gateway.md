# Wake gateway

A self-hosted Meow server can use an operator-run gateway without installing
Apple credentials. The phone registers automatically after receiving its APNs
token. Only routing metadata crosses the gateway; the phone fetches notification
content and sends replies directly to its saved Meow API. FCM is not involved.

```text
Wake:    Meow API → gateway → APNs → iPhone
Content: iPhone → Meow API
Replies: iPhone → Meow API
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

After registration, management and wake operations require distinct,
device-scoped credentials. `gwm_` manages one route and `gwp_` only wakes that
route. Neither reads content or answers questions. The gateway stores only
hashes of those credentials. The phone keeps them in Keychain and passes only
the wake capability to its own API. The API must store that outbound capability
recoverably, so protect its database and backups; inbound `mdt_` and `meow_`
credentials remain hash-only.

A gateway wake is a generic mutable APNs alert with no approval/reply category.
It contains only event ID, binding ID and `relay:true`. The extension checks its
current binding, fetches `GET /v1/notifications/{id}` from the locally saved API,
then builds the displayed text and actions. No URL from the push is followed.
Failed fetches show a generic alert without actions; expiry is checked again
before display and before responding. Replies use the existing durable offline
queue, mobile authentication and fixed idempotency key.

Server changes clear the local binding and attempt to revoke the old route.
Offline revocation is best-effort; old-binding notifications still cannot act
on the new server identity. Gateway retries can duplicate alerts after an
ambiguous network failure, while answer idempotency prevents duplicate question
resolution.

## Delivery and operations

- Wake rate limiting remains 60 attempts/minute per device route and survives
  restarts. Failed APNs attempts count toward that rate.
- The gateway sees APNs tokens, routing/event IDs, priority, expiry, network
  metadata and timing. Notification content and replies stay on the API path.
- `accepted` means APNs accepted the push; extension receipt and `read_at` are
  separate and do not prove that iOS displayed it to the user.
- Agent `GET /v1/whoami` exposes transport-neutral `push_registered` for setup
  and doctor; a gateway device does not need a direct APNs token on the API.
- SQLite upgrades v1→v2 transactionally. Old binaries cannot read v2 databases;
  keep a pre-upgrade snapshot if you need to roll back.

Verification: `python3 scripts/verify_gateway.py --simulator-id <dedicated-Meow-Gateway-simulator> --mutations`.
The suite uses simulated APNs boundaries; locked physical-device delivery and
Face ID for this route remain separate real-device checks.
