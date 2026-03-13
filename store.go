package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"agentswarm/internal/models"
)

// ════════════════════════════════════════════════════════════════════════
// INTERFACES — Swap implementations for testing
// ════════════════════════════════════════════════════════════════════════

// TradeStore persists orders and trade history (PostgreSQL).
type TradeStore interface {
	SaveOrder(ctx context.Context, order *models.Order) error
	UpdateOrder(ctx context.Context, order *models.Order) error
	GetOrdersByAgent(ctx context.Context, agentID string, limit int) ([]models.Order, error)
	GetPnLSince(ctx context.Context, agentID string, since time.Time) (float64, error)
	SaveMarketSnapshot(ctx context.Context, market *models.Market) error
}

// StateStore manages real-time agent state (Redis).
type StateStore interface {
	SetAgentState(ctx context.Context, state *models.AgentState) error
	GetAgentState(ctx context.Context, agentID string) (*models.AgentState, error)
	GetAllAgentStates(ctx context.Context) ([]models.AgentState, error)
	SetPosition(ctx context.Context, pos *models.Position) error
	GetPositions(ctx context.Context, agentID string) ([]models.Position, error)
	GetAllPositions(ctx context.Context) ([]models.Position, error)
	PublishSignal(ctx context.Context, signal *models.Signal) error
	CacheMarket(ctx context.Context, market *models.Market, ttl time.Duration) error
	GetCachedMarket(ctx context.Context, id string) (*models.Market, error)
	IncrCounter(ctx context.Context, key string) (int64, error)
}

// ════════════════════════════════════════════════════════════════════════
// IN-MEMORY IMPLEMENTATION — For development & paper trading
// ════════════════════════════════════════════════════════════════════════

// MemStore implements both TradeStore and StateStore in memory.
// Use this for development and paper trading. Swap for real
// Postgres/Redis implementations in production.
type MemStore struct {
	orders      []models.Order
	snapshots   []models.Market
	agentStates map[string]*models.AgentState
	positions   map[string][]models.Position
	marketCache map[string]*models.Market
	signals     []models.Signal
	counters    map[string]int64
}

func NewMemStore() *MemStore {
	return &MemStore{
		orders:      make([]models.Order, 0),
		snapshots:   make([]models.Market, 0),
		agentStates: make(map[string]*models.AgentState),
		positions:   make(map[string][]models.Position),
		marketCache: make(map[string]*models.Market),
		signals:     make([]models.Signal, 0),
		counters:    make(map[string]int64),
	}
}

// ── TradeStore ──

func (m *MemStore) SaveOrder(_ context.Context, order *models.Order) error {
	m.orders = append(m.orders, *order)
	return nil
}

func (m *MemStore) UpdateOrder(_ context.Context, order *models.Order) error {
	for i, o := range m.orders {
		if o.ID == order.ID {
			m.orders[i] = *order
			return nil
		}
	}
	return fmt.Errorf("order %s not found", order.ID)
}

func (m *MemStore) GetOrdersByAgent(_ context.Context, agentID string, limit int) ([]models.Order, error) {
	var result []models.Order
	for i := len(m.orders) - 1; i >= 0 && len(result) < limit; i-- {
		if m.orders[i].AgentID == agentID {
			result = append(result, m.orders[i])
		}
	}
	return result, nil
}

func (m *MemStore) GetPnLSince(_ context.Context, agentID string, since time.Time) (float64, error) {
	var pnl float64
	for _, o := range m.orders {
		if o.AgentID == agentID && o.CreatedAt.After(since) && o.Status == models.StatusFilled {
			pnl += o.PnL
		}
	}
	return pnl, nil
}

func (m *MemStore) SaveMarketSnapshot(_ context.Context, market *models.Market) error {
	m.snapshots = append(m.snapshots, *market)
	return nil
}

// ── StateStore ──

func (m *MemStore) SetAgentState(_ context.Context, state *models.AgentState) error {
	state.UpdatedAt = time.Now()
	m.agentStates[state.ID] = state
	return nil
}

func (m *MemStore) GetAgentState(_ context.Context, agentID string) (*models.AgentState, error) {
	if s, ok := m.agentStates[agentID]; ok {
		return s, nil
	}
	return nil, fmt.Errorf("agent %s not found", agentID)
}

func (m *MemStore) GetAllAgentStates(_ context.Context) ([]models.AgentState, error) {
	result := make([]models.AgentState, 0, len(m.agentStates))
	for _, s := range m.agentStates {
		result = append(result, *s)
	}
	return result, nil
}

func (m *MemStore) SetPosition(_ context.Context, pos *models.Position) error {
	positions := m.positions[pos.AgentID]
	for i, p := range positions {
		if p.MarketID == pos.MarketID && p.Platform == pos.Platform {
			positions[i] = *pos
			m.positions[pos.AgentID] = positions
			return nil
		}
	}
	m.positions[pos.AgentID] = append(positions, *pos)
	return nil
}

func (m *MemStore) GetPositions(_ context.Context, agentID string) ([]models.Position, error) {
	return m.positions[agentID], nil
}

func (m *MemStore) GetAllPositions(_ context.Context) ([]models.Position, error) {
	var result []models.Position
	for _, positions := range m.positions {
		result = append(result, positions...)
	}
	return result, nil
}

func (m *MemStore) PublishSignal(_ context.Context, signal *models.Signal) error {
	m.signals = append(m.signals, *signal)
	return nil
}

func (m *MemStore) CacheMarket(_ context.Context, market *models.Market, _ time.Duration) error {
	m.marketCache[market.ID] = market
	return nil
}

func (m *MemStore) GetCachedMarket(_ context.Context, id string) (*models.Market, error) {
	if mkt, ok := m.marketCache[id]; ok {
		return mkt, nil
	}
	return nil, fmt.Errorf("market %s not cached", id)
}

func (m *MemStore) IncrCounter(_ context.Context, key string) (int64, error) {
	m.counters[key]++
	return m.counters[key], nil
}

// ── JSON helpers for Redis serialization ──

func MarshalState(state *models.AgentState) ([]byte, error) {
	return json.Marshal(state)
}

func UnmarshalState(data []byte) (*models.AgentState, error) {
	var s models.AgentState
	err := json.Unmarshal(data, &s)
	return &s, err
}
