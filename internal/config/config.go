// Package config holds application configuration with injectable URLs.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// BondBoard describes a MOEX board to fetch and how to key it.
type BondBoard struct {
	Board    string // e.g. "TQOB"
	Label    string // e.g. "ofz" — used as Redis key suffix and JSON key
	RedisKey string // e.g. "bonds:ofz"
}

// Config holds application configuration including injectable URLs.
type Config struct {
	ServiceName    string
	DepositsAPIURL string
	MoexBaseURL    string
	DatabaseDSN    string
	RedisAddress   string

	// QueryCounts is the set of allowed "count" values for /query.
	// Default: {3, 10}.
	QueryCounts map[int]struct{}

	// BondBoards is the list of MOEX boards to fetch and store.
	// Default: TQOB:ofz, TQCB:corporate.
	BondBoards []BondBoard

	DBMaxOpenConns    int
	DBConnMaxLifetime time.Duration
	DBConnMaxIdleTime time.Duration
	DBStartupTimeout  time.Duration
}

func getEnvOrDefault(key, fallback string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return fallback
}

// parseQueryCounts parses a comma-separated list of positive integers.
// Example: "3,10" → {3: {}, 10: {}}.
func parseQueryCounts(raw string) (map[int]struct{}, error) {
	m := make(map[int]struct{})
	for _, s := range strings.Split(raw, ",") {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		n, err := strconv.Atoi(s)
		if err != nil || n <= 0 {
			return nil, fmt.Errorf("invalid count value %q", s)
		}
		m[n] = struct{}{}
	}
	if len(m) == 0 {
		return nil, fmt.Errorf("no valid count values")
	}
	return m, nil
}

// parseBondBoards parses a comma-separated list of "BOARD:label" pairs.
// Example: "TQOB:ofz,TQCB:corporate".
func parseBondBoards(raw string) ([]BondBoard, error) {
	var boards []BondBoard
	for _, s := range strings.Split(raw, ",") {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		parts := strings.SplitN(s, ":", 2)
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			return nil, fmt.Errorf("invalid board spec %q, expected BOARD:label", s)
		}
		boards = append(boards, BondBoard{
			Board:    parts[0],
			Label:    parts[1],
			RedisKey: "bonds:" + parts[1],
		})
	}
	if len(boards) == 0 {
		return nil, fmt.Errorf("no valid board specs")
	}
	return boards, nil
}

// DefaultConfig returns the default production configuration.
func DefaultConfig() Config {
	queryCounts := map[int]struct{}{3: {}, 10: {}}
	if raw := os.Getenv("QUERY_COUNTS"); raw != "" {
		if parsed, err := parseQueryCounts(raw); err == nil {
			queryCounts = parsed
		}
	}

	bondBoards := []BondBoard{
		{Board: "TQOB", Label: "ofz", RedisKey: "bonds:ofz"},
		{Board: "TQCB", Label: "corporate", RedisKey: "bonds:corporate"},
	}
	if raw := os.Getenv("BOND_BOARDS"); raw != "" {
		if parsed, err := parseBondBoards(raw); err == nil {
			bondBoards = parsed
		}
	}

	return Config{
		ServiceName:       getEnvOrDefault("SERVICE_NAME", "mysvc"),
		DepositsAPIURL:    getEnvOrDefault("DEPOSITS_API_URL", "https://www.banki.ru/products/deposits/api/group/moskva/"),
		MoexBaseURL:       getEnvOrDefault("MOEX_BASE_URL", "https://iss.moex.com"),
		DatabaseDSN:       getEnvOrDefault("DATABASE_URL", "postgres://postgres:postgres@localhost:5432/deposits?sslmode=disable"),
		RedisAddress:      getEnvOrDefault("REDIS_URL", "localhost:6379"),
		QueryCounts:       queryCounts,
		BondBoards:        bondBoards,
		DBMaxOpenConns:    4,
		DBConnMaxLifetime: 5 * time.Minute,
		DBConnMaxIdleTime: 5 * time.Minute,
		DBStartupTimeout:  30 * time.Second,
	}
}
