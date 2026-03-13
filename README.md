# AgentSwarm 🐝

**AI-powered prediction market trading system with 6 autonomous agents operating across Kalshi and Polymarket.**

Built in Go. Real-time data from [Attena API](https://attena.xyz). LLM integration via Claude API.

```
┌─────────────────────────────────────────────────────────────┐
│                     AGENTSWARM ARCHITECTURE                  │
│                                                              │
│  ┌──────────────┐   ┌──────────┐   ┌───────────────────┐   │
│  │  Attena API   │──▶│  NATS    │──▶│  6 Go Agents      │   │
│  │  NewsAPI      │   │ JetStream│   │  (goroutines)     │   │
│  │  RSS/GDELT    │   └──────────┘   └─────────┬─────────┘   │
│  └──────────────┘                             │             │
│                                    ┌──────────▼──────────┐  │
│                                    │  Exchange Router     │  │
│                                    │  ┌───────┐┌───────┐ │  │
│                                    │  │Kalshi ││Poly-  │ │  │
│                                    │  │ API   ││market │ │  │
│                                    │  └───────┘└───────┘ │  │
│                                    └─────────────────────┘  │
│                                                              │
│  ┌──────────┐  ┌──────────┐  ┌──────────────────────────┐  │
│  │ PostgreSQL│  │  Redis   │  │  HTTP API → Dashboard    │  │
│  │ (trades)  │  │ (state)  │  │  :8080                   │  │
│  └──────────┘  └──────────┘  └──────────────────────────┘  │
└─────────────────────────────────────────────────────────────┘
```

---

## The 6 Agents

| # | Agent | Type | Model | Strategy |
|---|-------|------|-------|----------|
| 1 | **Arbitrage Alpha** ⚡ | Rule-based | - | Cross-platform price divergence. Buys cheap side, sells expensive side. |
| 2 | **News Reactor** 📡 | LLM-powered | Claude Sonnet | Ingests breaking news → Claude assesses impact → trades affected markets. |
| 3 | **Momentum Wave** 🌊 | Statistical | XGBoost (future) | Rolling z-score momentum detection + volume spike analysis. |
| 4 | **Spread Maker** 🏗️ | Rule-based | - | Market making on illiquid markets. Bid/ask both sides, capture spread. |
| 5 | **Ensemble Core** 🧠 | Meta-learner | Weighted voting | Only trades when 3+ agents agree. Weights agents by recent Sharpe ratio. |
| 6 | **Risk Sentinel** 🛡️ | Rule-based | - | Portfolio risk, exposure limits, correlation checks, circuit breaker. |

---

## Quick Start

### 1. Prerequisites
```bash
# Install Go 1.22+
# Install Docker & Docker Compose
```

### 2. Setup
```bash
git clone https://github.com/sangam142/agentswarm.git
cd agentswarm
cp .env.example .env
# Edit .env with your API keys (at minimum, ANTHROPIC_API_KEY)
```

### 3. Run with Docker (recommended)
```bash
cd deployments
docker-compose up -d
# Dashboard: http://localhost:8080
# Agents start automatically in paper-trading mode
```

### 4. Run locally (development)
```bash
# Start infra
docker-compose up -d postgres redis nats

# Run the swarm
go run ./cmd/swarm
```

---

## Paper Trading → Live Trading

The system starts in **paper trading mode** by default. Paper exchanges simulate fills with realistic latency and slippage.

To go live:

1. **Kalshi**: Set `KALSHI_EMAIL` and `KALSHI_PASSWORD` in `.env`. Apply for API access at kalshi.com.
2. **Polymarket**: Set `POLYMARKET_PRIVATE_KEY` with a funded Polygon wallet. The wallet needs USDC on Polygon and approved spending on the CTF Exchange contract.

**Start small.** Fund each exchange with $100-500 initially.

---

## How to Train / Improve Each Agent

### Agent 1: Arbitrage Alpha
**No training needed** — pure rule-based. Tune these parameters:
- `MinSpread`: Minimum price gap to trigger (default 0.04 = 4¢). Lower = more trades but more risk of fees eating profit.
- `FeeEstimate`: Estimated round-trip fees (default 0.03). Check actual Kalshi/Polymarket fee schedules.
- `MinVolume`: Only arb markets with enough liquidity to actually fill.

**Backtesting**: Run the data collector for 1-2 weeks, logging Attena snapshots to PostgreSQL. Then replay the data through the agent to measure theoretical P&L.

### Agent 2: News Reactor (LLM Agent)
**Training = prompt engineering + evaluation.**

1. **Collect ground truth**: Log news events and their actual market impact over 2-4 weeks.
2. **Evaluate prompt**: For each historical event, send it through Claude and compare the assessment with what actually happened.
3. **Iterate**: Adjust the system prompt to improve direction accuracy. Target >65% directional accuracy.
4. **Cache patterns**: Build a lookup table of `event_type → market_impact` for the fastest execution path.

Key tuning:
- `MinConfidence`: Raise to 0.75+ for more conservative trading.
- `ClaudeModel`: Use `claude-sonnet-4-20250514` for speed. Switch to `claude-opus-4-20250514` for complex geopolitical events.

### Agent 3: Momentum Wave (future ML)
**Currently statistical.** To add ML:

1. **Collect features** (run data collector for 2-4 weeks):
   - 1h, 6h, 24h price change
   - Volume ratio (current / 7d average)
   - Time to close (days remaining)
   - Category momentum (avg price change across category)
   
2. **Label data**: Target = "price moves >5% in direction within 2 hours"

3. **Train model**:
   ```python
   import xgboost as xgb
   from sklearn.model_selection import TimeSeriesSplit
   
   model = xgb.XGBClassifier(
       n_estimators=100,
       max_depth=4,
       learning_rate=0.1,
   )
   
   # Use time-series split (not random) to avoid lookahead bias
   tscv = TimeSeriesSplit(n_splits=5)
   for train_idx, val_idx in tscv.split(X):
       model.fit(X[train_idx], y[train_idx])
       score = model.score(X[val_idx], y[val_idx])
       print(f"Fold accuracy: {score:.3f}")
   ```

4. **Export model** and load in Go via ONNX or call a Python microservice.

### Agent 4: Spread Maker
**Tune parameters:**
- `TargetSpread`: Wider = safer (less inventory risk), narrower = more fills.
- `MaxInventory`: Lower = less directional risk. Start at 50, increase as you gain confidence.
- Market selection: The `MinVolume` / `MaxVolume` range defines your sweet spot. Too liquid = no edge. Too illiquid = can't fill.

### Agent 5: Ensemble Core
**Auto-tunes via performance-based weighting.** Call `UpdateWeights()` daily. The weights naturally shift toward better-performing agents.

Manual override: Edit `agentWeights` map to boost/reduce specific agents.

### Agent 6: Risk Sentinel
**Configure your risk tolerance:**
- `CircuitBreakerPct`: 5% default. Lower if you're risk-averse.
- `MaxCategoryPct`: 20% default. Prevents concentration in one category.
- `MaxTotalExposure`: Set based on your total bankroll.

---

## Project Structure

```
agentswarm/
├── cmd/swarm/main.go              # Entry point — wires everything, starts agents
├── internal/
│   ├── agent/
│   │   ├── base.go                # Base agent with shared logic
│   │   ├── arbitrage.go           # Agent 1: Cross-platform arbitrage
│   │   ├── news_reactor.go        # Agent 2: LLM news trader
│   │   ├── momentum_and_mm.go     # Agent 3+4: Momentum + Market Maker
│   │   └── ensemble_and_risk.go   # Agent 5+6: Ensemble + Risk
│   ├── config/config.go           # Environment-based configuration
│   ├── exchange/exchange.go       # Exchange interface + paper/live implementations
│   ├── models/models.go           # Core domain types
│   └── store/store.go             # Storage interfaces + in-memory implementation
├── pkg/attena/client.go           # Attena Search API client
├── scripts/schema.sql             # PostgreSQL schema
├── deployments/docker-compose.yml # Full stack: Go + Postgres + Redis + NATS
├── Dockerfile
├── .env.example
└── README.md
```

---

## API Endpoints (for Dashboard)

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/api/health` | System health + active agent count |
| `GET` | `/api/agents` | All agent states (P&L, trades, win rate) |
| `GET` | `/api/agents/{id}` | Single agent details |
| `GET` | `/api/positions` | All open positions |

---

## Deployment Checklist

- [ ] Copy `.env.example` to `.env`
- [ ] Add `ANTHROPIC_API_KEY` (required for News Reactor)
- [ ] Add `NEWS_API_KEY` (optional, uses mocks without it)
- [ ] Run `docker-compose up -d` in `deployments/`
- [ ] Verify at `http://localhost:8080/api/health`
- [ ] Monitor logs: `docker-compose logs -f swarm`
- [ ] Let paper trade for 1-2 weeks before going live
- [ ] Fund Kalshi + Polymarket accounts with small amounts
- [ ] Add exchange credentials to `.env`
- [ ] Restart with `docker-compose restart swarm`

---

## Key Design Decisions

1. **Attena API as primary data source**: Aggregates 80K+ markets across Kalshi + Polymarket. One API instead of two separate integrations. Use `agent=true` parameter to skip their LLM layer and save ~500ms.

2. **LLM NOT in the hot path**: Claude API calls take 500ms-2s. Instead, the News Reactor pre-computes decision trees and caches assessments. The actual trade decision is a cache lookup, not an API call.

3. **Paper trading by default**: The exchange router pattern means swapping from paper to live is a config change, not a code change.

4. **Inter-agent communication via channels**: The Ensemble agent listens to signals from all other agents. In production, swap Go channels for NATS subjects.

5. **Risk Sentinel as circuit breaker**: Runs independently, can halt all trading if losses spike. This is your safety net.

---

## License

MIT. Use at your own risk. This is experimental software. Never risk money you can't afford to lose.
