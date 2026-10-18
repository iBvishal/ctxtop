// Package ollama reads a running Ollama server.
//
// Ollama has no metrics endpoint (checked against v0.40.6: no /metrics route
// and no metrics setting). What it does expose:
//   - GET /api/ps: loaded models, their size and the part in GPU memory
//     (size_vram), context length and when they unload.
//   - its access log: one [GIN] line per request with status and latency,
//     but no token counts.
//
// Recent Ollama runs each loaded model in a child llama-server on a random
// port. When discovery finds those runners, their /slots give each model's
// live speed and context fill. Runners are matched to models by the model
// blob: the runner's --model path is the blob a model's manifest names.
// Older Ollama, without llama-server runners, only has the above.
package ollama

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/iBvishal/ctxtop/internal/llamacpp"
	"github.com/iBvishal/ctxtop/internal/snapshot"
)

// Runtime is the name ctxtop reports for Ollama.
const Runtime = "ollama"

type psResponse struct {
	Models []struct {
		Name    string `json:"name"`
		Model   string `json:"model"`
		Size    int64  `json:"size"`
		Details struct {
			QuantizationLevel string `json:"quantization_level"`
		} `json:"details"`
		ExpiresAt     time.Time `json:"expires_at"`
		SizeVRAM      int64     `json:"size_vram"`
		ContextLength int       `json:"context_length"`
	} `json:"models"`
}

// Poller reads one Ollama server.
type Poller struct {
	BaseURL string
	Client  *http.Client
	// LogPaths are tried in order for the access log; the first that
	// exists is read.
	LogPaths []string
	// Window is how far back the access log is read for requests.
	Window time.Duration
	Now    func() time.Time
	// Runners are the base URLs of the llama-server processes running this
	// server's models.
	Runners []string
	// ModelsDir is Ollama's model store, holding manifests/ and blobs/.
	ModelsDir string

	runners map[string]*llamacpp.Poller
}

func NewPoller(baseURL string) *Poller {
	return &Poller{
		BaseURL:   strings.TrimRight(baseURL, "/"),
		Client:    &http.Client{Timeout: 2 * time.Second},
		LogPaths:  DefaultLogPaths(),
		Window:    time.Minute,
		Now:       time.Now,
		ModelsDir: DefaultModelsDir(),
		runners:   map[string]*llamacpp.Poller{},
	}
}

// DefaultModelsDir is where Ollama keeps models: $OLLAMA_MODELS, or
// ~/.ollama/models.
func DefaultModelsDir() string {
	if d := os.Getenv("OLLAMA_MODELS"); d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".ollama", "models")
}

// DefaultLogPaths are where Ollama writes its log: the macOS app, then
// Homebrew's service on Apple Silicon and Intel.
func DefaultLogPaths() []string {
	var paths []string
	if home, err := os.UserHomeDir(); err == nil {
		paths = append(paths, filepath.Join(home, ".ollama", "logs", "server.log"))
	}
	return append(paths, "/opt/homebrew/var/log/ollama.log", "/usr/local/var/log/ollama.log")
}

func (p *Poller) Poll(ctx context.Context) (snapshot.Server, error) {
	srv := snapshot.Server{Runtime: Runtime, URL: p.BaseURL}

	var v struct {
		Version string `json:"version"`
	}
	if err := get(ctx, p.Client, p.BaseURL+"/api/version", &v); err != nil {
		return srv, err
	}
	srv.Build = v.Version

	var ps psResponse
	if err := get(ctx, p.Client, p.BaseURL+"/api/ps", &ps); err != nil {
		return srv, err
	}
	for _, m := range ps.Models {
		model := snapshot.Model{
			Name:        m.Name,
			Quant:       m.Details.QuantizationLevel,
			ContextSize: m.ContextLength,
			Mem:         uint64(max(m.Size, 0)),
		}
		gpu := uint64(max(m.SizeVRAM, 0))
		model.MemGPU = &gpu
		if !m.ExpiresAt.IsZero() {
			t := m.ExpiresAt
			model.UnloadsAt = &t
		}
		srv.Models = append(srv.Models, model)
	}

	p.attachRunners(ctx, srv.Models)
	srv.Requests = p.recentRequests()
	return srv, nil
}

// attachRunners polls each runner and gives its slots to the model it runs.
func (p *Poller) attachRunners(ctx context.Context, models []snapshot.Model) {
	if p.runners == nil {
		p.runners = map[string]*llamacpp.Poller{}
	}
	byBlob := map[string]int{}
	for i, m := range models {
		if blob := ModelBlob(p.ModelsDir, m.Name); blob != "" {
			byBlob[blob] = i
		}
	}
	live := map[string]bool{}
	for _, u := range p.Runners {
		live[u] = true
		rp, ok := p.runners[u]
		if !ok {
			rp = llamacpp.NewPoller(u)
			p.runners[u] = rp
		}
		rs, err := rp.Poll(ctx)
		if err != nil || len(rs.Models) == 0 {
			continue
		}
		i, ok := byBlob[blobDigest(rs.Models[0].Name)]
		if !ok && len(models) == 1 && len(p.Runners) == 1 {
			i, ok = 0, true
		}
		if ok {
			models[i].Slots = rs.Models[0].Slots
		}
	}
	for u := range p.runners {
		if !live[u] {
			delete(p.runners, u)
		}
	}
}

// blobDigest returns the hex digest in a blob file name ("sha256-<hex>") or
// manifest digest ("sha256:<hex>").
func blobDigest(name string) string {
	name = filepath.Base(name)
	for _, prefix := range []string{"sha256-", "sha256:"} {
		if strings.HasPrefix(name, prefix) {
			return name[len(prefix):]
		}
	}
	return ""
}

// ModelBlob returns the hex digest of a model's weights blob, read from its
// manifest in dir, or "" if the manifest can't be read.
func ModelBlob(dir, name string) string {
	b, err := os.ReadFile(manifestPath(dir, name))
	if err != nil {
		return ""
	}
	var m struct {
		Layers []struct {
			MediaType string `json:"mediaType"`
			Digest    string `json:"digest"`
		} `json:"layers"`
	}
	if json.Unmarshal(b, &m) != nil {
		return ""
	}
	for _, l := range m.Layers {
		if l.MediaType == "application/vnd.ollama.image.model" {
			return blobDigest(l.Digest)
		}
	}
	return ""
}

// manifestPath maps a model name to its manifest file. "qwen3:14b" lives
// at manifests/registry.ollama.ai/library/qwen3/14b, "me/m" at
// .../registry.ollama.ai/me/m/latest and "hf.co/org/repo:q4" at
// manifests/hf.co/org/repo/q4.
func manifestPath(dir, name string) string {
	host, ns, tag := "registry.ollama.ai", "library", "latest"
	if i := strings.LastIndex(name, ":"); i > strings.LastIndex(name, "/") {
		name, tag = name[:i], name[i+1:]
	}
	parts := strings.Split(name, "/")
	model := parts[len(parts)-1]
	switch len(parts) {
	case 1:
	case 2:
		ns = parts[0]
	default:
		host, ns = parts[0], strings.Join(parts[1:len(parts)-1], "/")
	}
	return filepath.Join(dir, "manifests", host, ns, model, tag)
}

func (p *Poller) recentRequests() []snapshot.Request {
	for _, path := range p.LogPaths {
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		defer f.Close()
		tail, err := readTail(f, 256<<10)
		if err != nil {
			return nil
		}
		return ParseAccessLog(tail, p.Now().Add(-p.Window), time.Local)
	}
	return nil
}

// readTail returns up to n bytes from the end of f, starting at a line
// boundary.
func readTail(f *os.File, n int64) ([]byte, error) {
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	off := max(st.Size()-n, 0)
	b := make([]byte, st.Size()-off)
	if _, err := f.ReadAt(b, off); err != nil && err != io.EOF {
		return nil, err
	}
	if off > 0 {
		if i := bytes.IndexByte(b, '\n'); i >= 0 {
			b = b[i+1:]
		}
	}
	return b, nil
}

// ginLine matches Gin's default access log line:
//
//	[GIN] 2026/10/18 - 14:22:31 | 200 |  2.512312s |       127.0.0.1 | POST     "/api/chat"
var ginLine = regexp.MustCompile(`^\[GIN\] (\d{4}/\d{2}/\d{2} - \d{2}:\d{2}:\d{2}) \|\s*(\d{3}) \|\s*(\S+) \|\s*\S+ \|\s*(\S+)\s+"([^"]*)"`)

// inferencePaths are the routes that run a model. Everything else (and
// ctxtop's own /api/ps polling) is left out.
var inferencePaths = map[string]bool{
	"/api/generate":        true,
	"/api/chat":            true,
	"/api/embed":           true,
	"/api/embeddings":      true,
	"/v1/chat/completions": true,
	"/v1/completions":      true,
	"/v1/embeddings":       true,
	"/v1/responses":        true,
	"/v1/messages":         true,
}

// ParseAccessLog returns the inference requests in log that finished at or
// after since. Gin writes local time without a zone, so loc says which.
func ParseAccessLog(log []byte, since time.Time, loc *time.Location) []snapshot.Request {
	var out []snapshot.Request
	sc := bufio.NewScanner(bytes.NewReader(log))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		m := ginLine.FindStringSubmatch(sc.Text())
		if m == nil || m[4] != http.MethodPost || !inferencePaths[m[5]] {
			continue
		}
		t, err := time.ParseInLocation("2006/01/02 - 15:04:05", m[1], loc)
		if err != nil || t.Before(since.Truncate(time.Second)) {
			continue
		}
		status, _ := strconv.Atoi(m[2])
		req := snapshot.Request{Time: t, Path: m[5], Status: status}
		if d, err := time.ParseDuration(m[3]); err == nil {
			req.Latency = d.Seconds()
		}
		out = append(out, req)
	}
	return out
}

func get(ctx context.Context, client *http.Client, url string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(v)
}

// Probe reports whether baseURL is an Ollama server, by GET /api/version.
func Probe(ctx context.Context, client *http.Client, baseURL string) bool {
	var v struct {
		Version string `json:"version"`
	}
	return get(ctx, client, strings.TrimRight(baseURL, "/")+"/api/version", &v) == nil && v.Version != ""
}
