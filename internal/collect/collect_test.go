package collect

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/iBvishal/ctxtop/internal/advice"
	"github.com/iBvishal/ctxtop/internal/discover"
	"github.com/iBvishal/ctxtop/internal/llamacpp"
	"github.com/iBvishal/ctxtop/internal/snapshot"
)

// llamaServer serves a busy slot whose decoded count grows by 10 per poll.
func llamaServer(t *testing.T) *httptest.Server {
	t.Helper()
	decoded := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/props":
			_, _ = w.Write([]byte(`{"default_generation_settings":{"n_ctx":4096},"model_path":"/m/demo.gguf"}`))
		case "/slots":
			decoded += 10
			_, _ = w.Write([]byte(`[{"id":0,"id_task":1,"n_ctx":4096,"is_processing":true,"n_prompt_tokens":100,` +
				`"next_token":[{"n_decoded":` + strconv.Itoa(decoded) + `}]}]`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newTest(found func() []discover.Found) (*Collector, *int) {
	calls := 0
	c := &Collector{
		Discover: func(context.Context) []discover.Found { calls++; return found() },
		ReadHost: func() snapshot.Host { return snapshot.Host{OS: "test"} },
	}
	return c, &calls
}

func TestPollersSurviveRounds(t *testing.T) {
	srv := llamaServer(t)
	c, calls := newTest(func() []discover.Found {
		return []discover.Found{{Runtime: llamacpp.Runtime, URL: srv.URL, PID: 7}}
	})
	c.Rediscover = time.Hour
	defer c.Close()

	first := c.Snapshot(context.Background())
	if first.Servers[0].Models[0].Slots[0].GenTPS != 0 {
		t.Error("first round has a rate")
	}
	second := c.Snapshot(context.Background())
	s := second.Servers[0]
	if s.PID != 7 || s.Models[0].Slots[0].GenTPS <= 0 {
		t.Errorf("second round = %+v", s)
	}
	if *calls != 1 {
		t.Errorf("discovery ran %d times, want 1", *calls)
	}
}

func TestRescanAndRemoval(t *testing.T) {
	srv := llamaServer(t)
	present := true
	c, calls := newTest(func() []discover.Found {
		if !present {
			return nil
		}
		return []discover.Found{{Runtime: llamacpp.Runtime, URL: srv.URL}}
	})
	defer c.Close()

	if got := c.Snapshot(context.Background()); len(got.Servers) != 1 {
		t.Fatalf("servers = %d", len(got.Servers))
	}
	present = false
	c.Rescan()
	if got := c.Snapshot(context.Background()); len(got.Servers) != 0 {
		t.Errorf("server still listed after it went away: %+v", got.Servers)
	}
	if *calls != 2 {
		t.Errorf("discovery ran %d times, want 2", *calls)
	}
}

func TestUnknownAndUnreachable(t *testing.T) {
	c, _ := newTest(func() []discover.Found {
		return []discover.Found{
			{Runtime: "", URL: "http://127.0.0.1:1"},
			{Runtime: llamacpp.Runtime, URL: "http://127.0.0.1:1", PID: 9},
		}
	})
	defer c.Close()
	got := c.Snapshot(context.Background())
	if got.Servers[0].Runtime != "unknown" || got.Servers[0].Error == "" {
		t.Errorf("unknown = %+v", got.Servers[0])
	}
	if got.Servers[1].Error == "" || got.Servers[1].PID != 9 {
		t.Errorf("unreachable = %+v", got.Servers[1])
	}
	if got.Host.OS != "test" || got.Version != snapshot.Version {
		t.Errorf("snapshot = %+v", got)
	}
}

func TestSnapshotCarriesAdvice(t *testing.T) {
	c, _ := newTest(func() []discover.Found { return nil })
	c.ReadHost = func() snapshot.Host { return snapshot.Host{Pressure: "critical", MemTotal: 16 << 30} }
	c.Advice = advice.New()
	got := c.Snapshot(context.Background())
	if len(got.Advice) != 1 || got.Advice[0].Rule != "memory-pressure" {
		t.Errorf("advice = %+v", got.Advice)
	}
}
