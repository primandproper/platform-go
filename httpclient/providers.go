package httpclient

import (
	"net/http"
)

// NewHTTPClient provides an HTTP client from config.
// If cfg is nil, defaults are used.
func NewHTTPClient(cfg *Config) *http.Client {
	if cfg == nil {
		cfg = &Config{}
	}
	cfg.EnsureDefaults()
	return cfg.BuildClient()
}
