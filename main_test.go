package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/iBvishal/ctxtop/internal/snapshot"
)

func TestPrintText(t *testing.T) {
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	gpu := uint64(7 << 30)
	unload := now.Add(4*time.Minute + 30*time.Second)
	queued := 2
	s := snapshot.Snapshot{
		Time: now,
		Host: snapshot.Host{OS: "darwin", Arch: "arm64", MemTotal: 16 << 30, MemUsed: 12 << 30, Pressure: "warn"},
		Servers: []snapshot.Server{
			{Runtime: "llama.cpp", URL: "http://127.0.0.1:8080", PID: 412, Models: []snapshot.Model{{
				Name: "gemma-3-4b-it-Q8_0.gguf", ContextSize: 8192,
				Slots: []snapshot.Slot{
					{ID: 0, Busy: true, ContextSize: 8192, ContextUsed: 7263, ContextFill: 7263.0 / 8192, GenTPS: 51},
					{ID: 1, ContextSize: 8192},
				},
			}}},
			{Runtime: "ollama", URL: "http://127.0.0.1:11434", Models: []snapshot.Model{{
				Name: "qwen3:14b", Quant: "Q4_K_M", ContextSize: 8192,
				Mem: 10 << 30, MemGPU: &gpu, UnloadsAt: &unload,
			}}, Requests: []snapshot.Request{
				{Time: now, Path: "/api/chat", Status: 200, Latency: 12.5},
			}},
			{Runtime: "lmstudio", URL: "http://127.0.0.1:1234", Error: "install the lms CLI to see speed and queue (lms bootstrap)",
				Models:   []snapshot.Model{{Name: "qwen3-4b", Status: "generating", Queued: &queued, Parallel: 1}},
				Requests: []snapshot.Request{{Time: now, Model: "qwen3-4b", PromptTokens: 880, OutputTokens: 41, GenTPS: 44.2, TTFT: 0.31}},
			},
		},
	}
	var b bytes.Buffer
	if err := printText(&b, s); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	for _, want := range []string{
		"memory 12.0 GB / 16.0 GB",
		"pressure warn",
		"llama.cpp  http://127.0.0.1:8080  pid 412",
		"7,263 / 8,192",
		"89%",
		"51.0",
		"qwen3:14b  Q4_K_M  ctx 8,192  mem 10.0 GB (7.0 GB GPU, 3.0 GB CPU)  unloads in 4m30s",
		"/api/chat  200  12.50s",
		"qwen3-4b  generating  2 queued (1 parallel)",
		"in 880  out 41",
		"44.2 t/s",
		"ttft 0.31s",
		"! install the lms CLI",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

func TestPrintTextNoServers(t *testing.T) {
	var b bytes.Buffer
	if err := printText(&b, snapshot.Snapshot{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), "No LLM servers found") {
		t.Errorf("output = %q", b.String())
	}
}

func TestPrintDoctor(t *testing.T) {
	cases := []struct {
		advice []snapshot.Advice
		code   int
		want   string
	}{
		{nil, 0, "No problems found."},
		{[]snapshot.Advice{{Rule: "unloading-soon", Level: "info", Subject: "q", Message: "m", Fix: "f"}}, 0, "● q: m"},
		{[]snapshot.Advice{{Rule: "queued", Level: "warn", Subject: "q", Message: "m", Fix: "f"}}, 1, "▲ q: m"},
		{[]snapshot.Advice{
			{Rule: "queued", Level: "warn", Subject: "q", Message: "m", Fix: "f"},
			{Rule: "memory-pressure", Level: "critical", Subject: "memory", Message: "m", Fix: "f"},
		}, 2, "✖ memory: m"},
	}
	for _, c := range cases {
		var b bytes.Buffer
		s := snapshot.Snapshot{
			Servers: []snapshot.Server{{Runtime: "ollama", URL: "http://127.0.0.1:11434"}},
			Advice:  c.advice,
		}
		code, err := printDoctor(&b, s)
		if err != nil {
			t.Fatal(err)
		}
		if code != c.code || !strings.Contains(b.String(), c.want) {
			t.Errorf("code %d, output %q; want %d and %q", code, b.String(), c.code, c.want)
		}
		if !strings.Contains(b.String(), "Checked ollama (127.0.0.1:11434).") {
			t.Errorf("output %q doesn't say what was checked", b.String())
		}
	}
}
