#!/usr/bin/env bash
# Phase 0 runtime acceptance: start guanjind, probe /healthz & /readyz,
# then prove fail-closed by taking Redis down.
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

HTTP_PORT="${SMOKE_HTTP_PORT:-18085}"
METRICS_PORT="${SMOKE_METRICS_PORT:-19095}"
DB_URL="${GUANJIN_DATABASE_URL:-postgres://guanjin:guanjin@127.0.0.1:5432/guanjin?sslmode=disable}"
REDIS_URL="${GUANJIN_REDIS_URL:-redis://127.0.0.1:6379/0}"
MASTER_KEY="${GUANJIN_MASTER_KEY:-$(head -c 32 /dev/urandom | base64)}"

# The sandbox runs an egress HTTP proxy; bypass it for all loopback calls.
curl_np() { curl --noproxy '*' -s "$@"; }

fails=0
pass() { echo "PASS: $*"; }
fail() { echo "FAIL: $*"; fails=$((fails + 1)); }

cleanup() {
  [ -n "${PID:-}" ] && kill "$PID" >/dev/null 2>&1
  wait "$PID" 2>/dev/null
}
trap cleanup EXIT

echo "== kill stale guanjind =="
pkill -f 'bin/guanjind' >/dev/null 2>&1 || true
sleep 1

echo "== build =="
make build >/dev/null || { echo "build failed"; exit 1; }

echo "== migrate up =="
if GUANJIN_PUBLIC_BASE_URL="http://localhost:${HTTP_PORT}" \
   GUANJIN_DATABASE_URL="$DB_URL" \
   GUANJIN_REDIS_URL="$REDIS_URL" \
   GUANJIN_MASTER_KEY="$MASTER_KEY" \
   ./bin/guanjind migrate up; then
  pass "guanjind migrate up"
else
  fail "guanjind migrate up"; exit 1
fi

echo "== start guanjind =="
GUANJIN_ENV="dev" \
GUANJIN_HTTP_ADDR=":${HTTP_PORT}" \
GUANJIN_METRICS_ADDR="127.0.0.1:${METRICS_PORT}" \
GUANJIN_PUBLIC_BASE_URL="http://localhost:${HTTP_PORT}" \
GUANJIN_DATABASE_URL="$DB_URL" \
GUANJIN_REDIS_URL="$REDIS_URL" \
GUANJIN_MASTER_KEY="$MASTER_KEY" \
./bin/guanjind >/tmp/guanjind-smoke.log 2>&1 &
PID=$!

stopped=0
for i in $(seq 1 40); do
  kill -0 "$PID" 2>/dev/null || { stopped=1; break; }
  if curl_np -f -o /dev/null "http://127.0.0.1:${HTTP_PORT}/healthz"; then break; fi
  sleep 0.3
done
if [ "$stopped" = "1" ]; then
  echo "!! guanjind exited early; log tail:"; tail -8 /tmp/guanjind-smoke.log
fi

probe() {
  local url="$1"; shift
  local out; out="$(mktemp)"
  local code; code="$(curl_np -o "$out" -w '%{http_code}' "$url")"
  echo "  status=$code body=$(cat "$out")" >&2
  rm -f "$out"
  echo "$code"
}

echo "== /healthz =="
code="$(probe "http://127.0.0.1:${HTTP_PORT}/healthz" | tail -1)"
[ "$code" = "200" ] && pass "/healthz 200" || fail "/healthz expected 200 got $code"

echo "== /readyz (all deps up) =="
code="$(probe "http://127.0.0.1:${HTTP_PORT}/readyz" | tail -1)"
[ "$code" = "200" ] && pass "/readyz 200 (db+redis+keys up)" || fail "/readyz expected 200 got $code"

echo "== take Redis down (fail-closed check) =="
redis-cli shutdown nosave >/dev/null 2>&1 || true
sleep 1
code="$(probe "http://127.0.0.1:${HTTP_PORT}/readyz" | tail -1)"
[ "$code" = "503" ] && pass "/readyz 503 when Redis down (fail-closed)" || fail "/readyz expected 503 got $code"

echo "== restore Redis =="
redis-server --daemonize yes --save "" >/dev/null 2>&1 || true
sleep 1
code="$(probe "http://127.0.0.1:${HTTP_PORT}/readyz" | tail -1)"
[ "$code" = "200" ] && pass "/readyz back to 200" || fail "/readyz expected 200 after restore got $code"

echo "== /metrics =="
code="$(probe "http://127.0.0.1:${METRICS_PORT}/metrics" | tail -1)"
[ "$code" = "200" ] && pass "/metrics 200" || fail "/metrics expected 200 got $code"

echo
if [ "$fails" -eq 0 ]; then echo "SMOKE RESULT: ALL PASS"; else echo "SMOKE RESULT: $fails FAILURE(S)"; fi
exit "$fails"
