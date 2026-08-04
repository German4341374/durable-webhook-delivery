package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"
)

type Config struct {
	DatabaseURL         string
	MasterKey           []byte
	AdminToken          string
	ListenAddress       string
	WorkerConcurrency   int
	PollInterval        time.Duration
	LeaseDuration       time.Duration
	AllowPrivateTargets bool
	MaxBodyBytes        int64
}

func Load() (Config, error) {
	masterKey, err := base64.StdEncoding.DecodeString(os.Getenv("MASTER_KEY_BASE64"))
	if err != nil || len(masterKey) != 32 {
		return Config{}, errors.New("MASTER_KEY_BASE64 must be base64 for exactly 32 bytes")
	}
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		return Config{}, errors.New("DATABASE_URL is required")
	}
	adminToken := os.Getenv("ADMIN_TOKEN")
	if len(adminToken) < 32 {
		return Config{}, errors.New("ADMIN_TOKEN must contain at least 32 characters")
	}
	workerConcurrency, err := envInt("WORKER_CONCURRENCY", 8, 1, 128)
	if err != nil {
		return Config{}, err
	}
	maxBodyBytes, err := envInt("MAX_BODY_BYTES", 1_048_576, 1_024, 10_485_760)
	if err != nil {
		return Config{}, err
	}
	return Config{
		DatabaseURL:         databaseURL,
		MasterKey:           masterKey,
		AdminToken:          adminToken,
		ListenAddress:       envString("LISTEN_ADDRESS", ":8080"),
		WorkerConcurrency:   workerConcurrency,
		PollInterval:        envDuration("POLL_INTERVAL", 500*time.Millisecond),
		LeaseDuration:       envDuration("LEASE_DURATION", 30*time.Second),
		AllowPrivateTargets: os.Getenv("ALLOW_PRIVATE_TARGETS") == "true",
		MaxBodyBytes:        int64(maxBodyBytes),
	}, nil
}

func envString(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func envInt(name string, fallback, minimum, maximum int) (int, error) {
	value := os.Getenv(name)
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < minimum || parsed > maximum {
		return 0, fmt.Errorf("%s must be an integer from %d to %d", name, minimum, maximum)
	}
	return parsed, nil
}

func envDuration(name string, fallback time.Duration) time.Duration {
	value := os.Getenv(name)
	if value == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(value)
	if err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
}
