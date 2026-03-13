# ═══════════════════════════════════════════════════════════════
# AgentSwarm Makefile
# ═══════════════════════════════════════════════════════════════

.PHONY: all setup infra run build test collect train clean

# ── Quick Start ──
all: build run

# ── Setup ──
setup:
	chmod +x setup.sh && ./setup.sh

# ── Infrastructure (Postgres + Redis + NATS) ──
infra:
	cd deployments && docker-compose up -d postgres redis nats
	@echo "Waiting for Postgres..."
	@sleep 3
	@docker exec -i agentswarm-postgres-1 psql -U swarm -d agentswarm < scripts/schema.sql 2>/dev/null || true
	@echo "✅ Infrastructure ready"
	@echo "   Postgres: localhost:5432"
	@echo "   Redis:    localhost:6379"
	@echo "   NATS:     localhost:4222"

infra-down:
	cd deployments && docker-compose down

# ── Build ──
build:
	go build -o bin/swarm ./cmd/swarm
	@echo "✅ Built bin/swarm"

# ── Run (paper trading) ──
run: build
	@echo "Starting AgentSwarm in paper trading mode..."
	./bin/swarm

# ── Run with live exchanges (⚠️  real money) ──
run-live: build
	@echo "⚠️  LIVE TRADING MODE — real money at risk!"
	@read -p "Are you sure? (yes/no): " confirm && [ "$$confirm" = "yes" ] || exit 1
	LIVE_MODE=true ./bin/swarm

# ── Data Collection (run for 1-2 weeks before training) ──
collect:
	@echo "Starting data collector..."
	python ml/collect_data.py

# ── Train ML Models ──
train:
	@echo "Training Momentum Agent model..."
	cd ml && source venv/bin/activate && python train_momentum.py
	@echo "✅ Model saved to ml/models/"

# ── Test ──
test:
	go test ./...

# ── Test Attena API connection ──
test-api:
	@echo "Testing Attena API..."
	@curl -s "https://attena-api.fly.dev/api/search/?q=bitcoin&limit=2&agent=true" | jq '.results[0].title, .meta.latency_ms'
	@echo ""
	@echo "Testing categories..."
	@curl -s "https://attena-api.fly.dev/api/search/?category=politics&sort=volume&limit=2&agent=true" | jq '.results[0].title'

# ── Clean ──
clean:
	rm -rf bin/ ml/data/*.json ml/models/*.joblib
	cd deployments && docker-compose down -v

# ── Logs ──
logs:
	cd deployments && docker-compose logs -f swarm
