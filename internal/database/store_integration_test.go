package database

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/German4341374/durable-webhook-delivery/internal/domain"
	"github.com/German4341374/durable-webhook-delivery/internal/idgen"
)

func integrationStore(t *testing.T) *Store {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL is not configured")
	}
	store, err := Open(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	migrations := filepath.Join("..", "..", "migrations")
	if err := store.Migrate(context.Background(), migrations); err != nil {
		store.Close()
		t.Fatal(err)
	}
	if _, err := store.Pool.Exec(context.Background(), "TRUNCATE channels CASCADE"); err != nil {
		store.Close()
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	return store
}

func createTestChannel(t *testing.T, store *Store, maxAttempts int) domain.Channel {
	t.Helper()
	id, _ := idgen.UUID()
	channel, err := store.CreateChannel(context.Background(), domain.Channel{
		ID: id, Name: "integration", TargetURL: "http://receiver:8081/success",
		SecretCiphertext: []byte("ciphertext"), SecretNonce: []byte("nonce"), TimeoutMS: 1000,
		MaxAttempts: maxAttempts, RateLimitPerSecond: 10, MaxConcurrency: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	return channel
}

func TestTransactionalOutboxAndIdempotency(t *testing.T) {
	store := integrationStore(t)
	channel := createTestChannel(t, store, 3)
	eventID, duplicate, err := store.IngestEvent(context.Background(), channel.ID, "event-key", []byte(`{"event":1}`), "application/json", map[string]string{"Authorization": "[REDACTED]"})
	if err != nil || duplicate {
		t.Fatalf("first ingest failed: id=%s duplicate=%v error=%v", eventID, duplicate, err)
	}
	duplicateID, duplicate, err := store.IngestEvent(context.Background(), channel.ID, "event-key", []byte(`{"event":2}`), "application/json", nil)
	if err != nil || !duplicate || duplicateID != eventID {
		t.Fatalf("duplicate result: id=%s duplicate=%v error=%v", duplicateID, duplicate, err)
	}
	var events, outbox int
	if err := store.Pool.QueryRow(context.Background(), "SELECT count(*) FROM webhook_events").Scan(&events); err != nil {
		t.Fatal(err)
	}
	if err := store.Pool.QueryRow(context.Background(), "SELECT count(*) FROM outbox_events").Scan(&outbox); err != nil {
		t.Fatal(err)
	}
	if events != 1 || outbox != 1 {
		t.Fatalf("event/outbox transaction mismatch: %d/%d", events, outbox)
	}
	if dispatched, err := store.DispatchOutbox(context.Background(), 10); err != nil || dispatched != 1 {
		t.Fatalf("dispatch result: %d %v", dispatched, err)
	}
	if dispatched, err := store.DispatchOutbox(context.Background(), 10); err != nil || dispatched != 0 {
		t.Fatalf("outbox was not idempotent: %d %v", dispatched, err)
	}
}

func TestDeadLetterReplayAndLeaseRecovery(t *testing.T) {
	store := integrationStore(t)
	channel := createTestChannel(t, store, 1)
	_, _, err := store.IngestEvent(context.Background(), channel.ID, "dead-letter-key", []byte("payload"), "text/plain", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.DispatchOutbox(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	claimed, err := store.ClaimDelivery(context.Background(), "worker-one", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	status := 500
	if err := store.FinishAttempt(context.Background(), claimed, "worker-one", domain.AttemptResult{
		ResponseStatus: &status, Error: "controlled HTTP 500", Duration: time.Millisecond,
		DeadLetter: true,
	}); err != nil {
		t.Fatal(err)
	}
	delivery, err := store.Delivery(context.Background(), claimed.ID)
	if err != nil || delivery.Status != "dead_letter" {
		t.Fatalf("delivery not dead-lettered: %+v %v", delivery, err)
	}
	attempts, err := store.Attempts(context.Background(), claimed.ID)
	if err != nil || len(attempts) != 1 || attempts[0].Response == nil || *attempts[0].Response != 500 {
		t.Fatalf("attempt history missing: %+v %v", attempts, err)
	}
	if err := store.Replay(context.Background(), claimed.ID); err != nil {
		t.Fatal(err)
	}
	reclaimed, err := store.ClaimDelivery(context.Background(), "worker-two", time.Millisecond)
	if err != nil || reclaimed.CycleAttempts != 1 || reclaimed.AttemptCount != 2 {
		t.Fatalf("replay counters incorrect: %+v %v", reclaimed, err)
	}
	time.Sleep(5 * time.Millisecond)
	count, err := store.RequeueExpired(context.Background())
	if err != nil || count != 1 {
		t.Fatalf("expired lease not recovered: %d %v", count, err)
	}
	recovered, err := store.Delivery(context.Background(), claimed.ID)
	if err != nil || recovered.Status != "retry" {
		t.Fatalf("delivery was not requeued: %+v %v", recovered, err)
	}
}

func TestReplayRejectsNonDeadLetter(t *testing.T) {
	store := integrationStore(t)
	channel := createTestChannel(t, store, 3)
	_, _, _ = store.IngestEvent(context.Background(), channel.ID, "pending-key", []byte("payload"), "text/plain", nil)
	_, _ = store.DispatchOutbox(context.Background(), 10)
	deliveries, err := store.Deliveries(context.Background(), 10, 0)
	if err != nil || len(deliveries) != 1 {
		t.Fatalf("missing delivery: %+v %v", deliveries, err)
	}
	if err := store.Replay(context.Background(), deliveries[0].ID); err == nil {
		t.Fatal("pending delivery replay was accepted")
	}
}
