package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"backend/internal/config"
	"backend/internal/models"
	"backend/internal/moex"

	"github.com/redis/go-redis/v9"
)

const bankiBaseURL = "https://www.banki.ru"

// ProductResult is a single entry in the query response JSON.
type ProductResult struct {
	Type       string  `json:"type"`
	APR        float64 `json:"apr"`
	Earnings   float64 `json:"earnings"`
	AmountFrom int     `json:"amount_from"`
	AmountTo   *int    `json:"amount_to"`
	Link       string  `json:"link"`
}

// BondResult is a single bond entry in the query response JSON.
type BondResult struct {
	Type            string   `json:"type"`
	SECID           string   `json:"secid"`
	ShortName       string   `json:"short_name"`
	YieldToMaturity *float64 `json:"yield_to_maturity"`
	CouponPercent   float64  `json:"coupon_percent"`
	CouponValue     float64  `json:"coupon_value"`
	FaceValue       float64  `json:"face_value"`
	Last            *float64 `json:"last"`
	MatDate         string   `json:"mat_date"`
	Earnings        float64  `json:"earnings"`
	Link            string   `json:"link"`
}

// processQuery loads cached products from Redis, checks which ones accept the
// given sum, and returns up to count matching products per category. Positions
// that could not be filled are nil (JSON null).
func processQuery(ctx context.Context, rdb *redis.Client, sum, count int, bondBoards []config.BondBoard) (map[string]any, error) {
	result := make(map[string]any, len(periods)*2+len(bondBoards))

	categories := []struct {
		prefix   string
		prodType string
	}{
		{"deposits", "deposit"},
		{"savings", "saving_account"},
	}

	for _, cat := range categories {
		for _, period := range periods {
			redisKey := cat.prefix + ":" + period
			jsonKey := cat.prefix + "_" + period

			products, err := loadProducts(ctx, rdb, redisKey)
			if err != nil {
				// Data not in Redis yet — return null-filled slots.
				log.Printf("query: %s not available: %v", redisKey, err)
				result[jsonKey] = make([]*ProductResult, count)
				continue
			}

			slots := make([]*ProductResult, count)
			found := 0

			for i := range products {
				if found >= count {
					break
				}
				if !fitsSum(products[i], sum) {
					continue
				}

				days := periodDays[period]
				earnings := float64(sum) * (products[i].EfficientRate / 100.0) * (float64(days) / 365.0)
				earnings = math.Round(earnings*100) / 100

				slots[found] = &ProductResult{
					Type:       cat.prodType,
					APR:        products[i].EfficientRate,
					Earnings:   earnings,
					AmountFrom: products[i].AmountFrom,
					AmountTo:   products[i].AmountTo,
					Link:       bankiBaseURL + products[i].ProductURL,
				}
				found++
			}

			result[jsonKey] = slots
		}
	}

	// --- Bonds ---
	for _, bb := range bondBoards {
		jsonKey := "bonds_" + bb.Label
		bonds, err := loadBonds(ctx, rdb, bb.RedisKey)
		if err != nil {
			log.Printf("query: %s not available: %v", bb.RedisKey, err)
			result[jsonKey] = make([]*BondResult, count)
			continue
		}

		slots := make([]*BondResult, count)
		found := 0

		for i := range bonds {
			if found >= count {
				break
			}
			b := bonds[i]

			// How many bonds can you buy with this sum?
			// Price = (Last% / 100) × FaceValue + AccruedInt
			price := bondPrice(b)
			if price <= 0 {
				continue
			}
			numBonds := math.Floor(float64(sum) / price)

			// Annual coupon income for these bonds, scaled to 1 year.
			var annualIncome float64
			if b.CouponPeriod > 0 {
				couponsPerYear := 365.0 / float64(b.CouponPeriod)
				annualIncome = numBonds * b.CouponValue * couponsPerYear
			}
			annualIncome = math.Round(annualIncome*100) / 100

			slots[found] = &BondResult{
				Type:            bb.Label,
				SECID:           b.SECID,
				ShortName:       b.ShortName,
				YieldToMaturity: b.YieldToMaturity,
				CouponPercent:   b.CouponPercent,
				CouponValue:     b.CouponValue,
				FaceValue:       b.FaceValue,
				Last:            b.Last,
				MatDate:         b.MatDate,
				Earnings:        annualIncome,
				Link:            moex.MoexBondURL + b.SECID,
			}
			found++
		}

		result[jsonKey] = slots
	}

	return result, nil
}

// bondPrice returns the full price (clean + accrued) of one bond in ₽.
func bondPrice(b models.Bond) float64 {
	if b.Last == nil || *b.Last <= 0 {
		return 0
	}
	return (*b.Last / 100.0) * b.FaceValue + b.AccruedInt
}

// fitsSum checks whether the product can be opened for the given sum.
func fitsSum(d models.Deposit, sum int) bool {
	if sum < d.AmountFrom {
		return false
	}
	if d.AmountTo != nil && sum > *d.AmountTo {
		return false
	}
	return true
}

// loadProducts deserializes a cached product list from Redis.
func loadProducts(ctx context.Context, rdb *redis.Client, key string) ([]models.Deposit, error) {
	data, err := rdb.Get(ctx, key).Bytes()
	if err != nil {
		return nil, err
	}
	var items []models.Deposit
	if err := json.Unmarshal(data, &items); err != nil {
		return nil, fmt.Errorf("unmarshal %s: %w", key, err)
	}
	return items, nil
}

// loadBonds deserializes a cached bond list from Redis.
func loadBonds(ctx context.Context, rdb *redis.Client, key string) ([]models.Bond, error) {
	data, err := rdb.Get(ctx, key).Bytes()
	if err != nil {
		return nil, err
	}
	var items []models.Bond
	if err := json.Unmarshal(data, &items); err != nil {
		return nil, fmt.Errorf("unmarshal %s: %w", key, err)
	}
	return items, nil
}

// queryHandler returns an HTTP handler that accepts GET /query?sum=N&count=M
// and returns JSON. The allowed count values are controlled by cfg.QueryCounts.
func queryHandler(rdb *redis.Client, cfg config.Config) http.HandlerFunc {
	// Pre-build the error message for invalid count values.
	allowed := make([]int, 0, len(cfg.QueryCounts))
	for k := range cfg.QueryCounts {
		allowed = append(allowed, k)
	}
	sort.Ints(allowed)
	parts := make([]string, len(allowed))
	for i, v := range allowed {
		parts[i] = strconv.Itoa(v)
	}
	countErrMsg := "count must be one of: " + strings.Join(parts, ", ")

	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		sumStr := r.URL.Query().Get("sum")
		if sumStr == "" {
			http.Error(w, "missing required query parameter: sum", http.StatusBadRequest)
			return
		}
		sum, err := strconv.Atoi(sumStr)
		if err != nil || sum <= 0 {
			http.Error(w, "sum must be a positive integer", http.StatusBadRequest)
			return
		}

		countStr := r.URL.Query().Get("count")
		if countStr == "" {
			http.Error(w, "missing required query parameter: count", http.StatusBadRequest)
			return
		}
		count, err := strconv.Atoi(countStr)
		if err != nil {
			http.Error(w, countErrMsg, http.StatusBadRequest)
			return
		}
		if _, ok := cfg.QueryCounts[count]; !ok {
			http.Error(w, countErrMsg, http.StatusBadRequest)
			return
		}

		result, err := processQuery(r.Context(), rdb, sum, count, cfg.BondBoards)
		if err != nil {
			log.Printf("query: %v", err)
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}

		writeJSON(w, http.StatusOK, result)
	}
}
