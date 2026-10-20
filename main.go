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
	"os/signal"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/iBvishal/ctxtop/internal/advice"
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
	// "ctxtop doctor [flags]": check once, print advice, exit by severity.
	// "ctxtop serve [flags]": serve Prometheus metrics until stopped.
	var cmd string
	if len(os.Args) > 1 && (os.Args[1] == "doctor" || os.Args[1] == "serve") {
		cmd = os.Args[1]
		os.Args = append(os.Args[:1], os.Args[2:]...)
	}
	doctor := cmd == "doctor"

	var (
		asJSON   = flag.Bool("json", false, "print one snapshot as JSON and exit")
		once     = flag.Bool("once", false, "print one snapshot as text and exit")
		interval = flag.Duration("interval", time.Second, "refresh interval of the live view (serve: how often to poll, default 5s)")
		addr     = flag.String("addr", "127.0.0.1:9464", "for serve: address to serve metrics on")
		sample   = flag.Duration("sample", time.Second, "for --once and --json: time between the two polls that measure speed")
		showVer  = flag.Bool("version", false, "print the version and exit")
		urls     urlList
	)
	flag.Var(&urls, "url", "read the server at this URL instead of discovering servers (repeatable)")
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "Usage: ctxtop [flags]\n       ctxtop doctor [flags]\n       ctxtop serve [flags]\n\n"+
			"Finds local LLM servers and shows speed, memory and context fill, and\n"+
			"what to change. Runs a live view in a terminal; prints one snapshot\n"+
			"otherwise. doctor checks once, prints only the advice and exits 0 when\n"+
			"healthy, 1 on warnings, 2 on anything critical. serve polls in the\n"+
			"background and serves Prometheus metrics on /metrics.\n\n")
		flag.PrintDefaults()
	}
	flag.Parse()

	if *showVer {
		fmt.Println("ctxtop", version)
		return
	}

	c := collect.New(urls)
	defer c.Close()

	if doctor {
		snap := snapshotOnce(c, *sample)
		code, err := printDoctor(os.Stdout, snap)
		if err != nil {
			fmt.Fprintln(os.Stderr, "ctxtop:", err)
			os.Exit(3)
		}
		c.Close()
		os.Exit(code)
	}

	if cmd == "serve" {
		every := 5 * time.Second
		flag.Visit(func(f *flag.Flag) {
			if f.Name == "interval" {
				every = max(*interval, 100*time.Millisecond)
			}
		})
		c.Rediscover = 15 * time.Second
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		if err := serve(ctx, c, *addr, every); err != nil {
			fmt.Fprintln(os.Stderr, "ctxtop:", err)
			c.Close()
			os.Exit(1)
		}
		return
	}

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
	if len(s.Advice) > 0 {
		fmt.Fprintln(w, "\nadvice")
		for _, l := range advice.Lines(s.Advice) {
			fmt.Fprintln(w, "  "+l)
		}
	}
	return nil
}

// printDoctor prints what was found and the advice, and returns the exit
// code: 0 healthy, 1 warnings, 2 critical. Info alone is healthy.
func printDoctor(w io.Writer, s snapshot.Snapshot) (int, error) {
	if len(s.Servers) == 0 {
		fmt.Fprintln(w, "No LLM servers found. ctxtop looks for llama-server, Ollama and LM Studio on this machine.")
	} else {
		var found []string
		for _, srv := range s.Servers {
			found = append(found, srv.Runtime+" ("+strings.TrimPrefix(srv.URL, "http://")+")")
		}
		fmt.Fprintf(w, "Checked %s.\n", strings.Join(found, ", "))
	}
	if len(s.Advice) == 0 {
		_, err := fmt.Fprintln(w, "No problems found.")
		return 0, err
	}
	fmt.Fprintln(w)
	for _, l := range advice.Lines(s.Advice) {
		if _, err := fmt.Fprintln(w, l); err != nil {
			return 0, err
		}
	}
	switch advice.Worst(s.Advice) {
	case advice.Critical:
		return 2, nil
	case advice.Warn:
		return 1, nil
	}
	return 0, nil
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
