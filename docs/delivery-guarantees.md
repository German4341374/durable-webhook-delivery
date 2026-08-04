# Delivery guarantees

## Acceptance boundary

The ingress transaction inserts `webhook_events` and `outbox_events` together. The API sends HTTP 202 only after commit. A rollback leaves neither row. The outbox dispatcher later creates one delivery per event using a unique constraint and marks the outbox row dispatched in the same transaction.

## At-least-once semantics

A worker atomically changes one eligible delivery to `delivering`, increments its counters, and assigns a time-limited lease. It does not hold a database transaction during the network request. Success is durable only after a second transaction stores the attempt and marks the delivery `delivered`.

The interval between target success and the result commit is the duplicate window. A process or database failure there causes lease recovery and a repeated HTTP request. Exactly-once delivery cannot be guaranteed because PostgreSQL and an arbitrary HTTP target do not share a transaction protocol.

Every target request includes stable `X-Webhook-Event-ID` and `X-Webhook-Delivery-ID`. Consumers should make the event ID unique in their own transaction before applying business effects.

## Duplicate ingress

`(channel_id, idempotency_key)` is unique. A repeated key returns the original event ID and `duplicate: true`; a different payload with the same key is still treated as the same event. Producers must not reuse keys for different business events.

## Replay

Only dead-letter deliveries can be manually replayed. Replay resets the per-cycle attempt count and preserves total attempts, history, payload hash, event ID, delivery ID, and dead-letter audit row. This can intentionally create another target request and therefore still requires consumer idempotency.
