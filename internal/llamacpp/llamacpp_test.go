package llamacpp

import (
	"context"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeServer serves testdata files. slots is the sequence of /slots
// fixtures; each request takes the next one and the last one repeats.
func fakeServer(t *testing.T, slots ...string) *httptest.Server {
	t.Helper()
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/props":
			serveFile(t, w, "props.json", http.StatusOK)
		case "/slots":
			name := slots[min(n, len(slots)-1)]
			n++
			status := http.StatusOK
			if strings.Contains(name, "disabled") {
				status = http.StatusNotImplemented
			}
			serveFile(t, w, name, status)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func serveFile(t *testing.T, w http.ResponseWriter, name string, status int) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(b)
}

// stepClock returns a clock that advances by step on every call.
func stepClock(step time.Duration) func() time.Time {
	t := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	return func() time.Time {
		now := t
		t = t.Add(step)
		return now
	}
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestPollFirstHasNoRates(t *testing.T) {
	srv := fakeServer(t, "slots_t0.json")
	p := NewPoller(srv.URL)

	got, err := p.Poll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.Runtime != Runtime || got.Build != "b7012-acme123" || got.Error != "" {
		t.Fatalf("server = %+v", got)
	}
	if len(got.Models) != 1 {
		t.Fatalf("models = %d, want 1", len(got.Models))
	}
	m := got.Models[0]
	if m.Name != "gemma-3-4b-it-Q8_0.gguf" || m.ContextSize != 8192 {
		t.Errorf("model = %q ctx %d", m.Name, m.ContextSize)
	}
	if m.MetricsEnabled == nil || *m.MetricsEnabled {
		t.Errorf("metrics_enabled = %v, want false", m.MetricsEnabled)
	}
	if len(m.Slots) != 2 {
		t.Fatalf("slots = %d, want 2", len(m.Slots))
	}
	s := m.Slots[0]
	if !s.Busy || s.ContextUsed != 7212 || s.Generated != 12 || s.Cached != 200 {
		t.Errorf("slot 0 = %+v", s)
	}
	if !near(s.ContextFill, 7212.0/8192.0) {
		t.Errorf("fill = %v", s.ContextFill)
	}
	if s.GenTPS != 0 || s.PromptTPS != 0 {
		t.Errorf("first poll has rates: %+v", s)
	}
	if idle := m.Slots[1]; idle.Busy || idle.ContextUsed != 0 || idle.ContextFill != 0 {
		t.Errorf("idle slot = %+v", idle)
	}
}

func TestPollRatesBetweenPolls(t *testing.T) {
	srv := fakeServer(t, "slots_t0.json", "slots_t1.json")
	p := NewPoller(srv.URL)
	p.Now = stepClock(time.Second)

	if _, err := p.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, err := p.Poll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	slots := got.Models[0].Slots

	// Slot 0 decoded 63-12 = 51 tokens in 1 s, and processed no prompt.
	if !near(slots[0].GenTPS, 51) || slots[0].PromptTPS != 0 {
		t.Errorf("slot 0 rates = gen %v prompt %v", slots[0].GenTPS, slots[0].PromptTPS)
	}
	// Slot 1 started a new task between polls: no rate, rather than a
	// guess over a partial interval.
	if slots[1].GenTPS != 0 || slots[1].PromptTPS != 0 {
		t.Errorf("slot 1 rates = %+v", slots[1])
	}
	if !slots[1].Busy || slots[1].ContextUsed != 410 {
		t.Errorf("slot 1 = %+v", slots[1])
	}
}

func TestPollSlotsDisabled(t *testing.T) {
	srv := fakeServer(t, "slots_disabled.json")
	got, err := NewPoller(srv.URL).Poll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.Error, "does not support slots endpoint") {
		t.Errorf("error = %q", got.Error)
	}
	if len(got.Models) != 1 || got.Models[0].ContextSize != 8192 || len(got.Models[0].Slots) != 0 {
		t.Errorf("models = %+v", got.Models)
	}
}

func TestPollUnreachable(t *testing.T) {
	srv := fakeServer(t, "slots_t0.json")
	url := srv.URL
	srv.Close()
	if _, err := NewPoller(url).Poll(context.Background()); err == nil {
		t.Fatal("want an error for a closed server")
	}
}

func TestProbe(t *testing.T) {
	srv := fakeServer(t, "slots_t0.json")
	if !Probe(context.Background(), srv.Client(), srv.URL) {
		t.Error("llama-server not recognised")
	}
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"version":"0.40.6"}`))
	}))
	defer other.Close()
	if Probe(context.Background(), other.Client(), other.URL) {
		t.Error("non-llama server recognised")
	}
}

func TestModelName(t *testing.T) {
	cases := []struct {
		alias, path, want string
	}{
		{"", "/m/qwen.gguf", "qwen.gguf"},
		{"/m/qwen.gguf", "/m/qwen.gguf", "qwen.gguf"},
		{"my-model", "/m/qwen.gguf", "my-model"},
		{"my-model", "", "my-model"},
	}
	for _, c := range cases {
		if got := modelName(props{ModelAlias: c.alias, ModelPath: c.path}); got != c.want {
			t.Errorf("modelName(%q, %q) = %q, want %q", c.alias, c.path, got, c.want)
		}
	}
}

func TestPropsAreCached(t *testing.T) {
	propsReads := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/props" {
			propsReads++
			serveFile(t, w, "props.json", http.StatusOK)
			return
		}
		serveFile(t, w, "slots_t0.json", http.StatusOK)
	}))
	defer srv.Close()

	p := NewPoller(srv.URL)
	p.Now = stepClock(3 * time.Second)
	for range 4 {
		if _, err := p.Poll(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	// Polls at 0, 3, 6 and 9 s with a 10 s refresh: one read.
	if propsReads != 1 {
		t.Errorf("/props read %d times, want 1", propsReads)
	}
	if _, err := p.Poll(context.Background()); err != nil { // 12 s
		t.Fatal(err)
	}
	if propsReads != 2 {
		t.Errorf("/props read %d times after 12 s, want 2", propsReads)
	}
}

func TestServerGoneWithCachedProps(t *testing.T) {
	srv := fakeServer(t, "slots_t0.json")
	p := NewPoller(srv.URL)
	if _, err := p.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	srv.Close()
	if _, err := p.Poll(context.Background()); err == nil {
		t.Fatal("want an error once the server is gone, even with /props cached")
	}
}
