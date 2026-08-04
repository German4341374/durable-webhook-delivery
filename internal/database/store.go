package database

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/German4341374/durable-webhook-delivery/internal/domain"
	"github.com/German4341374/durable-webhook-delivery/internal/idgen"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Store struct {
	Pool *pgxpool.Pool
}

func Open(ctx context.Context, databaseURL string) (*Store, error) {
	configuration, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, err
	}
	configuration.MaxConns = 20
	configuration.MinConns = 2
	configuration.MaxConnLifetime = 30 * time.Minute
	pool, err := pgxpool.NewWithConfig(ctx, configuration)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return &Store{Pool: pool}, nil
}

func (s *Store) Close() { s.Pool.Close() }

func (s *Store) Migrate(ctx context.Context, directory string) error {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return err
	}
	files := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() && filepath.Ext(entry.Name()) == ".sql" {
			files = append(files, entry.Name())
		}
	}
	sort.Strings(files)
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", int64(7_240_917)); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version text PRIMARY KEY, checksum text NOT NULL, applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		return err
	}
	for _, name := range files {
		body, err := os.ReadFile(filepath.Join(directory, name))
		if err != nil {
			return err
		}
		digest := sha256.Sum256(body)
		checksum := hex.EncodeToString(digest[:])
		var existing string
		err = tx.QueryRow(ctx, "SELECT checksum FROM schema_migrations WHERE version=$1", name).Scan(&existing)
		if err == nil {
			if existing != checksum {
				return fmt.Errorf("applied migration changed: %s", name)
			}
			continue
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if _, err := tx.Exec(ctx, string(body)); err != nil {
			return fmt.Errorf("apply %s: %w", name, err)
		}
		if _, err := tx.Exec(ctx, "INSERT INTO schema_migrations(version, checksum) VALUES($1,$2)", name, checksum); err != nil {
			return err
		}
		slog.Info("migration applied", "version", name)
	}
	return tx.Commit(ctx)
}

func (s *Store) CreateChannel(ctx context.Context, channel domain.Channel) (domain.Channel, error) {
	err := s.Pool.QueryRow(ctx, `INSERT INTO channels
		(id,name,target_url,secret_ciphertext,secret_nonce,timeout_ms,max_attempts,rate_limit_per_second,max_concurrency)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)
		RETURNING created_at`, channel.ID, channel.Name, channel.TargetURL, channel.SecretCiphertext,
		channel.SecretNonce, channel.TimeoutMS, channel.MaxAttempts, channel.RateLimitPerSecond,
		channel.MaxConcurrency).Scan(&channel.CreatedAt)
	return channel, err
}

func (s *Store) ChannelByName(ctx context.Context, name string) (domain.Channel, error) {
	return scanChannel(s.Pool.QueryRow(ctx, `SELECT id,name,target_url,secret_ciphertext,secret_nonce,
		timeout_ms,max_attempts,rate_limit_per_second,max_concurrency,created_at FROM channels WHERE name=$1`, name))
}

func (s *Store) ListChannels(ctx context.Context) ([]domain.Channel, error) {
	rows, err := s.Pool.Query(ctx, `SELECT id,name,target_url,secret_ciphertext,secret_nonce,
		timeout_ms,max_attempts,rate_limit_per_second,max_concurrency,created_at FROM channels ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]domain.Channel, 0)
	for rows.Next() {
		channel, err := scanChannel(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, channel)
	}
	return result, rows.Err()
}

type scanner interface{ Scan(...any) error }

func scanChannel(row scanner) (domain.Channel, error) {
	var channel domain.Channel
	err := row.Scan(&channel.ID, &channel.Name, &channel.TargetURL, &channel.SecretCiphertext,
		&channel.SecretNonce, &channel.TimeoutMS, &channel.MaxAttempts, &channel.RateLimitPerSecond,
		&channel.MaxConcurrency, &channel.CreatedAt)
	return channel, err
}

func (s *Store) IngestEvent(ctx context.Context, channelID, idempotencyKey string, payload []byte, contentType string, headers map[string]string) (eventID string, duplicate bool, err error) {
	eventID, err = idgen.UUID()
	if err != nil {
		return "", false, err
	}
	digest := sha256.Sum256(payload)
	encodedHeaders, err := json.Marshal(headers)
	if err != nil {
		return "", false, err
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return "", false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	command, err := tx.Exec(ctx, `INSERT INTO webhook_events
		(id,channel_id,idempotency_key,payload,payload_hash,content_type,masked_headers)
		VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT(channel_id,idempotency_key) DO NOTHING`,
		eventID, channelID, idempotencyKey, payload, hex.EncodeToString(digest[:]), contentType, encodedHeaders)
	if err != nil {
		return "", false, err
	}
	if command.RowsAffected() == 0 {
		duplicate = true
		if err := tx.QueryRow(ctx, `SELECT id FROM webhook_events WHERE channel_id=$1 AND idempotency_key=$2`, channelID, idempotencyKey).Scan(&eventID); err != nil {
			return "", false, err
		}
	} else if _, err := tx.Exec(ctx, "INSERT INTO outbox_events(event_id) VALUES($1)", eventID); err != nil {
		return "", false, err
	}
	return eventID, duplicate, tx.Commit(ctx)
}

func (s *Store) DispatchOutbox(ctx context.Context, limit int) (int64, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	rows, err := tx.Query(ctx, `SELECT id,event_id FROM outbox_events
		WHERE dispatched_at IS NULL ORDER BY id FOR UPDATE SKIP LOCKED LIMIT $1`, limit)
	if err != nil {
		return 0, err
	}
	type entry struct {
		id      int64
		eventID string
	}
	entries := make([]entry, 0, limit)
	for rows.Next() {
		var value entry
		if err := rows.Scan(&value.id, &value.eventID); err != nil {
			rows.Close()
			return 0, err
		}
		entries = append(entries, value)
	}
	rows.Close()
	for _, value := range entries {
		deliveryID, err := idgen.UUID()
		if err != nil {
			return 0, err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO deliveries(id,event_id,status)
			VALUES($1,$2,'pending') ON CONFLICT(event_id) DO NOTHING`, deliveryID, value.eventID); err != nil {
			return 0, err
		}
		if _, err := tx.Exec(ctx, "UPDATE outbox_events SET dispatched_at=now() WHERE id=$1", value.id); err != nil {
			return 0, err
		}
	}
	return int64(len(entries)), tx.Commit(ctx)
}

func (s *Store) RequeueExpired(ctx context.Context) (int64, error) {
	command, err := s.Pool.Exec(ctx, `UPDATE deliveries SET status='retry', worker_id=NULL,
		lease_until=NULL, next_attempt_at=now(), last_error='worker lease expired', updated_at=now()
		WHERE status='delivering' AND lease_until < now()`)
	return command.RowsAffected(), err
}

func (s *Store) ClaimDelivery(ctx context.Context, workerID string, lease time.Duration) (domain.ClaimedDelivery, error) {
	row := s.Pool.QueryRow(ctx, `WITH candidate AS (
		SELECT d.id FROM deliveries d WHERE d.status IN ('pending','retry') AND d.next_attempt_at <= now()
		ORDER BY d.next_attempt_at,d.created_at FOR UPDATE SKIP LOCKED LIMIT 1
	), claimed AS (
		UPDATE deliveries d SET status='delivering',attempt_count=attempt_count+1,
		cycle_attempt_count=cycle_attempt_count+1,worker_id=$1,lease_until=now()+$2::interval,updated_at=now()
		FROM candidate WHERE d.id=candidate.id RETURNING d.*
	), attempt_started AS (
		INSERT INTO delivery_attempts(delivery_id,attempt_number,started_at)
		SELECT id,attempt_count,now() FROM claimed RETURNING delivery_id
	)
	SELECT c.id,c.event_id,e.channel_id,ch.name,ch.target_url,c.status,c.attempt_count,
		c.cycle_attempt_count,ch.max_attempts,c.next_attempt_at,c.response_status,c.last_error,
		c.delivered_at,c.replay_count,e.payload_hash,c.created_at,c.updated_at,e.payload,e.content_type,
		ch.secret_ciphertext,ch.secret_nonce,ch.timeout_ms,ch.rate_limit_per_second,ch.max_concurrency
	FROM claimed c JOIN attempt_started a ON a.delivery_id=c.id
	JOIN webhook_events e ON e.id=c.event_id JOIN channels ch ON ch.id=e.channel_id`,
		workerID, fmt.Sprintf("%f seconds", lease.Seconds()))
	var result domain.ClaimedDelivery
	var timeoutMS int
	err := row.Scan(&result.ID, &result.EventID, &result.ChannelID, &result.ChannelName,
		&result.TargetURL, &result.Status, &result.AttemptCount, &result.CycleAttempts,
		&result.MaxAttempts, &result.NextAttemptAt, &result.ResponseStatus, &result.LastError,
		&result.DeliveredAt, &result.ReplayCount, &result.PayloadHash, &result.CreatedAt,
		&result.UpdatedAt, &result.Payload, &result.ContentType, &result.SecretCiphertext,
		&result.SecretNonce, &timeoutMS, &result.RateLimit,
		&result.Concurrency)
	result.Timeout = time.Duration(timeoutMS) * time.Millisecond
	return result, err
}

func (s *Store) FinishAttempt(ctx context.Context, delivery domain.ClaimedDelivery, workerID string, result domain.AttemptResult) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	command, err := tx.Exec(ctx, `UPDATE delivery_attempts SET completed_at=now(),duration_ms=$1,
		response_status=$2,error_message=$3 WHERE delivery_id=$4 AND attempt_number=$5 AND completed_at IS NULL`,
		result.Duration.Milliseconds(), result.ResponseStatus, nullableString(result.Error), delivery.ID, delivery.AttemptCount)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return errors.New("delivery attempt was not open")
	}
	if result.Error == "" && result.ResponseStatus != nil && *result.ResponseStatus >= 200 && *result.ResponseStatus < 300 {
		command, err := tx.Exec(ctx, `UPDATE deliveries SET status='delivered',response_status=$1,
			last_error=NULL,delivered_at=now(),worker_id=NULL,lease_until=NULL,updated_at=now()
			WHERE id=$2 AND status='delivering' AND worker_id=$3`, result.ResponseStatus, delivery.ID, workerID)
		if err != nil {
			return fmt.Errorf("complete delivery lease mismatch: %w", err)
		}
		if command.RowsAffected() != 1 {
			return errors.New("complete delivery lease mismatch")
		}
	} else if result.DeadLetter {
		command, err := tx.Exec(ctx, `UPDATE deliveries SET status='dead_letter',response_status=$1,
			last_error=$2,worker_id=NULL,lease_until=NULL,updated_at=now()
			WHERE id=$3 AND status='delivering' AND worker_id=$4`, result.ResponseStatus, result.Error, delivery.ID, workerID)
		if err != nil {
			return fmt.Errorf("dead-letter delivery lease mismatch: %w", err)
		}
		if command.RowsAffected() != 1 {
			return errors.New("dead-letter delivery lease mismatch")
		}
		if _, err := tx.Exec(ctx, `INSERT INTO dead_letter_events(delivery_id,reason)
			VALUES($1,$2) ON CONFLICT(delivery_id) DO UPDATE SET reason=EXCLUDED.reason,
			created_at=now(),replayed_at=NULL`, delivery.ID, result.Error); err != nil {
			return err
		}
	} else {
		command, err := tx.Exec(ctx, `UPDATE deliveries SET status='retry',response_status=$1,
			last_error=$2,next_attempt_at=$3,worker_id=NULL,lease_until=NULL,updated_at=now()
			WHERE id=$4 AND status='delivering' AND worker_id=$5`, result.ResponseStatus, result.Error,
			result.RetryAt, delivery.ID, workerID)
		if err != nil {
			return fmt.Errorf("retry delivery lease mismatch: %w", err)
		}
		if command.RowsAffected() != 1 {
			return errors.New("retry delivery lease mismatch")
		}
	}
	return tx.Commit(ctx)
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func (s *Store) Replay(ctx context.Context, deliveryID string) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	command, err := tx.Exec(ctx, `UPDATE deliveries SET status='retry',cycle_attempt_count=0,
		next_attempt_at=now(),worker_id=NULL,lease_until=NULL,last_error=NULL,replay_count=replay_count+1,updated_at=now()
		WHERE id=$1 AND status='dead_letter'`, deliveryID)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return errors.New("delivery is not in dead-letter state")
	}
	if _, err := tx.Exec(ctx, "UPDATE dead_letter_events SET replayed_at=now() WHERE delivery_id=$1", deliveryID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) Delivery(ctx context.Context, id string) (domain.Delivery, error) {
	return scanDelivery(s.Pool.QueryRow(ctx, deliverySelect+" WHERE d.id=$1", id))
}

func (s *Store) Deliveries(ctx context.Context, limit, offset int) ([]domain.Delivery, error) {
	rows, err := s.Pool.Query(ctx, deliverySelect+" ORDER BY d.created_at DESC LIMIT $1 OFFSET $2", limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]domain.Delivery, 0)
	for rows.Next() {
		delivery, err := scanDelivery(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, delivery)
	}
	return result, rows.Err()
}

func (s *Store) Attempts(ctx context.Context, deliveryID string) ([]domain.DeliveryAttempt, error) {
	rows, err := s.Pool.Query(ctx, `SELECT attempt_number,started_at,completed_at,duration_ms,
		response_status,error_message FROM delivery_attempts WHERE delivery_id=$1 ORDER BY attempt_number`, deliveryID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]domain.DeliveryAttempt, 0)
	for rows.Next() {
		var attempt domain.DeliveryAttempt
		if err := rows.Scan(&attempt.AttemptNumber, &attempt.StartedAt, &attempt.CompletedAt,
			&attempt.DurationMS, &attempt.Response, &attempt.Error); err != nil {
			return nil, err
		}
		result = append(result, attempt)
	}
	return result, rows.Err()
}

const deliverySelect = `SELECT d.id,d.event_id,e.channel_id,ch.name,ch.target_url,d.status,
	d.attempt_count,d.cycle_attempt_count,ch.max_attempts,d.next_attempt_at,d.response_status,d.last_error,
	d.delivered_at,d.replay_count,e.payload_hash,d.created_at,d.updated_at
	FROM deliveries d JOIN webhook_events e ON e.id=d.event_id JOIN channels ch ON ch.id=e.channel_id`

func scanDelivery(row scanner) (domain.Delivery, error) {
	var delivery domain.Delivery
	err := row.Scan(&delivery.ID, &delivery.EventID, &delivery.ChannelID, &delivery.ChannelName,
		&delivery.TargetURL, &delivery.Status, &delivery.AttemptCount, &delivery.CycleAttempts,
		&delivery.MaxAttempts, &delivery.NextAttemptAt, &delivery.ResponseStatus, &delivery.LastError,
		&delivery.DeliveredAt, &delivery.ReplayCount, &delivery.PayloadHash, &delivery.CreatedAt,
		&delivery.UpdatedAt)
	return delivery, err
}
