package httpclient

import (
	"net/http"

	"github.com/samber/do/v2"
)

// RegisterHTTPClient registers an *http.Client with the injector.
func RegisterHTTPClient(i do.Injector) {
	do.Provide[*http.Client](i, func(i do.Injector) (*http.Client, error) {
		return NewHTTPClient(do.MustInvoke[*Config](i)), nil
	})
}
