package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/German4341374/durable-webhook-delivery/internal/cryptoutil"
	"github.com/German4341374/durable-webhook-delivery/internal/database"
	"github.com/German4341374/durable-webhook-delivery/internal/metrics"
	"github.com/German4341374/durable-webhook-delivery/internal/security"
)

func integrationAPI(t *testing.T, maxBody int64) (*API, http.Handler) {
	t.Helper()
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not configured")
	}
	store, err := database.Open(context.Background(), databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(context.Background(), filepath.Join("..", "..", "migrations")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Pool.Exec(context.Background(), "TRUNCATE channels CASCADE"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	cipher, _ := cryptoutil.NewCipher(make([]byte, 32))
	service := &API{Store: store, Cipher: cipher, Policy: security.TargetPolicy{AllowPrivate: true},
		Metrics: &metrics.Metrics{}, MaxBodyBytes: maxBody,
		AdminToken: "integration-admin-token-at-least-32-characters"}
	return service, service.Handler()
}

func createChannelViaAPI(t *testing.T, handler http.Handler, targetURL, secret string) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"name": "incoming", "target_url": targetURL, "secret": secret})
	request := httptest.NewRequest(http.MethodPost, "/api/channels", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Admin-Token", "integration-admin-token-at-least-32-characters")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("channel creation failed: %d %s", response.Code, response.Body.String())
	}
}

func TestWebhookHMACAndDuplicateHandling(t *testing.T) {
	service, handler := integrationAPI(t, 1024)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	defer target.Close()
	secret := "integration-development-secret"
	createChannelViaAPI(t, handler, target.URL, secret)
	payload := []byte(`{"incident":"INC-1"}`)
	send := func(signature string) map[string]any {
		request := httptest.NewRequest(http.MethodPost, "/webhooks/incoming", bytes.NewReader(payload))
		request.Header.Set("Idempotency-Key", "stable-event-key")
		request.Header.Set("X-Webhook-Signature", signature)
		request.Header.Set("Authorization", "Bearer must-not-persist")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusAccepted {
			t.Fatalf("webhook failed: %d %s", response.Code, response.Body.String())
		}
		var value map[string]any
		_ = json.Unmarshal(response.Body.Bytes(), &value)
		return value
	}
	signature := cryptoutil.Sign([]byte(secret), payload)
	first := send(signature)
	second := send(signature)
	if first["duplicate"] != false || second["duplicate"] != true || first["event_id"] != second["event_id"] {
		t.Fatalf("unexpected idempotency responses: first=%v second=%v", first, second)
	}
	var headers map[string]string
	if err := service.Store.Pool.QueryRow(context.Background(), "SELECT masked_headers FROM webhook_events").Scan(&headers); err != nil {
		t.Fatal(err)
	}
	if headers["Authorization"] != "[REDACTED]" {
		t.Fatalf("authorization header persisted without masking: %v", headers)
	}
}

func TestWebhookRejectsInvalidSignature(t *testing.T) {
	_, handler := integrationAPI(t, 1024)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer target.Close()
	createChannelViaAPI(t, handler, target.URL, "integration-development-secret")
	request := httptest.NewRequest(http.MethodPost, "/webhooks/incoming", bytes.NewReader([]byte("payload")))
	request.Header.Set("Idempotency-Key", "event-key")
	request.Header.Set("X-Webhook-Signature", "sha256=00")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("invalid HMAC accepted: %d", response.Code)
	}
}

func TestWebhookEnforcesBodyLimit(t *testing.T) {
	_, handler := integrationAPI(t, 16)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer target.Close()
	secret := "integration-development-secret"
	createChannelViaAPI(t, handler, target.URL, secret)
	payload, _ := base64.StdEncoding.DecodeString("QUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUE=")
	request := httptest.NewRequest(http.MethodPost, "/webhooks/incoming", bytes.NewReader(payload))
	request.Header.Set("Idempotency-Key", "large-event")
	request.Header.Set("X-Webhook-Signature", cryptoutil.Sign([]byte(secret), payload))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("large body accepted: %d %s", response.Code, response.Body.String())
	}
}
