package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"backend/internal/config"
	"backend/internal/deposits"
	"backend/internal/models"
	"backend/internal/moex"
	"backend/internal/schema"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/redis/go-redis/v9"
)

var (
	// ErrDBNotReady is returned when the database has not been initialized yet.
	ErrDBNotReady = errors.New("database not initialized")
	// ErrNoDeposits guards the table swap: publishing an empty result set would
	// silently wipe the live table.
	ErrNoDeposits = errors.New("refusing to publish an empty deposit set")
	// ErrShuttingDown is returned when work is submitted during shutdown.
	ErrShuttingDown = errors.New("backend is shutting down")
)

// DepositsResponse represents the structure returned by the API.
type DepositsResponse struct {
	GroupedTable []struct {
		models.Deposit
		DepositResultRows []models.Deposit `json:"deposit_result_rows"`
	} `json:"grouped_table"`
}

// Backend is the main application struct that manages HTTP fetching and database I/O.
type Backend struct {
	cfg            config.Config
	httpClient     *http.Client
	depositsClient *deposits.Client[DepositsResponse]
	moexClient     *moex.Client
	rdb            *redis.Client

	db   atomic.Pointer[sql.DB]
	dbUp atomic.Bool

	// saveMu serializes writers. Readers do not take it: they read through
	// atomic.Pointer and Postgres handles the visibility of the table swap.
	saveMu sync.Mutex

	// initMu guards InitDB against concurrent/repeated calls and protects
	// stopWatch, which Close reads.
	initMu    sync.Mutex
	stopWatch context.CancelFunc
	watchWG   sync.WaitGroup

	// jobsMu makes "is it still legal to add a background job?" and
	// "wg.Add" a single atomic step, so Add can never race with Wait.
	jobsMu  sync.Mutex
	closing bool
	jobs    sync.WaitGroup

	closeOnce sync.Once
	closeErr  error
}

// NewBackend creates a new Backend with default configuration.
func NewBackend() *Backend {
	cfg := config.DefaultConfig()

	rdb := redis.NewClient(&redis.Options{
		Addr: cfg.RedisAddress,
	})

	depClient := deposits.New(deposits.Config[DepositsResponse]{
		BaseURL:      cfg.DepositsAPIURL,
		Interval:     3 * time.Second, // 20/min
		Burst:        1,
		MaxQueueWait: 2 * time.Second,
		MaxRetries:   3,
		CacheTTL:     45 * time.Second,
		StaleTTL:     5 * time.Minute,
		RedisClient:  rdb,
	})

	moexClient := moex.New(moex.Config{
		BaseURL:      cfg.MoexBaseURL,
		Interval:     500 * time.Millisecond, // 6 req/s — conservative for ISS
		Burst:        3,
		MaxQueueWait: 2 * time.Second,
		MaxRetries:   3,
		CacheTTL:     30 * time.Second,
		StaleTTL:     5 * time.Minute,
		RedisClient:  rdb,
	})

	return &Backend{
		cfg: cfg,
		httpClient: &http.Client{
			Timeout: 60 * time.Second,
		},
		depositsClient: depClient,
		moexClient:     moexClient,
		rdb:            rdb,
	}
}

// database returns the live handle or an error. Never dereference b.db blindly:
// InitDB may have failed or never run.
func (b *Backend) database() (*sql.DB, error) {
	db := b.db.Load()
	if db == nil {
		return nil, ErrDBNotReady
	}
	return db, nil
}

// addJob reserves a slot in the background job group, or refuses if Close has
// already started draining.
func (b *Backend) addJob() error {
	b.jobsMu.Lock()
	defer b.jobsMu.Unlock()
	if b.closing {
		return ErrShuttingDown
	}
	b.jobs.Add(1)
	return nil
}

// Close shuts the backend down in dependency order and is safe to call twice.
func (b *Backend) Close() error {
	b.closeOnce.Do(func() {
		// 1. Stop accepting new background work.
		b.jobsMu.Lock()
		b.closing = true
		b.jobsMu.Unlock()

		// 2. Stop the health watcher and wait for its in-flight ping, so it
		//    cannot touch the handle we are about to close.
		b.initMu.Lock()
		stop := b.stopWatch
		b.initMu.Unlock()
		if stop != nil {
			stop()
		}
		b.watchWG.Wait()

		// 3. Drain in-flight saves.
		b.jobs.Wait()

		if b.depositsClient != nil {
			b.depositsClient.Close()
		}
		if b.moexClient != nil {
			b.moexClient.Close()
		}
		if db := b.db.Load(); db != nil {
			if err := db.Close(); err != nil {
				log.Printf("failed to close db: %s", err)
				b.closeErr = err
			}
		}
		if b.rdb != nil {
			if err := b.rdb.Close(); err != nil {
				log.Printf("failed to close redis: %s", err)
				if b.closeErr == nil {
					b.closeErr = err
				}
			}
		}
	})
	return b.closeErr
}

// Shutdown waits for all background goroutines to finish and then closes the
// database connection.
func (b *Backend) Shutdown() {
	if err := b.Close(); err != nil {
		log.Printf("shutdown: %v", err)
	}
}

// InitDB opens (or creates) the PostgreSQL database. It is not re-entrant.
func (b *Backend) InitDB(ctx context.Context) error {
	b.initMu.Lock()
	defer b.initMu.Unlock()
	if b.db.Load() != nil {
		return errors.New("InitDB: already initialized")
	}

	cfg, err := pgx.ParseConfig(b.cfg.DatabaseDSN) // handles both DSN formats
	if err != nil {
		return fmt.Errorf("parse dsn: %w", err)
	}
	setDefault(cfg.RuntimeParams, "statement_timeout", "5000")
	setDefault(cfg.RuntimeParams, "lock_timeout", "3000")
	setDefault(cfg.RuntimeParams, "idle_in_transaction_session_timeout", "10000")
	setDefault(cfg.RuntimeParams, "application_name", appName(b.cfg.ServiceName))

	db := stdlib.OpenDB(*cfg)
	db.SetMaxOpenConns(b.cfg.DBMaxOpenConns)
	db.SetMaxIdleConns(b.cfg.DBMaxOpenConns)
	db.SetConnMaxLifetime(b.cfg.DBConnMaxLifetime)
	db.SetConnMaxIdleTime(b.cfg.DBConnMaxIdleTime)

	if err := pingWithBackoff(ctx, db, b.cfg.DBStartupTimeout); err != nil {
		db.Close()
		return fmt.Errorf("database unreachable: %w", err)
	}

	if err := migrate(ctx, db); err != nil {
		db.Close()
		return fmt.Errorf("migrate: %w", err)
	}

	b.db.Store(db)
	b.dbUp.Store(true)

	// lifetime ctx, NOT the startup ctx
	watchCtx, stop := context.WithCancel(context.Background())
	b.stopWatch = stop
	b.watchWG.Add(1)
	go func() {
		defer b.watchWG.Done()
		b.watchDB(watchCtx)
	}()
	return nil
}

func setDefault(m map[string]string, k, v string) {
	if _, ok := m[k]; !ok {
		m[k] = v
	}
}

func hostname() string {
	h, err := os.Hostname()
	if err != nil || h == "" {
		return "unknown"
	}
	return h
}

// appName builds application_name, which Postgres truncates at NAMEDATALEN-1.
func appName(service string) string {
	name := service + "/" + hostname()
	if len(name) > 63 {
		name = name[:63]
	}
	return name
}

const migrationLockID = 1234567890

func migrate(ctx context.Context, db *sql.DB) error {
	// A transaction gives us SET LOCAL and pg_advisory_xact_lock: both are
	// unwound automatically, so no session state (and no lock) can leak back
	// into the pool on a failure path.
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	// DDL needs room the DSN limits do not give it.
	if _, err := tx.ExecContext(ctx, `SET LOCAL statement_timeout = 0`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `SET LOCAL lock_timeout = '30s'`); err != nil {
		return err
	}

	if err := acquireMigrationLock(ctx, tx); err != nil {
		return err
	}

	if _, err := tx.ExecContext(ctx, schema.CreateStatsTableSQL("daily_stats")); err != nil {
		return err
	}
	return tx.Commit()
}

// acquireMigrationLock polls the non-blocking variant: pg_advisory_xact_lock
// ignores lock_timeout and would hang forever behind a stuck peer, whereas this
// loop gives up when ctx does.
func acquireMigrationLock(ctx context.Context, tx *sql.Tx) error {
	t := time.NewTicker(500 * time.Millisecond)
	defer t.Stop()

	for {
		var got bool
		if err := tx.QueryRowContext(ctx, `SELECT pg_try_advisory_xact_lock($1)`, migrationLockID).Scan(&got); err != nil {
			return fmt.Errorf("acquire lock: %w", err)
		}
		if got {
			return nil
		}
		select {
		case <-t.C:
		case <-ctx.Done():
			return fmt.Errorf("acquire lock: %w", ctx.Err())
		}
	}
}

func pingWithBackoff(ctx context.Context, db *sql.DB, budget time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()

	backoff := 100 * time.Millisecond
	for attempt := 1; ; attempt++ {
		pctx, pcancel := context.WithTimeout(ctx, 3*time.Second)
		err := db.PingContext(pctx)
		pcancel()
		if err == nil {
			return nil
		}
		log.Printf("db ping attempt %d: %v", attempt, err)

		jitter := time.Duration(rand.Int63n(int64(backoff/2) + 1))
		select {
		case <-time.After(backoff + jitter):
		case <-ctx.Done():
			return fmt.Errorf("gave up after %d attempts: %w", attempt, err)
		}
		if backoff < 5*time.Second {
			backoff *= 2
		}
	}
}

const downStrikes = 3

func (b *Backend) watchDB(ctx context.Context) {
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	fails := 0

	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			db := b.db.Load()
			if db == nil {
				continue
			}
			// generous: must exceed pool wait, or saturation reads as outage
			c, cancel := context.WithTimeout(ctx, 3*time.Second)
			err := db.PingContext(c)
			cancel()

			if err == nil {
				if fails >= downStrikes {
					log.Printf("db recovered")
				}
				fails = 0
				b.dbUp.Store(true)
				continue
			}
			if ctx.Err() != nil { // shutting down, not an outage
				return
			}
			fails++
			log.Printf("db ping failed (%d/%d): %v", fails, downStrikes, err)
			if fails >= downStrikes {
				b.dbUp.Store(false)
			}
		}
	}
}

func (b *Backend) Live(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK) // never ping here
}

func (b *Backend) Ready(w http.ResponseWriter, r *http.Request) {
	if !b.dbUp.Load() {
		http.Error(w, "db down", http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (b *Backend) Metrics(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	db := b.db.Load()
	if db == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"initialized":false}`))
		return
	}
	if err := json.NewEncoder(w).Encode(struct {
		Initialized bool `json:"initialized"`
		Up          bool `json:"up"`
		sql.DBStats
	}{true, b.dbUp.Load(), db.Stats()}); err != nil {
		log.Printf("metrics: encode failed: %v", err)
	}
}

// saveToDatabase persists deposits using a staging table swap for atomicity.
// The parameter is deliberately not named "deposits": that would shadow the
// imported package of the same name.

type CategoryStat struct {
	Date          string  `json:"date"`
	Category      string  `json:"category"`
	AverageAPR    float64 `json:"average_apr"`
	AverageIncome float64 `json:"average_income"`
}

func (b *Backend) SaveStats(ctx context.Context, stats []CategoryStat) error {
	db, err := b.database()
	if err != nil {
		return err
	}
	for _, s := range stats {
		_, err = db.ExecContext(ctx, schema.InsertStatSQL("daily_stats"), s.Date, s.Category, s.AverageAPR, s.AverageIncome)
		if err != nil {
			return err
		}
	}
	return nil
}

func (b *Backend) GetStats(ctx context.Context) ([]CategoryStat, error) {
	db, err := b.database()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, schema.GetLatestStatsSQL("daily_stats"))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var stats []CategoryStat
	for rows.Next() {
		var s CategoryStat
		var t time.Time
		if err := rows.Scan(&t, &s.Category, &s.AverageAPR, &s.AverageIncome); err != nil {
			return nil, err
		}
		s.Date = t.Format("2006-01-02")
		stats = append(stats, s)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return stats, nil
}

