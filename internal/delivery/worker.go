package delivery

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/German4341374/durable-webhook-delivery/internal/cryptoutil"
	"github.com/German4341374/durable-webhook-delivery/internal/database"
	"github.com/German4341374/durable-webhook-delivery/internal/domain"
	"github.com/German4341374/durable-webhook-delivery/internal/idgen"
	"github.com/German4341374/durable-webhook-delivery/internal/metrics"
	"github.com/German4341374/durable-webhook-delivery/internal/security"
	"github.com/jackc/pgx/v5"
)

type Worker struct {
	Store        *database.Store
	Cipher       *cryptoutil.Cipher
	Policy       security.TargetPolicy
	Metrics      *metrics.Metrics
	Controller   *TargetController
	Concurrency  int
	PollInterval time.Duration
	Lease        time.Duration
	ID           string
}

func NewWorker(store *database.Store, cipher *cryptoutil.Cipher, policy security.TargetPolicy, values *metrics.Metrics, concurrency int, poll, lease time.Duration) (*Worker, error) {
	id, err := idgen.UUID()
	if err != nil {
		return nil, err
	}
	return &Worker{
		Store: store, Cipher: cipher, Policy: policy, Metrics: values,
		Controller: NewTargetController(), Concurrency: concurrency,
		PollInterval: poll, Lease: lease, ID: id,
	}, nil
}

func (w *Worker) Run(ctx context.Context) error {
	go w.dispatchOutbox(ctx)
	go w.recoverLeases(ctx)
	errorsChannel := make(chan error, w.Concurrency)
	for index := range w.Concurrency {
		go func(slot int) { errorsChannel <- w.deliveryLoop(ctx, fmt.Sprintf("%s-%d", w.ID, slot)) }(index)
	}
	for range w.Concurrency {
		if err := <-errorsChannel; err != nil && !errors.Is(err, context.Canceled) {
			return err
		}
	}
	return nil
}

func (w *Worker) dispatchOutbox(ctx context.Context) {
	ticker := time.NewTicker(w.PollInterval)
	defer ticker.Stop()
	for {
		if count, err := w.Store.DispatchOutbox(ctx, 100); err != nil && !errors.Is(err, context.Canceled) {
			slog.Error("outbox dispatch failed", "error", err)
		} else if count > 0 {
			slog.Info("outbox dispatched", "count", count)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (w *Worker) recoverLeases(ctx context.Context) {
	ticker := time.NewTicker(w.Lease / 2)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			count, err := w.Store.RequeueExpired(ctx)
			if err != nil {
				slog.Error("lease recovery failed", "error", err)
			} else if count > 0 {
				slog.Warn("expired worker leases recovered", "count", count)
			}
		}
	}
}

func (w *Worker) deliveryLoop(ctx context.Context, workerID string) error {
	for {
		delivery, err := w.Store.ClaimDelivery(ctx, workerID, w.Lease)
		if errors.Is(err, pgx.ErrNoRows) {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(w.PollInterval):
				continue
			}
		}
		if err != nil {
			slog.Error("claim delivery failed", "worker_id", workerID, "error", err)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(2 * w.PollInterval):
				continue
			}
		}
		w.Metrics.Active.Add(1)
		result := w.send(ctx, delivery)
		w.Metrics.Active.Add(-1)
		if err := w.Store.FinishAttempt(ctx, delivery, workerID, result); err != nil {
			slog.Error("persist delivery result failed", "delivery_id", delivery.ID, "error", err)
			continue
		}
		w.recordMetrics(result)
	}
}

func (w *Worker) send(parent context.Context, delivery domain.ClaimedDelivery) domain.AttemptResult {
	started := time.Now()
	result := domain.AttemptResult{}
	secret, err := w.Cipher.Decrypt(delivery.SecretCiphertext, delivery.SecretNonce, delivery.ChannelID)
	if err != nil {
		result.Error = "decrypt channel secret: authentication failed"
		result.DeadLetter = true
		result.Duration = time.Since(started)
		return result
	}
	ctx, cancel := context.WithTimeout(parent, delivery.Timeout)
	defer cancel()
	release, err := w.Controller.Acquire(ctx, delivery.TargetURL, delivery.RateLimit, delivery.Concurrency)
	if err != nil {
		return w.failure(delivery, fmt.Errorf("target limit: %w", err), nil, time.Since(started), false)
	}
	defer release()
	if _, err := w.Policy.ValidateURL(ctx, delivery.TargetURL); err != nil {
		return w.failure(delivery, fmt.Errorf("target rejected: %w", err), nil, time.Since(started), true)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, delivery.TargetURL, bytes.NewReader(delivery.Payload))
	if err != nil {
		return w.failure(delivery, err, nil, time.Since(started), true)
	}
	request.Header.Set("Content-Type", delivery.ContentType)
	request.Header.Set("User-Agent", "durable-webhook-delivery/1.0")
	request.Header.Set("X-Webhook-Signature", cryptoutil.Sign(secret, delivery.Payload))
	request.Header.Set("X-Webhook-Event-ID", delivery.EventID)
	request.Header.Set("X-Webhook-Delivery-ID", delivery.ID)
	response, err := w.Policy.Client(delivery.Timeout).Do(request)
	if err != nil {
		return w.failure(delivery, err, nil, time.Since(started), false)
	}
	defer response.Body.Close()
	status := response.StatusCode
	if status >= 200 && status < 300 {
		return domain.AttemptResult{ResponseStatus: &status, Duration: time.Since(started)}
	}
	retryable := status == http.StatusRequestTimeout || status == http.StatusTooManyRequests || status >= 500
	return w.failure(delivery, fmt.Errorf("target returned HTTP %d", status), &status, time.Since(started), !retryable)
}

func (w *Worker) failure(delivery domain.ClaimedDelivery, err error, status *int, duration time.Duration, permanent bool) domain.AttemptResult {
	dead := permanent || delivery.CycleAttempts >= delivery.MaxAttempts
	return domain.AttemptResult{
		ResponseStatus: status,
		Error:          err.Error(),
		Duration:       duration,
		RetryAt:        time.Now().Add(Backoff(delivery.CycleAttempts, time.Second)),
		DeadLetter:     dead,
	}
}

func (w *Worker) recordMetrics(result domain.AttemptResult) {
	if result.Error == "" {
		w.Metrics.Delivered.Add(1)
		return
	}
	w.Metrics.Failed.Add(1)
	if result.DeadLetter {
		w.Metrics.DeadLetter.Add(1)
	} else {
		w.Metrics.Retries.Add(1)
	}
}
