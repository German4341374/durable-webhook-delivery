package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/German4341374/durable-webhook-delivery/internal/cryptoutil"
	"github.com/German4341374/durable-webhook-delivery/internal/database"
	"github.com/German4341374/durable-webhook-delivery/internal/domain"
	"github.com/German4341374/durable-webhook-delivery/internal/idgen"
	"github.com/German4341374/durable-webhook-delivery/internal/metrics"
	"github.com/German4341374/durable-webhook-delivery/internal/security"
	"github.com/jackc/pgx/v5"
)

type API struct {
	Store        *database.Store
	Cipher       *cryptoutil.Cipher
	Policy       security.TargetPolicy
	Metrics      *metrics.Metrics
	MaxBodyBytes int64
	AdminToken   string
}

func (a *API) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health/live", a.live)
	mux.HandleFunc("GET /health/ready", a.ready)
	mux.HandleFunc("GET /metrics", a.prometheus)
	mux.HandleFunc("POST /api/channels", a.createChannel)
	mux.HandleFunc("GET /api/channels", a.listChannels)
	mux.HandleFunc("POST /webhooks/{channel}", a.receiveWebhook)
	mux.HandleFunc("GET /api/deliveries", a.listDeliveries)
	mux.HandleFunc("GET /api/deliveries/{id}", a.getDelivery)
	mux.HandleFunc("POST /api/deliveries/{id}/replay", a.replay)
	return a.requestMiddleware(mux)
}

type createChannelRequest struct {
	Name               string `json:"name"`
	TargetURL          string `json:"target_url"`
	Secret             string `json:"secret"`
	TimeoutMS          int    `json:"timeout_ms"`
	MaxAttempts        int    `json:"max_attempts"`
	RateLimitPerSecond int    `json:"rate_limit_per_second"`
	MaxConcurrency     int    `json:"max_concurrency"`
}

func (a *API) createChannel(w http.ResponseWriter, r *http.Request) {
	if !a.requireAdmin(w, r) {
		return
	}
	var request createChannelRequest
	if err := decodeJSON(w, r, &request, 32*1024); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	request.Name = strings.TrimSpace(request.Name)
	if request.Name == "" || len(request.Name) > 100 || len(request.Secret) < 16 || len(request.Secret) > 512 {
		writeError(w, http.StatusBadRequest, "invalid_channel", "name and a 16-512 byte secret are required")
		return
	}
	if request.TimeoutMS == 0 {
		request.TimeoutMS = 5000
	}
	if request.MaxAttempts == 0 {
		request.MaxAttempts = 5
	}
	if request.RateLimitPerSecond == 0 {
		request.RateLimitPerSecond = 10
	}
	if request.MaxConcurrency == 0 {
		request.MaxConcurrency = 2
	}
	if request.TimeoutMS < 100 || request.TimeoutMS > 30000 || request.MaxAttempts < 1 || request.MaxAttempts > 20 || request.RateLimitPerSecond < 1 || request.RateLimitPerSecond > 1000 || request.MaxConcurrency < 1 || request.MaxConcurrency > 100 {
		writeError(w, http.StatusBadRequest, "invalid_channel", "channel limits are outside supported ranges")
		return
	}
	parsed, err := a.Policy.ValidateURL(r.Context(), request.TargetURL)
	if err != nil {
		writeError(w, http.StatusBadRequest, "unsafe_target", err.Error())
		return
	}
	id, err := idgen.UUID()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "could not create channel")
		return
	}
	ciphertext, nonce, err := a.Cipher.Encrypt([]byte(request.Secret), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "could not protect channel secret")
		return
	}
	channel, err := a.Store.CreateChannel(r.Context(), domain.Channel{
		ID: id, Name: request.Name, TargetURL: parsed.String(), TimeoutMS: request.TimeoutMS,
		MaxAttempts: request.MaxAttempts, RateLimitPerSecond: request.RateLimitPerSecond,
		MaxConcurrency: request.MaxConcurrency, SecretCiphertext: ciphertext, SecretNonce: nonce,
	})
	if err != nil {
		writeError(w, http.StatusConflict, "channel_conflict", "channel name already exists or values are invalid")
		return
	}
	writeJSON(w, http.StatusCreated, channel)
}

func (a *API) listChannels(w http.ResponseWriter, r *http.Request) {
	if !a.requireAdmin(w, r) {
		return
	}
	channels, err := a.Store.ListChannels(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "could not list channels")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": channels})
}

func (a *API) receiveWebhook(w http.ResponseWriter, r *http.Request) {
	channel, err := a.Store.ChannelByName(r.Context(), r.PathValue("channel"))
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "channel_not_found", "channel does not exist")
		return
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "could not load channel")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, a.MaxBodyBytes)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, "payload_too_large", fmt.Sprintf("body limit is %d bytes", a.MaxBodyBytes))
		return
	}
	secret, err := a.Cipher.Decrypt(channel.SecretCiphertext, channel.SecretNonce, channel.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "secret_unavailable", "channel secret could not be decrypted")
		return
	}
	if !cryptoutil.VerifySignature(secret, body, r.Header.Get("X-Webhook-Signature")) {
		writeError(w, http.StatusUnauthorized, "invalid_signature", "HMAC signature is invalid")
		return
	}
	idempotencyKey := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if idempotencyKey == "" || len(idempotencyKey) > 200 {
		writeError(w, http.StatusBadRequest, "invalid_idempotency_key", "Idempotency-Key is required and limited to 200 characters")
		return
	}
	eventID, duplicate, err := a.Store.IngestEvent(r.Context(), channel.ID, idempotencyKey, body,
		r.Header.Get("Content-Type"), security.MaskHeaders(r.Header))
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "ingestion_failed", "event and outbox entry were not committed")
		return
	}
	a.Metrics.Received.Add(1)
	if duplicate {
		a.Metrics.Duplicates.Add(1)
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"event_id": eventID, "duplicate": duplicate})
}

func (a *API) listDeliveries(w http.ResponseWriter, r *http.Request) {
	if !a.requireAdmin(w, r) {
		return
	}
	limit := boundedQueryInt(r, "limit", 50, 1, 200)
	offset := boundedQueryInt(r, "offset", 0, 0, 1_000_000)
	items, err := a.Store.Deliveries(r.Context(), limit, offset)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "could not list deliveries")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "limit": limit, "offset": offset})
}

func (a *API) getDelivery(w http.ResponseWriter, r *http.Request) {
	if !a.requireAdmin(w, r) {
		return
	}
	delivery, err := a.Store.Delivery(r.Context(), r.PathValue("id"))
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "delivery_not_found", "delivery does not exist")
		return
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "could not load delivery")
		return
	}
	attempts, err := a.Store.Attempts(r.Context(), delivery.ID)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "could not load history")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"delivery": delivery, "attempts": attempts})
}

func (a *API) replay(w http.ResponseWriter, r *http.Request) {
	if !a.requireAdmin(w, r) {
		return
	}
	if err := a.Store.Replay(r.Context(), r.PathValue("id")); err != nil {
		writeError(w, http.StatusConflict, "replay_rejected", err.Error())
		return
	}
	a.Metrics.Replays.Add(1)
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "scheduled"})
}

func (a *API) requireAdmin(w http.ResponseWriter, r *http.Request) bool {
	provided := []byte(r.Header.Get("X-Admin-Token"))
	expected := []byte(a.AdminToken)
	if len(provided) != len(expected) || !cryptoutil.ConstantTimeEqual(provided, expected) {
		writeError(w, http.StatusUnauthorized, "admin_authentication_required", "valid X-Admin-Token is required")
		return false
	}
	return true
}

func (a *API) live(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "alive"})
}

func (a *API) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), time.Second)
	defer cancel()
	if err := a.Store.Pool.Ping(ctx); err != nil {
		writeError(w, http.StatusServiceUnavailable, "not_ready", "PostgreSQL is unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

func (a *API) prometheus(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	a.Metrics.WritePrometheus(w)
}

func (a *API) requestMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID := r.Header.Get("X-Request-ID")
		if requestID == "" || len(requestID) > 100 {
			digest := sha256.Sum256([]byte(fmt.Sprintf("%d-%s", time.Now().UnixNano(), r.RemoteAddr)))
			requestID = hex.EncodeToString(digest[:8])
		}
		w.Header().Set("X-Request-ID", requestID)
		started := time.Now()
		next.ServeHTTP(w, r)
		slog.Info("http request", "request_id", requestID, "method", r.Method,
			"path", r.URL.Path, "duration_ms", time.Since(started).Milliseconds(),
			"headers", security.MaskHeaders(r.Header))
	})
}

func decodeJSON(w http.ResponseWriter, r *http.Request, target any, limit int64) error {
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return errors.New("request must contain one JSON object")
	}
	return nil
}

func boundedQueryInt(r *http.Request, name string, fallback, minimum, maximum int) int {
	value, err := strconv.Atoi(r.URL.Query().Get(name))
	if err != nil || value < minimum || value > maximum {
		return fallback
	}
	return value
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}
