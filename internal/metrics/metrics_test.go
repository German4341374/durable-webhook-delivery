package metrics

import (
	"bytes"
	"strings"
	"testing"
)

func TestPrometheusOutput(t *testing.T) {
	metrics := &Metrics{}
	metrics.Received.Add(2)
	metrics.Active.Add(1)
	var output bytes.Buffer
	metrics.WritePrometheus(&output)
	if !strings.Contains(output.String(), "webhook_received_total 2") || !strings.Contains(output.String(), "webhook_active_workers 1") {
		t.Fatalf("unexpected metrics: %s", output.String())
	}
}
