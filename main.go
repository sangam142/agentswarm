package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"agentswarm/internal/agent"
	"agentswarm/internal/config"
	"agentswarm/internal/exchange"
	"agentswarm/internal/models"
	"agentswarm/internal/store"
	"agentswarm/pkg/attena"
)

func main() {
	fmt.Println(`
    ╔═══════════════════════════════════════════╗
    ║         AGENTSWARM v0.1.0                 ║
    ║   AI Prediction Market Trading System     ║
    ║   6 Agents • Kalshi + Polymarket          ║
    ╚═══════════════════════════════════════════╝
	`)

	// ── Load Config ──
	cfg := config.Load()

	// ── Initialize Infrastructure ──
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Attena API client
	attenaClient := attena.NewClient(cfg.AttenaBaseURL, cfg.AttenaAPIKey)

	// Storage (in-memory for development, swap for Postgres/Redis in prod)
	memStore := store.NewMemStore()

	// Exchange router with paper trading
	router := exchange.NewRouter()
	router.Register("kalshi", exchange.NewPaperExchange("kalshi", 10000))
	router.Register("polymarket", exchange.NewPaperExchange("polymarket", 10000))

	// If real exchange credentials are provided, use real exchanges
	if cfg.KalshiEmail != "" {
		kalshi, err := exchange.NewKalshiExchange(cfg.KalshiAPIBase, cfg.KalshiEmail, cfg.KalshiPassword)
		if err == nil {
			router.Register("kalshi", kalshi)
			fmt.Println("[INIT] Kalshi exchange: LIVE")
		}
	}
	if cfg.PolymarketKey != "" {
		poly, err := exchange.NewPolymarketExchange(cfg.PolymarketCLOB, cfg.PolymarketKey, cfg.PolymarketRPC)
		if err == nil {
			router.Register("polymarket", poly)
			fmt.Println("[INIT] Polymarket exchange: LIVE")
		}
	}

	// Shared signal channel for inter-agent communication
	signalCh := make(chan models.Signal, 1000)

	// ── Shared Dependencies ──
	deps := &agent.Deps{
		Attena:   attenaClient,
		Router:   router,
		Trades:   memStore,
		State:    memStore,
		SignalCh: signalCh,
	}

	// ── Create All 6 Agents ──
	arbCfg := agent.DefaultArbitrageConfig()
	arbAgent := agent.NewArbitrageAgent(deps, arbCfg)

	newsCfg := agent.DefaultNewsReactorConfig()
	newsCfg.ClaudeAPIKey = cfg.ClaudeAPIKey
	newsCfg.NewsAPIKey = cfg.NewsAPIKey
	newsAgent := agent.NewNewsReactorAgent(deps, newsCfg)

	momCfg := agent.DefaultMomentumConfig()
	momAgent := agent.NewMomentumAgent(deps, momCfg)

	mmCfg := agent.DefaultSpreadMakerConfig()
	mmAgent := agent.NewSpreadMakerAgent(deps, mmCfg)

	ensCfg := agent.DefaultEnsembleConfig()
	ensAgent := agent.NewEnsembleAgent(deps, ensCfg)

	riskCfg := agent.DefaultRiskConfig()
	riskAgent := agent.NewRiskSentinel(deps, riskCfg)

	agents := []agent.Agent{arbAgent, newsAgent, momAgent, mmAgent, ensAgent, riskAgent}

	// ── Register initial states ──
	for _, a := range agents {
		memStore.SetAgentState(ctx, a.Status())
	}

	// ── Start HTTP API for Dashboard ──
	go startHTTPServer(cfg.HTTPPort, memStore, agents)

	// ── Start All Agents ──
	var wg sync.WaitGroup
	for _, a := range agents {
		wg.Add(1)
		go func(ag agent.Agent) {
			defer wg.Done()
			fmt.Printf("[LAUNCH] Starting %s...\n", ag.Name())
			if err := ag.Start(ctx); err != nil && err != context.Canceled {
				fmt.Printf("[ERROR] %s crashed: %v\n", ag.Name(), err)
			}
		}(a)
	}

	fmt.Printf("\n[SWARM] All %d agents launched. Dashboard: http://localhost:%d\n\n", len(agents), cfg.HTTPPort)

	// ── Graceful Shutdown ──
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	fmt.Println("\n[SHUTDOWN] Stopping all agents...")
	cancel()

	for _, a := range agents {
		a.Stop()
	}
	wg.Wait()
	fmt.Println("[SHUTDOWN] All agents stopped. Goodbye.")
}

// ════════════════════════════════════════════════════════════════════════
// HTTP API — Serves data to the React dashboard
// ════════════════════════════════════════════════════════════════════════

func startHTTPServer(port int, stateStore store.StateStore, agents []agent.Agent) {
	mux := http.NewServeMux()

	// CORS middleware
	cors := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
			if r.Method == "OPTIONS" {
				w.WriteHeader(204)
				return
			}
			h(w, r)
		}
	}

	// GET /api/agents — All agent states
	mux.HandleFunc("/api/agents", cors(func(w http.ResponseWriter, r *http.Request) {
		states, err := stateStore.GetAllAgentStates(r.Context())
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(states)
	}))

	// GET /api/agents/{id} — Single agent state
	mux.HandleFunc("/api/agents/", cors(func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Path[len("/api/agents/"):]
		state, err := stateStore.GetAgentState(r.Context(), id)
		if err != nil {
			http.Error(w, err.Error(), 404)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(state)
	}))

	// GET /api/positions — All positions
	mux.HandleFunc("/api/positions", cors(func(w http.ResponseWriter, r *http.Request) {
		positions, err := stateStore.GetAllPositions(r.Context())
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(positions)
	}))

	// GET /api/health
	mux.HandleFunc("/api/health", cors(func(w http.ResponseWriter, r *http.Request) {
		active := 0
		for _, a := range agents {
			if a.Status().Status == models.AgentActive {
				active++
			}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"status":        "ok",
			"active_agents": active,
			"total_agents":  len(agents),
			"uptime":        time.Since(startTime).String(),
		})
	}))

	addr := fmt.Sprintf(":%d", port)
	fmt.Printf("[HTTP] API server listening on %s\n", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		fmt.Printf("[HTTP] Server error: %v\n", err)
	}
}

var startTime = time.Now()
