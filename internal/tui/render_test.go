package tui

import (
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/iBvishal/ctxtop/internal/snapshot"
)

func demo() snapshot.Snapshot {
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	gpuMem := uint64(7 << 30)
	util := 88.0
	return snapshot.Snapshot{
		Time: now,
		Host: snapshot.Host{
			OS: "darwin", Arch: "arm64", Chip: "Apple M4",
			MemTotal: 16 << 30, MemUsed: 12 << 30, SwapTotal: 2 << 30, SwapUsed: 1 << 30,
			Pressure: "warn", GPUUtil: &util, GPUMem: 3 << 30, Power: &snapshot.Power{CPU: 4.2, GPU: 14},
		},
		Advice: []snapshot.Advice{{
			Rule: "context-full", Level: "warn", Subject: "gemma-3-4b-it-Q8_0.gguf, slot 0",
			Message: "Context is 89% (7,263 of 8,192 tokens) full. When it fills, generation stops and the reply is cut off.",
			Fix:     "Restart llama-server with a larger -c. It's split across 2 slots, so each gets 8,192 of -c 16,384.",
		}},
		Servers: []snapshot.Server{
			{Runtime: "llama.cpp", URL: "http://127.0.0.1:8080", PID: 412, Build: "b11669",
				Models: []snapshot.Model{{Name: "gemma-3-4b-it-Q8_0.gguf", ContextSize: 8192, Slots: []snapshot.Slot{
					{ID: 0, Busy: true, ContextSize: 8192, ContextUsed: 7263, ContextFill: 7263.0 / 8192, GenTPS: 51},
					{ID: 1, ContextSize: 8192},
				}}}},
			{Runtime: "ollama", URL: "http://127.0.0.1:11434", PID: 977,
				Models:   []snapshot.Model{{Name: "qwen3:14b", Quant: "Q4_K_M", Mem: 10 << 30, MemGPU: &gpuMem}},
				Requests: []snapshot.Request{{Time: now.Add(-30 * time.Second), Path: "/api/chat", Status: 200, Latency: 12.5}}},
			{Runtime: "lmstudio", URL: "http://127.0.0.1:1234", Error: "lms ps: exit status 1",
				Requests: []snapshot.Request{{Time: now.Add(-5 * time.Second), Model: "qwen3-4b", OutputTokens: 41, GenTPS: 44.2}}},
		},
	}
}

func TestRenderShowsEverything(t *testing.T) {
	out := ansi.Strip(Render(demo(), true, 0, 0, time.Second))
	for _, want := range []string{
		"ctxtop  Apple M4",
		"12.0 GB / 16.0 GB",
		"PRESSURE warn",
		"GPU",
		" 88%",
		"GPU MEM 3.0 GB",
		"cpu 4.2 W  gpu 14.0 W",
		"llama.cpp  127.0.0.1:8080  pid 412  b11669",
		"slot 0  busy",
		"89%  7,263 / 8,192",
		"gen 51.0 t/s",
		"qwen3:14b  Q4_K_M  mem 10.0 GB (7.0 GB GPU, 3.0 GB CPU)",
		"! lms ps: exit status 1",
		"ADVICE",
		"▲ gemma-3-4b-it-Q8_0.gguf, slot 0  Context is 89%",
		"→ Restart llama-server with a larger -c.",
		"RECENT REQUESTS",
		"q quit  r rescan  every 1s",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	// Newest request first.
	if strings.Index(out, "qwen3-4b") > strings.Index(out, "/api/chat") {
		t.Error("requests are not newest first")
	}
}

func TestRenderFitsTheTerminal(t *testing.T) {
	const w, h = 60, 12
	out := Render(demo(), true, w, h, time.Second)
	lines := strings.Split(out, "\n")
	if len(lines) != h {
		t.Fatalf("got %d lines, want %d", len(lines), h)
	}
	for i, l := range lines {
		if lipgloss.Width(l) > w {
			t.Errorf("line %d is %d wide: %q", i, lipgloss.Width(l), ansi.Strip(l))
		}
	}
	if !strings.Contains(ansi.Strip(lines[h-1]), "q quit") {
		t.Errorf("footer not on the last line: %q", ansi.Strip(lines[h-1]))
	}
}

func TestRenderStates(t *testing.T) {
	if out := ansi.Strip(Render(snapshot.Snapshot{}, false, 80, 5, time.Second)); !strings.Contains(out, "looking for LLM servers") {
		t.Errorf("before the first poll: %q", out)
	}
	if out := ansi.Strip(Render(snapshot.Snapshot{}, true, 0, 0, time.Second)); !strings.Contains(out, "No LLM servers found") {
		t.Errorf("no servers: %q", out)
	}
}

func TestBar(t *testing.T) {
	cases := map[float64]int{0: 0, 0.5: 6, 1: 12, 1.7: 12, -1: 0}
	for frac, filled := range cases {
		got := strings.Count(ansi.Strip(bar(frac)), "█")
		if got != filled {
			t.Errorf("bar(%v) filled %d, want %d", frac, got, filled)
		}
	}
}

func TestHostFitsEightyColumns(t *testing.T) {
	for i, l := range hostLines(demo().Host) {
		if w := lipgloss.Width(l); w > 80 {
			t.Errorf("host line %d is %d wide: %q", i, w, ansi.Strip(l))
		}
	}
}

func TestAdviceWrapsInsteadOfCutting(t *testing.T) {
	lines := adviceLines(demo().Advice, 60)
	joined := ansi.Strip(strings.Join(lines, " "))
	for _, word := range []string{"cut", "off.", "16,384."} {
		if !strings.Contains(joined, word) {
			t.Errorf("lost %q when wrapping: %q", word, joined)
		}
	}
	for i, l := range lines {
		if w := lipgloss.Width(l); w > 60 {
			t.Errorf("line %d is %d wide: %q", i, w, ansi.Strip(l))
		}
	}
}

func TestWrap(t *testing.T) {
	got := wrap("one two three four five six seven eight nine ten eleven twelve", 20)
	for _, l := range got {
		if len(l) > 20 {
			t.Errorf("line too long: %q", l)
		}
	}
	if strings.Join(got, " ") != "one two three four five six seven eight nine ten eleven twelve" {
		t.Errorf("words changed: %q", got)
	}
}
