// Package collect keeps one poller per discovered server and turns a round
// of polls into a snapshot. Pollers live across rounds so each round's
// rates are measured against the one before.
package collect

import (
	"context"
	"io"
	"strings"
	"sync/atomic"
	"time"

	"github.com/iBvishal/ctxtop/internal/advice"
	"github.com/iBvishal/ctxtop/internal/discover"
	"github.com/iBvishal/ctxtop/internal/hw"
	"github.com/iBvishal/ctxtop/internal/llamacpp"
	"github.com/iBvishal/ctxtop/internal/lmstudio"
	"github.com/iBvishal/ctxtop/internal/ollama"
	"github.com/iBvishal/ctxtop/internal/snapshot"
)

type poller interface {
	Poll(ctx context.Context) (snapshot.Server, error)
}

type entry struct {
	found  discover.Found
	poller poller // nil when the runtime is unknown
}

// Collector is not safe for concurrent use, except Rescan.
type Collector struct {
	// Rediscover is how often discovery runs again. Zero means only once.
	Rediscover time.Duration
	// Discover and ReadHost are replaced in tests.
	Discover func(ctx context.Context) []discover.Found
	ReadHost func() snapshot.Host
	// Advice evaluates each snapshot; nil means no advice.
	Advice *advice.Engine

	entries      []entry
	lastDiscover time.Time
	rescan       atomic.Bool
}

// New returns a collector for the given server URLs, or for every server
// discovery finds when urls is empty.
func New(urls []string) *Collector {
	d := discover.New()
	c := &Collector{ReadHost: hw.Read, Advice: advice.New()}
	if len(urls) == 0 {
		c.Discover = d.Find
		return c
	}
	c.Discover = func(ctx context.Context) []discover.Found {
		out := make([]discover.Found, 0, len(urls))
		for _, u := range urls {
			u = strings.TrimRight(u, "/")
			out = append(out, discover.Found{Runtime: d.Identify(ctx, u), URL: u})
		}
		return out
	}
	return c
}

// Rescan makes the next Snapshot run discovery again. It may be called from
// any goroutine.
func (c *Collector) Rescan() { c.rescan.Store(true) }

// Snapshot discovers servers if due, polls each once and reads the host.
func (c *Collector) Snapshot(ctx context.Context) snapshot.Snapshot {
	if c.lastDiscover.IsZero() || c.rescan.Swap(false) ||
		(c.Rediscover > 0 && time.Since(c.lastDiscover) >= c.Rediscover) {
		c.refresh(c.Discover(ctx))
		c.lastDiscover = time.Now()
	}

	servers := make([]snapshot.Server, 0, len(c.entries))
	for _, e := range c.entries {
		f := e.found
		base := snapshot.Server{Runtime: f.Runtime, URL: f.URL, PID: f.PID, Process: f.Process}
		if e.poller == nil {
			base.Runtime = "unknown"
			base.Error = "not a llama-server, Ollama or LM Studio"
			servers = append(servers, base)
			continue
		}
		s, err := e.poller.Poll(ctx)
		if err != nil {
			base.Error = err.Error()
			servers = append(servers, base)
			continue
		}
		s.PID, s.Process = f.PID, f.Process
		servers = append(servers, s)
	}

	snap := snapshot.Snapshot{
		Version: snapshot.Version,
		Time:    time.Now().UTC().Truncate(time.Millisecond),
		Host:    c.ReadHost(),
		Servers: servers,
	}
	if c.Advice != nil {
		snap.Advice = c.Advice.Evaluate(snap)
	}
	return snap
}

// refresh replaces the server list, keeping the pollers of servers still
// there and closing the rest.
func (c *Collector) refresh(found []discover.Found) {
	old := map[string]entry{}
	for _, e := range c.entries {
		old[key(e.found)] = e
	}
	next := make([]entry, 0, len(found))
	for _, f := range found {
		e, ok := old[key(f)]
		if ok {
			delete(old, key(f))
			e.found = f
		} else {
			e = entry{found: f, poller: newPoller(f)}
		}
		if p, ok := e.poller.(*ollama.Poller); ok {
			p.Runners = f.Runners
		}
		next = append(next, e)
	}
	for _, e := range old {
		closePoller(e.poller)
	}
	c.entries = next
}

// Close stops anything the pollers started, such as LM Studio's log stream.
func (c *Collector) Close() {
	for _, e := range c.entries {
		closePoller(e.poller)
	}
	c.entries = nil
}

func key(f discover.Found) string { return f.Runtime + " " + f.URL }

func newPoller(f discover.Found) poller {
	switch f.Runtime {
	case llamacpp.Runtime:
		return llamacpp.NewPoller(f.URL)
	case ollama.Runtime:
		return ollama.NewPoller(f.URL)
	case lmstudio.Runtime:
		return lmstudio.NewPoller(f.URL)
	}
	return nil
}

func closePoller(p poller) {
	if c, ok := p.(io.Closer); ok {
		_ = c.Close()
	}
}
