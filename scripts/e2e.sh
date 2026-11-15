#!/usr/bin/env bash
# e2e.sh runs the core against a real HTTPS origin and checks the whole
# pipeline with curl and the API: capture, mock, rewrite, breakpoint, replay.
#
# It needs curl and an internet connection. Run it from the repository root.
set -euo pipefail

PROXY_ADDR=127.0.0.1:18899
API_ADDR=127.0.0.1:18898
DATA_DIR=$(mktemp -d)
LOG=$DATA_DIR/core.log
BIN=$DATA_DIR/qingqiuxia
TARGET=${TARGET:-https://example.com/}

cleanup() {
  [[ -n "${CORE_PID:-}" ]] && kill "$CORE_PID" 2>/dev/null || true
  wait "${CORE_PID:-}" 2>/dev/null || true
}
trap cleanup EXIT

fail() { echo "FAIL: $*" >&2; exit 1; }
ok() { echo "  ok  $*"; }
api() { curl -fsS "http://$API_ADDR$1" "${@:2}"; }

echo "building"
go build -o "$BIN" ./cmd/qingqiuxia

echo "starting the core"
"$BIN" -headless -data "$DATA_DIR" -proxy "$PROXY_ADDR" -api "$API_ADDR" >"$LOG" 2>&1 &
CORE_PID=$!
for _ in $(seq 50); do
  api /api/status >/dev/null 2>&1 && break
  sleep 0.1
done
api /api/status >/dev/null || { cat "$LOG"; fail "the API did not come up"; }

echo "1. capture"
curl -fsS -o /dev/null --proxy "http://$PROXY_ADDR" --cacert "$DATA_DIR/ca.crt" "$TARGET"
count=$(api /api/sessions | python3 -c 'import json,sys; print(json.load(sys.stdin)["total"])')
[[ $count -ge 1 ]] || fail "no session captured"
ok "$count session(s) captured and decrypted"

echo "2. mock"
api /api/rules -X POST -H 'Content-Type: application/json' -d '{
  "name":"smoke mock","kind":"mock","phase":"request","pattern":"*",
  "mock":{"status":418,"body":"teapot","contentType":"text/plain"}}' >/dev/null
got=$(curl -s -o /dev/null -w '%{http_code}' --proxy "http://$PROXY_ADDR" --cacert "$DATA_DIR/ca.crt" "$TARGET")
[[ $got == 418 ]] || fail "mock returned $got"
ok "a mock rule answered with $got without an origin"

echo "3. rewrite"
rules=$(api /api/rules)
ids=$(echo "$rules" | python3 -c 'import json,sys; print(" ".join(str(r["id"]) for r in json.load(sys.stdin)["rules"]))')
for id in $ids; do api "/api/rules/$id" -X DELETE >/dev/null; done
api /api/rules -X POST -H 'Content-Type: application/json' -d '{
  "name":"smoke rewrite","kind":"rewrite","phase":"response","pattern":"*",
  "actions":[{"type":"replace-body","find":"<title>[^<]*</title>","replace":"<title>请求侠</title>"}]}' >/dev/null
curl -fsS --proxy "http://$PROXY_ADDR" --cacert "$DATA_DIR/ca.crt" "$TARGET" | grep -q "请求侠" \
  || fail "the response was not rewritten"
ok "a response rule rewrote the body"

echo "4. breakpoint"
api /api/rules -X PUT -H 'Content-Type: application/json' -d '{"rules":[]}' >/dev/null
api /api/breakpoints -X PUT -H 'Content-Type: application/json' -d '{
  "breakpoints":[{"name":"smoke pause","phase":"request","pattern":"*"}]}' >/dev/null
curl -fsS -o /dev/null --proxy "http://$PROXY_ADDR" --cacert "$DATA_DIR/ca.crt" "$TARGET" &
CURL_PID=$!
pause_id=""
for _ in $(seq 50); do
  pause_id=$(api /api/pauses | python3 -c 'import json,sys; p=json.load(sys.stdin)["pauses"]; print(p[0]["id"] if p else "")')
  [[ -n $pause_id ]] && break
  sleep 0.1
done
[[ -n $pause_id ]] || fail "no pause was raised"
api "/api/pauses/$pause_id" -X PUT -H 'Content-Type: application/json' \
  -d '{"side":"request","headers":{"User-Agent":["qingqiuxia-e2e"]}}' >/dev/null
api "/api/pauses/$pause_id/resume" -X POST >/dev/null
wait "$CURL_PID" || true
ok "a request was paused, edited and released (pause $pause_id)"

echo "5. replay"
api /api/breakpoints -X PUT -H 'Content-Type: application/json' -d '{"breakpoints":[]}' >/dev/null
before=$(api /api/sessions | python3 -c 'import json,sys; print(json.load(sys.stdin)["total"])')
last=$(api /api/sessions | python3 -c 'import json,sys; print(json.load(sys.stdin)["sessions"][0]["id"])')
api "/api/sessions/$last/repeat" -X POST -H 'Content-Type: application/json' -d '{"count":1}' >/dev/null
sleep 0.5
after=$(api /api/sessions | python3 -c 'import json,sys; print(json.load(sys.stdin)["total"])')
[[ $after -gt $before ]] || fail "a replay was not captured ($before then $after)"
ok "a replay went out and was captured ($before then $after)"

echo "6. status"
api /api/status | python3 -m json.tool | sed 's/^/  /'

echo
echo "all checks passed"
