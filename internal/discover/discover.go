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
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/iBvishal/ctxtop/internal/llamacpp"
)

// Ollama is the runtime name for servers ctxtop can find but not read yet.
const Ollama = "ollama"

// DefaultPorts are probed even when no known process owns them:
// llama-server and Ollama.
var DefaultPorts = []int{8080, 11434}

// processRuntimes maps a process name to its runtime.
var processRuntimes = map[string]string{
	"llama-server": llamacpp.Runtime,
	"ollama":       Ollama,
}

// Found is one server discovery turned up.
type Found struct {
	Runtime string
	URL     string
	PID     int
	Process string
}

// Listener is one listening TCP socket.
type Listener struct {
	PID     int
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
	for _, l := range listeners {
		owner[l.Port] = l
		rt, ok := processRuntimes[l.Command]
		if !ok {
			continue
		}
		u := url(l.Host, l.Port)
		byURL[u] = Found{Runtime: rt, URL: u, PID: l.PID, Process: l.Command}
	}

	for _, port := range d.Ports {
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

	out := make([]Found, 0, len(byURL))
	for _, f := range byURL {
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Runtime != out[j].Runtime {
			return out[i].Runtime < out[j].Runtime
		}
		return out[i].URL < out[j].URL
	})
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

func (d *Discoverer) probe(ctx context.Context, baseURL string) string {
	if llamacpp.Probe(ctx, d.Client, baseURL) {
		return llamacpp.Runtime
	}
	if probeOllama(ctx, d.Client, baseURL) {
		return Ollama
	}
	return ""
}

func probeOllama(ctx context.Context, client *http.Client, baseURL string) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/api/version", nil)
	if err != nil {
		return false
	}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	var v struct {
		Version string `json:"version"`
	}
	return json.NewDecoder(resp.Body).Decode(&v) == nil && v.Version != ""
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
	// +c 0: full command names. -F pcn: machine-readable pid, command, name.
	out, err := exec.CommandContext(ctx, path, "-nP", "+c", "0", "-iTCP", "-sTCP:LISTEN", "-Fpcn").Output()
	if err != nil && len(out) == 0 {
		// lsof exits 1 when nothing matches.
		return nil, fmt.Errorf("lsof: %w", err)
	}
	return ParseLsof(out), nil
}

// ParseLsof reads `lsof -F pcn` output. Each process starts with a p line,
// then a c line, then one n line per socket ("*:8080", "127.0.0.1:8080",
// "[::1]:8080"). Sockets listed for both IPv4 and IPv6 are merged.
func ParseLsof(out []byte) []Listener {
	var (
		res  []Listener
		pid  int
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
			cmd = ""
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
			res = append(res, Listener{PID: pid, Command: cmd, Host: host, Port: port})
		}
	}
	return res
}
