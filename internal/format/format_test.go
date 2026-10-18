package format

import (
	"testing"
	"time"

	"github.com/iBvishal/ctxtop/internal/snapshot"
)

func TestCount(t *testing.T) {
	cases := map[int]string{0: "0", 999: "999", 1000: "1,000", 65536: "65,536", 1234567: "1,234,567", -4096: "-4,096"}
	for n, want := range cases {
		if got := Count(n); got != want {
			t.Errorf("Count(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestModel(t *testing.T) {
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	gpu := uint64(7 << 30)
	unload := now.Add(90 * time.Second)
	queued := 2
	cases := []struct {
		m    snapshot.Model
		want string
	}{
		{snapshot.Model{Name: "a.gguf", ContextSize: 4096}, "a.gguf  ctx 4,096"},
		{snapshot.Model{Name: "qwen3:14b", Quant: "Q4_K_M", Mem: 10 << 30, MemGPU: &gpu, UnloadsAt: &unload},
			"qwen3:14b  Q4_K_M  mem 10.0 GB (7.0 GB GPU, 3.0 GB CPU)  unloads in 1m30s"},
		{snapshot.Model{Name: "q", Mem: 7 << 30, MemGPU: &gpu}, "q  mem 7.0 GB"},
		{snapshot.Model{Name: "q", Status: "generating", Queued: &queued, Parallel: 1}, "q  generating  2 queued (1 parallel)"},
	}
	for _, c := range cases {
		if got := Model(c.m, now); got != c.want {
			t.Errorf("Model() = %q, want %q", got, c.want)
		}
	}
}

func TestRequest(t *testing.T) {
	r := snapshot.Request{PromptTokens: 880, OutputTokens: 41, GenTPS: 44.2, TTFT: 0.31, Latency: 1.24}
	if got, want := Request(r), "in 880  out 41  44.2 t/s  ttft 0.31s  1.24s"; got != want {
		t.Errorf("Request() = %q, want %q", got, want)
	}
	if got := Request(snapshot.Request{Status: 200, Latency: 12.483}); got != "200  12.48s" {
		t.Errorf("Request() = %q", got)
	}
}
