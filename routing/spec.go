package routing

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/swaggest/openapi-go/openapi3"
)

// Spec returns the accumulated OpenAPI 3 specification. It reflects every route
// registered so far; call it after registration (and MountOpenAPI) is complete.
func (r *Router) Spec() *openapi3.Spec {
	return r.reflector.Spec
}

// MarshalSpec renders the accumulated spec as indented JSON.
func (r *Router) MarshalSpec() ([]byte, error) {
	return json.MarshalIndent(r.reflector.Spec, "", "  ")
}

// MountOpenAPI registers two routes on the backend: specPath serves the spec as
// JSON, and (when uiPath is non-empty) uiPath serves a self-contained docs UI
// page that renders it. Both routes go through the backend, so they inherit all
// of its middleware and instrumentation.
//
// Call this after all typed routes are registered so the served spec is complete.
func (r *Router) MountOpenAPI(specPath, uiPath string) {
	logger := r.o11y.Logger()

	r.backend.Handle(http.MethodGet, specPath, http.HandlerFunc(func(res http.ResponseWriter, _ *http.Request) {
		body, err := r.MarshalSpec()
		if err != nil {
			http.Error(res, "could not marshal OpenAPI spec", http.StatusInternalServerError)

			return
		}

		res.Header().Set("Content-Type", "application/json")
		res.WriteHeader(http.StatusOK)
		if _, writeErr := res.Write(body); writeErr != nil {
			logger.Error("writing OpenAPI spec response", writeErr)
		}
	}))

	if uiPath == "" {
		return
	}

	page := []byte(docsPage(specPath))
	r.backend.Handle(http.MethodGet, uiPath, http.HandlerFunc(func(res http.ResponseWriter, _ *http.Request) {
		res.Header().Set("Content-Type", "text/html; charset=utf-8")
		res.WriteHeader(http.StatusOK)
		if _, writeErr := res.Write(page); writeErr != nil {
			logger.Error("writing OpenAPI docs response", writeErr)
		}
	}))
}

// docsPage returns an HTML page that renders the spec at specURL using Stoplight
// Elements. The rendering library is loaded from a CDN; the page itself is
// otherwise self-contained.
func docsPage(specURL string) string {
	return fmt.Sprintf(`<!doctype html>
<html>
  <head>
    <meta charset="utf-8">
    <meta name="viewport" content="width=device-width, initial-scale=1">
    <title>API Reference</title>
    <link rel="stylesheet" href="https://unpkg.com/@stoplight/elements/styles.min.css">
    <script src="https://unpkg.com/@stoplight/elements/web-components.min.js"></script>
  </head>
  <body style="margin:0">
    <elements-api apiDescriptionUrl=%q router="hash" layout="sidebar"></elements-api>
  </body>
</html>`, specURL)
}
