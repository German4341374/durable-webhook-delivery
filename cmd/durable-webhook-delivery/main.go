package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/German4341374/durable-webhook-delivery/internal/api"
	"github.com/German4341374/durable-webhook-delivery/internal/config"
	"github.com/German4341374/durable-webhook-delivery/internal/cryptoutil"
	"github.com/German4341374/durable-webhook-delivery/internal/database"
	"github.com/German4341374/durable-webhook-delivery/internal/delivery"
	"github.com/German4341374/durable-webhook-delivery/internal/metrics"
	"github.com/German4341374/durable-webhook-delivery/internal/security"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	if len(os.Args) < 2 {
		fatal(errors.New("usage: durable-webhook-delivery serve|worker|migrate|healthcheck"))
	}
	if os.Args[1] == "healthcheck" {
		if len(os.Args) != 3 {
			fatal(errors.New("healthcheck requires a URL"))
		}
		response, err := (&http.Client{Timeout: 2 * time.Second}).Get(os.Args[2])
		if err != nil {
			fatal(fmt.Errorf("health check request failed: %w", err))
		}
		if response.StatusCode != http.StatusOK {
			_ = response.Body.Close()
			fatal(fmt.Errorf("health check returned HTTP %d", response.StatusCode))
		}
		_ = response.Body.Close()
		return
	}
	configuration, err := config.Load()
	if err != nil {
		fatal(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	store, err := database.Open(ctx, configuration.DatabaseURL)
	if err != nil {
		fatal(fmt.Errorf("connect PostgreSQL: %w", err))
	}
	defer store.Close()
	if err := store.Migrate(ctx, env("MIGRATIONS_DIR", "migrations")); err != nil {
		fatal(fmt.Errorf("migrate database: %w", err))
	}
	if os.Args[1] == "migrate" {
		slog.Info("database migrations complete")
		return
	}
	cipher, err := cryptoutil.NewCipher(configuration.MasterKey)
	if err != nil {
		fatal(err)
	}
	values := &metrics.Metrics{}
	policy := security.TargetPolicy{AllowPrivate: configuration.AllowPrivateTargets}
	switch os.Args[1] {
	case "serve":
		handler := (&api.API{Store: store, Cipher: cipher, Policy: policy, Metrics: values,
			MaxBodyBytes: configuration.MaxBodyBytes, AdminToken: configuration.AdminToken}).Handler()
		serve(ctx, configuration.ListenAddress, handler)
	case "worker":
		worker, err := delivery.NewWorker(store, cipher, policy, values, configuration.WorkerConcurrency, configuration.PollInterval, configuration.LeaseDuration)
		if err != nil {
			fatal(err)
		}
		go serve(ctx, env("WORKER_HEALTH_ADDRESS", ":9090"), workerHandler(store, values))
		if err := worker.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			fatal(err)
		}
	default:
		fatal(fmt.Errorf("unknown command: %s", os.Args[1]))
	}
}

func serve(ctx context.Context, address string, handler http.Handler) {
	server := &http.Server{
		Addr: address, Handler: handler, ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout: 35 * time.Second, WriteTimeout: 35 * time.Second, IdleTimeout: 60 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdownContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownContext); err != nil {
			slog.Error("HTTP shutdown failed", "error", err)
		}
	}()
	slog.Info("HTTP server started", "address", address)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fatal(err)
	}
}

func workerHandler(store *database.Store, values *metrics.Metrics) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health/live", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("GET /health/ready", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), time.Second)
		defer cancel()
		if store.Pool.Ping(ctx) != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		values.WritePrometheus(w)
	})
	return mux
}

func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func fatal(err error) {
	slog.Error("fatal", "error", err)
	os.Exit(1)
}
