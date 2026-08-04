package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/German4341374/durable-webhook-delivery/internal/metrics"
)

func TestLivenessDoesNotDependOnDatabase(t *testing.T) {
	handler := (&API{Metrics: &metrics.Metrics{}}).Handler()
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/health/live", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "alive") {
		t.Fatalf("unexpected liveness response: %d %s", response.Code, response.Body.String())
	}
}

func TestManagementEndpointRequiresAdminToken(t *testing.T) {
	handler := (&API{Metrics: &metrics.Metrics{}, AdminToken: strings.Repeat("a", 32)}).Handler()
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/channels", nil))
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("management endpoint was public: %d", response.Code)
	}
}

func TestMetricsEndpoint(t *testing.T) {
	values := &metrics.Metrics{}
	values.Received.Add(3)
	handler := (&API{Metrics: values}).Handler()
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "webhook_received_total 3") {
		t.Fatalf("unexpected metrics response: %d %s", response.Code, response.Body.String())
	}
}
