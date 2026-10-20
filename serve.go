package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"sync/atomic"
	"time"

	"github.com/iBvishal/ctxtop/internal/collect"
	"github.com/iBvishal/ctxtop/internal/metrics"
	"github.com/iBvishal/ctxtop/internal/snapshot"
)

// serve polls every interval and serves the latest snapshot as Prometheus
// metrics on addr until ctx is done. Only this goroutine uses c.
func serve(ctx context.Context, c *collect.Collector, addr string, interval time.Duration) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	var latest atomic.Pointer[snapshot.Snapshot]
	srv := &http.Server{Handler: metricsHandler(&latest), ReadHeaderTimeout: 5 * time.Second}

	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			snap := c.Snapshot(pctx)
			cancel()
			latest.Store(&snap)
			select {
			case <-t.C:
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()

	fmt.Fprintf(os.Stderr, "ctxtop: serving metrics on http://%s/metrics\n", ln.Addr())
	if err := srv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func metricsHandler(latest *atomic.Pointer[snapshot.Snapshot]) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, r *http.Request) {
		snap := latest.Load()
		if snap == nil {
			http.Error(w, "first poll still running", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", metrics.ContentType)
		_ = metrics.Write(w, *snap, version)
	})
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "ctxtop", version, "\nMetrics are at /metrics.")
	})
	return mux
}
