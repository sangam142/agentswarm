package exchange

import (
	"context"
	"fmt"
	"math/rand"
	"time"

	"github.com/sangam142/agentswarm/internal/models"
)

// ════════════════════════════════════════════════════════════════════════
// EXCHANGE INTERFACE
// ════════════════════════════════════════════════════════════════════════

// Exchange abstracts order placement across platforms.
type Exchange interface {
	Name() string
	PlaceOrder(ctx context.Context, order *models.Order) (*models.Order, error)
	CancelOrder(ctx context.Context, orderID string) error
	GetBalance(ctx context.Context) (float64, error)
}

// ════════════════════════════════════════════════════════════════════════
// PAPER TRADING EXCHANGE — Simulates fills for backtesting
// ════════════════════════════════════════════════════════════════════════

// PaperExchange simulates an exchange for development and backtesting.
// All orders fill instantly at the requested price with some slippage.
type PaperExchange struct {
	name     string
	balance  float64
	slippage float64 // max slippage in price points (e.g., 0.02)
	fills    []models.Order
}

func NewPaperExchange(name string, startingBalance float64) *PaperExchange {
	return &PaperExchange{
		name:     name,
		balance:  startingBalance,
		slippage: 0.01,
		fills:    make([]models.Order, 0),
	}
}

func (p *PaperExchange) Name() string { return p.name }

func (p *PaperExchange) PlaceOrder(ctx context.Context, order *models.Order) (*models.Order, error) {
	start := time.Now()

	// Simulate network latency (50-200ms)
	delay := time.Duration(50+rand.Intn(150)) * time.Millisecond
	time.Sleep(delay)

	// Calculate fill price with slippage
	slip := (rand.Float64() - 0.5) * 2 * p.slippage
	fillPrice := order.Price + slip
	if fillPrice < 0.01 {
		fillPrice = 0.01
	}
	if fillPrice > 0.99 {
		fillPrice = 0.99
	}

	// Check balance
	cost := fillPrice * float64(order.Size)
	if cost > p.balance {
		order.Status = models.StatusFailed
		order.UpdatedAt = time.Now()
		return order, fmt.Errorf("insufficient balance: need $%.2f, have $%.2f", cost, p.balance)
	}

	// Fill the order
	p.balance -= cost
	order.Status = models.StatusFilled
	order.FilledAt = fillPrice
	order.LatencyMs = time.Since(start).Milliseconds()
	order.UpdatedAt = time.Now()

	p.fills = append(p.fills, *order)
	return order, nil
}

func (p *PaperExchange) CancelOrder(_ context.Context, orderID string) error {
	return nil // paper orders fill instantly
}

func (p *PaperExchange) GetBalance(_ context.Context) (float64, error) {
	return p.balance, nil
}

func (p *PaperExchange) GetFills() []models.Order {
	return p.fills
}

// ════════════════════════════════════════════════════════════════════════
// KALSHI EXCHANGE — Real Kalshi API integration
// ════════════════════════════════════════════════════════════════════════

// KalshiExchange implements the Exchange interface for Kalshi.
//
// To use this in production:
// 1. Apply for API access at https://kalshi.com/
// 2. Get API key via POST /login with email + password
// 3. All orders go through POST /portfolio/orders
//
// API docs: https://trading-api.readme.io/reference
type KalshiExchange struct {
	baseURL string
	token   string
	client  interface{} // *http.Client in production
}

func NewKalshiExchange(baseURL, email, password string) (*KalshiExchange, error) {
	// In production, this would:
	// 1. POST /login with {email, password}
	// 2. Store the returned token
	// 3. Use token in Authorization header for all subsequent requests
	return &KalshiExchange{
		baseURL: baseURL,
	}, nil
}

func (k *KalshiExchange) Name() string { return "kalshi" }

func (k *KalshiExchange) PlaceOrder(ctx context.Context, order *models.Order) (*models.Order, error) {
	// Production implementation:
	//
	// POST {baseURL}/portfolio/orders
	// Headers: Authorization: Bearer {token}
	// Body: {
	//   "ticker": order.MarketID,
	//   "action": "buy" or "sell",
	//   "side": "yes" or "no",
	//   "type": "limit" or "market",
	//   "count": order.Size,
	//   "yes_price": int(order.Price * 100), // Kalshi uses cents
	// }
	//
	// Response includes order_id and status.
	return nil, fmt.Errorf("kalshi exchange not configured — set KALSHI_EMAIL and KALSHI_PASSWORD")
}

func (k *KalshiExchange) CancelOrder(ctx context.Context, orderID string) error {
	// DELETE {baseURL}/portfolio/orders/{orderID}
	return fmt.Errorf("kalshi exchange not configured")
}

func (k *KalshiExchange) GetBalance(ctx context.Context) (float64, error) {
	// GET {baseURL}/portfolio/balance
	return 0, fmt.Errorf("kalshi exchange not configured")
}

// ════════════════════════════════════════════════════════════════════════
// POLYMARKET EXCHANGE — Real Polymarket CLOB integration
// ════════════════════════════════════════════════════════════════════════

// PolymarketExchange implements the Exchange interface for Polymarket.
//
// Polymarket uses a CLOB (Central Limit Order Book) on Polygon.
// Orders are EIP-712 signed messages sent to their CLOB API.
//
// To use this in production:
// 1. Create a Polygon wallet
// 2. Fund it with USDC on Polygon
// 3. Approve the Polymarket CTF Exchange contract to spend USDC
// 4. Sign orders using EIP-712 typed data
// 5. Submit to CLOB API
//
// CLOB API: https://docs.polymarket.com/
type PolymarketExchange struct {
	clobBase   string
	privateKey string
	rpcURL     string
}

func NewPolymarketExchange(clobBase, privateKey, rpcURL string) (*PolymarketExchange, error) {
	return &PolymarketExchange{
		clobBase:   clobBase,
		privateKey: privateKey,
		rpcURL:     rpcURL,
	}, nil
}

func (p *PolymarketExchange) Name() string { return "polymarket" }

func (p *PolymarketExchange) PlaceOrder(ctx context.Context, order *models.Order) (*models.Order, error) {
	// Production implementation:
	//
	// 1. Build order struct:
	//    {
	//      "tokenID": <condition token ID from market>,
	//      "price": order.Price (as string, e.g. "0.65"),
	//      "size": order.Size,
	//      "side": "BUY" or "SELL",
	//      "feeRateBps": 0,
	//      "nonce": <random nonce>,
	//      "expiration": <unix timestamp>,
	//    }
	//
	// 2. Sign with EIP-712 using private key
	//
	// 3. POST {clobBase}/order with signed payload
	//
	// 4. Parse response for order ID and fill status
	//
	// Key Go libraries needed:
	//   - github.com/ethereum/go-ethereum/crypto (signing)
	//   - github.com/ethereum/go-ethereum/common (addresses)
	return nil, fmt.Errorf("polymarket exchange not configured — set POLYMARKET_PRIVATE_KEY")
}

func (p *PolymarketExchange) CancelOrder(ctx context.Context, orderID string) error {
	// DELETE {clobBase}/order/{orderID}
	return fmt.Errorf("polymarket exchange not configured")
}

func (p *PolymarketExchange) GetBalance(ctx context.Context) (float64, error) {
	// Query USDC balance on Polygon via RPC
	return 0, fmt.Errorf("polymarket exchange not configured")
}

// ════════════════════════════════════════════════════════════════════════
// ROUTER — Routes orders to the correct exchange
// ════════════════════════════════════════════════════════════════════════

// Router selects the appropriate exchange for an order.
type Router struct {
	exchanges map[string]Exchange
}

func NewRouter() *Router {
	return &Router{
		exchanges: make(map[string]Exchange),
	}
}

func (r *Router) Register(name string, ex Exchange) {
	r.exchanges[name] = ex
}

func (r *Router) Route(platform string) (Exchange, error) {
	ex, ok := r.exchanges[platform]
	if !ok {
		return nil, fmt.Errorf("no exchange registered for platform: %s", platform)
	}
	return ex, nil
}

func (r *Router) PlaceOrder(ctx context.Context, order *models.Order) (*models.Order, error) {
	ex, err := r.Route(order.Platform)
	if err != nil {
		return nil, err
	}
	return ex.PlaceOrder(ctx, order)
}
