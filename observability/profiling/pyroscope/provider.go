package pyroscope

import (
	"context"
	"maps"
	"runtime"

	"github.com/primandproper/platform-go/v2/errors"
	"github.com/primandproper/platform-go/v2/observability/logging"
	"github.com/primandproper/platform-go/v2/observability/profiling"
	profilingnoop "github.com/primandproper/platform-go/v2/observability/profiling/noop"

	"github.com/grafana/pyroscope-go"
)

// ProvideProfilingProvider creates a Pyroscope-based profiling provider.
func ProvideProfilingProvider(ctx context.Context, logger logging.Logger, serviceName string, cfg *Config) (profiling.Provider, error) {
	if cfg == nil {
		return profilingnoop.NewProvider(), nil
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

	profiler, err := pyroscope.Start(pyroCfg)
	if err != nil {
		return nil, errors.Wrap(err, "starting pyroscope profiler")
	}

	logger.WithValue("server_address", cfg.ServerAddress).
		WithValue("upload_rate", cfg.UploadRate.String()).
		Info("started pyroscope profiler")

	return &provider{
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

var _ profiling.Provider = (*provider)(nil)

type provider struct {
	profiler *pyroscope.Profiler
	logger   logging.Logger
}

func (p *provider) Start(ctx context.Context) error {
	// Pyroscope starts immediately in ProvideProfilingProvider.
	// Start is a no-op for pyroscope since we already called pyroscope.Start.
	return nil
}

func (p *provider) Shutdown(ctx context.Context) error {
	if p.profiler != nil {
		if err := p.profiler.Stop(); err != nil {
			return err
		}
		p.logger.Info("stopped pyroscope profiler")
	}
	return nil
}
