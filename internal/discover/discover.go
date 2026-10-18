// Package discover finds inference servers running on this machine.
//
// It maps listening TCP sockets to processes with lsof and matches known
// process names, then probes the runtimes' default ports for anything
// missed (a renamed binary, or a system without lsof). It only ever sends
// GET requests to the local machine.
package discover

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"net"
	"net/http"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/iBvishal/ctxtop/internal/llamacpp"
	"github.com/iBvishal/ctxtop/internal/lmstudio"
	"github.com/iBvishal/ctxtop/internal/ollama"
)

// DefaultPorts are probed even when no known process owns them:
// llama-server, Ollama and LM Studio.
var DefaultPorts = []int{8080, 11434, 1234}

// processRuntimes maps a process name to its runtime.
var processRuntimes = map[string]string{
	"llama-server": llamacpp.Runtime,
	"ollama":       ollama.Runtime,
}

// Found is one server discovery turned up.
type Found struct {
	Runtime string
	URL     string
	PID     int
	Process string
	// Runners are the URLs of llama-server processes an Ollama server
	// started to run its models.
	Runners []string
}

// Listener is one listening TCP socket.
type Listener struct {
	PID     int
	PPID    int
	Command string
	Host    string
	Port    int
}

type Discoverer struct {
	Client *http.Client
	Ports  []int
	// Listeners lists listening sockets; tests replace it. A nil result
	// with no error means the platform can't list them.
	Listeners func(context.Context) ([]Listener, error)
}

func New() *Discoverer {
	return &Discoverer{
		Client:    &http.Client{Timeout: 500 * time.Millisecond},
		Ports:     DefaultPorts,
		Listeners: lsofListeners,
	}
}

// Find returns every server found, sorted by runtime then URL. A failure to
// list sockets is not fatal: the default-port probe still runs.
func (d *Discoverer) Find(ctx context.Context) []Found {
	byURL := map[string]Found{}
	listeners, _ := d.Listeners(ctx)
	owner := map[int]Listener{}
	ports := append([]int(nil), d.Ports...)
	for _, l := range listeners {
		owner[l.Port] = l
		rt, ok := processRuntimes[l.Command]
		if !ok {
			// LM Studio serves from its app process; its port is set in
			// the app, so probe whatever it listens on.
			if strings.Contains(strings.ToLower(l.Command), "lm studio") {
				ports = append(ports, l.Port)
			}
			continue
		}
		u := url(l.Host, l.Port)
		byURL[u] = Found{Runtime: rt, URL: u, PID: l.PID, Process: l.Command}
	}

	for _, port := range ports {
		if f, ok := foundOnPort(byURL, port); ok && f.Runtime != "" {
			continue
		}
		host := "127.0.0.1"
		l, owned := owner[port]
		if owned {
			host = l.Host
		}
		u := url(host, port)
		rt := d.probe(ctx, u)
		if rt == "" {
			continue
		}
		f := Found{Runtime: rt, URL: u}
		if owned {
			f.PID, f.Process = l.PID, l.Command
		}
		byURL[u] = f
	}

	out := adoptRunners(byURL, listeners)
	sort.Slice(out, func(i, j int) bool {
		if out[i].Runtime != out[j].Runtime {
			return out[i].Runtime < out[j].Runtime
		}
		return out[i].URL < out[j].URL
	})
	return out
}

// adoptRunners folds the processes an Ollama server started into it. Recent
// Ollama runs each model in a child llama-server on a random port; those
// become the server's Runners instead of separate servers.
func adoptRunners(byURL map[string]Found, listeners []Listener) []Found {
	parent := map[int]int{}
	for _, l := range listeners {
		parent[l.PID] = l.PPID
	}
	ollamas := map[int]string{}
	for u, f := range byURL {
		if f.Runtime == ollama.Runtime && f.PID != 0 {
			ollamas[f.PID] = u
		}
	}
	isChild := func(f Found) (string, bool) {
		u, ok := ollamas[parent[f.PID]]
		return u, ok && f.PID != 0
	}

	for _, f := range byURL {
		if ou, ok := isChild(f); ok && f.Runtime == llamacpp.Runtime {
			o := byURL[ou]
			o.Runners = append(o.Runners, f.URL)
			byURL[ou] = o
		}
	}
	out := make([]Found, 0, len(byURL))
	for _, f := range byURL {
		if _, ok := isChild(f); ok {
			continue
		}
		sort.Strings(f.Runners)
		out = append(out, f)
	}
	return out
}

func foundOnPort(byURL map[string]Found, port int) (Found, bool) {
	suffix := ":" + strconv.Itoa(port)
	for u, f := range byURL {
		if strings.HasSuffix(u, suffix) {
			return f, true
		}
	}
	return Found{}, false
}

// Identify returns the runtime serving baseURL, or "" if it's none ctxtop
// knows.
func (d *Discoverer) Identify(ctx context.Context, baseURL string) string {
	return d.probe(ctx, baseURL)
}

func (d *Discoverer) probe(ctx context.Context, baseURL string) string {
	if llamacpp.Probe(ctx, d.Client, baseURL) {
		return llamacpp.Runtime
	}
	if ollama.Probe(ctx, d.Client, baseURL) {
		return ollama.Runtime
	}
	if lmstudio.Probe(ctx, d.Client, baseURL) {
		return lmstudio.Runtime
	}
	return ""
}

// url builds a base URL to reach a socket bound to host. A wildcard bind is
// reached on loopback.
func url(host string, port int) string {
	switch host {
	case "", "*", "0.0.0.0", "::", "[::]":
		host = "127.0.0.1"
	}
	host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	return "http://" + net.JoinHostPort(host, strconv.Itoa(port))
}

func lsofListeners(ctx context.Context) ([]Listener, error) {
	path, err := exec.LookPath("lsof")
	if err != nil {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	// +c 0: full command names. -F pRcn: machine-readable pid, parent pid,
	// command and name.
	out, err := exec.CommandContext(ctx, path, "-nP", "+c", "0", "-iTCP", "-sTCP:LISTEN", "-FpRcn").Output()
	if err != nil && len(out) == 0 {
		// lsof exits 1 when nothing matches.
		return nil, fmt.Errorf("lsof: %w", err)
	}
	return ParseLsof(out), nil
}

// ParseLsof reads `lsof -F pRcn` output. Each process starts with a p line,
// then R (parent pid) and c lines, then one n line per socket ("*:8080", "127.0.0.1:8080",
// "[::1]:8080"). Sockets listed for both IPv4 and IPv6 are merged.
func ParseLsof(out []byte) []Listener {
	var (
		res  []Listener
		pid  int
		ppid int
		cmd  string
		seen = map[string]bool{}
	)
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			continue
		}
		val := line[1:]
		switch line[0] {
		case 'p':
			pid, _ = strconv.Atoi(val)
			ppid, cmd = 0, ""
		case 'R':
			ppid, _ = strconv.Atoi(val)
		case 'c':
			cmd = val
		case 'n':
			i := strings.LastIndex(val, ":")
			if i < 0 {
				continue
			}
			port, err := strconv.Atoi(val[i+1:])
			if err != nil {
				continue
			}
			host := val[:i]
			key := fmt.Sprintf("%d/%d", pid, port)
			if seen[key] {
				// The same server on IPv4 and IPv6: keep the first.
				continue
			}
			seen[key] = true
			res = append(res, Listener{PID: pid, PPID: ppid, Command: cmd, Host: host, Port: port})
		}
	}
	return res
}
