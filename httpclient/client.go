package httpclient

import (
	"net"
	"net/http"
	"time"

	"github.com/primandproper/platform-go/v5/observability/tracing"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

// BuildClient constructs an HTTP client from config.
func (cfg *Config) BuildClient() *http.Client {
	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = defaultTimeout
	}

	var transport http.RoundTripper
	if cfg.EnableTracing {
		t := &http.Transport{
			Proxy: http.ProxyFromEnvironment,
			DialContext: (&net.Dialer{
				Timeout:   timeout,
				KeepAlive: 30 * time.Second,
			}).DialContext,
			MaxIdleConns:          cfg.MaxIdleConns,
			MaxIdleConnsPerHost:   cfg.MaxIdleConnsPerHost,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: 2 * timeout,
			IdleConnTimeout:       3 * timeout,
		}
		transport = otelhttp.NewTransport(t, otelhttp.WithSpanNameFormatter(tracing.FormatSpan))
	} else {
		transport = &http.Transport{
			Proxy: http.ProxyFromEnvironment,
			DialContext: (&net.Dialer{
				Timeout:   timeout,
				KeepAlive: 30 * time.Second,
			}).DialContext,
			MaxIdleConns:          cfg.MaxIdleConns,
			MaxIdleConnsPerHost:   cfg.MaxIdleConnsPerHost,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: 2 * timeout,
			IdleConnTimeout:       3 * timeout,
		}
	}

	return &http.Client{
		Transport: transport,
		Timeout:   timeout,
	}
}
