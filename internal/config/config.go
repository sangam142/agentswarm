package config

import (
	"os"
	"strconv"
	"time"
)

// Config holds all application configuration.
// In production, load from env vars or a YAML file.
type Config struct {
	// ── Data Sources ──
	AttenaBaseURL string
	AttenaAPIKey  string // optional, for higher rate limits
	NewsAPIKey    string // newsapi.org
	ClaudeAPIKey  string // Anthropic API

	// ── Exchanges ──
	KalshiAPIBase   string
	KalshiEmail     string
	KalshiPassword  string
	PolymarketRPC   string // Polygon RPC endpoint
	PolymarketKey   string // Private key for signing orders
	PolymarketCLOB  string // CLOB API base

	// ── Infrastructure ──
	PostgresDSN string
	RedisAddr   string
	NatsURL     string

	// ── Agent Defaults ──
	PollInterval     time.Duration
	MaxPositionSize  float64
	CircuitBreakerPct float64 // e.g. 0.05 = 5%
	CircuitBreakerWindow time.Duration

	// ── Server ──
	HTTPPort int
}

func Load() *Config {
	return &Config{
		// Data
		AttenaBaseURL: envOr("ATTENA_BASE_URL", "https://attena-api.fly.dev/api/search/"),
		AttenaAPIKey:  os.Getenv("ATTENA_API_KEY"),
		NewsAPIKey:    os.Getenv("NEWS_API_KEY"),
		ClaudeAPIKey:  os.Getenv("ANTHROPIC_API_KEY"),

		// Kalshi
		KalshiAPIBase:  envOr("KALSHI_API_BASE", "https://api.elections.kalshi.com/trade-api/v2"),
		KalshiEmail:    os.Getenv("KALSHI_EMAIL"),
		KalshiPassword: os.Getenv("KALSHI_PASSWORD"),

		// Polymarket
		PolymarketRPC:  envOr("POLYMARKET_RPC", "https://polygon-rpc.com"),
		PolymarketKey:  os.Getenv("POLYMARKET_PRIVATE_KEY"),
		PolymarketCLOB: envOr("POLYMARKET_CLOB", "https://clob.polymarket.com"),

		// Infra
		PostgresDSN: envOr("POSTGRES_DSN", "postgres://localhost:5432/agentswarm?sslmode=disable"),
		RedisAddr:   envOr("REDIS_ADDR", "localhost:6379"),
		NatsURL:     envOr("NATS_URL", "nats://localhost:4222"),

		// Defaults
		PollInterval:         durOr("POLL_INTERVAL", 30*time.Second),
		MaxPositionSize:      floatOr("MAX_POSITION_SIZE", 500),
		CircuitBreakerPct:    floatOr("CIRCUIT_BREAKER_PCT", 0.05),
		CircuitBreakerWindow: durOr("CIRCUIT_BREAKER_WINDOW", 30*time.Minute),

		// Server
		HTTPPort: intOr("HTTP_PORT", 8080),
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func intOr(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			return i
		}
	}
	return fallback
}

func floatOr(key string, fallback float64) float64 {
	if v := os.Getenv(key); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return fallback
}

func durOr(key string, fallback time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return fallback
}
