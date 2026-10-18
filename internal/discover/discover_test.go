package discover

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"testing"

	"github.com/iBvishal/ctxtop/internal/llamacpp"
	"github.com/iBvishal/ctxtop/internal/lmstudio"
	"github.com/iBvishal/ctxtop/internal/ollama"
)

const lsofOut = `p412
R1
cllama-server
n127.0.0.1:8080
p977
R1
collama
n127.0.0.1:11434
p1500
R1
cnode
n*:3000
n[::]:3000
p2001
R1
cpostgres
n[::1]:5432
`

func TestParseLsof(t *testing.T) {
	got := ParseLsof([]byte(lsofOut))
	want := []Listener{
		{412, 1, "llama-server", "127.0.0.1", 8080},
		{977, 1, "ollama", "127.0.0.1", 11434},
		{1500, 1, "node", "*", 3000},
		{2001, 1, "postgres", "[::1]", 5432},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d listeners %+v, want %d", len(got), got, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("listener %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestURL(t *testing.T) {
	cases := map[string]string{
		"*":         "http://127.0.0.1:8080",
		"0.0.0.0":   "http://127.0.0.1:8080",
		"[::]":      "http://127.0.0.1:8080",
		"127.0.0.1": "http://127.0.0.1:8080",
		"[::1]":     "http://[::1]:8080",
		"10.0.0.5":  "http://10.0.0.5:8080",
	}
	for host, want := range cases {
		if got := url(host, 8080); got != want {
			t.Errorf("url(%q) = %q, want %q", host, got, want)
		}
	}
}

func port(t *testing.T, srv *httptest.Server) int {
	t.Helper()
	_, p, err := net.SplitHostPort(srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	n, _ := strconv.Atoi(p)
	return n
}

func llamaServer(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/props" {
			_, _ = w.Write([]byte(`{"default_generation_settings":{"n_ctx":4096}}`))
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func ollamaServer(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/version" {
			_, _ = w.Write([]byte(`{"version":"0.40.6"}`))
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestFindByProcessName(t *testing.T) {
	d := New()
	d.Ports = nil
	d.Listeners = func(context.Context) ([]Listener, error) { return ParseLsof([]byte(lsofOut)), nil }

	got := d.Find(context.Background())
	want := []Found{
		{Runtime: llamacpp.Runtime, URL: "http://127.0.0.1:8080", PID: 412, Process: "llama-server"},
		{Runtime: ollama.Runtime, URL: "http://127.0.0.1:11434", PID: 977, Process: "ollama"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestFindByProbe(t *testing.T) {
	llamaSrv, ollamaSrv := llamaServer(t), ollamaServer(t)
	other := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(other.Close)

	d := New()
	d.Ports = []int{port(t, llamaSrv), port(t, ollamaSrv), port(t, other)}
	// A renamed llama-server binary: lsof sees it, but not by a known name.
	d.Listeners = func(context.Context) ([]Listener, error) {
		return []Listener{{PID: 55, Command: "my-llm", Host: "127.0.0.1", Port: port(t, llamaSrv)}}, nil
	}

	got := d.Find(context.Background())
	if len(got) != 2 {
		t.Fatalf("got %+v, want 2 servers", got)
	}
	if got[0].Runtime != llamacpp.Runtime || got[0].PID != 55 || got[0].Process != "my-llm" {
		t.Errorf("llama = %+v", got[0])
	}
	if got[1].Runtime != ollama.Runtime || got[1].PID != 0 {
		t.Errorf("ollama = %+v", got[1])
	}
}

func TestFindWithoutLsof(t *testing.T) {
	llama := llamaServer(t)
	d := New()
	d.Ports = []int{port(t, llama)}
	d.Listeners = func(context.Context) ([]Listener, error) { return nil, nil }

	got := d.Find(context.Background())
	if len(got) != 1 || got[0].Runtime != llamacpp.Runtime {
		t.Fatalf("got %+v", got)
	}
}

func TestFindLMStudioOnItsOwnPort(t *testing.T) {
	lms := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v0/models" {
			_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"qwen3-4b","state":"loaded"}]}`))
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(lms.Close)

	d := New()
	d.Ports = nil
	d.Listeners = func(context.Context) ([]Listener, error) {
		return []Listener{{PID: 801, Command: "LM Studio", Host: "127.0.0.1", Port: port(t, lms)}}, nil
	}
	got := d.Find(context.Background())
	if len(got) != 1 || got[0].Runtime != lmstudio.Runtime || got[0].PID != 801 {
		t.Fatalf("got %+v", got)
	}
}

func TestFindFoldsOllamaRunners(t *testing.T) {
	d := New()
	d.Ports = nil
	d.Listeners = func(context.Context) ([]Listener, error) {
		return []Listener{
			{PID: 977, PPID: 1, Command: "ollama", Host: "127.0.0.1", Port: 11434},
			{PID: 1201, PPID: 977, Command: "llama-server", Host: "127.0.0.1", Port: 55417},
			{PID: 1202, PPID: 977, Command: "llama-server", Host: "127.0.0.1", Port: 55418},
			// An older Ollama runner is an ollama process; it isn't a server either.
			{PID: 1203, PPID: 977, Command: "ollama", Host: "127.0.0.1", Port: 55419},
			{PID: 412, PPID: 1, Command: "llama-server", Host: "127.0.0.1", Port: 8080},
		}, nil
	}
	got := d.Find(context.Background())
	if len(got) != 2 {
		t.Fatalf("got %+v, want llama-server and ollama", got)
	}
	if got[0].Runtime != llamacpp.Runtime || got[0].PID != 412 || len(got[0].Runners) != 0 {
		t.Errorf("llama-server = %+v", got[0])
	}
	o := got[1]
	want := []string{"http://127.0.0.1:55417", "http://127.0.0.1:55418"}
	if o.Runtime != ollama.Runtime || len(o.Runners) != 2 || o.Runners[0] != want[0] || o.Runners[1] != want[1] {
		t.Errorf("ollama = %+v, want runners %v", o, want)
	}
}
