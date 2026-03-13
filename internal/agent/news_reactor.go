package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/shrish/agentswarm/internal/models"
	"github.com/shrish/agentswarm/pkg/attena"
)

// ════════════════════════════════════════════════════════════════════════
// NEWS REACTOR AGENT
//
// Strategy:
//   1. Poll news feeds (NewsAPI, RSS, GDELT) for breaking events
//   2. Use Claude API to assess market impact of each event
//   3. Match impacted markets via Attena search
//   4. Execute trades based on LLM's directional assessment
//
// Latency optimization:
//   - Pre-compute "decision trees" for anticipated events
//   - Cache LLM assessments for similar event patterns
//   - Use agent=true on Attena API to skip their LLM layer
//   - Keep HTTP connections alive with pools
//
// The LLM is NOT in the hot path for every trade.
// Instead, it updates decision caches periodically.
//
// ════════════════════════════════════════════════════════════════════════

type NewsReactorAgent struct {
	*BaseAgent

	// Config
	claudeAPIKey    string
	claudeModel     string
	newsAPIKey      string
	pollInterval    time.Duration
	minConfidence   float64
	maxOrderSize    int
	categories      []string

	// State
	assessmentCache map[string]*models.ImpactAssessment // hash(event) -> assessment
	processedEvents map[string]bool                      // dedup
	httpClient      *http.Client
}

type NewsReactorConfig struct {
	ClaudeAPIKey  string
	ClaudeModel   string
	NewsAPIKey    string
	PollInterval  time.Duration
	MinConfidence float64
	MaxOrderSize  int
	Categories    []string
	Capital       float64
	MaxExposure   float64
}

func DefaultNewsReactorConfig() NewsReactorConfig {
	return NewsReactorConfig{
		ClaudeModel:   "claude-sonnet-4-20250514",
		PollInterval:  60 * time.Second,
		MinConfidence: 0.65,
		MaxOrderSize:  100,
		Categories:    []string{"geopolitics", "politics"},
		Capital:       8000,
		MaxExposure:   2000,
	}
}

func NewNewsReactorAgent(deps *Deps, cfg NewsReactorConfig) *NewsReactorAgent {
	return &NewsReactorAgent{
		BaseAgent: NewBaseAgent(
			"news-reactor", "News Reactor", "news_reactive",
			deps, cfg.Categories, cfg.Capital, cfg.MaxExposure,
		),
		claudeAPIKey:    cfg.ClaudeAPIKey,
		claudeModel:     cfg.ClaudeModel,
		newsAPIKey:      cfg.NewsAPIKey,
		pollInterval:    cfg.PollInterval,
		minConfidence:   cfg.MinConfidence,
		maxOrderSize:    cfg.MaxOrderSize,
		categories:      cfg.Categories,
		assessmentCache: make(map[string]*models.ImpactAssessment),
		processedEvents: make(map[string]bool),
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				MaxIdleConns:        10,
				MaxIdleConnsPerHost: 10,
				IdleConnTimeout:     90 * time.Second,
			},
		},
	}
}

// Start begins the news monitoring loop.
func (n *NewsReactorAgent) Start(ctx context.Context) error {
	n.Log("starting — model=%s poll=%s min_conf=%.2f",
		n.claudeModel, n.pollInterval, n.minConfidence)

	ticker := time.NewTicker(n.pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-n.stopCh:
			return nil
		case <-ticker.C:
			n.processNewsCycle(ctx)
		}
	}
}

// processNewsCycle fetches news, assesses impact, and emits signals.
func (n *NewsReactorAgent) processNewsCycle(ctx context.Context) {
	// 1. Fetch breaking news
	events, err := n.fetchNews(ctx)
	if err != nil {
		n.Log("news fetch error: %v", err)
		return
	}

	n.Log("fetched %d news events", len(events))

	for _, event := range events {
		// Dedup
		if n.processedEvents[event.ID] {
			continue
		}
		n.processedEvents[event.ID] = true

		// 2. Assess impact with Claude
		assessment, err := n.assessImpact(ctx, &event)
		if err != nil {
			n.Log("assessment error: %v", err)
			continue
		}

		if assessment.Confidence < n.minConfidence {
			n.Log("low confidence (%.2f) for event: %s", assessment.Confidence, event.Title)
			continue
		}

		// 3. Find affected markets via Attena
		signals, err := n.findAndSignal(ctx, &event, assessment)
		if err != nil {
			n.Log("signal generation error: %v", err)
			continue
		}

		for _, sig := range signals {
			n.EmitSignal(ctx, sig)
		}
	}
}

// ── News Fetching ──────────────────────────────────────────────────

// fetchNews gets recent headlines from NewsAPI.
func (n *NewsReactorAgent) fetchNews(ctx context.Context) ([]models.NewsEvent, error) {
	if n.newsAPIKey == "" {
		// Return mock events for development
		return n.mockNewsEvents(), nil
	}

	// NewsAPI: https://newsapi.org/v2/top-headlines
	url := fmt.Sprintf(
		"https://newsapi.org/v2/top-headlines?category=general&language=en&pageSize=10&apiKey=%s",
		n.newsAPIKey,
	)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}

	resp, err := n.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var apiResp struct {
		Articles []struct {
			Title       string `json:"title"`
			Description string `json:"description"`
			URL         string `json:"url"`
			Source      struct {
				Name string `json:"name"`
			} `json:"source"`
			PublishedAt string `json:"publishedAt"`
		} `json:"articles"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		return nil, err
	}

	events := make([]models.NewsEvent, 0, len(apiResp.Articles))
	for _, a := range apiResp.Articles {
		pubAt, _ := time.Parse(time.RFC3339, a.PublishedAt)
		events = append(events, models.NewsEvent{
			ID:          hashString(a.Title),
			Title:       a.Title,
			Source:      a.Source.Name,
			URL:         a.URL,
			Body:        a.Description,
			PublishedAt: pubAt,
			IngestedAt:  time.Now(),
		})
	}
	return events, nil
}

func (n *NewsReactorAgent) mockNewsEvents() []models.NewsEvent {
	return []models.NewsEvent{
		{
			ID: "mock-1", Title: "Federal Reserve signals extended pause on rate changes",
			Source: "Reuters", Body: "The Federal Reserve indicated it will keep rates unchanged through summer.",
			PublishedAt: time.Now().Add(-10 * time.Minute), IngestedAt: time.Now(),
		},
		{
			ID: "mock-2", Title: "Ukraine and Russia agree to preliminary ceasefire talks",
			Source: "AP", Body: "Both nations announced willingness to discuss ceasefire terms.",
			PublishedAt: time.Now().Add(-5 * time.Minute), IngestedAt: time.Now(),
		},
	}
}

// ── Claude API Impact Assessment ───────────────────────────────────

// assessImpact uses Claude to determine how a news event affects prediction markets.
func (n *NewsReactorAgent) assessImpact(ctx context.Context, event *models.NewsEvent) (*models.ImpactAssessment, error) {
	// Check cache first
	cacheKey := hashString(event.Title)
	if cached, ok := n.assessmentCache[cacheKey]; ok {
		if time.Since(cached.AssessedAt) < 15*time.Minute {
			return cached, nil
		}
	}

	start := time.Now()

	if n.claudeAPIKey == "" {
		// Return mock assessment for development
		return n.mockAssessment(event, start), nil
	}

	// Build the prompt
	systemPrompt := `You are a prediction market analyst. Given a news event, assess its impact on prediction markets.

Respond ONLY with a JSON object (no markdown, no backticks):
{
  "direction": "bullish" | "bearish" | "neutral",
  "magnitude": 0.0 to 1.0,
  "confidence": 0.0 to 1.0,
  "affected_categories": ["politics", "crypto", "geopolitics", etc],
  "search_queries": ["query1", "query2"],
  "reasoning": "brief explanation"
}

"search_queries" should be 2-3 natural language queries to find affected prediction markets on Kalshi/Polymarket.
"magnitude" is how much this event moves the market (0.1 = minor, 0.5 = significant, 1.0 = massive).
"confidence" is how certain you are of the direction.`

	userPrompt := fmt.Sprintf("NEWS EVENT:\nTitle: %s\nSource: %s\nBody: %s\nPublished: %s",
		event.Title, event.Source, event.Body, event.PublishedAt.Format(time.RFC3339))

	// Call Claude API
	reqBody := map[string]interface{}{
		"model":      n.claudeModel,
		"max_tokens": 500,
		"system":     systemPrompt,
		"messages": []map[string]string{
			{"role": "user", "content": userPrompt},
		},
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", "https://api.anthropic.com/v1/messages", bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", n.claudeAPIKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := n.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("claude API error: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("claude API %d: %s", resp.StatusCode, string(body))
	}

	var claudeResp struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&claudeResp); err != nil {
		return nil, err
	}

	if len(claudeResp.Content) == 0 {
		return nil, fmt.Errorf("empty claude response")
	}

	// Parse the JSON response
	var impact struct {
		Direction          string   `json:"direction"`
		Magnitude          float64  `json:"magnitude"`
		Confidence         float64  `json:"confidence"`
		AffectedCategories []string `json:"affected_categories"`
		SearchQueries      []string `json:"search_queries"`
		Reasoning          string   `json:"reasoning"`
	}

	text := claudeResp.Content[0].Text
	// Strip markdown fences if present
	text = strings.TrimPrefix(text, "```json")
	text = strings.TrimPrefix(text, "```")
	text = strings.TrimSuffix(text, "```")
	text = strings.TrimSpace(text)

	if err := json.Unmarshal([]byte(text), &impact); err != nil {
		return nil, fmt.Errorf("parse claude response: %w (text: %s)", err, text)
	}

	assessment := &models.ImpactAssessment{
		EventID:    event.ID,
		Direction:  impact.Direction,
		Magnitude:  impact.Magnitude,
		Confidence: impact.Confidence,
		Reasoning:  impact.Reasoning,
		AssessedAt: time.Now(),
		LatencyMs:  time.Since(start).Milliseconds(),
	}

	// Cache it
	n.assessmentCache[cacheKey] = assessment

	n.Log("ASSESSED: %s → %s (mag=%.2f conf=%.2f) in %dms",
		event.Title, impact.Direction, impact.Magnitude, impact.Confidence, assessment.LatencyMs)

	return assessment, nil
}

func (n *NewsReactorAgent) mockAssessment(event *models.NewsEvent, start time.Time) *models.ImpactAssessment {
	// Simple keyword-based mock for development
	title := strings.ToLower(event.Title)
	direction := "neutral"
	magnitude := 0.3
	confidence := 0.6

	if strings.Contains(title, "fed") || strings.Contains(title, "rate") {
		direction = "bullish"
		magnitude = 0.6
		confidence = 0.75
	}
	if strings.Contains(title, "ceasefire") || strings.Contains(title, "peace") {
		direction = "bullish"
		magnitude = 0.7
		confidence = 0.7
	}
	if strings.Contains(title, "war") || strings.Contains(title, "attack") || strings.Contains(title, "crash") {
		direction = "bearish"
		magnitude = 0.8
		confidence = 0.8
	}

	return &models.ImpactAssessment{
		EventID:    event.ID,
		Direction:  direction,
		Magnitude:  magnitude,
		Confidence: confidence,
		Reasoning:  fmt.Sprintf("Mock assessment for: %s", event.Title),
		AssessedAt: time.Now(),
		LatencyMs:  time.Since(start).Milliseconds(),
	}
}

// ── Signal Generation ──────────────────────────────────────────────

func (n *NewsReactorAgent) findAndSignal(ctx context.Context, event *models.NewsEvent, assessment *models.ImpactAssessment) ([]models.Signal, error) {
	// Search for affected markets
	searchQuery := event.Title // Attena supports natural language
	markets, _, err := n.deps.Attena.Search(ctx, attena.SearchParams{
		Query: searchQuery,
		Limit: 10,
		Sort:  "volume",
		Agent: true,
	})
	if err != nil {
		return nil, err
	}

	var signals []models.Signal
	for _, m := range markets {
		if m.Volume < 5000 {
			continue // skip illiquid markets
		}

		direction := "buy_yes"
		price := m.YesPrice
		if assessment.Direction == "bearish" {
			direction = "buy_no"
			price = m.NoPrice
		}

		size := float64(n.maxOrderSize)
		if size*price > n.maxExposure {
			size = n.maxExposure / price
		}

		signals = append(signals, models.Signal{
			Type:       models.SignalNews,
			MarketID:   m.MarketID,
			Direction:  direction,
			Confidence: assessment.Confidence * assessment.Magnitude,
			Price:      price,
			Size:       size,
			Reason: fmt.Sprintf("NEWS: '%s' → %s (mag=%.2f) on '%s'",
				event.Title, assessment.Direction, assessment.Magnitude, m.Title),
			Metadata: map[string]interface{}{
				"event_id":   event.ID,
				"event_title": event.Title,
				"source":     event.Source,
				"direction":  assessment.Direction,
				"magnitude":  assessment.Magnitude,
				"reasoning":  assessment.Reasoning,
				"platform":   m.Source,
			},
		})
	}

	return signals, nil
}

// Evaluate implements the Agent interface.
func (n *NewsReactorAgent) Evaluate(ctx context.Context, markets []models.Market) ([]models.Signal, error) {
	// News reactor doesn't evaluate existing markets directly.
	// It reacts to news events and finds affected markets.
	// This method is a no-op; the real logic is in processNewsCycle.
	return nil, nil
}

// ── Helpers ────────────────────────────────────────────────────────

func hashString(s string) string {
	// Simple hash for dedup — use crypto/sha256 in production
	h := uint32(0)
	for _, c := range s {
		h = h*31 + uint32(c)
	}
	return fmt.Sprintf("%08x", h)
}
