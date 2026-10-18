// Command ctxtop shows what your local LLM servers are doing: speed, memory,
// context fill and queue for each running model.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/iBvishal/ctxtop/internal/discover"
	"github.com/iBvishal/ctxtop/internal/hw"
	"github.com/iBvishal/ctxtop/internal/llamacpp"
	"github.com/iBvishal/ctxtop/internal/lmstudio"
	"github.com/iBvishal/ctxtop/internal/ollama"
	"github.com/iBvishal/ctxtop/internal/snapshot"
)

// version is set at release time with -ldflags "-X main.version=...".
var version = "dev"

type urlList []string

func (u *urlList) String() string     { return strings.Join(*u, ",") }
func (u *urlList) Set(v string) error { *u = append(*u, v); return nil }

func main() {
	var (
		asJSON  = flag.Bool("json", false, "print one snapshot as JSON and exit")
		sample  = flag.Duration("sample", time.Second, "time between the two polls that measure speed")
		showVer = flag.Bool("version", false, "print the version and exit")
		urls    urlList
	)
	flag.Var(&urls, "url", "read the server at this URL instead of discovering servers (repeatable)")
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "Usage: ctxtop [flags]\n\n"+
			"Finds local LLM servers and shows speed, memory and context fill.\n\n")
		flag.PrintDefaults()
	}
	flag.Parse()

	if *showVer {
		fmt.Println("ctxtop", version)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), *sample+15*time.Second)
	defer cancel()

	snap := collect(ctx, urls, *sample)
	var err error
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		err = enc.Encode(snap)
	} else {
		err = printText(os.Stdout, snap)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "ctxtop:", err)
		os.Exit(1)
	}
}

// collector is one runtime's reader. Rates come from the change between
// two polls, so each collector keeps the previous poll.
type collector interface {
	Poll(ctx context.Context) (snapshot.Server, error)
}

func newCollector(f discover.Found) collector {
	switch f.Runtime {
	case llamacpp.Runtime:
		return llamacpp.NewPoller(f.URL)
	case ollama.Runtime:
		p := ollama.NewPoller(f.URL)
		p.Runners = f.Runners
		return p
	case lmstudio.Runtime:
		return lmstudio.NewPoller(f.URL)
	}
	return nil
}

func collect(ctx context.Context, urls []string, sample time.Duration) snapshot.Snapshot {
	var found []discover.Found
	if len(urls) > 0 {
		d := discover.New()
		for _, u := range urls {
			u = strings.TrimRight(u, "/")
			found = append(found, discover.Found{Runtime: d.Identify(ctx, u), URL: u})
		}
	} else {
		found = discover.New().Find(ctx)
	}

	servers := make([]snapshot.Server, len(found))
	collectors := map[int]collector{}
	for i, f := range found {
		servers[i] = snapshot.Server{Runtime: f.Runtime, URL: f.URL, PID: f.PID, Process: f.Process}
		c := newCollector(f)
		if c == nil {
			servers[i].Runtime = "unknown"
			servers[i].Error = "not a llama-server, Ollama or LM Studio"
			continue
		}
		if cl, ok := c.(io.Closer); ok {
			defer cl.Close()
		}
		if _, err := c.Poll(ctx); err != nil {
			servers[i].Error = err.Error()
			continue
		}
		collectors[i] = c
	}

	if len(collectors) > 0 {
		select {
		case <-time.After(sample):
		case <-ctx.Done():
		}
	}
	for i, c := range collectors {
		s, err := c.Poll(ctx)
		if err != nil {
			servers[i].Error = err.Error()
			continue
		}
		s.PID, s.Process = servers[i].PID, servers[i].Process
		servers[i] = s
	}

	return snapshot.Snapshot{
		Version: snapshot.Version,
		Time:    time.Now().UTC().Truncate(time.Millisecond),
		Host:    hw.Read(),
		Servers: servers,
	}
}

func printText(w io.Writer, s snapshot.Snapshot) error {
	h := s.Host
	fmt.Fprintf(w, "ctxtop  %s/%s  memory %s / %s", h.OS, h.Arch, gb(h.MemUsed), gb(h.MemTotal))
	if h.SwapTotal > 0 {
		fmt.Fprintf(w, "  swap %s / %s", gb(h.SwapUsed), gb(h.SwapTotal))
	}
	if h.Pressure != "" {
		fmt.Fprintf(w, "  pressure %s", h.Pressure)
	}
	fmt.Fprintln(w)

	if len(s.Servers) == 0 {
		_, err := fmt.Fprintln(w, "\nNo LLM servers found. ctxtop looks for llama-server, Ollama and LM Studio on this machine.")
		return err
	}

	for _, srv := range s.Servers {
		fmt.Fprintf(w, "\n%s  %s", srv.Runtime, srv.URL)
		if srv.PID != 0 {
			fmt.Fprintf(w, "  pid %d", srv.PID)
		}
		fmt.Fprintln(w)
		if srv.Error != "" {
			fmt.Fprintf(w, "  ! %s\n", srv.Error)
		}
		for _, m := range srv.Models {
			fmt.Fprintf(w, "  %s\n", modelLine(m, s.Time))
			if len(m.Slots) == 0 {
				continue
			}
			tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', tabwriter.AlignRight)
			fmt.Fprintln(tw, "    SLOT\tSTATE\tCONTEXT\tFILL\tGEN t/s\tPROMPT t/s\t")
			for _, sl := range m.Slots {
				state := "idle"
				if sl.Busy {
					state = "busy"
				}
				fmt.Fprintf(tw, "    %d\t%s\t%s / %s\t%.0f%%\t%s\t%s\t\n", sl.ID, state,
					count(sl.ContextUsed), count(sl.ContextSize), sl.ContextFill*100, rate(sl.GenTPS), rate(sl.PromptTPS))
			}
			if err := tw.Flush(); err != nil {
				return err
			}
		}
		if err := printRequests(w, srv.Requests); err != nil {
			return err
		}
	}
	return nil
}

// modelLine is one model's summary: name, quant, context size, memory and
// what the runtime says it's doing.
func modelLine(m snapshot.Model, now time.Time) string {
	parts := []string{m.Name}
	if m.Quant != "" {
		parts = append(parts, m.Quant)
	}
	if m.ContextSize > 0 {
		parts = append(parts, "ctx "+count(m.ContextSize))
	}
	switch {
	case m.MemGPU != nil && *m.MemGPU < m.Mem:
		parts = append(parts, fmt.Sprintf("mem %s (%s GPU, %s CPU)", gb(m.Mem), gb(*m.MemGPU), gb(m.Mem-*m.MemGPU)))
	case m.Mem > 0:
		parts = append(parts, "mem "+gb(m.Mem))
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

// printRequests prints the last few finished requests, newest last.
func printRequests(w io.Writer, reqs []snapshot.Request) error {
	if len(reqs) == 0 {
		return nil
	}
	if len(reqs) > 5 {
		reqs = reqs[len(reqs)-5:]
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "    recent requests")
	for _, r := range reqs {
		what := r.Model
		if what == "" {
			what = r.Path
		}
		line := fmt.Sprintf("    %s\t%s", r.Time.Local().Format("15:04:05"), what)
		if r.Status != 0 {
			line += fmt.Sprintf("\t%d", r.Status)
		}
		if r.PromptTokens > 0 || r.OutputTokens > 0 {
			line += fmt.Sprintf("\tin %s  out %s", count(r.PromptTokens), count(r.OutputTokens))
		}
		if r.GenTPS > 0 {
			line += fmt.Sprintf("\t%.1f t/s", r.GenTPS)
		}
		if r.TTFT > 0 {
			line += fmt.Sprintf("\tttft %.2fs", r.TTFT)
		}
		if r.Latency > 0 {
			line += fmt.Sprintf("\t%.2fs", r.Latency)
		}
		fmt.Fprintln(tw, line)
	}
	return tw.Flush()
}

// rate formats tokens per second, or a dash when nothing was measured.
func rate(tps float64) string {
	if tps <= 0 {
		return "-"
	}
	return fmt.Sprintf("%.1f", tps)
}

func gb(b uint64) string { return fmt.Sprintf("%.1f GB", float64(b)/(1<<30)) }

// count formats a token count with thousands separators.
func count(n int) string {
	s := fmt.Sprint(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}
