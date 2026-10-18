// Package llamacpp reads a running llama-server.
//
// It polls GET /props (model, context size, whether /metrics is on) and
// GET /slots (per-slot progress). /slots is on by default and, unless the
// server runs with LLAMA_SERVER_SLOTS_DEBUG, carries no prompt text. Rates
// come from the change in each slot's counters between two polls.
package llamacpp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/iBvishal/ctxtop/internal/snapshot"
)

// Runtime is the name ctxtop reports for llama-server.
const Runtime = "llama.cpp"

type props struct {
	DefaultGenerationSettings struct {
		NCtx int `json:"n_ctx"`
	} `json:"default_generation_settings"`
	TotalSlots      int    `json:"total_slots"`
	ModelAlias      string `json:"model_alias"`
	ModelPath       string `json:"model_path"`
	BuildInfo       string `json:"build_info"`
	EndpointMetrics *bool  `json:"endpoint_metrics"`
}

type slot struct {
	ID                     int  `json:"id"`
	NCtx                   int  `json:"n_ctx"`
	IsProcessing           bool `json:"is_processing"`
	IDTask                 *int `json:"id_task"`
	NPromptTokens          int  `json:"n_prompt_tokens"`
	NPromptTokensProcessed int  `json:"n_prompt_tokens_processed"`
	NPromptTokensCache     int  `json:"n_prompt_tokens_cache"`
	NextToken              []struct {
		NDecoded int `json:"n_decoded"`
	} `json:"next_token"`
}

func (s slot) decoded() int {
	if len(s.NextToken) == 0 {
		return 0
	}
	return s.NextToken[0].NDecoded
}

type sample struct {
	at    time.Time
	slots map[int]slot
}

// Poller reads one server and remembers the previous poll to compute rates.
// It is not safe for concurrent use.
type Poller struct {
	BaseURL string
	Client  *http.Client
	// Now is the clock; tests replace it.
	Now  func() time.Time
	prev *sample
}

func NewPoller(baseURL string) *Poller {
	return &Poller{
		BaseURL: strings.TrimRight(baseURL, "/"),
		Client:  &http.Client{Timeout: 2 * time.Second},
		Now:     time.Now,
	}
}

// Poll reads the server once. The first poll has no rates; later polls
// measure against the one before.
func (p *Poller) Poll(ctx context.Context) (snapshot.Server, error) {
	srv := snapshot.Server{Runtime: Runtime, URL: p.BaseURL}

	var pr props
	if err := p.get(ctx, "/props", &pr); err != nil {
		return srv, err
	}
	srv.Build = pr.BuildInfo
	model := snapshot.Model{
		Name:           modelName(pr),
		ContextSize:    pr.DefaultGenerationSettings.NCtx,
		MetricsEnabled: pr.EndpointMetrics,
	}

	var slots []slot
	if err := p.get(ctx, "/slots", &slots); err != nil {
		// --no-slots or --api-key: keep what /props told us.
		srv.Error = err.Error()
		srv.Models = []snapshot.Model{model}
		p.prev = nil
		return srv, nil
	}

	cur := &sample{at: p.Now(), slots: make(map[int]slot, len(slots))}
	for _, s := range slots {
		cur.slots[s.ID] = s
		model.Slots = append(model.Slots, p.slotState(s, cur.at))
	}
	p.prev = cur
	srv.Models = []snapshot.Model{model}
	return srv, nil
}

func (p *Poller) slotState(s slot, at time.Time) snapshot.Slot {
	out := snapshot.Slot{
		ID:          s.ID,
		Busy:        s.IsProcessing,
		ContextSize: s.NCtx,
		// The slot's token list holds the prompt and every generated token,
		// so it is what occupies the context.
		ContextUsed: s.NPromptTokens,
		Generated:   s.decoded(),
		Cached:      s.NPromptTokensCache,
	}
	if s.NCtx > 0 {
		out.ContextFill = float64(s.NPromptTokens) / float64(s.NCtx)
	}
	if p.prev == nil || !s.IsProcessing || s.IDTask == nil {
		return out
	}
	old, ok := p.prev.slots[s.ID]
	if !ok || old.IDTask == nil || *old.IDTask != *s.IDTask {
		return out
	}
	dt := at.Sub(p.prev.at).Seconds()
	if dt <= 0 {
		return out
	}
	if d := s.decoded() - old.decoded(); d > 0 {
		out.GenTPS = float64(d) / dt
	}
	if d := s.NPromptTokensProcessed - old.NPromptTokensProcessed; d > 0 {
		out.PromptTPS = float64(d) / dt
	}
	return out
}

func modelName(pr props) string {
	if pr.ModelAlias != "" && pr.ModelAlias != pr.ModelPath {
		return pr.ModelAlias
	}
	if pr.ModelPath != "" {
		return filepath.Base(pr.ModelPath)
	}
	return pr.ModelAlias
}

func (p *Poller) get(ctx context.Context, path string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.BaseURL+path, nil)
	if err != nil {
		return err
	}
	resp, err := p.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: %s", path, errorMessage(resp.Status, body))
	}
	if err := json.Unmarshal(body, v); err != nil {
		return fmt.Errorf("GET %s: %w", path, err)
	}
	return nil
}

// errorMessage pulls llama-server's {"error":{"message":...}} out of a
// failed response, falling back to the HTTP status.
func errorMessage(status string, body []byte) string {
	var e struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &e) == nil && e.Error.Message != "" {
		return e.Error.Message
	}
	return status
}

// Probe reports whether baseURL is a llama-server, by the shape of /props.
func Probe(ctx context.Context, client *http.Client, baseURL string) bool {
	p := &Poller{BaseURL: strings.TrimRight(baseURL, "/"), Client: client}
	var raw map[string]json.RawMessage
	if err := p.get(ctx, "/props", &raw); err != nil {
		return false
	}
	_, ok := raw["default_generation_settings"]
	return ok
}
