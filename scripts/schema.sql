-- AgentSwarm Database Schema
-- Run: psql -U swarm -d agentswarm -f schema.sql

-- ── Market Snapshots ──
-- Historical price data for backtesting and analysis
CREATE TABLE IF NOT EXISTS market_snapshots (
    id              BIGSERIAL PRIMARY KEY,
    market_id       TEXT NOT NULL,
    title           TEXT NOT NULL,
    category        TEXT,
    subcategory     TEXT,
    source          TEXT NOT NULL,     -- 'kalshi' or 'polymarket'
    ticker          TEXT,
    yes_price       DECIMAL(6,4),
    no_price        DECIMAL(6,4),
    volume          DECIMAL(18,2),
    volume_24h      DECIMAL(18,2),
    source_url      TEXT,
    close_time      TIMESTAMPTZ,
    fetched_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    -- Index for time-series queries
    CONSTRAINT idx_snapshot_unique UNIQUE (market_id, source, fetched_at)
);

CREATE INDEX idx_snapshots_market ON market_snapshots (market_id, source);
CREATE INDEX idx_snapshots_time ON market_snapshots (fetched_at DESC);
CREATE INDEX idx_snapshots_category ON market_snapshots (category, fetched_at DESC);

-- ── Orders ──
-- Every trade placed by any agent
CREATE TABLE IF NOT EXISTS orders (
    id              TEXT PRIMARY KEY,
    agent_id        TEXT NOT NULL,
    signal_id       TEXT,
    platform        TEXT NOT NULL,
    market_id       TEXT NOT NULL,
    side            TEXT NOT NULL,     -- 'buy_yes', 'buy_no', 'sell_yes', 'sell_no'
    order_type      TEXT NOT NULL,     -- 'limit', 'market'
    price           DECIMAL(6,4),
    size            INTEGER NOT NULL,
    status          TEXT NOT NULL,     -- 'pending','filled','partial','canceled','failed'
    filled_at_price DECIMAL(6,4),
    pnl             DECIMAL(12,4) DEFAULT 0,
    latency_ms      INTEGER,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_orders_agent ON orders (agent_id, created_at DESC);
CREATE INDEX idx_orders_market ON orders (market_id);
CREATE INDEX idx_orders_status ON orders (status);
CREATE INDEX idx_orders_pnl ON orders (agent_id, pnl);

-- ── Signals ──
-- Every signal emitted by agents (for backtesting and audit)
CREATE TABLE IF NOT EXISTS signals (
    id              TEXT PRIMARY KEY,
    agent_id        TEXT NOT NULL,
    signal_type     TEXT NOT NULL,
    market_id       TEXT NOT NULL,
    direction       TEXT NOT NULL,
    confidence      DECIMAL(4,3),
    price           DECIMAL(6,4),
    size            DECIMAL(12,2),
    reason          TEXT,
    metadata        JSONB,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_signals_agent ON signals (agent_id, created_at DESC);
CREATE INDEX idx_signals_market ON signals (market_id);
CREATE INDEX idx_signals_type ON signals (signal_type);

-- ── News Events ──
-- Ingested news for the News Reactor agent
CREATE TABLE IF NOT EXISTS news_events (
    id              TEXT PRIMARY KEY,
    title           TEXT NOT NULL,
    source          TEXT,
    url             TEXT,
    body            TEXT,
    category        TEXT,
    sentiment       DECIMAL(4,3),
    published_at    TIMESTAMPTZ,
    ingested_at     TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_news_time ON news_events (published_at DESC);

-- ── Impact Assessments ──
-- LLM evaluations of news impact
CREATE TABLE IF NOT EXISTS impact_assessments (
    id              BIGSERIAL PRIMARY KEY,
    event_id        TEXT REFERENCES news_events(id),
    market_ids      TEXT[],
    direction       TEXT,
    magnitude       DECIMAL(4,3),
    confidence      DECIMAL(4,3),
    reasoning       TEXT,
    assessed_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    latency_ms      INTEGER
);

CREATE INDEX idx_assessments_event ON impact_assessments (event_id);

-- ── Agent State History ──
-- Periodic snapshots of agent performance (for the dashboard charts)
CREATE TABLE IF NOT EXISTS agent_state_history (
    id              BIGSERIAL PRIMARY KEY,
    agent_id        TEXT NOT NULL,
    total_pnl       DECIMAL(12,4),
    total_trades    INTEGER,
    win_count       INTEGER,
    loss_count      INTEGER,
    capital_used    DECIMAL(12,2),
    avg_latency_ms  INTEGER,
    recorded_at     TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_agent_history ON agent_state_history (agent_id, recorded_at DESC);

-- ── Arbitrage Pairs ──
-- Detected cross-platform arbitrage opportunities
CREATE TABLE IF NOT EXISTS arb_pairs (
    id              BIGSERIAL PRIMARY KEY,
    event_key       TEXT NOT NULL,
    kalshi_market   TEXT,
    poly_market     TEXT,
    kalshi_price    DECIMAL(6,4),
    poly_price      DECIMAL(6,4),
    spread          DECIMAL(6,4),
    detected_at     TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_arb_time ON arb_pairs (detected_at DESC);
CREATE INDEX idx_arb_spread ON arb_pairs (spread DESC);

-- ── Views for Dashboard ──

-- Daily P&L by agent
CREATE OR REPLACE VIEW daily_pnl AS
SELECT 
    agent_id,
    DATE(created_at) as trade_date,
    SUM(pnl) as daily_pnl,
    COUNT(*) as trade_count,
    COUNT(*) FILTER (WHERE pnl > 0) as wins,
    COUNT(*) FILTER (WHERE pnl < 0) as losses,
    AVG(latency_ms) as avg_latency
FROM orders
WHERE status = 'filled'
GROUP BY agent_id, DATE(created_at)
ORDER BY trade_date DESC;

-- Hourly market snapshots for charting
CREATE OR REPLACE VIEW hourly_prices AS
SELECT 
    market_id,
    source,
    DATE_TRUNC('hour', fetched_at) as hour,
    AVG(yes_price) as avg_yes,
    AVG(no_price) as avg_no,
    MAX(volume) as max_volume
FROM market_snapshots
GROUP BY market_id, source, DATE_TRUNC('hour', fetched_at)
ORDER BY hour DESC;
