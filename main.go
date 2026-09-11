package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"

	"backend/internal/config"

	"github.com/redis/go-redis/v9"
)

const (
	defaultPort     = "8080"
	initTimeout     = 2 * time.Minute
	fetchTimeout    = 10 * time.Minute
	shutdownTimeout = 15 * time.Second
)

func main() {
	// All cleanup lives in run(); log.Fatal here runs after run's defers.
	if err := run(); err != nil {
		log.Fatalf("fatal: %v", err)
	}
	log.Println("shutdown complete")
}

func run() error {
	// Installed before anything blocks, so a signal during startup is caught.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	backend := NewBackend()
	// Runs last: after srv.Shutdown has drained every handler.
	defer backend.Shutdown()

	// Bounded: migrate() can sit waiting on the advisory lock behind a peer.
	initCtx, cancelInit := context.WithTimeout(ctx, initTimeout)
	defer cancelInit()
	if err := backend.InitDB(initCtx); err != nil {
		return fmt.Errorf("initialize database: %w", err)
	}

	// Daily scheduler: fetches deposits + savings from banki.ru, stores
	// top-30 per period group in Redis. Runs immediately, then every 24 h.
	go startScheduler(ctx, backend)

	srv := &http.Server{
		Addr:    ":" + port(),
		Handler: routes(backend),
		// Without these a single idle socket can pin a connection forever.
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	srvErr := make(chan error, 1)
	go func() {
		log.Printf("server listening on %s", srv.Addr)
		err := srv.ListenAndServe()
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		srvErr <- err
	}()

	select {
	case err := <-srvErr:
		if err != nil {
			return fmt.Errorf("server failed: %w", err)
		}
		return nil
	case <-ctx.Done():
		stop() // restore default handling: a second Ctrl-C kills immediately
		log.Println("shutdown signal received, draining connections...")
	}

	// Detached from ctx: ctx is already cancelled by the signal.
	shutCtx, cancelShut := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancelShut()
	if err := srv.Shutdown(shutCtx); err != nil {
		log.Printf("http server shutdown error: %v", err)
		_ = srv.Close()
	}
	// Blocking here is what makes the deferred backend.Shutdown safe: no
	// handler is still holding the database when it closes.
	return <-srvErr
}

func routes(b *Backend) http.Handler {
	// Own mux, not DefaultServeMux: nothing gets registered behind our back.
	mux := http.NewServeMux()
	mux.HandleFunc("/live", b.Live)
	mux.HandleFunc("/ready", b.Ready)
	mux.HandleFunc("/metrics", b.Metrics)
	mux.HandleFunc("/fetch", fetchHandler(b))
	mux.HandleFunc("/bonds", bondsHandler(b.rdb, b.cfg))
	mux.HandleFunc("/query", queryHandler(b.rdb, b.cfg))
	mux.HandleFunc("/stats", statsHandler(b))
	return mux
}

func fetchHandler(b *Backend) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"message": "fetch endpoint deprecated, scheduler handles stats"})
	}
}


func bondsHandler(rdb *redis.Client, cfg config.Config) http.HandlerFunc {
	// Pre-build the set of valid board labels for filtering.
	validLabels := make(map[string]config.BondBoard, len(cfg.BondBoards))
	for _, bb := range cfg.BondBoards {
		validLabels[bb.Label] = bb
	}

	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		board := r.URL.Query().Get("board")

		type bondGroup struct {
			redisKey string
			jsonKey  string
		}

		var groups []bondGroup
		switch {
		case board == "" || board == "all":
			for _, bb := range cfg.BondBoards {
				groups = append(groups, bondGroup{bb.RedisKey, bb.Label})
			}
		default:
			bb, ok := validLabels[board]
			if !ok {
				labels := make([]string, 0, len(validLabels))
				for l := range validLabels {
					labels = append(labels, "'"+l+"'")
				}
				sort.Strings(labels)
				http.Error(w, "board must be "+strings.Join(labels, ", ")+", or 'all'", http.StatusBadRequest)
				return
			}
			groups = []bondGroup{{bb.RedisKey, bb.Label}}
		}

		result := make(map[string]json.RawMessage, len(groups))
		for _, g := range groups {
			data, err := rdb.Get(r.Context(), g.redisKey).Bytes()
			if err != nil {
				result[g.jsonKey] = []byte("[]")
				continue
			}
			result[g.jsonKey] = data
		}
		writeJSON(w, http.StatusOK, result)
	}
}

// writeDBError maps a storage error to a status code and keeps the details in
// the log rather than in the response body.
func writeDBError(w http.ResponseWriter, r *http.Request, err error, op string) {
	switch {
	case r.Context().Err() != nil:
		// Client hung up; the response is going nowhere.
		log.Printf("%s: client gone: %v", op, err)
	case errors.Is(err, sql.ErrNoRows):
		http.Error(w, "not found", http.StatusNotFound)
	case errors.Is(err, ErrDBNotReady):
		log.Printf("%s: %v", op, err)
		http.Error(w, "service unavailable", http.StatusServiceUnavailable)
	default:
		log.Printf("%s: %v", op, err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
	}
}

// writeJSON marshals first, then writes the header: a streaming encode that
// fails halfway has already committed a 200 and a truncated body. Headers must
// also be set before WriteHeader or they are silently dropped.
func writeJSON(w http.ResponseWriter, status int, v any) {
	buf, err := json.Marshal(v)
	if err != nil {
		log.Printf("failed to marshal JSON response: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	if _, err := w.Write(buf); err != nil {
		log.Printf("failed to write JSON response: %v", err)
	}
}

func port() string {
	if p := os.Getenv("PORT"); p != "" {
		return p
	}
	return defaultPort
}

func statsHandler(b *Backend) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		stats, err := b.GetStats(r.Context())
		if err != nil {
			writeDBError(w, r, err, "get stats")
			return
		}
		if stats == nil {
			stats = []CategoryStat{}
		}
		writeJSON(w, http.StatusOK, stats)
	}
}
