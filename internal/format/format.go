// Package format turns snapshot values into the short strings both the text
// snapshot and the live view print.
package format

import (
	"fmt"
	"strings"
	"time"

	"github.com/iBvishal/ctxtop/internal/snapshot"
)

// GB formats bytes as gigabytes (GiB, as macOS's Activity Monitor counts
// them) with one decimal.
func GB(b uint64) string { return fmt.Sprintf("%.1f GB", float64(b)/(1<<30)) }

// Count formats a token count with thousands separators.
func Count(n int) string {
	s := fmt.Sprint(n)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	if neg {
		s = "-" + s
	}
	return s
}

// Rate formats tokens per second, or a dash when nothing was measured.
func Rate(tps float64) string {
	if tps <= 0 {
		return "-"
	}
	return fmt.Sprintf("%.1f", tps)
}

// Model is one model's summary: name, quant, context size, memory and what
// the runtime says it's doing.
func Model(m snapshot.Model, now time.Time) string {
	parts := []string{m.Name}
	if m.Quant != "" {
		parts = append(parts, m.Quant)
	}
	if m.ContextSize > 0 {
		parts = append(parts, "ctx "+Count(m.ContextSize))
	}
	switch {
	case m.MemGPU != nil && *m.MemGPU < m.Mem:
		parts = append(parts, fmt.Sprintf("mem %s (%s GPU, %s CPU)", GB(m.Mem), GB(*m.MemGPU), GB(m.Mem-*m.MemGPU)))
	case m.Mem > 0:
		parts = append(parts, "mem "+GB(m.Mem))
	}
	if m.Status != "" {
		parts = append(parts, m.Status)
	}
	if m.Queued != nil && *m.Queued > 0 {
		q := fmt.Sprintf("%d queued", *m.Queued)
		if m.Parallel > 0 {
			q += fmt.Sprintf(" (%d parallel)", m.Parallel)
		}
		parts = append(parts, q)
	}
	if m.UnloadsAt != nil {
		if d := m.UnloadsAt.Sub(now).Round(time.Second); d > 0 {
			parts = append(parts, "unloads in "+d.String())
		}
	}
	return strings.Join(parts, "  ")
}

// Request is one finished request's numbers, without its time or source.
func Request(r snapshot.Request) string {
	var parts []string
	if r.Status != 0 {
		parts = append(parts, fmt.Sprint(r.Status))
	}
	if r.PromptTokens > 0 || r.OutputTokens > 0 {
		parts = append(parts, fmt.Sprintf("in %s  out %s", Count(r.PromptTokens), Count(r.OutputTokens)))
	}
	if r.GenTPS > 0 {
		parts = append(parts, fmt.Sprintf("%.1f t/s", r.GenTPS))
	}
	if r.TTFT > 0 {
		parts = append(parts, fmt.Sprintf("ttft %.2fs", r.TTFT))
	}
	if r.Latency > 0 {
		parts = append(parts, fmt.Sprintf("%.2fs", r.Latency))
	}
	return strings.Join(parts, "  ")
}
