package delivery

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/German4341374/durable-webhook-delivery/internal/cryptoutil"
	"github.com/German4341374/durable-webhook-delivery/internal/domain"
	"github.com/German4341374/durable-webhook-delivery/internal/security"
)

func TestBackoffStaysInsideExponentialWindow(t *testing.T) {
	for attempt := 1; attempt <= 8; attempt++ {
		value := Backoff(attempt, time.Second)
		maximum := time.Second * time.Duration(1<<(attempt-1))
		if value < 0 || value > maximum {
			t.Fatalf("attempt %d produced %s above %s", attempt, value, maximum)
		}
	}
}

func TestBackoffIsCapped(t *testing.T) {
	if value := Backoff(100, time.Second); value > time.Hour {
		t.Fatalf("backoff exceeded cap: %s", value)
	}
}

func TestTargetControllerLimitsConcurrency(t *testing.T) {
	controller := NewTargetController()
	release, err := controller.Acquire(context.Background(), "https://example.invalid/hook", 1000, 1)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := controller.Acquire(ctx, "https://example.invalid/other", 1000, 1); err == nil {
		t.Fatal("second concurrent permit unexpectedly acquired")
	}
	release()
}

func TestWorkerSendSuccessAndOutboundSignature(t *testing.T) {
	secret := []byte("long-development-secret")
	var signatureOK atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if cryptoutil.VerifySignature(secret, []byte(`{"ok":true}`), r.Header.Get("X-Webhook-Signature")) {
			signatureOK.Store(true)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	worker, claimed := testWorkerAndDelivery(t, server.URL, secret)
	result := worker.send(context.Background(), claimed)
	if result.Error != "" || result.ResponseStatus == nil || *result.ResponseStatus != http.StatusNoContent || !signatureOK.Load() {
		t.Fatalf("unexpected result: %+v signature=%v", result, signatureOK.Load())
	}
}

func TestWorkerRetriesServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "fail", http.StatusInternalServerError)
	}))
	defer server.Close()
	worker, claimed := testWorkerAndDelivery(t, server.URL, []byte("long-development-secret"))
	result := worker.send(context.Background(), claimed)
	if result.Error == "" || result.DeadLetter {
		t.Fatalf("expected retryable error: %+v", result)
	}
}

func TestWorkerDeadLettersPermanentClientError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "bad", http.StatusBadRequest)
	}))
	defer server.Close()
	worker, claimed := testWorkerAndDelivery(t, server.URL, []byte("long-development-secret"))
	result := worker.send(context.Background(), claimed)
	if !result.DeadLetter {
		t.Fatalf("expected permanent dead letter: %+v", result)
	}
}

func TestWorkerTimesOut(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(100 * time.Millisecond)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	worker, claimed := testWorkerAndDelivery(t, server.URL, []byte("long-development-secret"))
	claimed.Timeout = 10 * time.Millisecond
	result := worker.send(context.Background(), claimed)
	if result.Error == "" || result.DeadLetter {
		t.Fatalf("expected retryable timeout: %+v", result)
	}
}

func testWorkerAndDelivery(t *testing.T, target string, secret []byte) (*Worker, domain.ClaimedDelivery) {
	t.Helper()
	cipher, _ := cryptoutil.NewCipher(make([]byte, 32))
	ciphertext, nonce, err := cipher.Encrypt(secret, "channel-id")
	if err != nil {
		t.Fatal(err)
	}
	worker := &Worker{Cipher: cipher, Policy: security.TargetPolicy{AllowPrivate: true}, Controller: NewTargetController()}
	claimed := domain.ClaimedDelivery{
		Delivery: domain.Delivery{ID: "delivery-id", EventID: "event-id", ChannelID: "channel-id", TargetURL: target, CycleAttempts: 1, MaxAttempts: 3},
		Payload:  []byte(`{"ok":true}`), ContentType: "application/json", SecretCiphertext: ciphertext,
		SecretNonce: nonce, Timeout: time.Second, RateLimit: 100, Concurrency: 2,
	}
	return worker, claimed
}
