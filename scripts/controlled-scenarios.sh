#!/usr/bin/env bash
set -euo pipefail

set -a
source .env
set +a

api="http://127.0.0.1:8080"
compose=(docker compose)

wait_healthy() {
  local url="$1"
  for _ in $(seq 1 60); do
    if curl --fail --silent "$url" >/dev/null; then return 0; fi
    sleep 1
  done
  echo "Timed out waiting for $url" >&2
  return 1
}

create_channel() {
  local name="$1" target="$2" timeout="$3" attempts="$4"
  curl --fail --silent --show-error -X POST "$api/api/channels" \
    -H 'Content-Type: application/json' -H "X-Admin-Token: $ADMIN_TOKEN" \
    -d "{\"name\":\"$name\",\"target_url\":\"$target\",\"secret\":\"$DEMO_WEBHOOK_SECRET\",\"timeout_ms\":$timeout,\"max_attempts\":$attempts,\"rate_limit_per_second\":20,\"max_concurrency\":2}" >/dev/null
}

send_event() {
  local channel="$1" key="$2" body="$3"
  local signature
  signature="$(printf '%s' "$body" | openssl dgst -sha256 -hmac "$DEMO_WEBHOOK_SECRET" -hex | awk '{print $NF}')"
  curl --fail --silent --show-error -X POST "$api/webhooks/$channel" \
    -H 'Content-Type: application/json' -H "Idempotency-Key: $key" \
    -H "X-Webhook-Signature: sha256=$signature" -d "$body"
}

query_scalar() {
  "${compose[@]}" exec -T postgres psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -Atc "$1"
}

"${compose[@]}" up -d --build
wait_healthy "$api/health/ready"
wait_healthy "http://127.0.0.1:8081/health"

create_channel success "http://receiver:8081/success" 2000 3
create_channel server-error "http://receiver:8081/fail" 1000 2
create_channel timeout "http://receiver:8081/timeout?seconds=3" 500 2
create_channel restart "http://receiver:8081/timeout?seconds=7" 10000 3

first="$(send_event success duplicate-key '{"scenario":"duplicate"}')"
second="$(send_event success duplicate-key '{"scenario":"duplicate"}')"
test "$(printf '%s' "$first" | jq -r .duplicate)" = false
test "$(printf '%s' "$second" | jq -r .duplicate)" = true
test "$(query_scalar "SELECT count(*) FROM webhook_events WHERE idempotency_key='duplicate-key'")" = 1

send_event server-error error-key '{"scenario":"receiver-500"}' >/dev/null
send_event timeout timeout-key '{"scenario":"receiver-timeout"}' >/dev/null
for _ in $(seq 1 30); do
  dead="$(query_scalar "SELECT count(*) FROM deliveries WHERE status='dead_letter'")"
  if [ "$dead" -ge 2 ]; then break; fi
  sleep 1
done
test "${dead:-0}" -ge 2

dead_id="$(query_scalar "SELECT id FROM deliveries WHERE status='dead_letter' ORDER BY created_at LIMIT 1")"
curl --fail --silent -X POST "$api/api/deliveries/$dead_id/replay" -H "X-Admin-Token: $ADMIN_TOKEN" >/dev/null
sleep 4
test "$(query_scalar "SELECT replay_count FROM deliveries WHERE id='$dead_id'")" = 1

"${compose[@]}" restart postgres
wait_healthy "$api/health/ready"
send_event success after-db-restart '{"scenario":"postgres-restart"}' >/dev/null

send_event restart worker-restart '{"scenario":"worker-restart"}' >/dev/null
sleep 1
"${compose[@]}" kill worker
"${compose[@]}" up -d worker
for _ in $(seq 1 35); do
  state="$(query_scalar "SELECT status FROM deliveries d JOIN webhook_events e ON e.id=d.event_id WHERE e.idempotency_key='worker-restart'")"
  if [ "$state" = delivered ]; then break; fi
  sleep 1
done
test "${state:-}" = delivered
attempts="$(query_scalar "SELECT attempt_count FROM deliveries d JOIN webhook_events e ON e.id=d.event_id WHERE e.idempotency_key='worker-restart'")"
test "$attempts" -ge 2

curl --fail --silent "$api/metrics" | grep -q webhook_received_total
"${compose[@]}" exec -T worker /app/durable-webhook-delivery healthcheck http://127.0.0.1:9090/health/ready
echo "All controlled scenarios passed"
