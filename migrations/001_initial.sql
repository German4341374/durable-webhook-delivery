CREATE TABLE IF NOT EXISTS schema_migrations (
    version text PRIMARY KEY,
    checksum text NOT NULL,
    applied_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE channels (
    id uuid PRIMARY KEY,
    name text NOT NULL UNIQUE CHECK (length(name) BETWEEN 1 AND 100),
    target_url text NOT NULL,
    secret_ciphertext bytea NOT NULL,
    secret_nonce bytea NOT NULL,
    timeout_ms integer NOT NULL CHECK (timeout_ms BETWEEN 100 AND 30000),
    max_attempts integer NOT NULL CHECK (max_attempts BETWEEN 1 AND 20),
    rate_limit_per_second integer NOT NULL CHECK (rate_limit_per_second BETWEEN 1 AND 1000),
    max_concurrency integer NOT NULL CHECK (max_concurrency BETWEEN 1 AND 100),
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE webhook_events (
    id uuid PRIMARY KEY,
    channel_id uuid NOT NULL REFERENCES channels(id),
    idempotency_key text NOT NULL CHECK (length(idempotency_key) BETWEEN 1 AND 200),
    payload bytea NOT NULL,
    payload_hash text NOT NULL,
    content_type text NOT NULL,
    masked_headers jsonb NOT NULL,
    received_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (channel_id, idempotency_key)
);

CREATE TABLE outbox_events (
    id bigserial PRIMARY KEY,
    event_id uuid NOT NULL UNIQUE REFERENCES webhook_events(id),
    created_at timestamptz NOT NULL DEFAULT now(),
    dispatched_at timestamptz
);

CREATE INDEX outbox_pending_idx ON outbox_events (id) WHERE dispatched_at IS NULL;

CREATE TABLE deliveries (
    id uuid PRIMARY KEY,
    event_id uuid NOT NULL UNIQUE REFERENCES webhook_events(id),
    status text NOT NULL CHECK (status IN ('pending', 'delivering', 'retry', 'delivered', 'dead_letter')),
    attempt_count integer NOT NULL DEFAULT 0,
    cycle_attempt_count integer NOT NULL DEFAULT 0,
    next_attempt_at timestamptz NOT NULL DEFAULT now(),
    lease_until timestamptz,
    worker_id text,
    response_status integer,
    last_error text,
    delivered_at timestamptz,
    replay_count integer NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX deliveries_claim_idx ON deliveries (next_attempt_at, created_at)
WHERE status IN ('pending', 'retry');
CREATE INDEX deliveries_lease_idx ON deliveries (lease_until)
WHERE status = 'delivering';

CREATE TABLE delivery_attempts (
    id bigserial PRIMARY KEY,
    delivery_id uuid NOT NULL REFERENCES deliveries(id),
    attempt_number integer NOT NULL,
    started_at timestamptz NOT NULL,
    completed_at timestamptz,
    duration_ms bigint CHECK (duration_ms IS NULL OR duration_ms >= 0),
    response_status integer,
    error_message text,
    UNIQUE (delivery_id, attempt_number)
);

CREATE TABLE dead_letter_events (
    delivery_id uuid PRIMARY KEY REFERENCES deliveries(id),
    reason text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    replayed_at timestamptz
);

CREATE INDEX delivery_attempts_history_idx ON delivery_attempts (delivery_id, attempt_number DESC);
