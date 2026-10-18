package ollama

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var ist = time.FixedZone("IST", 5*3600+1800)

func fakeServer(t *testing.T) *httptest.Server {
	t.Helper()
	ps, err := os.ReadFile(filepath.Join("testdata", "ps.json"))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/version":
			_, _ = w.Write([]byte(`{"version":"0.40.6"}`))
		case "/api/ps":
			_, _ = w.Write(ps)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestPoll(t *testing.T) {
	srv := fakeServer(t)
	p := NewPoller(srv.URL)
	p.LogPaths = []string{filepath.Join("testdata", "missing.log"), filepath.Join("testdata", "server.log")}
	p.Now = func() time.Time { return time.Date(2026, 10, 18, 12, 4, 30, 0, time.Local) }

	got, err := p.Poll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.Runtime != Runtime || got.Build != "0.40.6" {
		t.Errorf("server = %+v", got)
	}
	if len(got.Models) != 2 {
		t.Fatalf("models = %d, want 2", len(got.Models))
	}
	m := got.Models[0]
	if m.Name != "qwen3:14b" || m.Quant != "Q4_K_M" || m.ContextSize != 8192 {
		t.Errorf("model = %+v", m)
	}
	if m.Mem != 10307921510 || m.MemGPU == nil || *m.MemGPU != 7945689497 {
		t.Errorf("mem = %d gpu %v", m.Mem, m.MemGPU)
	}
	want := time.Date(2026, 10, 18, 12, 9, 31, 123456000, ist)
	if m.UnloadsAt == nil || !m.UnloadsAt.Equal(want) {
		t.Errorf("unloads_at = %v, want %v", m.UnloadsAt, want)
	}
	if len(m.Slots) != 0 || m.Queued != nil {
		t.Errorf("ollama has no slots or queue: %+v", m)
	}
	// The fixture log is in time.Local; the window is the last minute.
	if len(got.Requests) != 2 {
		t.Fatalf("requests = %+v, want the 2 within a minute", got.Requests)
	}
}

func TestParseAccessLog(t *testing.T) {
	log, err := os.ReadFile(filepath.Join("testdata", "server.log"))
	if err != nil {
		t.Fatal(err)
	}
	since := time.Date(2026, 10, 18, 12, 0, 0, 0, ist)
	got := ParseAccessLog(log, since, ist)

	// GETs, /api/pull and non-GIN lines are left out.
	if len(got) != 3 {
		t.Fatalf("got %d requests %+v, want 3", len(got), got)
	}
	cases := []struct {
		path    string
		status  int
		latency float64
	}{
		{"/api/chat", 200, 12.483},
		{"/v1/chat/completions", 200, 1.502312},
		{"/api/generate", 500, 0.08725},
	}
	for i, c := range cases {
		r := got[i]
		if r.Path != c.path || r.Status != c.status || math.Abs(r.Latency-c.latency) > 1e-9 {
			t.Errorf("request %d = %+v, want %+v", i, r, c)
		}
	}
	if !got[0].Time.Equal(time.Date(2026, 10, 18, 12, 1, 6, 0, ist)) {
		t.Errorf("time = %v", got[0].Time)
	}

	if late := ParseAccessLog(log, time.Date(2026, 10, 18, 12, 4, 0, 0, ist), ist); len(late) != 1 {
		t.Errorf("since 12:04: got %d requests, want 1", len(late))
	}
}

func TestAccessLogReadIncrementally(t *testing.T) {
	path := filepath.Join(t.TempDir(), "server.log")
	line := func(sec int, p string) string {
		return fmt.Sprintf("[GIN] 2026/10/18 - 12:00:%02d | 200 |   1.5s |       127.0.0.1 | POST     %q\n", sec, p)
	}
	appendLog := func(s string) {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if _, err := f.WriteString(s); err != nil {
			t.Fatal(err)
		}
	}
	p := NewPoller("http://unused")
	p.LogPaths = []string{path}
	p.Now = func() time.Time { return time.Date(2026, 10, 18, 12, 0, 30, 0, time.Local) }

	appendLog(line(1, "/api/chat"))
	if got := p.recentRequests(); len(got) != 1 {
		t.Fatalf("first read: %d requests", len(got))
	}
	// A line still being written isn't parsed until it ends.
	full := line(2, "/api/generate")
	appendLog(full[:20])
	if got := p.recentRequests(); len(got) != 1 {
		t.Fatalf("partial line: %d requests", len(got))
	}
	appendLog(full[20:] + line(3, "/v1/chat/completions"))
	if got := p.recentRequests(); len(got) != 3 || got[2].Path != "/v1/chat/completions" {
		t.Fatalf("after append: %+v", got)
	}
	// Rotation: the file shrinks and is read from the start.
	if err := os.WriteFile(path, []byte(line(4, "/api/embed")), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := p.recentRequests(); len(got) != 4 || got[3].Path != "/api/embed" {
		t.Fatalf("after rotation: %+v", got)
	}
	// Requests age out of the window.
	p.Now = func() time.Time { return time.Date(2026, 10, 18, 12, 1, 4, 0, time.Local) }
	if got := p.recentRequests(); len(got) != 1 {
		t.Errorf("after the window: %+v", got)
	}
}

func TestAccessLogFirstReadIsBounded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "server.log")
	var b strings.Builder
	for b.Len() < maxLogRead+4096 {
		b.WriteString("[GIN] 2026/10/18 - 12:00:01 | 200 |   45µs |       127.0.0.1 | GET      \"/api/ps\"\n")
	}
	b.WriteString("[GIN] 2026/10/18 - 12:00:02 | 200 |   1.5s |       127.0.0.1 | POST     \"/api/chat\"\n")
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	p := NewPoller("http://unused")
	p.LogPaths = []string{path}
	p.Now = func() time.Time { return time.Date(2026, 10, 18, 12, 0, 30, 0, time.Local) }
	if got := p.recentRequests(); len(got) != 1 || got[0].Path != "/api/chat" {
		t.Errorf("got %+v", got)
	}
	if p.logOff != int64(b.Len()) {
		t.Errorf("offset %d, want %d", p.logOff, b.Len())
	}
}

func TestProbe(t *testing.T) {
	srv := fakeServer(t)
	if !Probe(context.Background(), srv.Client(), srv.URL) {
		t.Error("ollama not recognised")
	}
	other := httptest.NewServer(http.NotFoundHandler())
	defer other.Close()
	if Probe(context.Background(), other.Client(), other.URL) {
		t.Error("non-ollama server recognised")
	}
}

func TestManifestPath(t *testing.T) {
	cases := map[string]string{
		"qwen3:14b":           "manifests/registry.ollama.ai/library/qwen3/14b",
		"qwen3":               "manifests/registry.ollama.ai/library/qwen3/latest",
		"me/model:q4":         "manifests/registry.ollama.ai/me/model/q4",
		"hf.co/org/repo:Q8_0": "manifests/hf.co/org/repo/Q8_0",
		"localhost:5000/a/b":  "manifests/localhost:5000/a/b/latest",
	}
	for name, want := range cases {
		if got := manifestPath("", name); got != filepath.FromSlash(want) {
			t.Errorf("manifestPath(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestPollAttachesRunnerSlots(t *testing.T) {
	read := func(name string) []byte {
		b, err := os.ReadFile(filepath.Join("testdata", name))
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	runner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/props":
			_, _ = w.Write(read("runner_props.json"))
		case "/slots":
			_, _ = w.Write(read("runner_slots.json"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer runner.Close()

	p := NewPoller(fakeServer(t).URL)
	p.LogPaths = nil
	p.ModelsDir = filepath.Join("testdata", "models")
	p.Runners = []string{runner.URL}

	got, err := p.Poll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	qwen, embed := got.Models[0], got.Models[1]
	if len(qwen.Slots) != 1 || qwen.Slots[0].ContextUsed != 6200 || !qwen.Slots[0].Busy {
		t.Errorf("qwen3:14b slots = %+v", qwen.Slots)
	}
	if len(embed.Slots) != 0 {
		t.Errorf("the embedding model has no runner, got slots %+v", embed.Slots)
	}
}

func TestPollSingleRunnerWithoutManifest(t *testing.T) {
	runner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/props":
			_, _ = w.Write([]byte(`{"default_generation_settings":{"n_ctx":4096},"model_path":"/x/blobs/sha256-ffff"}`))
		case "/slots":
			_, _ = w.Write([]byte(`[{"id":0,"n_ctx":4096,"is_processing":false}]`))
		}
	}))
	defer runner.Close()
	one := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/version":
			_, _ = w.Write([]byte(`{"version":"0.40.6"}`))
		case "/api/ps":
			_, _ = w.Write([]byte(`{"models":[{"name":"custom:latest","size":100,"size_vram":100,"context_length":4096}]}`))
		}
	}))
	defer one.Close()

	p := NewPoller(one.URL)
	p.LogPaths = nil
	p.ModelsDir = t.TempDir()
	p.Runners = []string{runner.URL}
	got, err := p.Poll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Models[0].Slots) != 1 {
		t.Errorf("one model, one runner: slots = %+v", got.Models[0].Slots)
	}
}
