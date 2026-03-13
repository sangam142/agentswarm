package attena

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"agentswarm/internal/models"
)

// Client wraps the Attena Search API.
type Client struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
}

func NewClient(baseURL, apiKey string) *Client {
	return &Client{
		baseURL: baseURL,
		apiKey:  apiKey,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
			Transport: &http.Transport{
				MaxIdleConns:        20,
				MaxIdleConnsPerHost: 20,
				IdleConnTimeout:     90 * time.Second,
			},
		},
	}
}

// ── Request / Response Types ────────────────────────────────────────

type SearchParams struct {
	Query    string
	Limit    int
	Offset   int
	Category string // sports, crypto, weather, politics, geopolitics, economics
	Source   string // kalshi, polymarket
	Sort     string // relevance, volume, trending, closing_soon, newest
	Agent    bool   // skip LLM normalizer (~500ms faster)
}

type searchResponse struct {
	Query   string      `json:"query"`
	Results []rawMarket `json:"results"`
	Meta    searchMeta  `json:"meta"`
}

type searchMeta struct {
	Total     int     `json:"total"`
	LatencyMs float64 `json:"latency_ms"`
}

type rawMarket struct {
	ID           string  `json:"id"`
	Title        string  `json:"title"`
	Category     string  `json:"category"`
	Subcategory  string  `json:"subcategory"`
	Source       string  `json:"source"`
	MarketID     string  `json:"market_id"`
	YesPrice     float64 `json:"yes_price"`
	NoPrice      float64 `json:"no_price"`
	Volume       float64 `json:"volume"`
	Volume24h    float64 `json:"volume_24h"`
	SourceURL    string  `json:"source_url"`
	CloseTime    *string `json:"close_time"`
	OutcomeLabel string  `json:"outcome_label"`
	BracketCount int     `json:"bracket_count"`
	Ticker       string  `json:"ticker"`
	Rank         float64 `json:"rank"`
}

// ── Core Methods ────────────────────────────────────────────────────

// Search queries the Attena API and returns normalized markets.
func (c *Client) Search(ctx context.Context, params SearchParams) ([]models.Market, int, error) {
	u, err := url.Parse(c.baseURL)
	if err != nil {
		return nil, 0, fmt.Errorf("invalid base URL: %w", err)
	}

	q := u.Query()
	if params.Query != "" {
		q.Set("q", params.Query)
	}
	if params.Limit > 0 {
		q.Set("limit", strconv.Itoa(params.Limit))
	}
	if params.Offset > 0 {
		q.Set("offset", strconv.Itoa(params.Offset))
	}
	if params.Category != "" {
		q.Set("category", params.Category)
	}
	if params.Source != "" {
		q.Set("source", params.Source)
	}
	if params.Sort != "" {
		q.Set("sort", params.Sort)
	}
	if params.Agent {
		q.Set("agent", "true")
	}
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, 0, err
	}
	if c.apiKey != "" {
		req.Header.Set("X-API-Key", c.apiKey)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("attena request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == 429 {
		return nil, 0, fmt.Errorf("attena rate limited (429)")
	}
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return nil, 0, fmt.Errorf("attena HTTP %d: %s", resp.StatusCode, string(body))
	}

	var sr searchResponse
	if err := json.NewDecoder(resp.Body).Decode(&sr); err != nil {
		return nil, 0, fmt.Errorf("decode error: %w", err)
	}

	markets := make([]models.Market, 0, len(sr.Results))
	now := time.Now()
	for _, r := range sr.Results {
		m := models.Market{
			ID:           r.ID,
			Title:        r.Title,
			Category:     r.Category,
			Subcategory:  r.Subcategory,
			Source:       r.Source,
			MarketID:     r.MarketID,
			Ticker:       r.Ticker,
			YesPrice:     r.YesPrice,
			NoPrice:      r.NoPrice,
			Volume:       r.Volume,
			Volume24h:    r.Volume24h,
			SourceURL:    r.SourceURL,
			OutcomeLabel: r.OutcomeLabel,
			BracketCount: r.BracketCount,
			FetchedAt:    now,
		}
		if r.CloseTime != nil {
			if t, err := time.Parse(time.RFC3339, *r.CloseTime); err == nil {
				m.CloseTime = t
			}
		}
		markets = append(markets, m)
	}

	return markets, sr.Meta.Total, nil
}

// SearchAll fetches markets from both platforms for a given query.
// Returns two slices: kalshi markets and polymarket markets.
func (c *Client) SearchAll(ctx context.Context, query string, limit int) (kalshi, poly []models.Market, err error) {
	// Fetch from both sources in parallel
	type result struct {
		markets []models.Market
		source  string
		err     error
	}
	ch := make(chan result, 2)

	for _, src := range []string{"kalshi", "polymarket"} {
		go func(source string) {
			mkts, _, err := c.Search(ctx, SearchParams{
				Query:  query,
				Limit:  limit,
				Source: source,
				Sort:   "volume",
				Agent:  true,
			})
			ch <- result{mkts, source, err}
		}(src)
	}

	for i := 0; i < 2; i++ {
		r := <-ch
		if r.err != nil {
			err = r.err
			continue
		}
		switch r.source {
		case "kalshi":
			kalshi = r.markets
		case "polymarket":
			poly = r.markets
		}
	}
	return
}

// FetchByCategory returns top markets in a category sorted by volume.
func (c *Client) FetchByCategory(ctx context.Context, category string, limit int) ([]models.Market, error) {
	mkts, _, err := c.Search(ctx, SearchParams{
		Category: category,
		Limit:    limit,
		Sort:     "volume",
		Agent:    true,
	})
	return mkts, err
}

// FetchTrending returns the most actively traded markets right now.
func (c *Client) FetchTrending(ctx context.Context, limit int) ([]models.Market, error) {
	mkts, _, err := c.Search(ctx, SearchParams{
		Limit: limit,
		Sort:  "trending",
		Agent: true,
	})
	return mkts, err
}
