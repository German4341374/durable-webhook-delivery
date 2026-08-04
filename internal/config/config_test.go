package config

import (
	"encoding/base64"
	"strings"
	"testing"
)

func validEnvironment(t *testing.T) {
	t.Helper()
	t.Setenv("DATABASE_URL", "postgresql://local.invalid/webhooks")
	t.Setenv("MASTER_KEY_BASE64", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	t.Setenv("ADMIN_TOKEN", strings.Repeat("a", 32))
}

func TestLoadValidConfiguration(t *testing.T) {
	validEnvironment(t)
	configuration, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if configuration.WorkerConcurrency != 8 || configuration.MaxBodyBytes != 1_048_576 {
		t.Fatalf("unexpected defaults: %+v", configuration)
	}
}

func TestLoadRejectsShortMasterKey(t *testing.T) {
	validEnvironment(t)
	t.Setenv("MASTER_KEY_BASE64", base64.StdEncoding.EncodeToString([]byte("short")))
	if _, err := Load(); err == nil {
		t.Fatal("short key accepted")
	}
}

func TestLoadRejectsShortAdminToken(t *testing.T) {
	validEnvironment(t)
	t.Setenv("ADMIN_TOKEN", "short")
	if _, err := Load(); err == nil {
		t.Fatal("short admin token accepted")
	}
}

func TestLoadValidatesWorkerConcurrency(t *testing.T) {
	validEnvironment(t)
	t.Setenv("WORKER_CONCURRENCY", "1000")
	if _, err := Load(); err == nil {
		t.Fatal("invalid concurrency accepted")
	}
}
