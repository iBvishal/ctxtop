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

	tea "charm.land/bubbletea/v2"

	"github.com/iBvishal/ctxtop/internal/collect"
	"github.com/iBvishal/ctxtop/internal/format"
	"github.com/iBvishal/ctxtop/internal/snapshot"
	"github.com/iBvishal/ctxtop/internal/tui"
)

// version is set at release time with -ldflags "-X main.version=...".
var version = "dev"

type urlList []string

func (u *urlList) String() string     { return strings.Join(*u, ",") }
func (u *urlList) Set(v string) error { *u = append(*u, v); return nil }

func main() {
	var (
		asJSON   = flag.Bool("json", false, "print one snapshot as JSON and exit")
		once     = flag.Bool("once", false, "print one snapshot as text and exit")
		interval = flag.Duration("interval", time.Second, "refresh interval of the live view")
		sample   = flag.Duration("sample", time.Second, "for --once and --json: time between the two polls that measure speed")
		showVer  = flag.Bool("version", false, "print the version and exit")
		urls     urlList
	)
	flag.Var(&urls, "url", "read the server at this URL instead of discovering servers (repeatable)")
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "Usage: ctxtop [flags]\n\n"+
			"Finds local LLM servers and shows speed, memory and context fill.\n"+
			"Runs a live view in a terminal; prints one snapshot otherwise.\n\n")
		flag.PrintDefaults()
	}
	flag.Parse()

	if *showVer {
		fmt.Println("ctxtop", version)
		return
	}

	c := collect.New(urls)
	defer c.Close()

	var err error
	if *asJSON || *once || !isTerminal(os.Stdout) {
		snap := snapshotOnce(c, *sample)
		if *asJSON {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			err = enc.Encode(snap)
		} else {
			err = printText(os.Stdout, snap)
		}
	} else {
		c.Rediscover = 5 * time.Second
		_, err = tea.NewProgram(tui.New(c, max(*interval, 100*time.Millisecond))).Run()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "ctxtop:", err)
		os.Exit(1)
	}
}

// snapshotOnce polls twice, sample apart, so speeds and power are measured.
func snapshotOnce(c *collect.Collector, sample time.Duration) snapshot.Snapshot {
	ctx, cancel := context.WithTimeout(context.Background(), sample+20*time.Second)
	defer cancel()
	first := c.Snapshot(ctx)
	if len(first.Servers) == 0 {
		// Nothing to measure; a short gap still gets a power reading.
		sample = min(sample, 200*time.Millisecond)
	}
	select {
	case <-time.After(sample):
	case <-ctx.Done():
	}
	return c.Snapshot(ctx)
}

func isTerminal(f *os.File) bool {
	st, err := f.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

func printText(w io.Writer, s snapshot.Snapshot) error {
	h := s.Host
	fmt.Fprintf(w, "ctxtop  %s/%s  memory %s / %s", h.OS, h.Arch, format.GB(h.MemUsed), format.GB(h.MemTotal))
	if h.SwapTotal > 0 {
		fmt.Fprintf(w, "  swap %s / %s", format.GB(h.SwapUsed), format.GB(h.SwapTotal))
	}
	if h.Pressure != "" {
		fmt.Fprintf(w, "  pressure %s", h.Pressure)
	}
	if h.GPUUtil != nil {
		fmt.Fprintf(w, "  gpu %.0f%%", *h.GPUUtil)
	}
	if h.Power != nil {
		fmt.Fprintf(w, "  power cpu %.1f W gpu %.1f W", h.Power.CPU, h.Power.GPU)
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
			fmt.Fprintf(w, "  %s\n", format.Model(m, s.Time))
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
					format.Count(sl.ContextUsed), format.Count(sl.ContextSize), sl.ContextFill*100,
					format.Rate(sl.GenTPS), format.Rate(sl.PromptTPS))
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
		fmt.Fprintf(tw, "    %s\t%s\t%s\n", r.Time.Local().Format("15:04:05"), what, format.Request(r))
	}
	return tw.Flush()
}
