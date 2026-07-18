package pprof

import (
	"context"
	"net/http"
	"net/http/pprof"
	"runtime"
	"strconv"
	"time"

	"github.com/primandproper/platform-go/v5/errors"
	"github.com/primandproper/platform-go/v5/observability/logging"
	"github.com/primandproper/platform-go/v5/observability/profiling"
	profilingnoop "github.com/primandproper/platform-go/v5/observability/profiling/noop"
)

// NewProfilingProvider creates a pprof-based profiling provider that exposes
// /debug/pprof endpoints on an HTTP server.
func NewProfilingProvider(ctx context.Context, logger logging.Logger, cfg *Config) (profiling.Provider, error) {
	if cfg == nil {
		return profilingnoop.NewProvider(), nil
	}

	port := cfg.Port
	if port == 0 {
		port = DefaultPort
	}

	if cfg.EnableMutexProfile {
		runtime.SetMutexProfileFraction(5)
	}
	if cfg.EnableBlockProfile {
		runtime.SetBlockProfileRate(5)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)

	addr := ":" + strconv.FormatUint(uint64(port), 10)
	server := &http.Server{
		ReadHeaderTimeout: 5 * time.Second,
		Addr:              addr,
		Handler:           mux,
	}

	logger.WithValue("port", port).
		WithValue("addr", addr).
		Info("starting pprof HTTP server")

	return &provider{
		server: server,
		logger: logger,
	}, nil
}

var _ profiling.Provider = (*provider)(nil)

type provider struct {
	server *http.Server
	logger logging.Logger
}

func (p *provider) Start(ctx context.Context) error {
	go func() {
		if err := p.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			p.logger.Error("pprof server error", err)
		}
	}()
	return nil
}

func (p *provider) Shutdown(ctx context.Context) error {
	if p.server != nil {
		if err := p.server.Shutdown(ctx); err != nil {
			return errors.Wrap(err, "shutting down pprof server")
		}
		p.logger.Info("stopped pprof server")
	}
	return nil
}
