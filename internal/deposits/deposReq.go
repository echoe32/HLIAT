// Package Deposits provides a rate-limited, retrying, caching client for the
// deposits search API.
//
// Design notes:
//
//   - HTTP policy (retry, rate limiting) lives in http.RoundTripper middleware,
//     not in the business function. Anything that goes through this client gets
//     it for free.
//   - The upstream allows 20 req/min. That is one request every 3 seconds, so
//     the limiter is the real bottleneck at load, not the network. Caching and
//     singleflight are what make the client usable above that rate; the limiter
//     is only the last line of defense.
//   - When the queue in front of the limiter gets too long we shed load
//     (ErrBusy) instead of parking goroutines forever.
package deposits

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
	"golang.org/x/sync/singleflight"
	"golang.org/x/time/rate"
)

// periodRegex validates that the period provided is valid
var periodRegex = regexp.MustCompile(`^(?:0|(?:\d+y)?(?:\d+m)?)$`)

// DepositSearchParams provides the configurable parameters for searching the deposits and the logic for the fields
type DepositSearchParams struct {
	// Amount is the deposit amount in the selected currency. 0 means "any amount".
	Amount int
	// Period is the deposit term in a compact format: "1m", "6m", "1y", "2y", "1y6m".
	// An empty string means "any period".
	Period string
	// Capitalization: 0 = not set, 1 = require interest capitalization.
	Capitalization int
	// PartialWithdrawal: 0 = not set, 1 = require partial withdrawal option.
	PartialWithdrawal int
	// Replenishment: 0 = not set, 1 = require replenishment option.
	Replenishment int
	// PaymentPeriodPerMonth: 0 = not set, 1 = require monthly interest payments.
	PaymentPeriodPerMonth int
	// EarlyTerminationMethod: 0 = not set, 1 = require favorable early termination.
	EarlyTerminationMethod int
	// IsNoAdditionalExpenses: 0 = not set, 1 = require no additional expenses.
	IsNoAdditionalExpenses int
	// Type selects the deposit category:
	//   ""   or "all" = deposits & savings accounts,
	//   "0"  = deposits only,
	//   "14" = savings accounts only.
	Type string
	// IsFairRate: 0 = not set, 1 = require "fair rate" certification.
	IsFairRate int
	// PerPage sets the number of deposits returned per page
	PerPage int
}

// validateParams applies validation to all user-provided data for requesting deposits
func validateParams(params DepositSearchParams) error {
	if params.Amount < 0 {
		return fmt.Errorf("amount must be non-negative, got %d", params.Amount)
	}

	if params.Period != "" && !periodRegex.MatchString(params.Period) {
		return fmt.Errorf("period must match format like '1m', '2y', '1y6m', or '0', got %q", params.Period)
	}

	boolFields := map[string]int{
		"capitalization":            params.Capitalization,
		"partial_withdrawal":        params.PartialWithdrawal,
		"replenishment":             params.Replenishment,
		"payment_period_per_month":  params.PaymentPeriodPerMonth,
		"early_termination_method":  params.EarlyTerminationMethod,
		"is_no_additional_expenses": params.IsNoAdditionalExpenses,
		"is_fair_rate":              params.IsFairRate,
	}

	for name, val := range boolFields {
		if val != 0 && val != 1 {
			return fmt.Errorf("%s must be 0 or 1, got %d", name, val)
		}
	}

	validTypes := map[string]bool{"": true, "all": true, "0": true, "14": true}
	if !validTypes[params.Type] {
		return fmt.Errorf("type must be one of 'all', '0', '14', or empty, got %q", params.Type)
	}

	return nil
}

// ---------------------------------------------------------------------------
// Errors
// ---------------------------------------------------------------------------

// ErrBusy is returned when the request would have to wait longer than
// Config.MaxQueueWait for a rate limit token. Map it to 503 + Retry-After.
var ErrBusy = errors.New("deposits: upstream rate limit saturated")

// ErrResponseTooLarge is returned when the API response exceeds MaxResponseBytes.
var ErrResponseTooLarge = errors.New("deposits: response too large")

// APIError is returned for any non-2xx response. Callers can branch on it:
//
//	var apiErr *deposits.APIError
//	if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound { ... }
type APIError struct {
	StatusCode int
	Body       string // truncated
	URL        string // path + query, no scheme/host
}

func (e *APIError) Error() string {
	return fmt.Sprintf("deposits: unexpected status %d for %s: %s",
		e.StatusCode, e.URL, e.Body)
}

// flightTimeout bounds a single upstream fetch. It is deliberately independent
// of any caller's context: the singleflight leader fetches on behalf of every
// follower, so it must not inherit one caller's deadline.
const flightTimeout = 30 * time.Second

// ---------------------------------------------------------------------------
// Config
// ---------------------------------------------------------------------------

type Config[T any] struct {
	BaseURL string

	// Rate limiting. For "20 requests per minute" use Interval=3s, Burst=1.
	// Burst > 1 lets you fire N requests instantly and then stall, which many
	// APIs treat as abuse even when the per-minute average is legal.
	Interval time.Duration
	Burst    int

	// MaxQueueWait sheds load: if a request would wait longer than this for a
	// token, it fails immediately with ErrBusy instead of blocking. A caller
	// who waits 45s for deposit rates has already given up. 0 disables shedding.
	MaxQueueWait time.Duration

	// Retry policy. Applies to 429/5xx and transport errors.
	MaxRetries     int
	RetryBaseDelay time.Duration
	RetryMaxDelay  time.Duration

	// CacheTTL is how long a successful response is served from memory.
	// StaleTTL extends that window for use *only* when upstream is failing,
	// so a broken API degrades into slightly-old data rather than an outage.
	CacheTTL time.Duration
	StaleTTL time.Duration

	// MaxResponseBytes caps the in-memory response size.
	MaxResponseBytes int64

	// MaxIdleConnsPerHost defaults to 2 in net/http, which is the single most
	// common cause of connection churn in high-load Go services.
	MaxIdleConnsPerHost int

	UserAgent string

	// Transport, if set, replaces the tuned *http.Transport at the bottom of
	// the chain. Retry and rate limiting still wrap it. Useful for tests.
	Transport http.RoundTripper

	// Normalize runs once per fetch, before the value enters the cache. Do all
	// sorting and filtering here, not at the call site: the cached value is
	// shared by every caller and concurrent sorts on it are a data race.
	Normalize func(*T) error

	RedisClient *redis.Client
}

func (c *Config[T]) applyDefaults() {
	if c.BaseURL == "" {
		c.BaseURL = depositsAPIUrl
	}
	if c.Interval <= 0 {
		c.Interval = 3 * time.Second // 20/min
	}
	if c.Burst <= 0 {
		c.Burst = 1
	}
	if c.MaxRetries < 0 {
		c.MaxRetries = 0
	}
	if c.RetryBaseDelay <= 0 {
		c.RetryBaseDelay = 200 * time.Millisecond
	}
	if c.RetryMaxDelay <= 0 {
		c.RetryMaxDelay = 5 * time.Second
	}
	if c.MaxResponseBytes <= 0 {
		c.MaxResponseBytes = 10 << 20
	}
	if c.MaxIdleConnsPerHost <= 0 {
		c.MaxIdleConnsPerHost = 32
	}
	if c.UserAgent == "" {
		c.UserAgent = PickUserAgent()
	}
}

// ---------------------------------------------------------------------------
// Client
// ---------------------------------------------------------------------------

type Client[T any] struct {
	httpc     *http.Client
	baseURL   string
	baseQuery url.Values // static defaults, built once
	userAgent string
	maxBytes  int64
	cacheTTL  time.Duration
	staleTTL  time.Duration
	normalize func(*T) error

	sf singleflight.Group

	rdb *redis.Client
}

type redisCacheEntry[T any] struct {
	Val     T         `json:"v"`
	Fetched time.Time `json:"f"`
}

func New[T any](cfg Config[T]) *Client[T] {
	cfg.applyDefaults()

	base := cfg.Transport
	if base == nil {
		base = &http.Transport{
			Proxy: http.ProxyFromEnvironment,
			DialContext: (&net.Dialer{
				Timeout:   5 * time.Second,
				KeepAlive: 30 * time.Second,
			}).DialContext,
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          100,
			MaxIdleConnsPerHost:   cfg.MaxIdleConnsPerHost,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   5 * time.Second,
			ExpectContinueTimeout: time.Second,
			// Per-attempt ceiling. Deliberately not http.Client.Timeout, which
			// would cover all retries together and starve the last attempt.
			ResponseHeaderTimeout: 10 * time.Second,
		}
	}

	// Order matters: retry wraps rate limit, so every retry attempt also
	// spends a token. The reverse would let retries blow through the budget.
	transport := &retryTransport{
		base: &rateLimitTransport{
			base:    base,
			limiter: rate.NewLimiter(rate.Every(cfg.Interval), cfg.Burst),
			maxWait: cfg.MaxQueueWait,
		},
		maxRetries: cfg.MaxRetries,
		baseDelay:  cfg.RetryBaseDelay,
		maxDelay:   cfg.RetryMaxDelay,
	}

	// Static query params, computed once instead of on every request.
	bq := url.Values{}
	bq.Set("city", defaultCity)
	bq.Set("currency", defaultCurrency)
	bq.Set("page", defaultPageCount)
	bq.Set("sort", defaultSortParam)
	bq.Set("order", defaultOrder)
	bq.Set("defaultpensionoff", defaultPensionOff)
	bq.Set("defaultchildrenoff", defaultChildrenOff)

	return &Client[T]{
		httpc:     &http.Client{Transport: transport},
		baseURL:   cfg.BaseURL,
		baseQuery: bq,
		userAgent: cfg.UserAgent,
		maxBytes:  cfg.MaxResponseBytes,
		cacheTTL:  cfg.CacheTTL,
		staleTTL:  cfg.StaleTTL,
		normalize: cfg.Normalize,
		rdb:       cfg.RedisClient,
	}
}

// Close releases idle connections. Call on shutdown.
func (c *Client[T]) Close() {
	if t, ok := c.httpc.Transport.(interface{ CloseIdleConnections() }); ok {
		t.CloseIdleConnections()
	}
}

// Search returns deposits matching params, from cache when possible.
// Concurrent callers asking for the same params collapse into one upstream
// request.
//
// The returned value is SHARED with every other caller holding the same cache
// entry. Treat it as read-only: do not sort, filter, append to, or otherwise
// mutate it. Sorting belongs in Config.Normalize. If a caller needs its own
// ordering, copy the slice first.
func (c *Client[T]) Search(ctx context.Context, params DepositSearchParams) (T, error) {
	var zero T

	if err := validateParams(params); err != nil {
		return zero, fmt.Errorf("invalid search params: %w", err)
	}

	// url.Values.Encode sorts keys, so this is a stable cache key.
	key := c.query(params).Encode()

	if val, ok := c.lookup(ctx, key, c.cacheTTL); ok {
		return val, nil
	}

	v, err, _ := c.sf.Do(key, func() (any, error) {
		// A flight that finished between our lookup and here.
		if val, ok := c.lookup(ctx, key, c.cacheTTL); ok {
			return val, nil
		}

		// Detached: one caller cancelling must not fail the other followers.
		fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), flightTimeout)
		defer cancel()

		val, err := c.fetch(fctx, key)
		if err != nil {
			// Degrade to stale data rather than propagating an outage.
			// NOTE: this is silent. Add a metric or log here so a broken
			// upstream doesn't look healthy.
			if stale, ok := c.lookup(ctx, key, c.staleTTL); ok {
				return stale, nil
			}
			return nil, err
		}
		c.store(ctx, key, val)
		return val, nil
	})
	if err != nil {
		return zero, err
	}
	return v.(T), nil
}

// fetch performs one upstream request and turns it into a cacheable value.
func (c *Client[T]) fetch(ctx context.Context, rawQuery string) (T, error) {
	var val T

	body, err := c.do(ctx, rawQuery)
	if err != nil {
		return val, err
	}
	// Unknown JSON fields are ignored by default, so T only needs the fields
	// you actually use. Do not add DisallowUnknownFields here.
	if err := json.Unmarshal(body, &val); err != nil {
		return val, fmt.Errorf("decode deposits response: %w", err)
	}
	if c.normalize != nil {
		if err := c.normalize(&val); err != nil {
			return val, fmt.Errorf("normalize deposits response: %w", err)
		}
	}
	return val, nil
}

func (c *Client[T]) do(ctx context.Context, rawQuery string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create deposits request: %w", err)
	}
	req.URL.RawQuery = rawQuery

	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Accept-Language", defaultLanguageHeader)

	resp, err := c.httpc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("execute deposits request: %w", err)
	}
	// Always drain: an unread body pins the connection and defeats keep-alive,
	// which matters far more than usual when you're connection-bound.
	defer drain(resp.Body)

	body, err := readLimited(resp.Body, c.maxBytes)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &APIError{
			StatusCode: resp.StatusCode,
			Body:       snippet(body, 512),
			URL:        req.URL.RequestURI(),
		}
	}
	return body, nil
}

func (c *Client[T]) query(p DepositSearchParams) url.Values {
	// Shallow clone is safe here because we only ever Set (which replaces the
	// slice) and never Add (which would append into a shared backing array).
	q := maps.Clone(c.baseQuery)

	q.Set("amount", strconv.Itoa(p.Amount)) // 0 = any amount
	q.Set("period", p.Period)               // e.g. "6m", "1y6m"

	// Boolean-like filters: 0 = not set, 1 = required
	q.Set("capitalization", strconv.Itoa(p.Capitalization))
	q.Set("partial_withdrawal", strconv.Itoa(p.PartialWithdrawal))
	q.Set("replenishment", strconv.Itoa(p.Replenishment))
	q.Set("payment_period_per_month", strconv.Itoa(p.PaymentPeriodPerMonth))
	q.Set("early_termination_method", strconv.Itoa(p.EarlyTerminationMethod))
	q.Set("is_no_additional_expenses", strconv.Itoa(p.IsNoAdditionalExpenses))
	q.Set("is_fair_rate", strconv.Itoa(p.IsFairRate))

	// "" or "all" = all, "0" = deposits only, "14" = savings only
	q.Set("type", p.Type)

	if p.PerPage > 0 {
		q.Set("per_page", strconv.Itoa(p.PerPage))
	} else {
		q.Set("per_page", defaultPerPage)
	}

	return q
}

func (c *Client[T]) lookup(ctx context.Context, key string, ttl time.Duration) (T, bool) {
	var zero T
	if ttl <= 0 || c.rdb == nil {
		return zero, false
	}

	data, err := c.rdb.Get(ctx, "deposits:"+key).Bytes()
	if err != nil {
		return zero, false
	}

	var e redisCacheEntry[T]
	if err := json.Unmarshal(data, &e); err != nil {
		return zero, false
	}

	if time.Since(e.Fetched) > ttl {
		return zero, false
	}

	return e.Val, true
}

func (c *Client[T]) store(ctx context.Context, key string, val T) {
	if (c.cacheTTL <= 0 && c.staleTTL <= 0) || c.rdb == nil {
		return
	}

	e := redisCacheEntry[T]{Val: val, Fetched: time.Now()}
	data, err := json.Marshal(e)
	if err != nil {
		return
	}

	maxTTL := c.cacheTTL
	if c.staleTTL > maxTTL {
		maxTTL = c.staleTTL
	}

	c.rdb.Set(ctx, "deposits:"+key, data, maxTTL)
}

// ---------------------------------------------------------------------------
// Rate limit transport
// ---------------------------------------------------------------------------

type rateLimitTransport struct {
	base    http.RoundTripper
	limiter *rate.Limiter
	maxWait time.Duration
}

func (t *rateLimitTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	r := t.limiter.Reserve()
	if !r.OK() {
		return nil, ErrBusy
	}
	delay := r.Delay()
	if t.maxWait > 0 && delay > t.maxWait {
		r.Cancel() // return the token to the bucket
		return nil, ErrBusy
	}
	if delay > 0 {
		timer := time.NewTimer(delay)
		select {
		case <-timer.C:
		case <-req.Context().Done():
			timer.Stop()
			r.Cancel()
			return nil, req.Context().Err()
		}
	}
	return t.base.RoundTrip(req)
}

// ---------------------------------------------------------------------------
// Retry transport
// ---------------------------------------------------------------------------

type retryTransport struct {
	base       http.RoundTripper
	maxRetries int
	baseDelay  time.Duration
	maxDelay   time.Duration
}

func (t *retryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if t.maxRetries == 0 || !idempotent(req.Method) {
		return t.base.RoundTrip(req)
	}

	for attempt := 0; ; attempt++ {
		attemptReq, err := rewind(req)
		if err != nil {
			return nil, err
		}

		resp, err := t.base.RoundTrip(attemptReq)

		if err == nil && !retryableStatus(resp.StatusCode) {
			return resp, nil
		}
		// Don't retry a canceled context, and don't retry load shedding —
		// the limiter is already saturated, another attempt makes it worse.
		if req.Context().Err() != nil || errors.Is(err, ErrBusy) {
			return resp, err
		}
		if attempt >= t.maxRetries {
			return resp, err // hand back the last real response/error
		}

		delay := t.backoff(attempt, resp)
		if resp != nil {
			drain(resp.Body)
		}

		timer := time.NewTimer(delay)
		select {
		case <-timer.C:
		case <-req.Context().Done():
			timer.Stop()
			return nil, req.Context().Err()
		}
	}
}

// backoff prefers the server's Retry-After, then exponential with full jitter.
// Jitter matters: without it every replica retries in lockstep and re-hammers
// the API at exactly the same moment.
func (t *retryTransport) backoff(attempt int, resp *http.Response) time.Duration {
	if resp != nil {
		if d, ok := parseRetryAfter(resp.Header.Get("Retry-After")); ok {
			return min(d, t.maxDelay)
		}
	}
	d := t.baseDelay << attempt
	if d > t.maxDelay || d <= 0 { // <= 0 guards shift overflow
		d = t.maxDelay
	}
	return time.Duration(rand.Int64N(int64(d)) + 1)
}

func retryableStatus(code int) bool {
	switch code {
	case http.StatusTooManyRequests,
		http.StatusInternalServerError,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout:
		return true
	}
	return false
}

func idempotent(m string) bool {
	switch m {
	case http.MethodGet, http.MethodHead, http.MethodOptions,
		http.MethodTrace, http.MethodPut, http.MethodDelete:
		return true
	}
	return false
}

// rewind clones the request for a fresh attempt. A RoundTripper must not
// mutate the request it was given, and a consumed body can't be replayed.
func rewind(req *http.Request) (*http.Request, error) {
	r := req.Clone(req.Context())
	if req.Body != nil && req.Body != http.NoBody {
		if req.GetBody == nil {
			return nil, errors.New("deposits: cannot retry request without GetBody")
		}
		b, err := req.GetBody()
		if err != nil {
			return nil, fmt.Errorf("rewind request body: %w", err)
		}
		r.Body = b
	}
	return r, nil
}

// parseRetryAfter handles both forms in RFC 9110: delta-seconds and HTTP-date.
func parseRetryAfter(v string) (time.Duration, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, false
	}
	if secs, err := strconv.Atoi(v); err == nil {
		if secs < 0 {
			return 0, false
		}
		return time.Duration(secs) * time.Second, true
	}
	if ts, err := http.ParseTime(v); err == nil {
		if d := time.Until(ts); d > 0 {
			return d, true
		}
		return 0, true
	}
	return 0, false
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func drain(b io.ReadCloser) {
	if b == nil {
		return
	}
	io.Copy(io.Discard, io.LimitReader(b, 64<<10))
	b.Close()
}

// readLimited reads max+1 bytes so truncation is detectable. Wrapping the body
// in a bare LimitReader instead produces a misleading "unexpected EOF" from the
// JSON decoder when the cap is hit.
func readLimited(r io.Reader, max int64) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, max+1))
	if err != nil {
		return nil, fmt.Errorf("read deposits response: %w", err)
	}
	if int64(len(b)) > max {
		return nil, ErrResponseTooLarge
	}
	return b, nil
}

func snippet(b []byte, n int) string {
	if len(b) > n {
		return string(b[:n]) + "..."
	}
	return string(b)
}

// ---------------------------------------------------------------------------
// Usage
// ---------------------------------------------------------------------------
//
//	client := deposits.New(deposits.Config[DepositsResponse]{
//		Interval:     3 * time.Second, // 20/min
//		Burst:        1,
//		MaxQueueWait: 2 * time.Second, // shed rather than queue
//		MaxRetries:   3,
//		CacheTTL:     45 * time.Second,
//		StaleTTL:     5 * time.Minute,
//		Normalize: func(r *DepositsResponse) error {
//			slices.SortFunc(r.Items, func(a, b Deposit) int {
//				return cmp.Compare(b.Rate, a.Rate) // highest rate first
//			})
//			return nil
//		},
//	})
//	defer client.Close()
//
//	res, err := client.Search(ctx, params)
//	switch {
//	case errors.Is(err, deposits.ErrBusy):
//		http.Error(w, "busy", http.StatusServiceUnavailable)
//	case err != nil:
//		http.Error(w, "upstream error", http.StatusBadGateway)
//	}
//
// res is shared and read-only. To reorder it per caller, copy first:
//
//	items := slices.Clone(res.Items) // shallow: safe to reorder, not to mutate
//
// Build the client once at startup and share it. One per request gives every
// request its own limiter, cache, and connection pool, defeating the design.
//
// One limiter instance per process. If you run N replicas, 20/min is a *global*
// budget: either multiply Interval by N, or move the limiter into Redis.
//
// Testing: pass Config.Transport (or Config.BaseURL pointed at
// httptest.NewServer) to exercise retry and shedding without real traffic.
