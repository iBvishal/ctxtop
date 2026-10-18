// Package snapshot holds the data ctxtop reports: the machine, and every
// inference server found on it. The JSON shape is versioned so scripts and
// agents can depend on it.
package snapshot

import "time"

// Version is bumped when a field is renamed or removed. Adding a field does
// not bump it.
const Version = 1

type Snapshot struct {
	Version int       `json:"version"`
	Time    time.Time `json:"time"`
	Host    Host      `json:"host"`
	Servers []Server  `json:"servers"`
	Advice  []Advice  `json:"advice,omitempty"`
}

// Advice is one thing worth changing, from the rules in internal/advice.
type Advice struct {
	// Rule is a stable id, such as "context-full".
	Rule string `json:"rule"`
	// Level is "info", "warn" or "critical".
	Level string `json:"level"`
	// Subject is what it's about: a model, a slot or the machine.
	Subject string `json:"subject"`
	Message string `json:"message"`
	Fix     string `json:"fix"`
}

// Host is the machine's memory state. Sizes are bytes; zero means unknown.
type Host struct {
	OS        string `json:"os"`
	Arch      string `json:"arch"`
	Chip      string `json:"chip,omitempty"`
	MemTotal  uint64 `json:"mem_total"`
	MemUsed   uint64 `json:"mem_used"`
	SwapTotal uint64 `json:"swap_total"`
	SwapUsed  uint64 `json:"swap_used"`
	// Pressure is "normal", "warn" or "critical", or empty when the OS
	// doesn't report it.
	Pressure string `json:"pressure,omitempty"`
	// GPUUtil is the GPU's busy percentage; GPUMem is GPU memory in use in
	// bytes. Both come from the Apple GPU driver.
	GPUUtil *float64 `json:"gpu_util,omitempty"`
	GPUMem  uint64   `json:"gpu_mem,omitempty"`
	// Power is measured between two reads, so the first read has none.
	Power *Power `json:"power,omitempty"`
}

// Power is average power in watts since the previous read.
type Power struct {
	CPU float64 `json:"cpu"`
	GPU float64 `json:"gpu"`
	ANE float64 `json:"ane"`
}

// Server is one running inference server.
type Server struct {
	Runtime string `json:"runtime"`
	URL     string `json:"url"`
	PID     int    `json:"pid,omitempty"`
	Process string `json:"process,omitempty"`
	Build   string `json:"build,omitempty"`
	// Error is set when the server was found but couldn't be read. The
	// other fields still say what was found.
	Error  string  `json:"error,omitempty"`
	Models []Model `json:"models,omitempty"`
	// Requests are recently finished requests, newest last, for runtimes
	// that report them.
	Requests []Request `json:"requests,omitempty"`
}

// Model is one loaded model and, where the runtime exposes them, its slots.
// Pointer fields are nil when the runtime doesn't report them.
type Model struct {
	Name        string `json:"name"`
	Quant       string `json:"quant,omitempty"`
	ContextSize int    `json:"context_size,omitempty"`
	// Mem is the model's memory in bytes; MemGPU is the part in GPU
	// memory, so Mem - MemGPU spilled to CPU RAM.
	Mem    uint64  `json:"mem,omitempty"`
	MemGPU *uint64 `json:"mem_gpu,omitempty"`
	// Status is the runtime's own word for what the model is doing, such
	// as "idle" or "generating".
	Status   string `json:"status,omitempty"`
	Queued   *int   `json:"queued,omitempty"`
	Parallel int    `json:"parallel,omitempty"`
	// UnloadsAt is when the runtime will unload an idle model.
	UnloadsAt *time.Time `json:"unloads_at,omitempty"`
	// MetricsEnabled is nil when the runtime has no such switch.
	MetricsEnabled *bool  `json:"metrics_enabled,omitempty"`
	Slots          []Slot `json:"slots,omitempty"`
}

// Request is one finished request. Fields the runtime doesn't report are
// zero.
type Request struct {
	Time         time.Time `json:"time"`
	Model        string    `json:"model,omitempty"`
	Path         string    `json:"path,omitempty"`
	Status       int       `json:"status,omitempty"`
	Latency      float64   `json:"latency,omitempty"`
	PromptTokens int       `json:"prompt_tokens,omitempty"`
	OutputTokens int       `json:"output_tokens,omitempty"`
	GenTPS       float64   `json:"gen_tps,omitempty"`
	TTFT         float64   `json:"ttft,omitempty"`
	StopReason   string    `json:"stop_reason,omitempty"`
}

// Slot is one request lane. Rates are measured between two polls and are
// zero when the slot was idle or switched requests in between.
type Slot struct {
	ID          int     `json:"id"`
	Busy        bool    `json:"busy"`
	ContextSize int     `json:"context_size"`
	ContextUsed int     `json:"context_used"`
	ContextFill float64 `json:"context_fill"`
	Generated   int     `json:"generated"`
	Cached      int     `json:"cached"`
	GenTPS      float64 `json:"gen_tps,omitempty"`
	PromptTPS   float64 `json:"prompt_tps,omitempty"`
}
