# Running a Meow server

Read this only when the user wants to host their own server. Everything below comes from the repository https://github.com/AmazingAng/meow (`deploy/README.md` and `docs/wake-gateway.md` have the full details).

The server is a single Go process with one SQLite file. The iPhone and the CLI must both reach it. Pick one way to deliver push notifications:

| Mode | Needs | When |
| --- | --- | --- |
| Direct APNs | An Apple developer APNs key (`.p8`), Key ID and Team ID for bundle `blue.meow.app` | The user operates the app's Apple team |
| Wake gateway | `MEOW_PUSH_GATEWAY_URL` pointing at a gateway that holds the APNs key | Self-hosting without Apple credentials |
| No push | Nothing | Local testing. Messages show in the app inbox only, and push is recorded as `skipped`. |

Don't ask the user for a DeviceCheck key; Meow doesn't use one. Never read, print or commit `.p8`, `.env` or `.db` files.

## Local server for testing

```sh
git clone https://github.com/AmazingAng/meow && cd meow
go build -o bin/meow-server ./server/cmd/meow-server
MEOW_ADDR=:8080 bin/meow-server
curl -fsS http://127.0.0.1:8080/healthz      # {"status":"ok"}
```

On a physical iPhone, use the computer's LAN address, such as `http://192.168.1.20:8080`, on the same Wi-Fi.

## Docker with HTTPS (direct APNs)

You need a public host with DNS for the domain and inbound TCP 80/443 open. Caddy obtains the certificate.

```sh
install -d -m 700 "$HOME/.config/meow/deploy"
install -m 444 /path/to/AuthKey_XXXXXXXXXX.p8 "$HOME/.config/meow/deploy/apns.p8"
cp deploy/.env.example deploy/.env && chmod 600 deploy/.env
# The user edits deploy/.env: MEOW_DOMAIN, MEOW_APNS_KEY_ID, MEOW_APNS_TEAM_ID,
# and MEOW_APNS_KEY_PATH (absolute path of the copy above)
docker compose --env-file deploy/.env -f deploy/compose.yaml up -d --build --wait
curl --fail --show-error https://YOUR_DOMAIN/healthz
docker compose --env-file deploy/.env -f deploy/compose.yaml exec meow /meow-server check-apns
```

`check-apns` and the startup log say whether Apple accepted the credentials, for each environment. Xcode builds use sandbox; TestFlight and App Store builds use production.

## Docker with the wake gateway (no Apple credentials)

```sh
cd deploy
MEOW_DOMAIN=meow.example.com MEOW_PUSH_GATEWAY_URL=https://push.example.com \
  docker compose -p meow-relay -f compose.relay.yaml up -d --build
```

The app registers with the gateway automatically. The gateway only sees routing IDs, never the message content. The phone fetches content and sends replies directly to this server, so the server must still be reachable from the phone (public HTTPS, or a VPN when away from the LAN).

## Settings

| Variable | Default | |
| --- | --- | --- |
| `MEOW_ADDR` | `:8080` | Listen address |
| `MEOW_DB` | `meow.db` | Keep it on a persistent volume |
| `MEOW_RETENTION` | `720h` | Old notifications and resolved questions are pruned |
| `MEOW_TRUST_PROXY` | | Set to `true` behind a reverse proxy, so rate limits use `X-Forwarded-For` |
| `MEOW_APNS_KEY_FILE`, `MEOW_APNS_KEY_ID`, `MEOW_APNS_TEAM_ID`, `MEOW_APNS_TOPIC` | | Direct APNs |
| `MEOW_PUSH_GATEWAY_URL` | | Wake gateway |

Back up with `meow-server backup /path/backup.db`, not by copying the live file. `docker compose down -v` deletes the data volumes, so confirm with the user before running it.

After the server is up, the user enters its address in the iPhone app, then continues with step 2 in [../SKILL.md](../SKILL.md).
