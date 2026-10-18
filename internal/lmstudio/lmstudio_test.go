package lmstudio

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestHelperProcess stands in for the lms CLI. It is not a real test: it
// runs only when a test starts it as a subprocess.
func TestHelperProcess(t *testing.T) {
	if os.Getenv("CTXTOP_FAKE_LMS") != "1" {
		return
	}
	args := os.Args
	for i, a := range args {
		if a == "--" {
			args = args[i+1:]
			break
		}
	}
	switch strings.Join(args[1:], " ") {
	case "ps --json":
		b, _ := os.ReadFile(filepath.Join("testdata", "ps.json"))
		os.Stdout.Write(b)
	case "log stream --source model --filter output --json":
		b, _ := os.ReadFile(filepath.Join("testdata", "logstream.jsonl"))
		os.Stdout.Write(b)
		time.Sleep(10 * time.Second) // a stream stays open until killed
	default:
		os.Exit(2)
	}
	os.Exit(0)
}

func fakeCommand(ctx context.Context, name string, args ...string) *exec.Cmd {
	cs := append([]string{"-test.run=TestHelperProcess", "--", name}, args...)
	cmd := exec.CommandContext(ctx, os.Args[0], cs...)
	cmd.Env = append(os.Environ(), "CTXTOP_FAKE_LMS=1")
	return cmd
}

func restServer(t *testing.T) *httptest.Server {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "models_v0.json"))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v0/models" {
			_, _ = w.Write(b)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestPollWithCLI(t *testing.T) {
	p := NewPoller(restServer(t).URL)
	p.LMS = "lms"
	p.Command = fakeCommand
	p.Now = func() time.Time { return time.UnixMilli(1792300030000) }
	defer p.Close()

	if _, err := p.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Give the stream reader a moment to take the fixture lines.
	deadline := time.Now().Add(5 * time.Second)
	polls := 0
	for time.Now().Before(deadline) {
		srv, err := p.Poll(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if len(srv.Requests) == 2 {
			m := srv.Models[0]
			if m.Name != "qwen3-4b" || m.Quant != "4bit" || m.ContextSize != 32768 || m.Mem != 2640000000 {
				t.Errorf("model = %+v", m)
			}
			if m.Status != "generating" || m.Queued == nil || *m.Queued != 2 || m.Parallel != 1 {
				t.Errorf("state = %s queued %v parallel %d", m.Status, m.Queued, m.Parallel)
			}
			if e := srv.Models[1]; e.Parallel != 0 || e.Status != "idle" {
				t.Errorf("embedding model = %+v", e)
			}
			if srv.Error != "" {
				t.Errorf("error = %q", srv.Error)
			}
			r := srv.Requests[0]
			if r.GenTPS != 44.2 || r.TTFT != 0.31 || r.PromptTokens != 880 || r.OutputTokens != 41 || r.StopReason != "eosFound" {
				t.Errorf("request = %+v", r)
			}
			return
		}
		polls++
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("stream never delivered 2 requests after %d polls", polls)
}

func TestPollWithoutCLI(t *testing.T) {
	p := NewPoller(restServer(t).URL)
	p.LMS = ""
	got, err := p.Poll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Models) != 1 || got.Models[0].Name != "qwen3-4b" || got.Models[0].ContextSize != 16384 {
		t.Errorf("models = %+v", got.Models)
	}
	if !strings.Contains(got.Error, "lms") {
		t.Errorf("error should point at the lms CLI: %q", got.Error)
	}
}

func TestPollCLIFailsFallsBackToREST(t *testing.T) {
	p := NewPoller(restServer(t).URL)
	p.LMS = "lms"
	p.Command = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return fakeCommand(ctx, name, "unknown-subcommand")
	}
	defer p.Close()
	got, err := p.Poll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Models) != 1 || got.Error == "" {
		t.Errorf("server = %+v", got)
	}
}

func TestParseLogEvent(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("testdata", "logstream.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var n int
	for _, line := range strings.Split(string(b), "\n") {
		r, ok := ParseLogEvent([]byte(line))
		if !ok {
			continue
		}
		n++
		if r.Model != "qwen3-4b" || r.Time.Year() != 2026 {
			t.Errorf("request = %+v", r)
		}
	}
	// Server lines, outputs without stats and junk are skipped.
	if n != 2 {
		t.Errorf("parsed %d requests, want 2", n)
	}
}

func TestEventTime(t *testing.T) {
	ms := eventTime(1792300000000)
	s := eventTime(1792300000)
	if !ms.Equal(s) || ms.Year() != 2026 {
		t.Errorf("ms %v, s %v", ms, s)
	}
}

func TestProbe(t *testing.T) {
	srv := restServer(t)
	if !Probe(context.Background(), srv.Client(), srv.URL) {
		t.Error("LM Studio not recognised")
	}
	openai := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"some-model","object":"model"}]}`))
	}))
	defer openai.Close()
	if Probe(context.Background(), openai.Client(), openai.URL) {
		t.Error("plain OpenAI-compatible server recognised as LM Studio")
	}
}
