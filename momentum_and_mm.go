package agent

import (
	"context"
	"fmt"
	"math"
	"time"

	"agentswarm/internal/models"
)

// ════════════════════════════════════════════════════════════════════════
// AGENT 3: MOMENTUM WAVE — Sentiment / Momentum tracker
//
// Strategy:
//   Monitors market price momentum and volume spikes.
//   Uses rolling z-scores to detect sharp moves before they complete.
//   In production, augment with social sentiment from X/Reddit APIs.
//
// Training (when you add ML):
//   Features: 1h/6h/24h price change, volume ratio, time-to-close,
//             social sentiment z-score, category momentum
//   Target:   "price moves >5% in next 2 hours" (binary)
//   Model:    XGBoost or logistic regression
//   Data:     2-4 weeks of Attena API snapshots
//
// ════════════════════════════════════════════════════════════════════════

type MomentumAgent struct {
	*BaseAgent

	// Config
	zScoreThreshold float64 // minimum z-score to trigger signal
	lookbackWindow  int     // number of snapshots for rolling stats
	maxOrderSize    int
	maxDrawdown     float64 // pause if cumulative loss exceeds this

	// State — rolling price history for each market
	priceHistory  map[string][]float64 // market_id -> recent prices
	volumeHistory map[string][]float64
}

type MomentumConfig struct {
	ZScoreThreshold float64
	LookbackWindow  int
	MaxOrderSize    int
	MaxDrawdown     float64
	Categories      []string
	Capital         float64
	MaxExposure     float64
}

func DefaultMomentumConfig() MomentumConfig {
	return MomentumConfig{
		ZScoreThreshold: 2.0, // 2 standard deviations
		LookbackWindow:  20,  // 20 data points
		MaxOrderSize:    30,
		MaxDrawdown:     200, // pause at -$200
		Categories:      []string{"crypto", "entertainment"},
		Capital:         3000,
		MaxExposure:     500,
	}
}

func NewMomentumAgent(deps *Deps, cfg MomentumConfig) *MomentumAgent {
	return &MomentumAgent{
		BaseAgent: NewBaseAgent(
			"momentum-wave", "Momentum Wave", "sentiment",
			deps, cfg.Categories, cfg.Capital, cfg.MaxExposure,
		),
		zScoreThreshold: cfg.ZScoreThreshold,
		lookbackWindow:  cfg.LookbackWindow,
		maxOrderSize:    cfg.MaxOrderSize,
		maxDrawdown:     cfg.MaxDrawdown,
		priceHistory:    make(map[string][]float64),
		volumeHistory:   make(map[string][]float64),
	}
}

func (m *MomentumAgent) Start(ctx context.Context) error {
	m.Log("starting — z_threshold=%.1f lookback=%d max_drawdown=$%.0f",
		m.zScoreThreshold, m.lookbackWindow, m.maxDrawdown)

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-m.stopCh:
			return nil
		case <-ticker.C:
			// Check drawdown circuit breaker
			state := m.Status()
			if state.TotalPnL < -m.maxDrawdown {
				if state.Status != models.AgentPaused {
					m.Log("DRAWDOWN LIMIT: P&L = $%.2f — PAUSING", state.TotalPnL)
					m.mu.Lock()
					m.state.Status = models.AgentPaused
					m.mu.Unlock()
				}
				continue
			}

			// Fetch trending markets and evaluate
			markets, err := m.deps.Attena.FetchTrending(ctx, 30)
			if err != nil {
				m.Log("fetch error: %v", err)
				continue
			}

			signals, err := m.Evaluate(ctx, markets)
			if err != nil {
				m.Log("eval error: %v", err)
				continue
			}

			for _, sig := range signals {
				m.EmitSignal(ctx, sig)
			}
		}
	}
}

func (m *MomentumAgent) Evaluate(ctx context.Context, markets []models.Market) ([]models.Signal, error) {
	var signals []models.Signal

	for _, mkt := range markets {
		// Update price history
		hist := m.priceHistory[mkt.MarketID]
		hist = append(hist, mkt.YesPrice)
		if len(hist) > m.lookbackWindow {
			hist = hist[len(hist)-m.lookbackWindow:]
		}
		m.priceHistory[mkt.MarketID] = hist

		// Update volume history
		vhist := m.volumeHistory[mkt.MarketID]
		vhist = append(vhist, mkt.Volume24h)
		if len(vhist) > m.lookbackWindow {
			vhist = vhist[len(vhist)-m.lookbackWindow:]
		}
		m.volumeHistory[mkt.MarketID] = vhist

		// Need minimum data points
		if len(hist) < 5 {
			continue
		}

		// Calculate z-score of latest price
		mean, stddev := meanStd(hist[:len(hist)-1])
		if stddev < 0.005 {
			continue // too stable, no signal
		}

		current := hist[len(hist)-1]
		zScore := (current - mean) / stddev

		// Check volume spike (is current volume > 2x average?)
		volumeSpike := false
		if len(vhist) > 3 {
			vMean, _ := meanStd(vhist[:len(vhist)-1])
			if vhist[len(vhist)-1] > vMean*2 {
				volumeSpike = true
			}
		}

		// Generate signal if z-score exceeds threshold
		if math.Abs(zScore) >= m.zScoreThreshold {
			confidence := math.Min(1.0, math.Abs(zScore)/4.0)
			if volumeSpike {
				confidence = math.Min(1.0, confidence*1.3)
			}

			direction := "buy_yes"
			price := mkt.YesPrice
			if zScore < 0 {
				// Price dropped sharply — momentum is downward
				direction = "buy_no"
				price = mkt.NoPrice
			}

			size := float64(m.maxOrderSize)
			if size*price > m.maxExposure {
				size = m.maxExposure / price
			}

			signals = append(signals, models.Signal{
				Type:       models.SignalMomentum,
				MarketID:   mkt.MarketID,
				Direction:  direction,
				Confidence: confidence,
				Price:      price,
				Size:       size,
				Reason: fmt.Sprintf("MOMENTUM: z=%.2f (threshold=%.1f) vol_spike=%v on '%s'",
					zScore, m.zScoreThreshold, volumeSpike, mkt.Title),
				Metadata: map[string]interface{}{
					"z_score":      zScore,
					"volume_spike": volumeSpike,
					"mean_price":   mean,
					"stddev":       stddev,
					"platform":     mkt.Source,
				},
			})
		}
	}

	return signals, nil
}

func meanStd(data []float64) (float64, float64) {
	if len(data) == 0 {
		return 0, 0
	}
	sum := 0.0
	for _, v := range data {
		sum += v
	}
	mean := sum / float64(len(data))

	variance := 0.0
	for _, v := range data {
		diff := v - mean
		variance += diff * diff
	}
	variance /= float64(len(data))
	return mean, math.Sqrt(variance)
}

// ════════════════════════════════════════════════════════════════════════
// AGENT 4: SPREAD MAKER — Market making / liquidity provision
//
// Strategy:
//   Places limit orders on BOTH sides of illiquid markets.
//   Captures the bid-ask spread as profit.
//   Strict inventory management prevents one-sided exposure.
//
// Key parameters:
//   - Target spread: how wide to quote (wider = safer but fewer fills)
//   - Max inventory: maximum net position before hedging
//   - Rebalance threshold: when to cancel and re-quote
//
// ════════════════════════════════════════════════════════════════════════

type SpreadMakerAgent struct {
	*BaseAgent

	// Config
	targetSpread float64 // how wide to quote around mid (e.g., 0.05)
	maxInventory int     // max net contracts before stopping
	minVolume    float64 // minimum 24h volume to consider
	maxVolume    float64 // maximum volume (too liquid = no edge)
	rebalancePct float64 // re-quote if mid moves more than this
	maxOrderSize int

	// State
	inventory  map[string]int     // market_id -> net position (-ve = short YES)
	lastQuotes map[string]float64 // market_id -> last mid price quoted at
}

type SpreadMakerConfig struct {
	TargetSpread float64
	MaxInventory int
	MinVolume    float64
	MaxVolume    float64
	RebalancePct float64
	MaxOrderSize int
	Categories   []string
	Capital      float64
	MaxExposure  float64
}

func DefaultSpreadMakerConfig() SpreadMakerConfig {
	return SpreadMakerConfig{
		TargetSpread: 0.05, // 5 cent spread
		MaxInventory: 100,
		MinVolume:    1000,   // min $1K volume
		MaxVolume:    100000, // max $100K (above this, market is too efficient)
		RebalancePct: 0.02,   // re-quote if mid moves 2%
		MaxOrderSize: 20,
		Categories:   []string{"politics", "economics"},
		Capital:      10000,
		MaxExposure:  800,
	}
}

func NewSpreadMakerAgent(deps *Deps, cfg SpreadMakerConfig) *SpreadMakerAgent {
	return &SpreadMakerAgent{
		BaseAgent: NewBaseAgent(
			"spread-maker", "Spread Maker", "market_maker",
			deps, cfg.Categories, cfg.Capital, cfg.MaxExposure,
		),
		targetSpread: cfg.TargetSpread,
		maxInventory: cfg.MaxInventory,
		minVolume:    cfg.MinVolume,
		maxVolume:    cfg.MaxVolume,
		rebalancePct: cfg.RebalancePct,
		maxOrderSize: cfg.MaxOrderSize,
		inventory:    make(map[string]int),
		lastQuotes:   make(map[string]float64),
	}
}

func (s *SpreadMakerAgent) Start(ctx context.Context) error {
	s.Log("starting — spread=%.2f max_inv=%d", s.targetSpread, s.maxInventory)

	ticker := time.NewTicker(20 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-s.stopCh:
			return nil
		case <-ticker.C:
			for _, cat := range s.categories {
				markets, err := s.deps.Attena.FetchByCategory(ctx, cat, 20)
				if err != nil {
					s.Log("fetch error [%s]: %v", cat, err)
					continue
				}
				signals, _ := s.Evaluate(ctx, markets)
				for _, sig := range signals {
					s.EmitSignal(ctx, sig)
				}
			}
		}
	}
}

func (s *SpreadMakerAgent) Evaluate(ctx context.Context, markets []models.Market) ([]models.Signal, error) {
	var signals []models.Signal

	for _, mkt := range markets {
		// Filter: only quote on markets with appropriate liquidity
		if mkt.Volume24h < s.minVolume || mkt.Volume24h > s.maxVolume {
			continue
		}

		// Skip if close to expiry (< 24h)
		if !mkt.CloseTime.IsZero() && time.Until(mkt.CloseTime) < 24*time.Hour {
			continue
		}

		// Check inventory limits
		inv := s.inventory[mkt.MarketID]
		if math.Abs(float64(inv)) >= float64(s.maxInventory) {
			continue
		}

		// Calculate mid price and our quotes
		mid := (mkt.YesPrice + (1 - mkt.NoPrice)) / 2
		if mid < 0.05 || mid > 0.95 {
			continue // don't quote near extremes
		}

		// Check if we need to rebalance
		lastMid, quoted := s.lastQuotes[mkt.MarketID]
		if quoted && math.Abs(mid-lastMid) < s.rebalancePct {
			continue // mid hasn't moved enough to re-quote
		}

		halfSpread := s.targetSpread / 2
		bidPrice := mid - halfSpread
		askPrice := mid + halfSpread

		// Adjust for inventory: if we're long, make ask more aggressive
		invAdj := float64(inv) * 0.001
		bidPrice -= invAdj
		askPrice -= invAdj

		bidPrice = math.Max(0.02, math.Min(0.98, bidPrice))
		askPrice = math.Max(0.02, math.Min(0.98, askPrice))

		size := s.maxOrderSize
		if float64(size)*askPrice > s.maxExposure {
			size = int(s.maxExposure / askPrice)
		}
		if size < 1 {
			continue
		}

		// Emit BID (buy YES at lower price)
		signals = append(signals, models.Signal{
			Type:       models.SignalSentiment, // reusing type
			MarketID:   mkt.MarketID,
			Direction:  "buy_yes",
			Confidence: 0.5, // neutral — we're making a market, not predicting
			Price:      bidPrice,
			Size:       float64(size),
			Reason: fmt.Sprintf("MM BID: %.3f on '%s' (mid=%.3f spread=%.3f inv=%d)",
				bidPrice, mkt.Title, mid, s.targetSpread, inv),
			Metadata: map[string]interface{}{
				"strategy":  "market_maker",
				"side":      "bid",
				"mid":       mid,
				"spread":    s.targetSpread,
				"inventory": inv,
				"platform":  mkt.Source,
			},
		})

		// Emit ASK (sell YES / buy NO at higher price)
		signals = append(signals, models.Signal{
			Type:       models.SignalSentiment,
			MarketID:   mkt.MarketID,
			Direction:  "sell_yes",
			Confidence: 0.5,
			Price:      askPrice,
			Size:       float64(size),
			Reason: fmt.Sprintf("MM ASK: %.3f on '%s' (mid=%.3f spread=%.3f inv=%d)",
				askPrice, mkt.Title, mid, s.targetSpread, inv),
			Metadata: map[string]interface{}{
				"strategy":  "market_maker",
				"side":      "ask",
				"mid":       mid,
				"spread":    s.targetSpread,
				"inventory": inv,
				"platform":  mkt.Source,
			},
		})

		s.lastQuotes[mkt.MarketID] = mid
	}

	return signals, nil
}
