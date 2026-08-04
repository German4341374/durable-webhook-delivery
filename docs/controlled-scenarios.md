# Controlled scenarios

`scripts/controlled-scenarios.sh` uses only the isolated Compose network and synthetic payloads.

1. **Receiver HTTP 500:** a channel points to `/fail`, retries twice with jitter, then reaches dead letter.
2. **Receiver timeout:** `/timeout` waits longer than the channel timeout; the worker retries and dead-letters it.
3. **Duplicate event:** the exact key is submitted twice; responses change from `duplicate: false` to `true`, while PostgreSQL retains one event/outbox pair.
4. **PostgreSQL restart:** Compose restarts PostgreSQL, the readiness check recovers, and a new signed event is accepted afterward.
5. **Worker restart during delivery:** the worker is killed while `/timeout` is running. Its lease expires, the replacement worker claims the delivery, and the script asserts eventual delivery with at least two claims.

The scenario also performs an authenticated manual replay, checks metrics, and queries PostgreSQL for durable assertions. Because jitter is random, it polls bounded conditions instead of relying on exact timestamps.
