#!/bin/bash
# ═══════════════════════════════════════════════════════════════
# AgentSwarm — Full Setup Script for EndeavourOS / Arch Linux
# Run: chmod +x setup.sh && ./setup.sh
# ═══════════════════════════════════════════════════════════════

set -e
echo "
╔══════════════════════════════════════════════╗
║   AgentSwarm Setup — EndeavourOS             ║
║   This installs everything you need          ║
╚══════════════════════════════════════════════╝
"

# ── 1. System Dependencies ──
echo "[1/8] Installing system dependencies..."
sudo pacman -S --needed --noconfirm \
    go \
    docker \
    docker-compose \
    postgresql-libs \
    redis \
    python \
    python-pip \
    python-virtualenv \
    git \
    curl \
    jq

# ── 2. Enable Docker ──
echo "[2/8] Enabling Docker..."
sudo systemctl enable --now docker
sudo usermod -aG docker $USER
echo "  ⚠️  You may need to log out and back in for Docker group to take effect."
echo "  Or run: newgrp docker"

# ── 3. Project Structure ──
echo "[3/8] Setting up project structure..."
PROJECT_DIR="$HOME/projects/agentswarm"
mkdir -p "$PROJECT_DIR"
cd "$PROJECT_DIR"

# If files were downloaded from Claude, copy them in
if [ -d "/tmp/agentswarm" ]; then
    cp -r /tmp/agentswarm/* .
fi

# ── 4. Go Module Setup ──
echo "[4/8] Initializing Go modules..."
cat > go.mod << 'GOMOD'
module github.com/shrish/agentswarm

go 1.22

require (
	github.com/google/uuid v1.6.0
	github.com/rs/zerolog v1.32.0
)
GOMOD

# Create go.sum (will be populated by go mod tidy)
touch go.sum

# We removed external deps that need complex setup (pgx, redis, nats)
# The in-memory store works perfectly for development
# Add them back when you're ready for production

# ── 5. Environment File ──
echo "[5/8] Creating .env file..."
if [ ! -f .env ]; then
    cp .env.example .env 2>/dev/null || cat > .env << 'ENV'
# ═══ AgentSwarm Config ═══
ATTENA_BASE_URL=https://attena-api.fly.dev/api/search/
ATTENA_API_KEY=
ANTHROPIC_API_KEY=
NEWS_API_KEY=
KALSHI_EMAIL=
KALSHI_PASSWORD=
POLYMARKET_PRIVATE_KEY=
HTTP_PORT=8080
POLL_INTERVAL=30s
ENV
    echo "  ✏️  Edit .env with your API keys: nano .env"
else
    echo "  .env already exists, skipping."
fi

# ── 6. Python Environment for ML Training ──
echo "[6/8] Setting up Python ML environment..."
mkdir -p ml
python -m venv ml/venv
source ml/venv/bin/activate
pip install --quiet \
    xgboost \
    scikit-learn \
    pandas \
    numpy \
    requests \
    schedule \
    joblib \
    matplotlib
deactivate
echo "  Activate with: source ml/venv/bin/activate"

# ── 7. Docker Infrastructure ──
echo "[7/8] Pulling Docker images..."
docker pull postgres:16-alpine 2>/dev/null || true
docker pull redis:7-alpine 2>/dev/null || true
docker pull nats:2.10-alpine 2>/dev/null || true

# ── 8. Verify ──
echo "[8/8] Verification..."
echo "  Go:     $(go version)"
echo "  Docker: $(docker --version)"
echo "  Python: $(python --version)"
echo ""
echo "═══════════════════════════════════════════"
echo " Setup complete! Next steps:"
echo ""
echo " 1. cd $PROJECT_DIR"
echo " 2. nano .env          # add your ANTHROPIC_API_KEY"
echo " 3. make infra         # start Postgres + Redis + NATS"
echo " 4. make run           # start all agents (paper trading)"
echo " 5. make collect       # start data collection for training"
echo "═══════════════════════════════════════════"
