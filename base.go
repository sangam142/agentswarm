package agent

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/sangam142/agentswarm/internal/exchange"
	"github.com/sangam142/agentswarm/internal/models"
	"github.com/sangam142/agentswarm/internal/store"
	"github.com/sangam142/agentswarm/pkg/attena"
)

// ════════════════════════════════════════════════════════════════════════
// AGENT INTERFACE
// ════════════════════════════════════════════════════════════════════════

// Agent is the core interface all trading agents implement.
type Agent interface {
	// ID returns the unique agent identifier.
	ID() string

	// Name returns the human-readable agent name.
	Name() string

	// Start begins the agent's event loop.
	Start(ctx context.Context) error

	// Stop gracefully shuts down the agent.
	Stop() error

	// Status returns the current agent state.
	Status() *models.AgentState

	// Evaluate processes new market data and emits signals.
	Evaluate(ctx context.Context, markets []models.Market) ([]models.Signal, error)
}

// ════════════════════════════════════════════════════════════════════════
// BASE AGENT — Common logic shared by all agents
// ════════════════════════════════════════════════════════════════════════

// Deps holds shared dependencies injected into all agents.
type Deps struct {
	Attena   *attena.Client
	Router   *exchange.Router
	Trades   store.TradeStore
	State    store.StateStore
	SignalCh chan models.Signal // inter-agent communication
}

// BaseAgent provides common functionality for all agent types.
type BaseAgent struct {
	mu          sync.RWMutex
	id          string
	name        string
	agentType   string
	state       *models.AgentState
	deps        *Deps
	stopCh      chan struct{}
	categories  []string
	maxExposure float64
	logFn       func(string, ...interface{})
}

func NewBaseAgent(id, name, agentType string, deps *Deps, categories []string, capital, maxExposure float64) *BaseAgent {
	return &BaseAgent{
		id:          id,
		name:        name,
		agentType:   agentType,
		deps:        deps,
		stopCh:      make(chan struct{}),
		categories:  categories,
		maxExposure: maxExposure,
		state: &models.AgentState{
			ID:               id,
			Name:             name,
			Status:           models.AgentActive,
			CapitalAllocated: capital,
		},
		logFn: func(format string, args ...interface{}) {
			fmt.Printf("[%s] %s: %s\n", time.Now().Format("15:04:05"), name, fmt.Sprintf(format, args...))
		},
	}
}

func (b *BaseAgent) ID() string   { return b.id }
func (b *BaseAgent) Name() string { return b.name }

func (b *BaseAgent) Status() *models.AgentState {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.state
}

func (b *BaseAgent) Stop() error {
	close(b.stopCh)
	b.mu.Lock()
	b.state.Status = models.AgentStopped
	b.mu.Unlock()
	b.logFn("stopped")
	return nil
}

// EmitSignal publishes a signal to the shared channel and persists it.
func (b *BaseAgent) EmitSignal(ctx context.Context, signal models.Signal) error {
	signal.AgentID = b.id
	signal.CreatedAt = time.Now()

	// Persist
	if err := b.deps.State.PublishSignal(ctx, &signal); err != nil {
		return err
	}

	// Broadcast to other agents (non-blocking)
	select {
	case b.deps.SignalCh <- signal:
	default:
		// Channel full, skip
	}

	b.mu.Lock()
	b.state.LastSignalAt = time.Now()
	b.mu.Unlock()

	b.logFn("SIGNAL: %s %s conf=%.2f price=%.2f reason=%s",
		signal.Direction, signal.MarketID, signal.Confidence, signal.Price, signal.Reason)
	return nil
}

// ExecuteOrder places an order through the exchange router.
func (b *BaseAgent) ExecuteOrder(ctx context.Context, order *models.Order) (*models.Order, error) {
	order.AgentID = b.id
	order.CreatedAt = time.Now()
	order.Status = models.StatusPending

	start := time.Now()

	// Save pending order
	if err := b.deps.Trades.SaveOrder(ctx, order); err != nil {
		return nil, fmt.Errorf("save order: %w", err)
	}

	// Execute through router
	filled, err := b.deps.Router.PlaceOrder(ctx, order)
	if err != nil {
		order.Status = models.StatusFailed
		b.deps.Trades.UpdateOrder(ctx, order)
		return nil, fmt.Errorf("place order: %w", err)
	}

	filled.LatencyMs = time.Since(start).Milliseconds()

	// Update state
	b.mu.Lock()
	b.state.TotalTrades++
	b.state.TotalPnL += filled.PnL
	if filled.PnL > 0 {
		b.state.WinCount++
	} else if filled.PnL < 0 {
		b.state.LossCount++
	}
	b.state.CapitalUsed += filled.FilledAt * float64(filled.Size)
	b.state.LastTradeAt = time.Now()
	// Rolling average latency
	n := int64(b.state.TotalTrades)
	b.state.AvgLatencyMs = ((b.state.AvgLatencyMs * (n - 1)) + filled.LatencyMs) / n
	b.mu.Unlock()

	// Persist final state
	b.deps.Trades.UpdateOrder(ctx, filled)
	b.deps.State.SetAgentState(ctx, b.state)

	b.logFn("TRADE: %s %s @ %.3f (%d contracts) latency=%dms",
		filled.Side, filled.MarketID, filled.FilledAt, filled.Size, filled.LatencyMs)
	return filled, nil
}

// UpdateState persists current state to the store.
func (b *BaseAgent) UpdateState(ctx context.Context) error {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.deps.State.SetAgentState(ctx, b.state)
}

// Log emits a structured log message.
func (b *BaseAgent) Log(format string, args ...interface{}) {
	b.logFn(format, args...)
}
