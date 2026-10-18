// Package lmstudio reads a running LM Studio.
//
// With the lms CLI installed, it uses:
//   - `lms ps --json`: loaded models with size, context length, status,
//     queued count and parallel slots (lms src/subcommands/list.ts).
//   - `lms log stream --source model --filter output --json`: one event per
//     finished prediction, carrying LLMPredictionStats (tokens per second,
//     time to first token, token counts). The event also carries the
//     generated text; ctxtop decodes only the stats and never keeps it. The
//     log stream is marked unstable in lmstudio-js.
//
// Without the CLI it falls back to GET /api/v0/models, which lists loaded
// models but nothing about what they're doing.
package lmstudio

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/iBvishal/ctxtop/internal/snapshot"
)

// Runtime is the name ctxtop reports for LM Studio.
const Runtime = "lmstudio"

// psModel is one entry of `lms ps --json`: lmstudio-js LLMInstanceInfo plus
// the processing state lms adds.
type psModel struct {
	Identifier    string `json:"identifier"`
	ModelKey      string `json:"modelKey"`
	SizeBytes     int64  `json:"sizeBytes"`
	ContextLength int    `json:"contextLength"`
	Quantization  *struct {
		Name string `json:"name"`
	} `json:"quantization"`
	Status   string `json:"status"`
	Queued   *int   `json:"queued"`
	Parallel *int   `json:"parallel"`
}

// logEvent is one line of `lms log stream --json`. The output text is
// deliberately not a field.
type logEvent struct {
	Timestamp float64 `json:"timestamp"`
	Data      struct {
		Type            string `json:"type"`
		ModelIdentifier string `json:"modelIdentifier"`
		Stats           *struct {
			StopReason           string  `json:"stopReason"`
			TokensPerSecond      float64 `json:"tokensPerSecond"`
			TimeToFirstTokenSec  float64 `json:"timeToFirstTokenSec"`
			TotalTimeSec         float64 `json:"totalTimeSec"`
			PromptTokensCount    int     `json:"promptTokensCount"`
			PredictedTokensCount int     `json:"predictedTokensCount"`
		} `json:"stats"`
	} `json:"data"`
}

type restModels struct {
	Data []struct {
		ID                  string `json:"id"`
		Quantization        string `json:"quantization"`
		State               string `json:"state"`
		MaxContextLength    int    `json:"max_context_length"`
		LoadedContextLength int    `json:"loaded_context_length"`
	} `json:"data"`
}

// Poller reads one LM Studio. Close stops its log stream.
type Poller struct {
	BaseURL string
	Client  *http.Client
	// LMS is the path to the lms CLI, or empty when it isn't installed.
	LMS string
	// Command builds the commands Poller runs; tests replace it.
	Command func(ctx context.Context, name string, args ...string) *exec.Cmd
	Window  time.Duration
	Now     func() time.Time

	mu       sync.Mutex
	requests []snapshot.Request
	stream   *exec.Cmd
	started  bool
}

func NewPoller(baseURL string) *Poller {
	return &Poller{
		BaseURL: strings.TrimRight(baseURL, "/"),
		Client:  &http.Client{Timeout: 2 * time.Second},
		LMS:     FindLMS(),
		Command: exec.CommandContext,
		Window:  time.Minute,
		Now:     time.Now,
	}
}

// FindLMS returns the lms CLI's path: on PATH, or where LM Studio installs
// it.
func FindLMS() string {
	if p, err := exec.LookPath("lms"); err == nil {
		return p
	}
	if home, err := os.UserHomeDir(); err == nil {
		p := filepath.Join(home, ".lmstudio", "bin", "lms")
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	return ""
}

func (p *Poller) Poll(ctx context.Context) (snapshot.Server, error) {
	srv := snapshot.Server{Runtime: Runtime, URL: p.BaseURL}

	if p.LMS == "" {
		models, err := p.restModels(ctx)
		if err != nil {
			return srv, err
		}
		srv.Models = models
		srv.Error = "install the lms CLI to see speed and queue (lms bootstrap)"
		return srv, nil
	}

	p.startStream()
	models, err := p.psModels(ctx)
	if err != nil {
		// The CLI failed; the REST API still lists what's loaded.
		rest, rerr := p.restModels(ctx)
		if rerr != nil {
			return srv, errors.Join(err, rerr)
		}
		srv.Error = err.Error()
		models = rest
	}
	srv.Models = models
	srv.Requests = p.recentRequests()
	return srv, nil
}

func (p *Poller) psModels(ctx context.Context) ([]snapshot.Model, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := p.Command(ctx, p.LMS, "ps", "--json").Output()
	if err != nil {
		return nil, fmt.Errorf("lms ps: %w", err)
	}
	var ps []psModel
	if err := json.Unmarshal(out, &ps); err != nil {
		return nil, fmt.Errorf("lms ps: %w", err)
	}
	models := make([]snapshot.Model, 0, len(ps))
	for _, m := range ps {
		model := snapshot.Model{
			Name:        m.Identifier,
			ContextSize: m.ContextLength,
			Mem:         uint64(max(m.SizeBytes, 0)),
			Status:      m.Status,
			Queued:      m.Queued,
		}
		if model.Name == "" {
			model.Name = m.ModelKey
		}
		if m.Quantization != nil {
			model.Quant = m.Quantization.Name
		}
		if m.Parallel != nil {
			model.Parallel = *m.Parallel
		}
		models = append(models, model)
	}
	return models, nil
}

func (p *Poller) restModels(ctx context.Context) ([]snapshot.Model, error) {
	var rm restModels
	if err := get(ctx, p.Client, p.BaseURL+"/api/v0/models", &rm); err != nil {
		return nil, err
	}
	var models []snapshot.Model
	for _, m := range rm.Data {
		if m.State != "loaded" {
			continue
		}
		ctxLen := m.LoadedContextLength
		if ctxLen == 0 {
			ctxLen = m.MaxContextLength
		}
		models = append(models, snapshot.Model{Name: m.ID, Quant: m.Quantization, ContextSize: ctxLen})
	}
	return models, nil
}

// startStream starts `lms log stream` once. Its events are collected in the
// background until Close.
func (p *Poller) startStream() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.started {
		return
	}
	p.started = true
	cmd := p.Command(context.Background(), p.LMS, "log", "stream", "--source", "model", "--filter", "output", "--json")
	out, err := cmd.StdoutPipe()
	if err != nil {
		return
	}
	if err := cmd.Start(); err != nil {
		return
	}
	p.stream = cmd
	go p.readStream(out)
}

func (p *Poller) readStream(r io.Reader) {
	br := bufio.NewReaderSize(r, 64<<10)
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			if req, ok := ParseLogEvent(line); ok {
				p.mu.Lock()
				p.requests = append(p.requests, req)
				p.mu.Unlock()
			}
		}
		if err != nil {
			return
		}
	}
}

func (p *Poller) recentRequests() []snapshot.Request {
	p.mu.Lock()
	defer p.mu.Unlock()
	since := p.Now().Add(-p.Window)
	keep := p.requests[:0]
	for _, r := range p.requests {
		if !r.Time.Before(since) {
			keep = append(keep, r)
		}
	}
	p.requests = keep
	if len(keep) == 0 {
		return nil
	}
	return append([]snapshot.Request(nil), keep...)
}

// Close stops the log stream.
func (p *Poller) Close() error {
	p.mu.Lock()
	cmd := p.stream
	p.stream = nil
	p.mu.Unlock()
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	return nil
}

// ParseLogEvent turns one `lms log stream --json` line into a request. Only
// prediction-output events with stats count.
func ParseLogEvent(line []byte) (snapshot.Request, bool) {
	var ev logEvent
	if err := json.Unmarshal(line, &ev); err != nil {
		return snapshot.Request{}, false
	}
	st := ev.Data.Stats
	if ev.Data.Type != "llm.prediction.output" || st == nil {
		return snapshot.Request{}, false
	}
	return snapshot.Request{
		Time:         eventTime(ev.Timestamp),
		Model:        ev.Data.ModelIdentifier,
		Latency:      st.TotalTimeSec,
		PromptTokens: st.PromptTokensCount,
		OutputTokens: st.PredictedTokensCount,
		GenTPS:       st.TokensPerSecond,
		TTFT:         st.TimeToFirstTokenSec,
		StopReason:   st.StopReason,
	}, true
}

// eventTime reads a JavaScript timestamp: milliseconds since the epoch, or
// seconds if it's too small to be milliseconds.
func eventTime(ts float64) time.Time {
	if ts < 1e11 {
		ts *= 1000
	}
	return time.UnixMilli(int64(ts))
}

func get(ctx context.Context, client *http.Client, url string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(v)
}

// Probe reports whether baseURL is LM Studio: its /api/v0/models lists
// models with a load state, which plain OpenAI-compatible servers don't.
func Probe(ctx context.Context, client *http.Client, baseURL string) bool {
	var raw struct {
		Data []map[string]json.RawMessage `json:"data"`
	}
	if get(ctx, client, strings.TrimRight(baseURL, "/")+"/api/v0/models", &raw) != nil || len(raw.Data) == 0 {
		return false
	}
	_, ok := raw.Data[0]["state"]
	return ok
}
