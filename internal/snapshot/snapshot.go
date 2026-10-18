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
}

// Host is the machine's memory state. Sizes are bytes; zero means unknown.
type Host struct {
	OS        string `json:"os"`
	Arch      string `json:"arch"`
	MemTotal  uint64 `json:"mem_total"`
	MemUsed   uint64 `json:"mem_used"`
	SwapTotal uint64 `json:"swap_total"`
	SwapUsed  uint64 `json:"swap_used"`
	// Pressure is "normal", "warn" or "critical", or empty when the OS
	// doesn't report it.
	Pressure string `json:"pressure,omitempty"`
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
}

// Model is one loaded model and, where the runtime exposes them, its slots.
type Model struct {
	Name        string `json:"name"`
	ContextSize int    `json:"context_size,omitempty"`
	// MetricsEnabled is nil when the runtime has no such switch.
	MetricsEnabled *bool  `json:"metrics_enabled,omitempty"`
	Slots          []Slot `json:"slots,omitempty"`
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
