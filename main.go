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
	flag.Var(&urls, "url", "read this llama-server instead of discovering servers (repeatable)")
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

func collect(ctx context.Context, urls []string, sample time.Duration) snapshot.Snapshot {
	var found []discover.Found
	if len(urls) > 0 {
		for _, u := range urls {
			found = append(found, discover.Found{Runtime: llamacpp.Runtime, URL: strings.TrimRight(u, "/")})
		}
	} else {
		found = discover.New().Find(ctx)
	}

	servers := make([]snapshot.Server, len(found))
	pollers := map[int]*llamacpp.Poller{}
	for i, f := range found {
		servers[i] = snapshot.Server{Runtime: f.Runtime, URL: f.URL, PID: f.PID, Process: f.Process}
		if f.Runtime != llamacpp.Runtime {
			servers[i].Error = "found, but ctxtop can't read " + f.Runtime + " yet"
			continue
		}
		p := llamacpp.NewPoller(f.URL)
		if _, err := p.Poll(ctx); err != nil {
			servers[i].Error = err.Error()
			continue
		}
		pollers[i] = p
	}

	if len(pollers) > 0 {
		select {
		case <-time.After(sample):
		case <-ctx.Done():
		}
	}
	for i, p := range pollers {
		s, err := p.Poll(ctx)
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
		_, err := fmt.Fprintln(w, "\nNo LLM servers found. ctxtop looks for llama-server and Ollama on this machine.")
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
			fmt.Fprintf(w, "  %s  ctx %s\n", m.Name, count(m.ContextSize))
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
	}
	return nil
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
