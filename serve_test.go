package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/iBvishal/ctxtop/internal/metrics"
	"github.com/iBvishal/ctxtop/internal/snapshot"
)

func TestMetricsHandler(t *testing.T) {
	var latest atomic.Pointer[snapshot.Snapshot]
	h := metricsHandler(&latest)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("before the first poll: status %d, want 503", rec.Code)
	}

	latest.Store(&snapshot.Snapshot{
		Host:    snapshot.Host{MemTotal: 16 << 30, MemUsed: 12 << 30},
		Servers: []snapshot.Server{{Runtime: "llama.cpp", URL: "http://127.0.0.1:8080"}},
	})
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != metrics.ContentType {
		t.Errorf("Content-Type %q", ct)
	}
	for _, want := range []string{
		"ctxtop_host_memory_total_bytes 17179869184\n",
		`ctxtop_server_up{runtime="llama.cpp",url="http://127.0.0.1:8080"} 1`,
	} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("missing %q in:\n%s", want, rec.Body.String())
		}
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/metrics", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST: status %d, want 405", rec.Code)
	}
}
