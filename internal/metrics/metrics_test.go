package metrics

import (
	"bytes"
	"flag"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/iBvishal/ctxtop/internal/snapshot"
)

var update = flag.Bool("update", false, "rewrite testdata/metrics.txt")

func demo() snapshot.Snapshot {
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	gpuUtil := 81.0
	gpu := uint64(7 << 30)
	unload := now.Add(4*time.Minute + 30*time.Second)
	queued := 2
	return snapshot.Snapshot{
		Version: snapshot.Version,
		Time:    now,
		Host: snapshot.Host{
			OS: "darwin", Arch: "arm64", MemTotal: 16 << 30, MemUsed: 12 << 30,
			SwapTotal: 2 << 30, SwapUsed: 1 << 30, Pressure: "warn",
			GPUUtil: &gpuUtil, GPUMem: 1 << 30, Power: &snapshot.Power{CPU: 4.2, GPU: 9.8},
		},
		Servers: []snapshot.Server{
			{Runtime: "llama.cpp", URL: "http://127.0.0.1:8080", PID: 4120, Process: "llama-server", Build: "b11669",
				Models: []snapshot.Model{{
					Name: "gemma-3-4b-it-Q8_0.gguf", ContextSize: 16384,
					Slots: []snapshot.Slot{
						{ID: 0, Busy: true, ContextSize: 8192, ContextUsed: 7263, ContextFill: 0.886, GenTPS: 51},
						{ID: 1, ContextSize: 8192},
					},
				}}},
			{Runtime: "ollama", URL: "http://127.0.0.1:11434", Process: "ollama", Build: "0.40.6",
				Models: []snapshot.Model{{
					Name: "qwen3:14b", Quant: "Q4_K_M", ContextSize: 8192,
					Mem: 10 << 30, MemGPU: &gpu, UnloadsAt: &unload,
				}}},
			{Runtime: "lmstudio", URL: "http://127.0.0.1:1234",
				Models: []snapshot.Model{{Name: "qwen3-4b", Status: "generating", Queued: &queued, Parallel: 1}}},
			{Runtime: "llama.cpp", URL: "http://127.0.0.1:8081", Error: "connection refused"},
		},
		Advice: []snapshot.Advice{
			{Rule: "gpu-spill", Level: "warn", Subject: "qwen3:14b"},
			{Rule: "context-full", Level: "warn", Subject: "gemma-3-4b-it-Q8_0.gguf, slot 0"},
		},
	}
}

func TestWrite(t *testing.T) {
	var b bytes.Buffer
	if err := Write(&b, demo(), "v0.2.0"); err != nil {
		t.Fatal(err)
	}
	const golden = "testdata/metrics.txt"
	if *update {
		if err := os.WriteFile(golden, b.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if got := b.String(); got != string(want) {
		t.Errorf("output differs from %s (run go test -update to accept):\n%s", golden, got)
	}
}

// Every family has one HELP and one TYPE line, and its samples follow them
// without another family in between.
func TestWriteFamiliesContiguous(t *testing.T) {
	var b bytes.Buffer
	if err := Write(&b, demo(), "dev"); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	current := ""
	for _, line := range strings.Split(strings.TrimSpace(b.String()), "\n") {
		if name, ok := strings.CutPrefix(line, "# HELP "); ok {
			name, _, _ = strings.Cut(name, " ")
			if seen[name] {
				t.Errorf("%s: second HELP line", name)
			}
			seen[name], current = true, name
			continue
		}
		if strings.HasPrefix(line, "# TYPE ") {
			continue
		}
		name, _, _ := strings.Cut(line, "{")
		name, _, _ = strings.Cut(name, " ")
		if name != current {
			t.Errorf("sample %q outside its family (current %s)", line, current)
		}
	}
}

func TestWriteEmpty(t *testing.T) {
	var b bytes.Buffer
	if err := Write(&b, snapshot.Snapshot{}, "dev"); err != nil {
		t.Fatal(err)
	}
	want := "# HELP ctxtop_build_info ctxtop's version; always 1.\n# TYPE ctxtop_build_info gauge\nctxtop_build_info{version=\"dev\"} 1\n"
	if b.String() != want {
		t.Errorf("got:\n%s", b.String())
	}
}

func TestEscape(t *testing.T) {
	if got, want := escape("a\"b\\c\nd"), `a\"b\\c\nd`; got != want {
		t.Errorf("escape = %s, want %s", got, want)
	}
}
