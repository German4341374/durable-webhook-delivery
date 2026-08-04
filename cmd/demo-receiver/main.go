package main

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"
)

type receiver struct {
	mu      sync.Mutex
	seen    map[string]int
	flaky   map[string]int
	records []map[string]any
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	state := &receiver{seen: make(map[string]int), flaky: make(map[string]int)}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("GET /history", state.history)
	mux.HandleFunc("POST /success", state.success)
	mux.HandleFunc("POST /fail", state.fail)
	mux.HandleFunc("POST /timeout", state.timeout)
	mux.HandleFunc("POST /flaky", state.flakyResponse)
	server := &http.Server{Addr: ":8081", Handler: mux, ReadHeaderTimeout: 3 * time.Second}
	slog.Info("demo receiver started", "address", server.Addr)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		slog.Error("receiver stopped", "error", err)
		os.Exit(1)
	}
}

func (r *receiver) record(request *http.Request, status int) {
	id := request.Header.Get("X-Webhook-Event-ID")
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seen[id]++
	r.records = append(r.records, map[string]any{
		"event_id": id, "delivery_id": request.Header.Get("X-Webhook-Delivery-ID"),
		"count": r.seen[id], "status": status, "received_at": time.Now().UTC(),
	})
}

func (r *receiver) success(w http.ResponseWriter, request *http.Request) {
	r.record(request, http.StatusNoContent)
	w.WriteHeader(http.StatusNoContent)
}

func (r *receiver) fail(w http.ResponseWriter, request *http.Request) {
	r.record(request, http.StatusInternalServerError)
	http.Error(w, "controlled failure", http.StatusInternalServerError)
}

func (r *receiver) timeout(w http.ResponseWriter, request *http.Request) {
	duration, _ := strconv.Atoi(request.URL.Query().Get("seconds"))
	if duration < 1 || duration > 60 {
		duration = 10
	}
	time.Sleep(time.Duration(duration) * time.Second)
	r.record(request, http.StatusNoContent)
	w.WriteHeader(http.StatusNoContent)
}

func (r *receiver) flakyResponse(w http.ResponseWriter, request *http.Request) {
	id := request.Header.Get("X-Webhook-Event-ID")
	r.mu.Lock()
	r.flaky[id]++
	attempt := r.flaky[id]
	r.mu.Unlock()
	if attempt <= 2 {
		r.record(request, http.StatusInternalServerError)
		http.Error(w, "temporary controlled failure", http.StatusInternalServerError)
		return
	}
	r.success(w, request)
}

func (r *receiver) history(w http.ResponseWriter, _ *http.Request) {
	r.mu.Lock()
	defer r.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"items": r.records})
}
