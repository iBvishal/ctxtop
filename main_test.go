package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/iBvishal/ctxtop/internal/snapshot"
)

func TestCount(t *testing.T) {
	cases := map[int]string{0: "0", 999: "999", 1000: "1,000", 65536: "65,536", 1234567: "1,234,567"}
	for n, want := range cases {
		if got := count(n); got != want {
			t.Errorf("count(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestPrintText(t *testing.T) {
	s := snapshot.Snapshot{
		Host: snapshot.Host{OS: "darwin", Arch: "arm64", MemTotal: 16 << 30, MemUsed: 12 << 30, Pressure: "warn"},
		Servers: []snapshot.Server{
			{Runtime: "llama.cpp", URL: "http://127.0.0.1:8080", PID: 412, Models: []snapshot.Model{{
				Name: "gemma-3-4b-it-Q8_0.gguf", ContextSize: 8192,
				Slots: []snapshot.Slot{
					{ID: 0, Busy: true, ContextSize: 8192, ContextUsed: 7263, ContextFill: 7263.0 / 8192, GenTPS: 51},
					{ID: 1, ContextSize: 8192},
				},
			}}},
			{Runtime: "ollama", URL: "http://127.0.0.1:11434", Error: "found, but ctxtop can't read ollama yet"},
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
		"! found, but ctxtop can't read ollama yet",
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
