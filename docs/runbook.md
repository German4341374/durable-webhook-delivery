# Operations runbook

## Delivery backlog

Check readiness, worker logs, pending outbox rows, eligible deliveries, expired leases, and target-specific failures. Do not bulk-replay until the target owner confirms capacity and idempotency.

```sql
SELECT status, count(*) FROM deliveries GROUP BY status;
SELECT count(*) FROM outbox_events WHERE dispatched_at IS NULL;
SELECT channel_id, count(*) FROM webhook_events e JOIN deliveries d ON d.event_id=e.id WHERE d.status IN ('retry','dead_letter') GROUP BY channel_id;
```

## PostgreSQL outage

Ingress readiness becomes unavailable and uncommitted requests fail without HTTP 202. Restore PostgreSQL, confirm migrations and connection limits, then observe outbox and delivery queues drain. Do not delete leases manually; the reaper safely returns expired claims to retry.

## Target incident

Reduce the channel rate/concurrency through a reviewed database migration or stop the worker if the target requests a pause. Inspect attempt history without exposing payloads or headers. After recovery, replay only confirmed dead letters in controlled batches.

## Lost worker

Confirm the process is stopped, wait longer than `LEASE_DURATION`, and start a replacement. `delivering` rows with expired leases become `retry`. Expect possible duplicates for requests that reached the target before termination.

## Lost master key

Stop channel creation and delivery to avoid repeated decryption failures. Restore the exact key from the secret manager backup. If it is permanently lost, encrypted channel secrets are unrecoverable; obtain new secrets from channel owners and recreate channels through a controlled migration.

## Migration failure

Migrations run under one transaction and advisory lock. Restore the original applied SQL if a checksum changed; never edit migration history. Correct a failed new migration and rerun before starting API or worker processes.
