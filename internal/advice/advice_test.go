package advice

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iBvishal/ctxtop/internal/snapshot"
)

// load reads a testdata scenario: one snapshot, or an array of snapshots
// taken in order.
func load(t *testing.T, name string) []snapshot.Snapshot {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	var many []snapshot.Snapshot
	if json.Unmarshal(b, &many) == nil {
		return many
	}
	var one snapshot.Snapshot
	if err := json.Unmarshal(b, &one); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return []snapshot.Snapshot{one}
}

// run evaluates a scenario and returns the advice for its last snapshot.
func run(t *testing.T, name string) []snapshot.Advice {
	t.Helper()
	e := New()
	var got []snapshot.Advice
	for _, s := range load(t, name) {
		got = e.Evaluate(s)
	}
	return got
}

func TestScenarios(t *testing.T) {
	cases := []struct {
		file string
		want []string // rule:level, most severe first
	}{
		{"healthy.json", nil},
		{"gpu_spill.json", []string{"gpu-spill:critical", "gpu-spill:warn"}},
		{"context_llama.json", []string{"context-full:critical", "context-full:warn"}},
		{"context_ollama.json", []string{"context-full:warn"}},
		{"queued.json", []string{"queued:warn"}},
		{"swap_growth.json", []string{"swap-growing:warn"}},
		{"swap_flat.json", nil},
		{"pressure.json", []string{"memory-pressure:critical"}},
		{"pressure_other_apps.json", []string{"memory-pressure:warn"}},
		{"pressure_unknown_size.json", []string{"memory-pressure:warn"}},
		{"pressure_no_models.json", []string{"memory-pressure:warn"}},
		{"pressure_no_servers.json", []string{"memory-pressure:critical"}},
		{"unloading_soon.json", []string{"unloading-soon:info"}},
	}
	for _, c := range cases {
		t.Run(c.file, func(t *testing.T) {
			var got []string
			for _, a := range run(t, c.file) {
				got = append(got, a.Rule+":"+a.Level)
				if a.Subject == "" || a.Message == "" || a.Fix == "" {
					t.Errorf("incomplete advice: %+v", a)
				}
			}
			if strings.Join(got, ",") != strings.Join(c.want, ",") {
				t.Errorf("got %v, want %v", got, c.want)
			}
		})
	}
}

func TestMessagesCarryTheEvidence(t *testing.T) {
	find := func(advice []snapshot.Advice, subject string) snapshot.Advice {
		for _, a := range advice {
			if a.Subject == subject {
				return a
			}
		}
		t.Fatalf("no advice about %q in %+v", subject, advice)
		return snapshot.Advice{}
	}
	checks := []struct {
		file, subject string
		in            []string
	}{
		{"gpu_spill.json", "qwen3:14b", []string{"2.2 GB of 9.6 GB", "ollama stop llama3.3:70b"}},
		{"context_llama.json", "gemma-3-4b-it-Q8_0.gguf, slot 0", []string{"92% (3,768 of 4,096 tokens)", "generation stops", "split across 2 slots", "-c 8,192"}},
		{"context_llama.json", "gemma-3-4b-it-Q8_0.gguf, slot 1", []string{"The last request filled"}},
		{"context_ollama.json", "qwen3:8b", []string{"num_ctx (now 4,096)", "OLLAMA_CONTEXT_LENGTH"}},
		{"queued.json", "qwen3-4b", []string{"2 requests are waiting; one runs at a time", "Max Concurrent Predictions"}},
		{"swap_growth.json", "memory", []string{"Swap grew 0.4 GB in the last 3m0s", "ollama stop qwen3:14b"}},
		{"pressure.json", "memory", []string{"pressure is critical", "Loaded models take 12.1 GB of 16.0 GB", "qwen3:14b (9.6 GB): ollama stop qwen3:14b"}},
		{"unloading_soon.json", "qwen3:8b", []string{"Unloads in 40s", "OLLAMA_KEEP_ALIVE"}},
		{"pressure_other_apps.json", "memory", []string{"take 0.5 GB of 16.0 GB", "aren't the main cause"}},
		// With a model of unknown size, don't claim the models aren't the cause.
		// Nor name a model too small to matter.
		{"pressure_unknown_size.json", "memory", []string{"plus 1 whose size isn't reported", "Unload a model you aren't using, or close other apps."}},
		// Pressure still matters with nothing loaded: it decides whether a model fits.
		{"pressure_no_models.json", "memory", []string{"pressure is warn", "No models are loaded"}},
		{"pressure_no_servers.json", "memory", []string{"pressure is critical", "No models are loaded"}},
	}
	for _, c := range checks {
		a := find(run(t, c.file), c.subject)
		text := a.Message + " " + a.Fix
		for _, want := range c.in {
			if !strings.Contains(text, want) {
				t.Errorf("%s / %s: missing %q in %q", c.file, c.subject, want, text)
			}
		}
	}
}

func TestNoModelsNotBlamed(t *testing.T) {
	for _, file := range []string{"pressure_no_models.json", "pressure_no_servers.json"} {
		for _, a := range run(t, file) {
			if strings.Contains(a.Message+" "+a.Fix, "models aren't") {
				t.Errorf("%s: blames models that aren't loaded: %q", file, a.Fix)
			}
		}
	}
}

func TestSwapNeedsTime(t *testing.T) {
	// The growth is there from the second sample, but two minutes of it
	// are needed before the rule speaks.
	snaps := load(t, "swap_growth.json")
	e := New()
	for i, s := range snaps[:4] {
		for _, a := range e.Evaluate(s) {
			if a.Rule == "swap-growing" {
				t.Errorf("fired after %d samples", i+1)
			}
		}
	}
}

func TestWorst(t *testing.T) {
	if Worst(nil) != "" {
		t.Error("no advice should have no level")
	}
	got := Worst([]snapshot.Advice{{Level: Info}, {Level: Critical}, {Level: Warn}})
	if got != Critical {
		t.Errorf("Worst = %q", got)
	}
}
