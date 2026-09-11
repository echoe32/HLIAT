// Package moex provides a rate-limited, retrying, caching client for the
// Moscow Exchange Informational & Statistical Server (ISS).
//
// Design mirrors internal/deposits: HTTP policy (retry, rate limiting) lives
// in http.RoundTripper middleware. Singleflight collapses concurrent identical
// requests. Redis provides a two-tier cache (fresh + stale fallback).
package moex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"backend/internal/models"

	"github.com/redis/go-redis/v9"
	"golang.org/x/sync/singleflight"
	"golang.org/x/time/rate"
)

// ---------------------------------------------------------------------------
// Errors
// ---------------------------------------------------------------------------

// ErrBusy is returned when the rate limiter queue is full.
var ErrBusy = errors.New("moex: upstream rate limit saturated")

// ErrResponseTooLarge is returned when the ISS response exceeds MaxResponseBytes.
var ErrResponseTooLarge = errors.New("moex: response too large")

// APIError is returned for any non-2xx ISS response.
type APIError struct {
	StatusCode int
	Body       string
	URL        string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("moex: unexpected status %d for %s: %s",
		e.StatusCode, e.URL, e.Body)
}

// flightTimeout bounds a single upstream fetch independently of any caller's ctx.
const flightTimeout = 30 * time.Second

// ---------------------------------------------------------------------------
// Config
// ---------------------------------------------------------------------------

// Config configures the MOEX ISS client.
type Config struct {
	BaseURL string // e.g. "https://iss.moex.com"

	// Rate limiting. ISS has no documented hard limit; 500ms interval / burst 3
	// is a conservative default (6 req/s).
	Interval time.Duration
	Burst    int

	// MaxQueueWait sheds load when the limiter is saturated.
	MaxQueueWait time.Duration

	// Retry policy for 429/5xx and transport errors.
	MaxRetries     int
	RetryBaseDelay time.Duration
	RetryMaxDelay  time.Duration

	// CacheTTL controls how long a successful response is served from Redis.
	// StaleTTL extends that window for use only when upstream is failing.
	CacheTTL time.Duration
	StaleTTL time.Duration

	// MaxResponseBytes caps the in-memory response size.
	MaxResponseBytes int64

	MaxIdleConnsPerHost int

	// Transport, if set, replaces the tuned *http.Transport at the bottom of
	// the chain. Useful for tests.
	Transport http.RoundTripper

	RedisClient *redis.Client
}

func (c *Config) applyDefaults() {
	if c.BaseURL == "" {
		c.BaseURL = defaultBaseURL
	}
	if c.Interval <= 0 {
		c.Interval = 500 * time.Millisecond
	}
	if c.Burst <= 0 {
		c.Burst = 3
	}
	if c.MaxRetries < 0 {
		c.MaxRetries = 0
	}
	if c.RetryBaseDelay <= 0 {
		c.RetryBaseDelay = 300 * time.Millisecond
	}
	if c.RetryMaxDelay <= 0 {
		c.RetryMaxDelay = 5 * time.Second
	}
	if c.MaxResponseBytes <= 0 {
		c.MaxResponseBytes = 20 << 20 // 20 MiB — bond list can be large
	}
	if c.MaxIdleConnsPerHost <= 0 {
		c.MaxIdleConnsPerHost = 16
	}
}

// ---------------------------------------------------------------------------
// Client
// ---------------------------------------------------------------------------

// Client is a fault-tolerant MOEX ISS client.
type Client struct {
	httpc    *http.Client
	baseURL  string
	maxBytes int64
	cacheTTL time.Duration
	staleTTL time.Duration

	sf  singleflight.Group
	rdb *redis.Client
}

type redisCacheEntry struct {
	Val     []models.Bond `json:"v"`
	Fetched time.Time     `json:"f"`
}

// New creates a new MOEX ISS client.
func New(cfg Config) *Client {
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
			ResponseHeaderTimeout: 15 * time.Second,
		}
	}

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

	return &Client{
		httpc:    &http.Client{Transport: transport},
		baseURL:  cfg.BaseURL,
		maxBytes: cfg.MaxResponseBytes,
		cacheTTL: cfg.CacheTTL,
		staleTTL: cfg.StaleTTL,
		rdb:      cfg.RedisClient,
	}
}

// Close releases idle connections.
func (c *Client) Close() {
	if t, ok := c.httpc.Transport.(interface{ CloseIdleConnections() }); ok {
		t.CloseIdleConnections()
	}
}

// ---------------------------------------------------------------------------
// FetchBonds — main entry point
// ---------------------------------------------------------------------------

// FetchBonds returns bonds for the given board, from cache when possible.
// Concurrent callers for the same board collapse into one upstream request.
// The returned slice is SHARED — treat it as read-only.
func (c *Client) FetchBonds(ctx context.Context, board string) ([]models.Bond, error) {
	key := "moex:bonds:" + board

	if val, ok := c.lookup(ctx, key, c.cacheTTL); ok {
		return val, nil
	}

	v, err, _ := c.sf.Do(key, func() (any, error) {
		if val, ok := c.lookup(ctx, key, c.cacheTTL); ok {
			return val, nil
		}

		fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), flightTimeout)
		defer cancel()

		bonds, err := c.fetchBoard(fctx, board)
		if err != nil {
			if stale, ok := c.lookup(ctx, key, c.staleTTL); ok {
				return stale, nil
			}
			return nil, err
		}
		c.store(ctx, key, bonds)
		return bonds, nil
	})
	if err != nil {
		return nil, err
	}
	return v.([]models.Bond), nil
}

// ---------------------------------------------------------------------------
// ISS response parsing
// ---------------------------------------------------------------------------

// issResponse models the ISS JSON extended format: a top-level array of blocks.
// With iss.json=extended each data row is a map[string]any.
type issResponse []issBlock

type issBlock struct {
	Securities []map[string]any `json:"securities"`
	Marketdata []map[string]any `json:"marketdata"`
}

// fetchBoard fetches all bond pages for one board, merging securities+marketdata.
func (c *Client) fetchBoard(ctx context.Context, board string) ([]models.Bond, error) {
	var allBonds []models.Bond
	start := 0

	for {
		body, err := c.doRequest(ctx, board, start)
		if err != nil {
			return nil, err
		}

		var resp issResponse
		if err := json.Unmarshal(body, &resp); err != nil {
			return nil, fmt.Errorf("moex: decode ISS response: %w", err)
		}

		// The first element is charsetinfo, the second has our data blocks.
		if len(resp) < 2 {
			return nil, fmt.Errorf("moex: unexpected ISS response structure (got %d blocks)", len(resp))
		}
		block := resp[1]

		if len(block.Securities) == 0 {
			break // no more data
		}

		// Build marketdata index: (SECID+BOARDID) → row
		mdIndex := make(map[string]map[string]any, len(block.Marketdata))
		for _, row := range block.Marketdata {
			k := getString(row, "SECID") + ":" + getString(row, "BOARDID")
			mdIndex[k] = row
		}

		for _, sec := range block.Securities {
			bond := parseSecurity(sec)
			k := bond.SECID + ":" + bond.BoardID
			if md, ok := mdIndex[k]; ok {
				mergeMarketdata(&bond, md)
			}
			allBonds = append(allBonds, bond)
		}

		// ISS default page size is 100. If we got fewer, we're done.
		if len(block.Securities) < 100 {
			break
		}
		start += len(block.Securities)
	}

	return allBonds, nil
}

func (c *Client) doRequest(ctx context.Context, board string, start int) ([]byte, error) {
	u, err := url.Parse(c.baseURL + bondsSecuritiesPath)
	if err != nil {
		return nil, fmt.Errorf("moex: parse URL: %w", err)
	}

	q := u.Query()
	q.Set("iss.meta", "off")
	q.Set("iss.json", "extended")
	q.Set("lang", "en")
	q.Set("iss.only", "securities,marketdata")
	q.Set("securities.columns", strings.Join(securitiesColumns, ","))
	q.Set("marketdata.columns", strings.Join(marketdataColumns, ","))

	if board != "" {
		// Filter by board in the URL path. ISS supports
		// /iss/engines/stock/markets/bonds/boards/{board}/securities.json
		u.Path = fmt.Sprintf("/iss/engines/stock/markets/bonds/boards/%s/securities.json", board)
	}
	if start > 0 {
		q.Set("start", strconv.Itoa(start))
	}
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("moex: create request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Accept-Language", defaultLanguageHeader)

	resp, err := c.httpc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("moex: execute request: %w", err)
	}
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

// ---------------------------------------------------------------------------
// Row parsers
// ---------------------------------------------------------------------------

func parseSecurity(row map[string]any) models.Bond {
	return models.Bond{
		SECID:         getString(row, "SECID"),
		BoardID:       getString(row, "BOARDID"),
		ShortName:     getString(row, "SHORTNAME"),
		SecName:       getString(row, "SECNAME"),
		FaceValue:     getFloat(row, "FACEVALUE"),
		CouponValue:   getFloat(row, "COUPONVALUE"),
		CouponPercent: getFloat(row, "COUPONPERCENT"),
		CouponPeriod:  getInt(row, "COUPONPERIOD"),
		NextCoupon:    getString(row, "NEXTCOUPON"),
		MatDate:       getString(row, "MATDATE"),
		AccruedInt:    getFloat(row, "ACCRUEDINT"),
		ListLevel:     getInt(row, "LISTLEVEL"),
	}
}

func mergeMarketdata(b *models.Bond, row map[string]any) {
	if v, ok := getFloatPtr(row, "LAST"); ok {
		b.Last = v
	}
	if v, ok := getFloatPtr(row, "BID"); ok {
		b.Bid = v
	}
	if v, ok := getFloatPtr(row, "OFFER"); ok {
		b.Offer = v
	}
	if v, ok := getFloatPtr(row, "YIELD"); ok {
		b.YieldToMaturity = v
	}
}

// ---------------------------------------------------------------------------
// JSON value extractors (ISS returns mixed types in extended format)
// ---------------------------------------------------------------------------

func getString(m map[string]any, key string) string {
	v, ok := m[key]
	if !ok || v == nil {
		return ""
	}
	s, _ := v.(string)
	return s
}

func getFloat(m map[string]any, key string) float64 {
	v, ok := m[key]
	if !ok || v == nil {
		return 0
	}
	switch n := v.(type) {
	case float64:
		return n
	case json.Number:
		f, _ := n.Float64()
		return f
	}
	return 0
}

func getInt(m map[string]any, key string) int {
	v, ok := m[key]
	if !ok || v == nil {
		return 0
	}
	switch n := v.(type) {
	case float64:
		return int(n)
	case json.Number:
		i, _ := n.Int64()
		return int(i)
	}
	return 0
}

func getFloatPtr(m map[string]any, key string) (*float64, bool) {
	v, ok := m[key]
	if !ok || v == nil {
		return nil, false
	}
	switch n := v.(type) {
	case float64:
		return &n, true
	case json.Number:
		f, _ := n.Float64()
		return &f, true
	}
	return nil, false
}

// ---------------------------------------------------------------------------
// Redis cache
// ---------------------------------------------------------------------------

func (c *Client) lookup(ctx context.Context, key string, ttl time.Duration) ([]models.Bond, bool) {
	if ttl <= 0 || c.rdb == nil {
		return nil, false
	}

	data, err := c.rdb.Get(ctx, key).Bytes()
	if err != nil {
		return nil, false
	}

	var e redisCacheEntry
	if err := json.Unmarshal(data, &e); err != nil {
		return nil, false
	}

	if time.Since(e.Fetched) > ttl {
		return nil, false
	}
	return e.Val, true
}

func (c *Client) store(ctx context.Context, key string, val []models.Bond) {
	if (c.cacheTTL <= 0 && c.staleTTL <= 0) || c.rdb == nil {
		return
	}

	e := redisCacheEntry{Val: val, Fetched: time.Now()}
	data, err := json.Marshal(e)
	if err != nil {
		return
	}

	maxTTL := c.cacheTTL
	if c.staleTTL > maxTTL {
		maxTTL = c.staleTTL
	}

	c.rdb.Set(ctx, key, data, maxTTL)
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
		r.Cancel()
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
	if t.maxRetries == 0 {
		return t.base.RoundTrip(req)
	}

	for attempt := 0; ; attempt++ {
		attemptReq := req.Clone(req.Context())

		resp, err := t.base.RoundTrip(attemptReq)

		if err == nil && !retryableStatus(resp.StatusCode) {
			return resp, nil
		}
		if req.Context().Err() != nil || errors.Is(err, ErrBusy) {
			return resp, err
		}
		if attempt >= t.maxRetries {
			return resp, err
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

func (t *retryTransport) backoff(attempt int, resp *http.Response) time.Duration {
	if resp != nil {
		if d, ok := parseRetryAfter(resp.Header.Get("Retry-After")); ok {
			return min(d, t.maxDelay)
		}
	}
	d := t.baseDelay << attempt
	if d > t.maxDelay || d <= 0 {
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

func readLimited(r io.Reader, max int64) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, max+1))
	if err != nil {
		return nil, fmt.Errorf("moex: read response: %w", err)
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
