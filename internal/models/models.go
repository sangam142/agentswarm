package models

import (
	"time"
)

// ════════════════════════════════════════════════════════════════════════
// MARKET DATA
// ════════════════════════════════════════════════════════════════════════

// Market is a normalized prediction market from any source.
type Market struct {
	ID           string    `json:"id"`
	Title        string    `json:"title"`
	Category     string    `json:"category"`
	Subcategory  string    `json:"subcategory"`
	Source       string    `json:"source"` // "kalshi" or "polymarket"
	MarketID     string    `json:"market_id"`
	Ticker       string    `json:"ticker"`
	YesPrice     float64   `json:"yes_price"`
	NoPrice      float64   `json:"no_price"`
	Volume       float64   `json:"volume"`
	Volume24h    float64   `json:"volume_24h"`
	SourceURL    string    `json:"source_url"`
	CloseTime    time.Time `json:"close_time"`
	OutcomeLabel string    `json:"outcome_label"`
	BracketCount int       `json:"bracket_count"`
	FetchedAt    time.Time `json:"fetched_at"`
}

// MarketPair represents the same event available on two platforms.
// This is the fundamental unit for arbitrage.
type MarketPair struct {
	EventKey     string  // normalized key to match across platforms
	KalshiMarket *Market
	PolyMarket   *Market
	Spread       float64 // |kalshi.yes - poly.yes|
	SpreadPct    float64
	DetectedAt   time.Time
}

// ════════════════════════════════════════════════════════════════════════
// SIGNALS
// ════════════════════════════════════════════════════════════════════════

type SignalType string

const (
	SignalArbitrage  SignalType = "arbitrage"
	SignalNews       SignalType = "news"
	SignalSentiment  SignalType = "sentiment"
	SignalMomentum   SignalType = "momentum"
	SignalEnsemble   SignalType = "ensemble"
)

// Signal is emitted by agents when they detect a trading opportunity.
type Signal struct {
	ID         string     `json:"id"`
	AgentID    string     `json:"agent_id"`
	Type       SignalType `json:"type"`
	MarketID   string     `json:"market_id"`
	Direction  string     `json:"direction"` // "buy_yes", "buy_no", "sell_yes", "sell_no"
	Confidence float64    `json:"confidence"` // 0.0 - 1.0
	Price      float64    `json:"price"`
	Size       float64    `json:"size"` // dollar amount
	Reason     string     `json:"reason"`
	Metadata   map[string]interface{} `json:"metadata"`
	CreatedAt  time.Time  `json:"created_at"`
}

// ════════════════════════════════════════════════════════════════════════
// ORDERS & TRADES
// ════════════════════════════════════════════════════════════════════════

type OrderSide string

const (
	SideBuyYes  OrderSide = "buy_yes"
	SideBuyNo   OrderSide = "buy_no"
	SideSellYes OrderSide = "sell_yes"
	SideSellNo  OrderSide = "sell_no"
)

type OrderStatus string

const (
	StatusPending  OrderStatus = "pending"
	StatusFilled   OrderStatus = "filled"
	StatusPartial  OrderStatus = "partial"
	StatusCanceled OrderStatus = "canceled"
	StatusFailed   OrderStatus = "failed"
)

type OrderType string

const (
	OrderLimit  OrderType = "limit"
	OrderMarket OrderType = "market"
)

// Order represents a trade order sent to an exchange.
type Order struct {
	ID        string      `json:"id"`
	AgentID   string      `json:"agent_id"`
	SignalID  string      `json:"signal_id"`
	Platform  string      `json:"platform"` // "kalshi" or "polymarket"
	MarketID  string      `json:"market_id"`
	Side      OrderSide   `json:"side"`
	Type      OrderType   `json:"order_type"`
	Price     float64     `json:"price"`     // limit price (0-1)
	Size      int         `json:"size"`      // number of contracts
	Status    OrderStatus `json:"status"`
	FilledAt  float64     `json:"filled_at"` // actual fill price
	PnL       float64     `json:"pnl"`
	LatencyMs int64       `json:"latency_ms"`
	CreatedAt time.Time   `json:"created_at"`
	UpdatedAt time.Time   `json:"updated_at"`
}

// Position tracks an agent's current holdings in a market.
type Position struct {
	AgentID    string    `json:"agent_id"`
	Platform   string    `json:"platform"`
	MarketID   string    `json:"market_id"`
	Side       OrderSide `json:"side"`
	Contracts  int       `json:"contracts"`
	AvgPrice   float64   `json:"avg_price"`
	CurrentPnL float64   `json:"current_pnl"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// ════════════════════════════════════════════════════════════════════════
// AGENT STATE
// ════════════════════════════════════════════════════════════════════════

type AgentStatus string

const (
	AgentActive  AgentStatus = "active"
	AgentPaused  AgentStatus = "paused"
	AgentStopped AgentStatus = "stopped"
)

// AgentState is persisted to Redis for cross-process visibility.
type AgentState struct {
	ID              string      `json:"id"`
	Name            string      `json:"name"`
	Status          AgentStatus `json:"status"`
	TotalPnL        float64     `json:"total_pnl"`
	TotalTrades     int         `json:"total_trades"`
	WinCount        int         `json:"win_count"`
	LossCount       int         `json:"loss_count"`
	CapitalAllocated float64    `json:"capital_allocated"`
	CapitalUsed     float64     `json:"capital_used"`
	AvgLatencyMs    int64       `json:"avg_latency_ms"`
	LastSignalAt    time.Time   `json:"last_signal_at"`
	LastTradeAt     time.Time   `json:"last_trade_at"`
	UpdatedAt       time.Time   `json:"updated_at"`
}

func (s *AgentState) WinRate() float64 {
	total := s.WinCount + s.LossCount
	if total == 0 {
		return 0
	}
	return float64(s.WinCount) / float64(total) * 100
}

// ════════════════════════════════════════════════════════════════════════
// NEWS / EVENTS
// ════════════════════════════════════════════════════════════════════════

// NewsEvent from external feeds (NewsAPI, GDELT, RSS).
type NewsEvent struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Source    string    `json:"source"`
	URL       string    `json:"url"`
	Body      string    `json:"body"`
	Category  string    `json:"category"`
	Sentiment float64   `json:"sentiment"` // -1.0 to 1.0
	PublishedAt time.Time `json:"published_at"`
	IngestedAt  time.Time `json:"ingested_at"`
}

// ImpactAssessment is the LLM's evaluation of how a news event affects markets.
type ImpactAssessment struct {
	EventID     string   `json:"event_id"`
	MarketIDs   []string `json:"market_ids"`
	Direction   string   `json:"direction"` // "bullish", "bearish", "neutral"
	Magnitude   float64  `json:"magnitude"` // 0.0 - 1.0
	Confidence  float64  `json:"confidence"`
	Reasoning   string   `json:"reasoning"`
	AssessedAt  time.Time `json:"assessed_at"`
	LatencyMs   int64    `json:"latency_ms"`
}

// ════════════════════════════════════════════════════════════════════════
// RISK
// ════════════════════════════════════════════════════════════════════════

type RiskCheck struct {
	Passed       bool    `json:"passed"`
	Reason       string  `json:"reason"`
	Exposure     float64 `json:"exposure"`      // current total exposure
	MaxExposure  float64 `json:"max_exposure"`
	CategoryPct  float64 `json:"category_pct"`  // % of capital in this category
	CorrelationScore float64 `json:"correlation_score"`
}
