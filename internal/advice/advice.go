// Package advice turns snapshots into things worth changing. Each rule
// names the problem, the evidence and one fix. Thresholds are starting
// points, to tune against real sessions.
package advice

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/iBvishal/ctxtop/internal/format"
	"github.com/iBvishal/ctxtop/internal/snapshot"
)

// Levels, most severe last.
const (
	Info     = "info"
	Warn     = "warn"
	Critical = "critical"
)

const (
	contextWarn     = 0.85
	contextCritical = 0.98
	// A spill smaller than this is rounding in the runtime's accounting.
	minSpill = 100 << 20
	// Swap growth: at least swapMinGrowth over at least swapMinWindow, at
	// swapRate per minute or faster, read from up to swapHistory back.
	swapMinGrowth = 100 << 20
	swapMinWindow = 2 * time.Minute
	swapHistory   = 5 * time.Minute
	swapRate      = 50 << 20
	coldStartSoon = time.Minute
)

// Engine evaluates snapshots in order. It keeps the little history the
// swap rule needs, so use one Engine per stream of snapshots.
type Engine struct {
	swap []swapSample
}

type swapSample struct {
	at   time.Time
	used uint64
}

func New() *Engine { return &Engine{} }

// Evaluate returns the advice for s, most severe first.
func (e *Engine) Evaluate(s snapshot.Snapshot) []snapshot.Advice {
	var out []snapshot.Advice
	out = append(out, spill(s)...)
	out = append(out, contextFull(s)...)
	out = append(out, queued(s)...)
	out = append(out, e.swapGrowth(s)...)
	out = append(out, pressure(s)...)
	out = append(out, coldStart(s)...)
	sort.SliceStable(out, func(i, j int) bool { return rank(out[i].Level) > rank(out[j].Level) })
	return out
}

// Worst returns the most severe level in advice, or "" when there is none.
func Worst(advice []snapshot.Advice) string {
	worst := ""
	for _, a := range advice {
		if rank(a.Level) > rank(worst) {
			worst = a.Level
		}
	}
	return worst
}

func rank(level string) int {
	switch level {
	case Critical:
		return 3
	case Warn:
		return 2
	case Info:
		return 1
	}
	return 0
}

// spill: part of a model is in CPU memory, so it generates far slower.
func spill(s snapshot.Snapshot) []snapshot.Advice {
	var out []snapshot.Advice
	for _, srv := range s.Servers {
		for _, m := range srv.Models {
			if m.MemGPU == nil || m.Mem == 0 || *m.MemGPU >= m.Mem || m.Mem-*m.MemGPU < minSpill {
				continue
			}
			cpu := m.Mem - *m.MemGPU
			level := Warn
			if float64(cpu)/float64(m.Mem) >= 0.5 {
				level = Critical
			}
			fix := "Use a smaller quant or a smaller model."
			if others := otherModels(srv, m.Name); len(others) > 0 {
				fix = fmt.Sprintf("Unload %s to free GPU memory (ollama stop %s), or use a smaller quant or model.",
					others[0], others[0])
			}
			out = append(out, snapshot.Advice{
				Rule:    "gpu-spill",
				Level:   level,
				Subject: m.Name,
				Message: fmt.Sprintf("%s of %s doesn't fit in GPU memory and runs from CPU RAM, so generation is much slower.",
					format.GB(cpu), format.GB(m.Mem)),
				Fix: fix,
			})
		}
	}
	return out
}

func otherModels(srv snapshot.Server, name string) []string {
	var out []string
	for _, m := range srv.Models {
		if m.Name != name {
			out = append(out, m.Name)
		}
	}
	return out
}

// contextFull: a slot is close to its context size.
func contextFull(s snapshot.Snapshot) []snapshot.Advice {
	var out []snapshot.Advice
	for _, srv := range s.Servers {
		for _, m := range srv.Models {
			for _, sl := range m.Slots {
				if sl.ContextSize == 0 || sl.ContextFill < contextWarn {
					continue
				}
				level := Warn
				if sl.ContextFill >= contextCritical {
					level = Critical
				}
				subject := m.Name
				if len(m.Slots) > 1 {
					subject = fmt.Sprintf("%s, slot %d", m.Name, sl.ID)
				}
				used := fmt.Sprintf("%.0f%% (%s of %s tokens)", sl.ContextFill*100, format.Count(sl.ContextUsed), format.Count(sl.ContextSize))
				msg := "Context is " + used + " full."
				if !sl.Busy {
					msg = "The last request filled " + used + " of the context."
				}
				out = append(out, snapshot.Advice{
					Rule:    "context-full",
					Level:   level,
					Subject: subject,
					Message: msg + " " + contextConsequence(srv.Runtime),
					Fix:     contextFix(srv.Runtime, sl, len(m.Slots)),
				})
			}
		}
	}
	return out
}

func contextConsequence(runtime string) string {
	if runtime == "llama.cpp" {
		// ctx_shift is off by default: the slot stops and the reply is
		// marked truncated.
		return "When it fills, generation stops and the reply is cut off."
	}
	return "When it fills, the reply is cut off or the start of the prompt is dropped."
}

func contextFix(runtime string, sl snapshot.Slot, slots int) string {
	switch runtime {
	case "llama.cpp":
		if slots > 1 {
			return fmt.Sprintf("Restart llama-server with a larger -c. It's split across %d slots, so each gets %s of -c %s.",
				slots, format.Count(sl.ContextSize), format.Count(sl.ContextSize*slots))
		}
		return fmt.Sprintf("Restart llama-server with a larger -c (now %s).", format.Count(sl.ContextSize))
	case "ollama":
		return fmt.Sprintf("Raise num_ctx (now %s): OLLAMA_CONTEXT_LENGTH for the server, or num_ctx in the request's options. More context needs more memory.",
			format.Count(sl.ContextSize))
	}
	return "Load the model with a larger context length. More context needs more memory."
}

// queued: requests are waiting for a free slot. Only LM Studio reports its
// queue; llama.cpp's needs /metrics, which ctxtop doesn't read yet.
func queued(s snapshot.Snapshot) []snapshot.Advice {
	var out []snapshot.Advice
	for _, srv := range s.Servers {
		for _, m := range srv.Models {
			if m.Queued == nil || *m.Queued == 0 {
				continue
			}
			running := "one runs"
			if m.Parallel > 1 {
				running = fmt.Sprintf("%d run", m.Parallel)
			}
			out = append(out, snapshot.Advice{
				Rule:    "queued",
				Level:   Warn,
				Subject: m.Name,
				Message: fmt.Sprintf("%d %s waiting; %s at a time.", *m.Queued, plural(*m.Queued, "request is", "requests are"), running),
				Fix:     "Raise Max Concurrent Predictions in LM Studio's load settings for this model, if memory allows. Each request running at once needs its own context memory.",
			})
		}
	}
	return out
}

// swapGrowth: swap keeps growing while models are loaded.
func (e *Engine) swapGrowth(s snapshot.Snapshot) []snapshot.Advice {
	if s.Host.SwapTotal == 0 {
		return nil
	}
	e.swap = append(e.swap, swapSample{at: s.Time, used: s.Host.SwapUsed})
	cut := 0
	for cut < len(e.swap)-1 && s.Time.Sub(e.swap[cut].at) > swapHistory {
		cut++
	}
	e.swap = e.swap[cut:]

	first := e.swap[0]
	window := s.Time.Sub(first.at)
	loaded := loadedModels(s)
	if window < swapMinWindow || s.Host.SwapUsed < first.used || len(loaded) == 0 {
		return nil
	}
	grew := s.Host.SwapUsed - first.used
	if grew < swapMinGrowth || float64(grew)/window.Minutes() < swapRate {
		return nil
	}
	return []snapshot.Advice{{
		Rule:    "swap-growing",
		Level:   Warn,
		Subject: "memory",
		Message: fmt.Sprintf("Swap grew %s in the last %s while models are loaded, so the machine is running out of memory.",
			format.GB(grew), window.Round(time.Minute)) + modelShare(loaded, s.Host.MemTotal),
		Fix: memoryFix(loaded, s.Host.MemTotal),
	}}
}

// pressure: macOS says memory is short.
func pressure(s snapshot.Snapshot) []snapshot.Advice {
	if s.Host.Pressure != Warn && s.Host.Pressure != Critical {
		return nil
	}
	loaded := loadedModels(s)
	return []snapshot.Advice{{
		Rule:    "memory-pressure",
		Level:   s.Host.Pressure,
		Subject: "memory",
		Message: fmt.Sprintf("macOS memory pressure is %s.", s.Host.Pressure) + modelShare(loaded, s.Host.MemTotal),
		Fix:     memoryFix(loaded, s.Host.MemTotal),
	}}
}

// modelShare says how much memory the loaded models take, as far as the
// runtimes report it.
func modelShare(loaded []loadedModel, memTotal uint64) string {
	known, unknown := totalMem(loaded), unknownSize(loaded)
	if known == 0 || memTotal == 0 {
		return ""
	}
	msg := fmt.Sprintf(" Loaded models take %s of %s", format.GB(known), format.GB(memTotal))
	if unknown > 0 {
		msg += fmt.Sprintf(", plus %d whose size isn't reported", unknown)
	}
	return msg + "."
}

// memoryFix points at the largest model, unless models clearly aren't what
// fills memory, or the largest known one is too small to matter.
func memoryFix(loaded []loadedModel, memTotal uint64) string {
	if unknownSize(loaded) == 0 && memTotal > 0 && totalMem(loaded) < memTotal/4 {
		return "The models aren't the main cause: other apps are using most of the memory. Close some, or larger models will swap."
	}
	if len(loaded) > 0 && memTotal > 0 && loaded[0].mem < memTotal/10 {
		return "Unload a model you aren't using, or close other apps."
	}
	return unloadFix(loaded)
}

func unknownSize(loaded []loadedModel) int {
	n := 0
	for _, m := range loaded {
		if m.mem == 0 {
			n++
		}
	}
	return n
}

// coldStart: an Ollama model is about to unload while it's still in use.
func coldStart(s snapshot.Snapshot) []snapshot.Advice {
	var out []snapshot.Advice
	for _, srv := range s.Servers {
		if srv.Runtime != "ollama" || len(srv.Requests) == 0 {
			continue
		}
		for _, m := range srv.Models {
			if m.UnloadsAt == nil {
				continue
			}
			left := m.UnloadsAt.Sub(s.Time)
			if left <= 0 || left > coldStartSoon {
				continue
			}
			out = append(out, snapshot.Advice{
				Rule:    "unloading-soon",
				Level:   Info,
				Subject: m.Name,
				Message: fmt.Sprintf("Unloads in %s although requests are still coming; the next one after that waits for the model to load again.",
					left.Round(time.Second)),
				Fix: "Keep it loaded longer: set OLLAMA_KEEP_ALIVE (for example 30m), or keep_alive in requests.",
			})
		}
	}
	return out
}

type loadedModel struct {
	name    string
	runtime string
	mem     uint64
}

// loadedModels lists models with a known size, largest first.
func loadedModels(s snapshot.Snapshot) []loadedModel {
	var out []loadedModel
	for _, srv := range s.Servers {
		for _, m := range srv.Models {
			out = append(out, loadedModel{name: m.Name, runtime: srv.Runtime, mem: m.Mem})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].mem > out[j].mem })
	return out
}

func totalMem(models []loadedModel) uint64 {
	var t uint64
	for _, m := range models {
		t += m.mem
	}
	return t
}

func unloadFix(loaded []loadedModel) string {
	for _, m := range loaded {
		if m.mem == 0 {
			continue
		}
		fix := fmt.Sprintf("Unload a model you aren't using; the largest is %s (%s)", m.name, format.GB(m.mem))
		switch m.runtime {
		case "ollama":
			fix += ": ollama stop " + m.name
		case "lmstudio":
			fix += ": lms unload " + m.name
		}
		return fix + ". Or close other apps."
	}
	return "Unload a model you aren't using, or close other apps."
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// Lines formats advice for plain text: a marker, the subject and message,
// then an arrow and the fix below.
func Lines(advice []snapshot.Advice) []string {
	var out []string
	for _, a := range advice {
		out = append(out, fmt.Sprintf("%s %s: %s", Marker(a.Level), a.Subject, a.Message))
		out = append(out, "    → "+strings.TrimSpace(a.Fix))
	}
	return out
}

// Marker is the one-character sign for a level.
func Marker(level string) string {
	switch level {
	case Critical:
		return "✖"
	case Warn:
		return "▲"
	}
	return "●"
}
