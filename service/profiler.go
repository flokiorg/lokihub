package service

import (
	"context"

	"errors"
	"net/http"
	"net/http/pprof"
	"time"
)

func startProfiler(ctx context.Context, addr string) {
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)

	server := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() { //nolint:gosec // ctx is already cancelled at this point; a fresh, bounded context is required for graceful shutdown, see comment below
		<-ctx.Done()
		// A fresh context is required here: ctx is already Done(), and
		// Shutdown needs its own (bounded) deadline to wait for in-flight
		// requests instead of blocking forever.
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		err := server.Shutdown(shutdownCtx)
		if err != nil {
			panic("pprof server shutdown failed: " + err.Error())
		}
	}()

	go func() {
		err := server.ListenAndServe()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			panic("pprof server failed: " + err.Error())
		}
	}()
}
