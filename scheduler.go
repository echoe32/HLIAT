package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"sort"
	"time"

	"backend/internal/deposits"
	"backend/internal/models"

	"github.com/redis/go-redis/v9"

	"strings"
)

// periods is the set of duration buckets to fetch and group by.
var periods = []string{"1m", "3m", "4m", "6m", "9m"}

// periodDays maps each period code to its approximate length in days,
// used for earnings calculation.
var periodDays = map[string]int{
	"1m": 30,
	"3m": 90,
	"4m": 120,
	"6m": 180,
	"9m": 270,
}

// redisTopN is the maximum number of products stored per group.
const redisTopN = 30

// redisTTL outlives the 24h daily cadence so data is never absent.
const redisTTL = 25 * time.Hour

// startScheduler runs an initial fetch immediately, then re-fetches every 24 h.
// It blocks until ctx is cancelled.
func startScheduler(ctx context.Context, b *Backend) {
	log.Println("scheduler: running initial fetch cycle")
	runFetchCycle(ctx, b)
	runBondsFetchCycle(ctx, b)
	log.Println("scheduler: initial fetch complete, next run in 24 h")

	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			log.Println("scheduler: stopped")
			return
		case <-ticker.C:
			log.Println("scheduler: running daily fetch cycle")
			runFetchCycle(ctx, b)
			runBondsFetchCycle(ctx, b)
			log.Println("scheduler: daily fetch complete")
		}
	}
}

// runFetchCycle executes one full round: deposits for all periods, 2 s pause,
// then saving accounts for all periods.
func runFetchCycle(ctx context.Context, b *Backend) {
	// --- Deposits ---
	for _, period := range periods {
		if ctx.Err() != nil {
			return
		}
		params := deposits.DepositSearchParams{
			Type:                   "0", // deposits only
			Period:                 period,
			IsNoAdditionalExpenses: 1,
			PerPage:                40,
		}
		fetchAndStore(ctx, b, "deposits", period, params, filterDeposits)
	}

	// 2 s gap between the two request groups.
	log.Println("scheduler: waiting 2 s before savings fetch")
	select {
	case <-time.After(2 * time.Second):
	case <-ctx.Done():
		return
	}

	// --- Saving accounts ---
	for _, period := range periods {
		if ctx.Err() != nil {
			return
		}
		params := deposits.DepositSearchParams{
			Type:    "14", // saving accounts only
			Period:  period,
			PerPage: 40,
		}
		fetchAndStore(ctx, b, "savings", period, params, filterSavings)
	}
}

// fetchAndStore fetches one batch from the API, applies post-filtering, sorts
// by efficient_rate desc, truncates to top-30, and writes to Redis.
func fetchAndStore(
	ctx context.Context,
	b *Backend,
	category, period string,
	params deposits.DepositSearchParams,
	filter func([]models.Deposit) []models.Deposit,
) {
	response, err := b.depositsClient.Search(ctx, params)
	if err != nil {
		log.Printf("scheduler: %s %s fetch failed: %v", category, period, err)
		return
	}

	all := extractProducts(response)

	// Hardcoded sort check: API is asked for efficient_rate desc, verify it.
	checkSortOrder(all, category, period)

	filtered := filter(all)

	// Re-sort after filtering to guarantee order.
	sort.Slice(filtered, func(i, j int) bool {
		return filtered[i].EfficientRate > filtered[j].EfficientRate
	})

	if len(filtered) > redisTopN {
		filtered = filtered[:redisTopN]
	}

	storeProducts(ctx, b.rdb, category, period, filtered)
	calculateAndStoreAverages(ctx, b, fmt.Sprintf("%s:%s", category, period), filtered)
	log.Printf("scheduler: %s %s — %d raw → %d filtered → %d stored",
		category, period, len(all), len(filtered), min(len(filtered), redisTopN))
}

// --------------------------------------------------------------------------
// Extraction
// --------------------------------------------------------------------------

// extractProducts flattens the grouped_table response into a flat list.
func extractProducts(resp DepositsResponse) []models.Deposit {
	var out []models.Deposit
	for _, g := range resp.GroupedTable {
		if len(g.DepositResultRows) > 0 {
			out = append(out, g.DepositResultRows...)
		} else if g.Deposit.ProductURL != "" {
			out = append(out, g.Deposit)
		}
	}
	return out
}

// --------------------------------------------------------------------------
// Post-filters
// --------------------------------------------------------------------------

// filterDeposits keeps only "common" deposits: no new money, no key-rate link,
// no special restrictions.
func filterDeposits(src []models.Deposit) []models.Deposit {
	out := make([]models.Deposit, 0, len(src))
	for _, d := range src {
		if d.IsNewMoney {
			continue
		}
		if d.IsKeyRateLinked {
			continue
		}
		if d.SpecialRestrictions != nil && *d.SpecialRestrictions != "" {
			continue
		}
		out = append(out, d)
	}
	return out
}

// filterSavings keeps only "plain" saving accounts: no pension, no new money,
// no children.
func filterSavings(src []models.Deposit) []models.Deposit {
	out := make([]models.Deposit, 0, len(src))
	for _, d := range src {
		if d.IsPensionDeposit {
			continue
		}
		if d.IsNewMoney {
			continue
		}
		if d.IsChildrenDeposit {
			continue
		}
		out = append(out, d)
	}
	return out
}

// --------------------------------------------------------------------------
// Sort verification
// --------------------------------------------------------------------------

// checkSortOrder logs a warning when the API response is not sorted by
// efficient_rate descending. The caller always re-sorts, so this is
// informational only.
func checkSortOrder(items []models.Deposit, category, period string) {
	for i := 1; i < len(items); i++ {
		if items[i].EfficientRate > items[i-1].EfficientRate {
			log.Printf("scheduler: WARNING: %s %s not sorted desc by efficient_rate "+
				"at index %d (%.2f > %.2f), will re-sort",
				category, period, i, items[i].EfficientRate, items[i-1].EfficientRate)
			return
		}
	}
}

// --------------------------------------------------------------------------
// Redis persistence
// --------------------------------------------------------------------------

// storeProducts writes the slice to Redis as a JSON array.
// Key format: "deposits:1m", "savings:3m", etc.
func storeProducts(ctx context.Context, rdb *redis.Client, category, period string, items []models.Deposit) {
	key := fmt.Sprintf("%s:%s", category, period)

	data, err := json.Marshal(items)
	if err != nil {
		log.Printf("scheduler: failed to marshal %s: %v", key, err)
		return
	}

	if err := rdb.Set(ctx, key, data, redisTTL).Err(); err != nil {
		log.Printf("scheduler: failed to store %s in redis: %v", key, err)
	}
}

// --------------------------------------------------------------------------
// Bond fetch cycle (MOEX ISS)
// --------------------------------------------------------------------------

// runBondsFetchCycle fetches bonds for each configured board, filters, sorts by YTM desc,
// and stores the top results in Redis.
func runBondsFetchCycle(ctx context.Context, b *Backend) {
	if b.moexClient == nil {
		log.Println("scheduler: moex client not configured, skipping bonds fetch")
		return
	}

	log.Println("scheduler: starting bonds fetch cycle")
	for _, bb := range b.cfg.BondBoards {
		if ctx.Err() != nil {
			return
		}

		bonds, err := b.moexClient.FetchBonds(ctx, bb.Board)
		if err != nil {
			log.Printf("scheduler: bonds %s fetch failed: %v", bb.Board, err)
			continue
		}

		filtered := filterBonds(bonds, bb.Board)

		// Sort by YTM descending (highest yield first).
		sort.Slice(filtered, func(i, j int) bool {
			yi := ytm(filtered[i])
			yj := ytm(filtered[j])
			return yi > yj
		})

		if len(filtered) > redisTopN {
			filtered = filtered[:redisTopN]
		}

		storeBonds(ctx, b.rdb, bb.RedisKey, filtered)
		calculateAndStoreBondAverages(ctx, b, bb.RedisKey, filtered)
		log.Printf("scheduler: bonds %s — %d raw → %d filtered → %d stored",
			bb.Board, len(bonds), len(filtered), min(len(filtered), redisTopN))
	}
	log.Println("scheduler: bonds fetch cycle complete")
}

// filterBonds applies selection criteria:
//   - OFZ (TQOB): keep all (they are already government bonds, list level 1).
//   - Corporate (TQCB): listing level ≤ 2, positive coupon, non-empty maturity date.
//
// Common filter: must have a non-zero YTM and non-empty maturity date.
func filterBonds(src []models.Bond, board string) []models.Bond {
	out := make([]models.Bond, 0, len(src))
	for _, b := range src {
		// Must have a yield to be useful for comparison.
		if b.YieldToMaturity == nil || *b.YieldToMaturity <= 0 {
			continue
		}
		// Must have maturity date.
		if b.MatDate == "" {
			continue
		}
		if board == "TQCB" {
			if b.ListLevel > 2 {
				continue
			}
			if b.CouponPercent <= 0 {
				continue
			}
		}
		out = append(out, b)
	}
	return out
}

// ytm extracts the yield-to-maturity value, returning 0 if nil.
func ytm(b models.Bond) float64 {
	if b.YieldToMaturity != nil {
		return *b.YieldToMaturity
	}
	return 0
}

// storeBonds writes bond data to Redis.
func storeBonds(ctx context.Context, rdb *redis.Client, key string, items []models.Bond) {
	data, err := json.Marshal(items)
	if err != nil {
		log.Printf("scheduler: failed to marshal %s: %v", key, err)
		return
	}

	if err := rdb.Set(ctx, key, data, redisTTL).Err(); err != nil {
		log.Printf("scheduler: failed to store %s in redis: %v", key, err)
	}
}

func calculateAndStoreAverages(ctx context.Context, b *Backend, category string, items []models.Deposit) {
	if len(items) == 0 {
		return
	}
	var sumAPR float64
	var sumIncome float64

	defaultSum := 100000.0

	// period is embedded in category like "deposits:1m"
	parts := strings.Split(category, ":")
	var period string
	if len(parts) == 2 {
		period = parts[1]
	}
	days := float64(periodDays[period])

	for _, d := range items {
		sumAPR += d.EfficientRate
		if days > 0 {
			income := defaultSum * (d.EfficientRate / 100.0) * (days / 365.0)
			sumIncome += income
		}
	}

	avgAPR := sumAPR / float64(len(items))
	avgIncome := sumIncome / float64(len(items))

	stat := CategoryStat{
		Date:          time.Now().Format("2006-01-02"),
		Category:      category,
		AverageAPR:    avgAPR,
		AverageIncome: avgIncome,
	}

	if err := b.SaveStats(ctx, []CategoryStat{stat}); err != nil {
		log.Printf("scheduler: failed to save stats for %s: %v", category, err)
	}
}

func calculateAndStoreBondAverages(ctx context.Context, b *Backend, category string, items []models.Bond) {
	if len(items) == 0 {
		return
	}
	var sumAPR float64
	var sumIncome float64

	defaultSum := 100000.0
	validBonds := 0

	for _, bnd := range items {
		if bnd.YieldToMaturity != nil {
			sumAPR += *bnd.YieldToMaturity
		}

		var price float64
		if bnd.Last != nil && *bnd.Last > 0 {
			price = (*bnd.Last/100.0)*bnd.FaceValue + bnd.AccruedInt
		}

		if price > 0 {
			numBonds := math.Floor(defaultSum / price)
			if bnd.CouponPeriod > 0 {
				couponsPerYear := 365.0 / float64(bnd.CouponPeriod)
				annualIncome := numBonds * bnd.CouponValue * couponsPerYear
				sumIncome += annualIncome
			}
			validBonds++
		}
	}

	avgAPR := sumAPR / float64(len(items))
	avgIncome := 0.0
	if validBonds > 0 {
		avgIncome = sumIncome / float64(validBonds)
	}

	stat := CategoryStat{
		Date:          time.Now().Format("2006-01-02"),
		Category:      category,
		AverageAPR:    avgAPR,
		AverageIncome: avgIncome,
	}

	if err := b.SaveStats(ctx, []CategoryStat{stat}); err != nil {
		log.Printf("scheduler: failed to save stats for %s: %v", category, err)
	}
}
