package main

import (
	"context"
	"database/sql"
	"net/http"
	"testing"
	"time"

	"backend/internal/config"
	"backend/internal/deposits"
	"backend/internal/models"
	"backend/internal/schema"
	"backend/internal/testutil"

	_ "github.com/lib/pq"
	"github.com/redis/go-redis/v9"
)

// newTestBackend creates a Backend connected to a local Postgres and Redis.
func newTestBackend(t *testing.T) *Backend {
	t.Helper()

	cfg := config.DefaultConfig()

	db, err := sql.Open("postgres", cfg.DatabaseDSN)
	if err != nil {
		t.Fatalf("failed to open db: %v", err)
	}
	if err := db.Ping(); err != nil {
		t.Skipf("skipping test, postgres not available at %s: %v", cfg.DatabaseDSN, err)
	}

	rdb := redis.NewClient(&redis.Options{
		Addr: cfg.RedisAddress,
	})
	if err := rdb.Ping(context.Background()).Err(); err != nil {
		t.Skipf("skipping test, redis not available at %s: %v", cfg.RedisAddress, err)
	}

	app := &Backend{
		httpClient: &http.Client{Timeout: 5 * time.Second},
		rdb:        rdb,
		cfg:        cfg,
	}
	app.db.Store(db)
	app.dbUp.Store(true)
	app.depositsClient = deposits.New(deposits.Config[DepositsResponse]{
		BaseURL:     app.cfg.DepositsAPIURL,
		RedisClient: rdb,
	})

	if _, err := db.Exec("DROP TABLE IF EXISTS deposits"); err != nil {
		t.Fatalf("failed to drop table: %v", err)
	}
	if _, err := db.Exec(schema.CreateDepositsTableSQL("deposits")); err != nil {
		t.Fatalf("failed to create table: %v", err)
	}

	t.Cleanup(func() {
		db.Close()
		rdb.Close()
	})

	return app
}

func sampleDeposit(id int, url string) models.Deposit {
	return models.Deposit{
		ID:                          id,
		ProductURL:                  url,
		RateMin:                     18.0,
		RateMax:                     22.5,
		AmountFrom:                  10000,
		AmountTo:                    testutil.Ptr(5000000),
		PeriodFrom:                  30,
		PeriodTo:                    testutil.Ptr(365),
		ProductName:                 "Тест Продукт",
		BankName:                    "Тест Банк",
		DepositName:                 "Тест Вклад",
		IsSavingAccount:             false,
		IsChildrenDeposit:           false,
		IsPensionDeposit:            false,
		FeatureList:                 map[string]bool{"capitalization": true},
		SpecialRestrictions:         testutil.Ptr("Только для резидентов"),
		SpecialTypeListNames:        []string{"premium"},
		EfficientRate:               23.1,
		CapitalizationPeriods:       map[string]string{"monthly": "22.5%"},
		IsPartialWithdrawalPossible: true,
		IsReplenishmentPossible:     true,
		IsProlongationPossible:      testutil.Ptr(true),
		ProlongationMax:             testutil.Ptr(3),
		ProlongationComment:         testutil.Ptr("До 3 раз"),
		ProlongationCommentHtml:     testutil.Ptr("<p>До 3 раз</p>"),
		IsInOfficeOpeningPossible:   true,
		RatesExtremum: models.RatesExtremum{
			MinRate:   18.0,
			MaxRate:   22.5,
			MinAmount: 10000,
			MaxAmount: testutil.Ptr(5000000),
			MinPeriod: 30,
			MaxPeriod: testutil.Ptr(365),
			RatesTable: []models.RatesTable{
				{Rate: 22.5, FromNotation: "от 100 000₽", ToNotation: "до 1 000 000₽"},
			},
		},
		DetailedConditions:           testutil.Ptr("Подробные условия"),
		IsRateIncreasePossible:       true,
		RateIncreaseCommentHtml:      testutil.Ptr("<b>Повышение</b>"),
		ReplenishmentCommentHtml:     testutil.Ptr("<b>Пополнение</b>"),
		EarlyTerminationCommentHtml:  testutil.Ptr("<b>Досрочное</b>"),
		PaymentCommentHtml:           testutil.Ptr("<b>Выплата</b>"),
		CapitalizationCommentHtml:    testutil.Ptr("<b>Капитализация</b>"),
		PartialWithdrawalCommentHtml: testutil.Ptr("<b>Снятие</b>"),
		RateCommentHtml:              testutil.Ptr("<b>Ставка</b>"),
		PercentCalculation:           testutil.Ptr("365/365"),
		IsNewClient:                  false,
		NewClientComment:             nil,
		IsNewMoney:                   false,
		NewMoneyComment:              nil,
		IsKeyRateLinked:              false,
		KeyRateLinkedComment:         nil,
	}
}

// --- InitDB tests ---

// --- FetchDeposits integration test ---







// --- Shutdown tests ---

func TestShutdown_NilDB(t *testing.T) {
	app := &Backend{
		httpClient: &http.Client{},
	}
	// Should not panic when db is nil
	app.Shutdown()
}

func TestShutdown_WithDB(t *testing.T) {
	db, err := sql.Open("postgres", "postgres://invalid:invalid@localhost:5432/invalid?sslmode=disable")
	if err != nil {
		t.Fatalf("failed to open db: %v", err)
	}

	app := &Backend{
		httpClient: &http.Client{},
	}
	app.db.Store(db)
	// Should close cleanly
	app.Shutdown()

	// Verify DB is closed by trying to ping
	if err := db.Ping(); err == nil {
		t.Error("db should be closed after Shutdown")
	}
}

// --- NewBackend test ---

func TestNewBackend(t *testing.T) {
	app := NewBackend()
	if app == nil {
		t.Fatal("NewBackend returned nil")
	}
	if app.httpClient == nil {
		t.Error("httpClient should not be nil")
	}
	if app.httpClient.Timeout != 60*time.Second {
		t.Errorf("httpClient timeout: got %v, want 60s", app.httpClient.Timeout)
	}
}
