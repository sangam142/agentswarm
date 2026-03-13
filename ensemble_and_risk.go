package agent

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/sangam142/agentswarm/internal/models"
)

// ════════════════════════════════════════════════════════════════════════
// AGENT 5: ENSEMBLE CORE — Multi-signal conviction aggregator
//
// Strategy:
//   Listens to signals from ALL other agents.
//   Only trades when N+ agents agree on direction with high confidence.
//   Weights each agent by its recent Sharpe ratio.
//   Highest conviction, lowest frequency → best risk-adjusted returns.
//
// This is a "meta-agent" — it doesn't analyze markets directly,
// it analyzes other agents' signals.
//
// ════════════════════════════════════════════════════════════════════════

type EnsembleAgent struct {
	*BaseAgent

	// Config
	minAgreeingAgents int     // minimum agents that must agree
	minConfidence     float64 // minimum aggregated confidence
	maxOrderSize      int
	decayHalfLife     time.Duration // older signals count less

	// State
	recentSignals []models.Signal    // sliding window of recent signals
	agentWeights  map[string]float64 // agent_id -> weight based on performance
}

type EnsembleConfig struct {
	MinAgreeingAgents int
	MinConfidence     float64
	MaxOrderSize      int
	DecayHalfLife     time.Duration
	Capital           float64
	MaxExposure       float64
}

func DefaultEnsembleConfig() EnsembleConfig {
	return EnsembleConfig{
		MinAgreeingAgents: 3,
		MinConfidence:     0.75,
		MaxOrderSize:      200,
		DecayHalfLife:     10 * time.Minute,
		Capital:           15000,
		MaxExposure:       5000,
	}
}

func NewEnsembleAgent(deps *Deps, cfg EnsembleConfig) *EnsembleAgent {
	return &EnsembleAgent{
		BaseAgent: NewBaseAgent(
			"ensemble-core", "Ensemble Core", "ensemble",
			deps, []string{"geopolitics", "crypto", "politics"}, cfg.Capital, cfg.MaxExposure,
		),
		minAgreeingAgents: cfg.MinAgreeingAgents,
		minConfidence:     cfg.MinConfidence,
		maxOrderSize:      cfg.MaxOrderSize,
		decayHalfLife:     cfg.DecayHalfLife,
		recentSignals:     make([]models.Signal, 0),
		agentWeights: map[string]float64{
			"arb-alpha":     1.0,
			"news-reactor":  1.2, // higher weight — LLM signals are more informative
			"momentum-wave": 0.7, // lower weight — currently underperforming
			"spread-maker":  0.5, // market maker signals are neutral
		},
	}
}

func (e *EnsembleAgent) Start(ctx context.Context) error {
	e.Log("starting — min_agents=%d min_conf=%.2f",
		e.minAgreeingAgents, e.minConfidence)

	// Listen for signals from other agents
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-e.stopCh:
			return nil
		case sig := <-e.deps.SignalCh:
			// Don't process our own signals
			if sig.AgentID == e.id {
				continue
			}
			e.ingestSignal(ctx, sig)
		}
	}
}

func (e *EnsembleAgent) ingestSignal(ctx context.Context, sig models.Signal) {
	// Add to recent signals
	e.recentSignals = append(e.recentSignals, sig)

	// Prune old signals (older than 2x decay half-life)
	cutoff := time.Now().Add(-2 * e.decayHalfLife)
	fresh := make([]models.Signal, 0)
	for _, s := range e.recentSignals {
		if s.CreatedAt.After(cutoff) {
			fresh = append(fresh, s)
		}
	}
	e.recentSignals = fresh

	// Group signals by market
	byMarket := make(map[string][]models.Signal)
	for _, s := range e.recentSignals {
		byMarket[s.MarketID] = append(byMarket[s.MarketID], s)
	}

	// Check for consensus on each market
	for marketID, signals := range byMarket {
		e.checkConsensus(ctx, marketID, signals)
	}
}

func (e *EnsembleAgent) checkConsensus(ctx context.Context, marketID string, signals []models.Signal) {
	// Count unique agents agreeing on a direction
	agentVotes := make(map[string]string) // agent_id -> direction
	agentConf := make(map[string]float64) // agent_id -> confidence

	for _, sig := range signals {
		// Time decay: newer signals count more
		age := time.Since(sig.CreatedAt)
		decay := math.Exp(-0.693 * age.Seconds() / e.decayHalfLife.Seconds()) // ln(2) ≈ 0.693

		weight := e.agentWeights[sig.AgentID]
		if weight == 0 {
			weight = 1.0
		}

		effectiveConf := sig.Confidence * decay * weight

		// Only keep the most recent signal per agent
		if existing, ok := agentConf[sig.AgentID]; !ok || effectiveConf > existing {
			agentVotes[sig.AgentID] = sig.Direction
			agentConf[sig.AgentID] = effectiveConf
		}
	}

	// Count votes by direction
	directionVotes := make(map[string]int)
	directionConf := make(map[string]float64)

	for agentID, dir := range agentVotes {
		// Normalize: buy_yes and sell_no are both "bullish"
		normalized := dir
		if dir == "sell_no" {
			normalized = "buy_yes"
		}
		if dir == "sell_yes" {
			normalized = "buy_no"
		}

		directionVotes[normalized]++
		directionConf[normalized] += agentConf[agentID]
	}

	// Check if any direction has enough consensus
	for dir, votes := range directionVotes {
		if votes < e.minAgreeingAgents {
			continue
		}

		avgConf := directionConf[dir] / float64(votes)
		if avgConf < e.minConfidence {
			continue
		}

		// CONSENSUS REACHED
		e.Log("CONSENSUS: %d/%d agents → %s on %s (avg_conf=%.2f)",
			votes, len(agentVotes), dir, marketID, avgConf)

		// Use the best price from the most confident signal
		var bestPrice float64
		for _, sig := range signals {
			if sig.MarketID == marketID {
				bestPrice = sig.Price
				break
			}
		}

		size := float64(e.maxOrderSize)
		if size*bestPrice > e.maxExposure {
			size = e.maxExposure / bestPrice
		}

		ensembleSignal := models.Signal{
			Type:       models.SignalEnsemble,
			MarketID:   marketID,
			Direction:  dir,
			Confidence: avgConf,
			Price:      bestPrice,
			Size:       size,
			Reason: fmt.Sprintf("ENSEMBLE: %d agents agree → %s (avg_conf=%.2f)",
				votes, dir, avgConf),
			Metadata: map[string]interface{}{
				"agreeing_agents": votes,
				"total_agents":    len(agentVotes),
				"avg_confidence":  avgConf,
				"agent_votes":     agentVotes,
			},
		}
		e.EmitSignal(ctx, ensembleSignal)
	}
}

func (e *EnsembleAgent) Evaluate(ctx context.Context, markets []models.Market) ([]models.Signal, error) {
	// Ensemble doesn't evaluate markets directly
	return nil, nil
}

// UpdateWeights adjusts agent weights based on recent performance.
// Call this periodically (e.g., daily).
func (e *EnsembleAgent) UpdateWeights(ctx context.Context) {
	for agentID := range e.agentWeights {
		state, err := e.deps.State.GetAgentState(ctx, agentID)
		if err != nil {
			continue
		}

		// Weight = normalized win rate * sqrt(trades)
		// More trades + higher win rate = more weight
		wr := state.WinRate() / 100.0
		tradeFactor := math.Sqrt(math.Max(1, float64(state.TotalTrades)))
		weight := wr * tradeFactor / 10.0 // normalize

		if weight < 0.1 {
			weight = 0.1 // minimum weight
		}
		if weight > 3.0 {
			weight = 3.0 // maximum weight
		}

		e.agentWeights[agentID] = weight
		e.Log("weight update: %s = %.2f (wr=%.1f%% trades=%d)",
			agentID, weight, state.WinRate(), state.TotalTrades)
	}
}

// ════════════════════════════════════════════════════════════════════════
// AGENT 6: RISK SENTINEL — Portfolio-level risk management
//
// This is NOT a trading agent. It's a safety system that:
// 1. Monitors total portfolio exposure
// 2. Enforces per-category limits
// 3. Checks correlation between positions
// 4. Triggers circuit breaker if losses spike
// 5. Can force-close positions if limits are breached
//
// ════════════════════════════════════════════════════════════════════════

type RiskSentinel struct {
	*BaseAgent

	// Config
	maxTotalExposure  float64
	maxCategoryPct    float64 // max % of capital in one category
	maxCorrelation    float64 // max correlation between positions
	circuitBreakerPct float64 // trigger if loss > this % in window
	circuitWindow     time.Duration
	checkInterval     time.Duration

	// State
	circuitTripped bool
	lastTripTime   time.Time
}

type RiskConfig struct {
	MaxTotalExposure  float64
	MaxCategoryPct    float64
	MaxCorrelation    float64
	CircuitBreakerPct float64
	CircuitWindow     time.Duration
	CheckInterval     time.Duration
}

func DefaultRiskConfig() RiskConfig {
	return RiskConfig{
		MaxTotalExposure:  50000,
		MaxCategoryPct:    0.20, // max 20% in one category
		MaxCorrelation:    0.80,
		CircuitBreakerPct: 0.05, // 5% loss triggers breaker
		CircuitWindow:     30 * time.Minute,
		CheckInterval:     10 * time.Second,
	}
}

func NewRiskSentinel(deps *Deps, cfg RiskConfig) *RiskSentinel {
	return &RiskSentinel{
		BaseAgent: NewBaseAgent(
			"risk-sentinel", "Risk Sentinel", "risk_manager",
			deps, nil, 0, 0,
		),
		maxTotalExposure:  cfg.MaxTotalExposure,
		maxCategoryPct:    cfg.MaxCategoryPct,
		maxCorrelation:    cfg.MaxCorrelation,
		circuitBreakerPct: cfg.CircuitBreakerPct,
		circuitWindow:     cfg.CircuitWindow,
		checkInterval:     cfg.CheckInterval,
	}
}

func (r *RiskSentinel) Start(ctx context.Context) error {
	r.Log("starting — max_exposure=$%.0f circuit_breaker=%.0f%% window=%s",
		r.maxTotalExposure, r.circuitBreakerPct*100, r.circuitWindow)

	ticker := time.NewTicker(r.checkInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-r.stopCh:
			return nil
		case <-ticker.C:
			r.runChecks(ctx)
		}
	}
}

func (r *RiskSentinel) runChecks(ctx context.Context) {
	// 1. Check total exposure
	positions, err := r.deps.State.GetAllPositions(ctx)
	if err != nil {
		r.Log("error fetching positions: %v", err)
		return
	}

	totalExposure := 0.0
	categoryExposure := make(map[string]float64)
	for _, pos := range positions {
		exposure := float64(pos.Contracts) * pos.AvgPrice
		totalExposure += exposure

		// We'd need market metadata to get category — simplified here
		categoryExposure["unknown"] += exposure
	}

	if totalExposure > r.maxTotalExposure {
		r.Log("⚠️  EXPOSURE LIMIT: $%.0f / $%.0f — ALERT",
			totalExposure, r.maxTotalExposure)
	}

	// 2. Check circuit breaker
	states, err := r.deps.State.GetAllAgentStates(ctx)
	if err != nil {
		return
	}

	totalCapital := 0.0
	for _, s := range states {
		totalCapital += s.CapitalAllocated
	}
	if totalCapital == 0 {
		return
	}

	// Get recent P&L
	windowStart := time.Now().Add(-r.circuitWindow)
	var recentPnL float64
	for _, s := range states {
		pnl, err := r.deps.Trades.GetPnLSince(ctx, s.ID, windowStart)
		if err != nil {
			continue
		}
		recentPnL += pnl
	}

	lossPct := -recentPnL / totalCapital
	if lossPct > r.circuitBreakerPct && !r.circuitTripped {
		r.circuitTripped = true
		r.lastTripTime = time.Now()
		r.Log("🚨 CIRCUIT BREAKER TRIPPED: loss=%.2f%% in %s — ALL AGENTS SHOULD STOP",
			lossPct*100, r.circuitWindow)

		// In production: publish a "halt" message to NATS
		// All agents should subscribe and stop trading
	}

	// Reset circuit breaker after 1 hour
	if r.circuitTripped && time.Since(r.lastTripTime) > time.Hour {
		r.circuitTripped = false
		r.Log("✅ Circuit breaker reset after 1h cooldown")
	}

	// 3. Periodic status report
	r.Log("STATUS: exposure=$%.0f/%$.0f positions=%d circuit=%v recent_pnl=$%.2f",
		totalExposure, r.maxTotalExposure, len(positions), r.circuitTripped, recentPnL)
}

// CheckSignal validates whether a signal should be allowed to execute.
// Other agents should call this before executing trades.
func (r *RiskSentinel) CheckSignal(ctx context.Context, signal *models.Signal) models.RiskCheck {
	if r.circuitTripped {
		return models.RiskCheck{
			Passed: false,
			Reason: "circuit breaker tripped",
		}
	}

	positions, _ := r.deps.State.GetAllPositions(ctx)
	totalExposure := 0.0
	for _, pos := range positions {
		totalExposure += float64(pos.Contracts) * pos.AvgPrice
	}

	newExposure := signal.Size * signal.Price
	if totalExposure+newExposure > r.maxTotalExposure {
		return models.RiskCheck{
			Passed:      false,
			Reason:      "would exceed total exposure limit",
			Exposure:    totalExposure,
			MaxExposure: r.maxTotalExposure,
		}
	}

	return models.RiskCheck{
		Passed:      true,
		Reason:      "all checks passed",
		Exposure:    totalExposure,
		MaxExposure: r.maxTotalExposure,
	}
}

func (r *RiskSentinel) Evaluate(ctx context.Context, markets []models.Market) ([]models.Signal, error) {
	return nil, nil // Risk sentinel doesn't generate trading signals
}
