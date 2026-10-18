#!/usr/bin/env python3
"""Exercise a deployed Meow through its reverse proxy; keep credentials private.

Run `exercise`, restart/recreate the deployment, then run `persistence` with the
same --state-dir. --connect-ip overrides DNS while preserving TLS SNI and Host.
This creates a test device and messages. It never registers an APNs token.
"""

import argparse
import concurrent.futures
import http.client
import json
import os
from pathlib import Path
import socket
import ssl
import time
from urllib.parse import urlsplit
import uuid


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("phase", choices=["exercise", "persistence"])
    p.add_argument("--base-url", required=True)
    p.add_argument("--state-dir", type=Path, required=True)
    p.add_argument("--ca", type=Path)
    p.add_argument("--connect-ip")
    p.add_argument("--report", type=Path, help="optional path for the non-secret JSON result")
    args = p.parse_args()
    base = urlsplit(args.base_url)
    if base.scheme not in ("http", "https") or not base.hostname or base.path not in ("", "/"):
        p.error("base-url must be an HTTP(S) origin")
    context = ssl.create_default_context(cafile=str(args.ca) if args.ca else None)
    checks = []

    def request(method, path, data=None, token=None, idem=None, expected=200):
        if base.scheme == "https":
            conn = http.client.HTTPSConnection(base.hostname, base.port, timeout=75, context=context)
        else:
            conn = http.client.HTTPConnection(base.hostname, base.port, timeout=75)
        if args.connect_ip:
            conn._create_connection = lambda address, timeout, source_address=None: socket.create_connection(
                (args.connect_ip, address[1]), timeout, source_address
            )
        headers = {"Content-Type": "application/json"}
        if token:
            headers["Authorization"] = "Bearer " + token
        if idem:
            headers["Idempotency-Key"] = idem
        try:
            conn.request(method, path, json.dumps(data) if data is not None else None, headers)
            res = conn.getresponse()
            body = res.read()
            assert res.status == expected, f"{method} {path}: expected {expected}, got {res.status}"
            return json.loads(body) if body else None
        finally:
            conn.close()

    def checked(name):
        checks.append(name)
        print("PASS " + name, flush=True)

    assert request("GET", "/healthz")["status"] == "ok"
    checked("HTTPS proxy health" if base.scheme == "https" else "HTTP health")
    state_path = args.state_dir / "credentials.json"
    if args.phase == "exercise":
        reg = request("POST", "/v1/devices", {"name": "Compose deployment verification"}, expected=201)
        key, mobile = reg["key"]["secret"], reg["device_token"]
        state = {"key": key, "mobile": mobile, "device_id": reg["device"]["id"]}
        args.state_dir.mkdir(mode=0o700, parents=True, exist_ok=True)

        def save():
            fd = os.open(state_path, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
            with os.fdopen(fd, "w") as f:
                json.dump(state, f)

        save()
        checked("anonymous device registration")
        assert request("GET", "/v1/whoami", token=key)["device"]["id"] == state["device_id"]
        request("GET", "/v1/inbox", expected=401)
        request("GET", "/v1/inbox", token=key, expected=403)
        checked("credential isolation")

        payload = {"title": "Compose verification", "body": "Caddy HTTPS to SQLite", "source": "deployment-test"}
        idem = str(uuid.uuid4())
        n = request("POST", "/v1/notifications", payload, key, idem, 201)
        replay = request("POST", "/v1/notifications", payload, key, idem)
        assert n["id"] == replay["id"]
        state["notification_id"] = n["id"]
        save()
        inbox = request("GET", "/v1/inbox?filter=unread", token=mobile)["notifications"]
        assert any(item["id"] == n["id"] for item in inbox)
        assert request("GET", "/v1/inbox/counts", token=mobile)["unread"] == 1
        request("POST", f'/v1/notifications/{n["id"]}/delivered', {}, mobile, expected=204)
        request("POST", f'/v1/notifications/{n["id"]}/read', {}, mobile, expected=204)
        detail = request("GET", f'/v1/notifications/{n["id"]}', token=mobile)
        assert detail["delivered_at"] and detail["read_at"]
        assert request("GET", "/v1/inbox?filter=unread", token=mobile)["notifications"] == []
        checked("notification creation, replay, inbox counts, delivered and read")

        q = request("POST", "/v1/questions", {"question": "Approve deployment test?", "choices": ["approve", "reject"], "timeout": 120}, key, expected=201)
        path = f'/v1/questions/{q["id"]}/responses'
        assert request("GET", "/v1/inbox/counts", token=mobile)["pending"] == 1
        assert request("GET", "/v1/inbox?filter=pending", token=mobile)["notifications"][0]["question"]["id"] == q["id"]
        request("POST", path, {"answer": "approve"}, key, str(uuid.uuid4()), 403)
        request("POST", path, {"answer": "approve"}, mobile, expected=400)
        idem = str(uuid.uuid4())
        with concurrent.futures.ThreadPoolExecutor() as pool:
            started = time.monotonic()
            waiting = pool.submit(request, "GET", f'/v1/questions/{q["id"]}/wait?timeout=60', None, key)
            time.sleep(2)
            assert not waiting.done(), "long poll returned before answer"
            answered = request("POST", path, {"answer": "approve"}, mobile, idem, 201)
            result = waiting.result(timeout=15)
            assert result["status"] == "answered" and result["answer"] == "approve"
            assert time.monotonic() - started >= 2
        assert request("POST", path, {"answer": "approve"}, mobile, idem) == answered
        request("POST", path, {"answer": "reject"}, mobile, str(uuid.uuid4()), 409)
        assert request("GET", "/v1/inbox/counts", token=mobile)["pending"] == 0
        state["question_id"] = q["id"]
        save()
        checked("long poll, authenticated response, idempotent retry and conflict")

        q = request("POST", "/v1/questions", {"question": "Expiry must not approve", "timeout": 10}, key, expected=201)
        time.sleep(11)
        request("POST", f'/v1/questions/{q["id"]}/responses', {"answer": "approve"}, mobile, str(uuid.uuid4()), 410)
        assert request("GET", f'/v1/questions/{q["id"]}', token=key)["status"] == "expired"
        checked("expiry fails closed with HTTP 410")
        q = request("POST", "/v1/questions", {"question": "Cancellation test", "timeout": 120}, key, expected=201)
        assert request("POST", f'/v1/questions/{q["id"]}/cancel', {}, key)["status"] == "cancelled"
        checked("question cancellation")
    else:
        state = json.loads(state_path.read_text())
        assert request("GET", "/v1/whoami", token=state["key"])["device"]["id"] == state["device_id"]
        q = request("GET", f'/v1/questions/{state["question_id"]}', token=state["key"])
        assert q["status"] == "answered" and q["answer"] == "approve"
        n = request("GET", f'/v1/notifications/{state["notification_id"]}', token=state["mobile"])
        assert n["read_at"] and n["delivered_at"]
        checked("device credentials, answered question and notification metadata persisted")
    result = {"origin": args.base_url, "phase": args.phase, "checks": checks, "time_utc": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())}
    report = args.report or args.state_dir / (args.phase + "-result.json")
    report.write_text(json.dumps(result, indent=2) + "\n")


if __name__ == "__main__":
    main()
