package tui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/iBvishal/ctxtop/internal/advice"
	"github.com/iBvishal/ctxtop/internal/format"
	"github.com/iBvishal/ctxtop/internal/snapshot"
)

// Basic ANSI colours, so the view follows the terminal's own palette in
// light and dark themes.
var (
	bold    = lipgloss.NewStyle().Bold(true)
	faint   = lipgloss.NewStyle().Faint(true)
	label   = lipgloss.NewStyle().Faint(true)
	runtime = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("6"))
	warn    = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	green   = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	yellow  = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	red     = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
)

const barWidth = 12

// Render draws one frame. width and height are the terminal's size; zero
// means unknown, and nothing is cut.
func Render(s snapshot.Snapshot, have bool, width, height int, interval time.Duration) string {
	footer := faint.Render(fmt.Sprintf("q quit  r rescan  every %s", interval))
	if !have {
		return fit([]string{bold.Render("ctxtop") + "  looking for LLM servers…"}, nil, footer, width, height)
	}

	body := append(hostLines(s.Host), "")
	if len(s.Servers) == 0 {
		body = append(body,
			"No LLM servers found. ctxtop looks for llama-server, Ollama and LM Studio",
			"and checks again every few seconds.",
		)
	}
	for _, srv := range s.Servers {
		body = append(body, serverLines(srv, s.Time)...)
		body = append(body, "")
	}
	if len(s.Advice) > 0 {
		body = append(body, adviceLines(s.Advice, width)...)
		body = append(body, "")
	}
	return fit(body, requestLines(s), footer, width, height)
}

// adviceLines shows each piece of advice as its subject and message, then
// the fix, both wrapped to the terminal so nothing is cut.
func adviceLines(list []snapshot.Advice, width int) []string {
	if width <= 0 {
		width = 100
	}
	out := []string{label.Render("ADVICE")}
	for _, a := range list {
		style := faint
		switch a.Level {
		case advice.Critical:
			style = red
		case advice.Warn:
			style = yellow
		}
		// The message starts after the subject and continues four spaces in,
		// so a long model name doesn't squeeze it into a narrow column.
		head := style.Render(advice.Marker(a.Level)) + " " + bold.Render(a.Subject) + "  "
		first := wrap(a.Message, width-lipgloss.Width(head))
		out = append(out, head+first[0])
		rest := strings.TrimSpace(strings.TrimPrefix(a.Message, first[0]))
		if rest != "" {
			for _, l := range wrap(rest, width-4) {
				out = append(out, "    "+l)
			}
		}
		for i, l := range wrap(a.Fix, width-6) {
			prefix := "      "
			if i == 0 {
				prefix = "    → "
			}
			out = append(out, prefix+faint.Render(l))
		}
	}
	return out
}

// wrap breaks text into lines of at most width cells, at spaces.
func wrap(text string, width int) []string {
	width = max(width, 20)
	var lines []string
	line := ""
	for _, word := range strings.Fields(text) {
		switch {
		case line == "":
			line = word
		case lipgloss.Width(line)+1+lipgloss.Width(word) <= width:
			line += " " + word
		default:
			lines = append(lines, line)
			line = word
		}
	}
	if line != "" || len(lines) == 0 {
		lines = append(lines, line)
	}
	return lines
}

// fit lays out body, then as many recent requests as fit, then the footer
// on the last line, and cuts every line to width.
func fit(body, requests []string, footer string, width, height int) string {
	lines := body
	if height > 0 {
		room := height - 1
		if len(lines) > room {
			lines = lines[:room]
		} else if free := room - len(lines); free >= 2 && len(requests) > 0 {
			lines = append(lines, requests[:min(len(requests), free)]...)
		}
		for len(lines) < room {
			lines = append(lines, "")
		}
	} else {
		lines = append(lines, requests...)
	}
	lines = append(lines, footer)
	if width > 0 {
		for i, l := range lines {
			lines[i] = ansi.Truncate(l, width, "…")
		}
	}
	return strings.Join(lines, "\n")
}

func hostLines(h snapshot.Host) []string {
	title := bold.Render("ctxtop")
	if h.Chip != "" {
		title += "  " + h.Chip
	} else {
		title += "  " + h.OS + "/" + h.Arch
	}

	var parts []string
	if h.MemTotal > 0 {
		frac := float64(h.MemUsed) / float64(h.MemTotal)
		parts = append(parts, fmt.Sprintf("%s %s %s / %s", label.Render("MEM"), bar(frac), format.GB(h.MemUsed), format.GB(h.MemTotal)))
	}
	if h.SwapTotal > 0 {
		parts = append(parts, fmt.Sprintf("%s %s", label.Render("SWAP"), format.GB(h.SwapUsed)))
	}
	if h.Pressure != "" {
		p := h.Pressure
		switch p {
		case "warn":
			p = yellow.Render(p)
		case "critical":
			p = red.Render(p)
		}
		parts = append(parts, label.Render("PRESSURE")+" "+p)
	}
	lines := []string{title, strings.Join(parts, "   ")}

	// GPU and power get their own line so neither is cut on an 80-column
	// terminal.
	var gpu []string
	if h.GPUUtil != nil {
		gpu = append(gpu, fmt.Sprintf("%s %s %3.0f%%", label.Render("GPU"), bar(*h.GPUUtil/100), *h.GPUUtil))
	}
	if h.GPUMem > 0 {
		gpu = append(gpu, fmt.Sprintf("%s %s", label.Render("GPU MEM"), format.GB(h.GPUMem)))
	}
	if h.Power != nil {
		gpu = append(gpu, fmt.Sprintf("%s cpu %.1f W  gpu %.1f W", label.Render("POWER"), h.Power.CPU, h.Power.GPU))
	}
	if len(gpu) > 0 {
		lines = append(lines, strings.Join(gpu, "   "))
	}
	return lines
}

func serverLines(srv snapshot.Server, now time.Time) []string {
	head := runtime.Render(srv.Runtime) + "  " + strings.TrimPrefix(srv.URL, "http://")
	var meta []string
	if srv.PID != 0 {
		meta = append(meta, fmt.Sprintf("pid %d", srv.PID))
	}
	if srv.Build != "" {
		meta = append(meta, srv.Build)
	}
	if len(meta) > 0 {
		head += "  " + faint.Render(strings.Join(meta, "  "))
	}
	lines := []string{head}
	if srv.Error != "" {
		lines = append(lines, warn.Render("  ! "+srv.Error))
	}
	for _, m := range srv.Models {
		lines = append(lines, "  "+format.Model(m, now))
		for _, sl := range m.Slots {
			lines = append(lines, slotLine(sl))
		}
	}
	if len(srv.Models) == 0 && srv.Error == "" {
		lines = append(lines, faint.Render("  no model loaded"))
	}
	return lines
}

func slotLine(sl snapshot.Slot) string {
	state := faint.Render("idle")
	if sl.Busy {
		state = green.Render("busy")
	}
	line := fmt.Sprintf("    slot %d  %s  %s %3.0f%%  %s / %s",
		sl.ID, state, bar(sl.ContextFill), sl.ContextFill*100, format.Count(sl.ContextUsed), format.Count(sl.ContextSize))
	if sl.GenTPS > 0 {
		line += fmt.Sprintf("   gen %s t/s", bold.Render(format.Rate(sl.GenTPS)))
	}
	if sl.PromptTPS > 0 {
		line += fmt.Sprintf("   prompt %s t/s", format.Rate(sl.PromptTPS))
	}
	return line
}

// requestLines lists every server's recent requests, newest first, under a
// heading.
func requestLines(s snapshot.Snapshot) []string {
	type row struct {
		t    time.Time
		line string
	}
	var rows []row
	for _, srv := range s.Servers {
		for _, r := range srv.Requests {
			what := r.Model
			if what == "" {
				what = r.Path
			}
			rows = append(rows, row{r.Time, fmt.Sprintf("  %s  %-9s %-24s %s",
				r.Time.Local().Format("15:04:05"), srv.Runtime, what, format.Request(r))})
		}
	}
	if len(rows) == 0 {
		return nil
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].t.After(rows[j].t) })
	out := []string{label.Render("RECENT REQUESTS")}
	for _, r := range rows {
		out = append(out, r.line)
	}
	return out
}

// bar draws frac (0 to 1) as a fixed-width bar, green below 70%, yellow
// below 90%, red above.
func bar(frac float64) string {
	frac = max(0, min(1, frac))
	n := int(frac*barWidth + 0.5)
	style := green
	switch {
	case frac >= 0.9:
		style = red
	case frac >= 0.7:
		style = yellow
	}
	return style.Render(strings.Repeat("█", n)) + faint.Render(strings.Repeat("░", barWidth-n))
}
