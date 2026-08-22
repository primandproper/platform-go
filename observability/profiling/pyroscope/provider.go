// Package pyroscope implements profiling.Provider by pushing profiles
// continuously to a Pyroscope server.
//
// It is the provider for profiles you did not know you would need: CPU, alloc,
// in-use memory, and goroutine profiles are uploaded on a timer under the
// service name, so the profile covering an incident exists whether or not
// anyone was collecting during it. The pprof sibling is the opposite trade —
// nothing leaves the process, but somebody has to fetch a profile while the
// behavior is still happening.
//
// Profiling begins when the provider is constructed, not when Start is called;
// Start is a no-op kept for the interface. That means construction is already
// the side effect, and a provider built and discarded is still uploading until
// Shutdown.
//
// Enabling the mutex or block profiles sets the runtime's sampling rate for the
// whole process and adds the corresponding profile types to the upload set. Both
// cost something on every contended lock, which is why they are off by default.
//
// The Insecure flag disables TLS certificate verification on uploads — the
// pyroscope client exposes no such knob, so uploads are routed through an HTTP
// client that skips it. It exists for a self-signed internal endpoint and
// nothing else: it disables verification altogether rather than trusting a
// particular certificate.
package pyroscope

import (
	"context"
	"crypto/tls"
	"maps"
	"net/http"
	"runtime"

	"github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/observability/logging"
	"github.com/primandproper/platform-go/v13/observability/profiling"

	"github.com/grafana/pyroscope-go"
)

// NewProfilingProvider creates a Pyroscope-based profiling provider.
//
// A nil Config is an error rather than a noop. Asking this package for a
// provider is asking for Pyroscope, and handing back something that profiles
// nothing would hide the misconfiguration for the life of the process.
func NewProfilingProvider(ctx context.Context, logger logging.Logger, serviceName string, cfg *Config) (*Provider, error) {
	if cfg == nil {
		return nil, errors.Wrap(errors.ErrNilInputParameter, "nil pyroscope config")
	}

	if cfg.EnableMutexProfile {
		runtime.SetMutexProfileFraction(5)
	}
	if cfg.EnableBlockProfile {
		runtime.SetBlockProfileRate(5)
	}

	profileTypes := defaultProfileTypes()
	if cfg.EnableMutexProfile {
		profileTypes = append(profileTypes, pyroscope.ProfileMutexCount, pyroscope.ProfileMutexDuration)
	}
	if cfg.EnableBlockProfile {
		profileTypes = append(profileTypes, pyroscope.ProfileBlockCount, pyroscope.ProfileBlockDuration)
	}

	tags := make(map[string]string)
	maps.Copy(tags, cfg.Tags)

	pyroCfg := pyroscope.Config{
		ApplicationName:   serviceName,
		ServerAddress:     cfg.ServerAddress,
		UploadRate:        cfg.UploadRate,
		ProfileTypes:      profileTypes,
		Tags:              tags,
		Logger:            nil, // disable pyroscope's own logging; we use our logger
		BasicAuthUser:     cfg.BasicAuthUser,
		BasicAuthPassword: cfg.BasicAuthPassword,
	}

	if cfg.Insecure {
		// Opt-in TLS-verification skip for self-signed/internal Pyroscope endpoints. pyroscope-go
		// exposes no Insecure knob directly, so route uploads through a client that skips verification.
		pyroCfg.HTTPClient = &http.Client{
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // gated on the explicit Insecure config flag
			},
		}
	}

	profiler, err := pyroscope.Start(pyroCfg)
	if err != nil {
		return nil, errors.Wrap(err, "starting pyroscope profiler")
	}

	logger.WithValue("server_address", cfg.ServerAddress).
		WithValue("upload_rate", cfg.UploadRate.String()).
		Info("started pyroscope profiler")

	return &Provider{
		profiler: profiler,
		logger:   logger,
	}, nil
}

func defaultProfileTypes() []pyroscope.ProfileType {
	return []pyroscope.ProfileType{
		pyroscope.ProfileCPU,
		pyroscope.ProfileAllocObjects,
		pyroscope.ProfileAllocSpace,
		pyroscope.ProfileInuseObjects,
		pyroscope.ProfileInuseSpace,
		pyroscope.ProfileGoroutines,
	}
}

var _ profiling.Provider = (*Provider)(nil)

// Provider is the Pyroscope profiling.Provider implementation. It is exported,
// and returned by NewProfilingProvider, so a caller who has chosen Pyroscope can
// depend on that choice rather than on the interface every profiler shares.
type Provider struct {
	profiler *pyroscope.Profiler
	logger   logging.Logger
}

func (p *Provider) Start(ctx context.Context) error {
	// Pyroscope starts immediately in NewProfilingProvider.
	// Start is a no-op for pyroscope since we already called pyroscope.Start.
	return nil
}

func (p *Provider) Shutdown(ctx context.Context) error {
	if p.profiler != nil {
		if err := p.profiler.Stop(); err != nil {
			return err
		}
		p.logger.Info("stopped pyroscope profiler")
	}
	return nil
}
