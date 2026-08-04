package metrics

import (
	"fmt"
	"io"
	"sync/atomic"
)

type Metrics struct {
	Received   atomic.Uint64
	Duplicates atomic.Uint64
	Delivered  atomic.Uint64
	Failed     atomic.Uint64
	Retries    atomic.Uint64
	DeadLetter atomic.Uint64
	Replays    atomic.Uint64
	Active     atomic.Int64
}

func (m *Metrics) WritePrometheus(w io.Writer) {
	values := []struct {
		name  string
		type_ string
		value uint64
	}{
		{"webhook_received_total", "counter", m.Received.Load()},
		{"webhook_duplicate_total", "counter", m.Duplicates.Load()},
		{"webhook_delivered_total", "counter", m.Delivered.Load()},
		{"webhook_failed_attempt_total", "counter", m.Failed.Load()},
		{"webhook_retry_scheduled_total", "counter", m.Retries.Load()},
		{"webhook_dead_letter_total", "counter", m.DeadLetter.Load()},
		{"webhook_manual_replay_total", "counter", m.Replays.Load()},
	}
	for _, metric := range values {
		_, _ = fmt.Fprintf(w, "# TYPE %s %s\n%s %d\n", metric.name, metric.type_, metric.name, metric.value)
	}
	_, _ = fmt.Fprintf(w, "# TYPE webhook_active_workers gauge\nwebhook_active_workers %d\n", m.Active.Load())
}
