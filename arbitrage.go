package agent

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/sangam142/agentswarm/internal/models"
)

// ════════════════════════════════════════════════════════════════════════
// ARBITRAGE AGENT
//
// Strategy:
//   Monitors identical events on Kalshi and Polymarket via Attena API.
//   When the same event has a price spread > threshold (after fees),
//   buys the cheap side and sells the expensive side for risk-free profit.
//
// How it works:
//   1. Fetch markets from both platforms for each tracked category
//   2. Match markets across platforms using title similarity
//   3. Calculate spread = |kalshi_yes - poly_yes|
//   4. If spread > fee_threshold, emit arb signal
//   5. Execute simultaneously on both platforms
//
// Fees:
//   Kalshi: ~2-7% of profit (varies by market)
//   Polymarket: ~1-2% taker fee
//   Minimum spread for profitability: typically 4-6 cents
//
// ════════════════════════════════════════════════════════════════════════

type ArbitrageAgent struct {
	*BaseAgent

	// Config
	minSpread      float64 // minimum spread in price points to trigger (e.g., 0.04)
	feeEstimate    float64 // estimated round-trip fee as fraction (e.g., 0.03)
	maxOrderSize   int     // max contracts per order
	scanCategories []string
	scanInterval   time.Duration
	minVolume      float64 // minimum volume to consider a market

	// State
	knownPairs map[string]*models.MarketPair
}

type ArbitrageConfig struct {
	MinSpread      float64
	FeeEstimate    float64
	MaxOrderSize   int
	ScanCategories []string
	ScanInterval   time.Duration
	MinVolume      float64
	Capital        float64
	MaxExposure    float64
}

func DefaultArbitrageConfig() ArbitrageConfig {
	return ArbitrageConfig{
		MinSpread:      0.04, // 4 cents minimum spread
		FeeEstimate:    0.03, // ~3% round-trip fees
		MaxOrderSize:   50,   // 50 contracts max
		ScanCategories: []string{"crypto", "politics", "geopolitics"},
		ScanInterval:   30 * time.Second,
		MinVolume:      10000, // $10K minimum volume
		Capital:        5000,
		MaxExposure:    1000,
	}
}

func NewArbitrageAgent(deps *Deps, cfg ArbitrageConfig) *ArbitrageAgent {
	return &ArbitrageAgent{
		BaseAgent: NewBaseAgent(
			"arb-alpha", "Arbitrage Alpha", "arbitrage",
			deps, cfg.ScanCategories, cfg.Capital, cfg.MaxExposure,
		),
		minSpread:      cfg.MinSpread,
		feeEstimate:    cfg.FeeEstimate,
		maxOrderSize:   cfg.MaxOrderSize,
		scanCategories: cfg.ScanCategories,
		scanInterval:   cfg.ScanInterval,
		minVolume:      cfg.MinVolume,
		knownPairs:     make(map[string]*models.MarketPair),
	}
}

// Start begins the arbitrage scanning loop.
func (a *ArbitrageAgent) Start(ctx context.Context) error {
	a.Log("starting — min_spread=%.2f fee_est=%.2f categories=%v",
		a.minSpread, a.feeEstimate, a.scanCategories)

	ticker := time.NewTicker(a.scanInterval)
	defer ticker.Stop()

	// Initial scan
	a.scan(ctx)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-a.stopCh:
			return nil
		case <-ticker.C:
			a.scan(ctx)
		}
	}
}

// scan fetches markets from both platforms and looks for arbitrage.
func (a *ArbitrageAgent) scan(ctx context.Context) {
	for _, category := range a.scanCategories {
		pairs, err := a.findPairs(ctx, category)
		if err != nil {
			a.Log("scan error [%s]: %v", category, err)
			continue
		}

		for _, pair := range pairs {
			if pair.Spread > a.minSpread+a.feeEstimate {
				a.Log("ARB DETECTED: %s spread=%.3f (threshold=%.3f)",
					pair.EventKey, pair.Spread, a.minSpread+a.feeEstimate)

				signals, err := a.generateArbSignals(pair)
				if err != nil {
					a.Log("signal error: %v", err)
					continue
				}

				for _, sig := range signals {
					a.EmitSignal(ctx, sig)
				}
			}
		}
	}
}

// findPairs fetches markets from both platforms and matches them.
func (a *ArbitrageAgent) findPairs(ctx context.Context, category string) ([]models.MarketPair, error) {
	// Fetch from both platforms via Attena
	kalshi, poly, err := a.deps.Attena.SearchAll(ctx, category, 50)
	if err != nil {
		return nil, err
	}

	a.Log("scan [%s]: kalshi=%d poly=%d", category, len(kalshi), len(poly))

	// Match markets across platforms by title similarity
	var pairs []models.MarketPair
	for _, k := range kalshi {
		if k.Volume < a.minVolume {
			continue
		}
		for _, p := range poly {
			if p.Volume < a.minVolume {
				continue
			}
			if matchMarkets(&k, &p) {
				spread := math.Abs(k.YesPrice - p.YesPrice)
				pair := models.MarketPair{
					EventKey:     normalizeTitle(k.Title),
					KalshiMarket: &k,
					PolyMarket:   &p,
					Spread:       spread,
					SpreadPct:    spread / math.Max(k.YesPrice, p.YesPrice) * 100,
					DetectedAt:   time.Now(),
				}
				pairs = append(pairs, pair)
				a.knownPairs[pair.EventKey] = &pair
			}
		}
	}

	return pairs, nil
}

// matchMarkets checks if two markets from different platforms refer to the same event.
func matchMarkets(a, b *models.Market) bool {
	// Strategy 1: Same subcategory and similar title
	if a.Subcategory != "" && a.Subcategory == b.Subcategory {
		sim := titleSimilarity(a.Title, b.Title)
		if sim > 0.5 {
			return true
		}
	}

	// Strategy 2: High title similarity alone
	return titleSimilarity(a.Title, b.Title) > 0.7
}

// titleSimilarity computes a simple word-overlap Jaccard similarity.
func titleSimilarity(a, b string) float64 {
	wordsA := tokenize(a)
	wordsB := tokenize(b)

	if len(wordsA) == 0 || len(wordsB) == 0 {
		return 0
	}

	setA := make(map[string]bool)
	for _, w := range wordsA {
		setA[w] = true
	}

	intersection := 0
	setB := make(map[string]bool)
	for _, w := range wordsB {
		setB[w] = true
		if setA[w] {
			intersection++
		}
	}

	union := len(setA)
	for w := range setB {
		if !setA[w] {
			union++
		}
	}

	return float64(intersection) / float64(union)
}

func tokenize(s string) []string {
	s = strings.ToLower(s)
	// Remove common stop words
	stop := map[string]bool{
		"the": true, "a": true, "an": true, "will": true, "by": true,
		"in": true, "of": true, "to": true, "and": true, "or": true,
		"be": true, "is": true, "at": true, "on": true, "for": true,
		"what": true, "which": true, "who": true, "how": true,
	}
	words := strings.Fields(s)
	var result []string
	for _, w := range words {
		w = strings.Trim(w, ".,!?\"'()[]{}:;-")
		if len(w) > 1 && !stop[w] {
			result = append(result, w)
		}
	}
	return result
}

func normalizeTitle(s string) string {
	tokens := tokenize(s)
	return strings.Join(tokens, "_")
}

// generateArbSignals creates buy/sell signals for an arbitrage pair.
func (a *ArbitrageAgent) generateArbSignals(pair models.MarketPair) ([]models.Signal, error) {
	k := pair.KalshiMarket
	p := pair.PolyMarket

	var signals []models.Signal

	netSpread := pair.Spread - a.feeEstimate
	if netSpread <= 0 {
		return nil, fmt.Errorf("spread %.3f below fee threshold %.3f", pair.Spread, a.feeEstimate)
	}

	confidence := math.Min(1.0, netSpread/0.10) // Higher spread = higher confidence

	size := a.maxOrderSize
	cost := float64(size) * math.Max(k.YesPrice, p.YesPrice)
	if cost > a.maxExposure {
		size = int(a.maxExposure / math.Max(k.YesPrice, p.YesPrice))
	}
	if size < 1 {
		return nil, fmt.Errorf("position too small after sizing")
	}

	if k.YesPrice < p.YesPrice {
		// Kalshi is cheaper — buy YES on Kalshi, sell YES (buy NO) on Polymarket
		signals = append(signals, models.Signal{
			Type:       models.SignalArbitrage,
			MarketID:   k.MarketID,
			Direction:  "buy_yes",
			Confidence: confidence,
			Price:      k.YesPrice,
			Size:       float64(size),
			Reason:     fmt.Sprintf("ARB: buy Kalshi YES @ %.3f, sell Poly YES @ %.3f, spread=%.3f", k.YesPrice, p.YesPrice, pair.Spread),
			Metadata: map[string]interface{}{
				"pair_key":   pair.EventKey,
				"platform":   "kalshi",
				"counter":    "polymarket",
				"spread":     pair.Spread,
				"net_spread": netSpread,
			},
		})
		signals = append(signals, models.Signal{
			Type:       models.SignalArbitrage,
			MarketID:   p.MarketID,
			Direction:  "buy_no",
			Confidence: confidence,
			Price:      p.NoPrice,
			Size:       float64(size),
			Reason:     fmt.Sprintf("ARB: counter-leg sell Poly YES (buy NO) @ %.3f", p.NoPrice),
			Metadata: map[string]interface{}{
				"pair_key": pair.EventKey,
				"platform": "polymarket",
				"counter":  "kalshi",
				"spread":   pair.Spread,
			},
		})
	} else {
		// Polymarket is cheaper — buy YES on Poly, sell YES (buy NO) on Kalshi
		signals = append(signals, models.Signal{
			Type:       models.SignalArbitrage,
			MarketID:   p.MarketID,
			Direction:  "buy_yes",
			Confidence: confidence,
			Price:      p.YesPrice,
			Size:       float64(size),
			Reason:     fmt.Sprintf("ARB: buy Poly YES @ %.3f, sell Kalshi YES @ %.3f, spread=%.3f", p.YesPrice, k.YesPrice, pair.Spread),
			Metadata: map[string]interface{}{
				"pair_key":   pair.EventKey,
				"platform":   "polymarket",
				"counter":    "kalshi",
				"spread":     pair.Spread,
				"net_spread": netSpread,
			},
		})
		signals = append(signals, models.Signal{
			Type:       models.SignalArbitrage,
			MarketID:   k.MarketID,
			Direction:  "buy_no",
			Confidence: confidence,
			Price:      k.NoPrice,
			Size:       float64(size),
			Reason:     fmt.Sprintf("ARB: counter-leg sell Kalshi YES (buy NO) @ %.3f", k.NoPrice),
			Metadata: map[string]interface{}{
				"pair_key": pair.EventKey,
				"platform": "kalshi",
				"counter":  "polymarket",
				"spread":   pair.Spread,
			},
		})
	}

	return signals, nil
}

// Evaluate implements the Agent interface for batch evaluation.
func (a *ArbitrageAgent) Evaluate(ctx context.Context, markets []models.Market) ([]models.Signal, error) {
	// Group by source
	kalshi := make([]models.Market, 0)
	poly := make([]models.Market, 0)
	for _, m := range markets {
		switch m.Source {
		case "kalshi":
			kalshi = append(kalshi, m)
		case "polymarket":
			poly = append(poly, m)
		}
	}

	var allSignals []models.Signal
	for _, k := range kalshi {
		for _, p := range poly {
			if matchMarkets(&k, &p) {
				spread := math.Abs(k.YesPrice - p.YesPrice)
				if spread > a.minSpread+a.feeEstimate {
					pair := models.MarketPair{
						EventKey:     normalizeTitle(k.Title),
						KalshiMarket: &k,
						PolyMarket:   &p,
						Spread:       spread,
						DetectedAt:   time.Now(),
					}
					signals, err := a.generateArbSignals(pair)
					if err != nil {
						continue
					}
					allSignals = append(allSignals, signals...)
				}
			}
		}
	}

	return allSignals, nil
}
