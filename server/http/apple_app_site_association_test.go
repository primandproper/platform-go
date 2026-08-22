package http

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/primandproper/platform-go/v13/encoding"
	loggingnoop "github.com/primandproper/platform-go/v13/observability/logging/noop"
	tracingnoop "github.com/primandproper/platform-go/v13/observability/tracing/noop"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestAppleAppSiteAssociationConfig_Enabled(T *testing.T) {
	T.Parallel()

	T.Run("enabled when both fields are set", func(t *testing.T) {
		t.Parallel()

		cfg := &AppleAppSiteAssociationConfig{TeamID: "ABCD1234XY", BundleID: "com.example.ios"}

		test.True(t, cfg.Enabled())
	})

	T.Run("disabled when empty", func(t *testing.T) {
		t.Parallel()

		cfg := &AppleAppSiteAssociationConfig{}

		test.False(t, cfg.Enabled())
	})

	T.Run("disabled when only team ID is set", func(t *testing.T) {
		t.Parallel()

		cfg := &AppleAppSiteAssociationConfig{TeamID: "ABCD1234XY"}

		test.False(t, cfg.Enabled())
	})

	T.Run("disabled when only bundle ID is set", func(t *testing.T) {
		t.Parallel()

		cfg := &AppleAppSiteAssociationConfig{BundleID: "com.example.ios"}

		test.False(t, cfg.Enabled())
	})

	T.Run("disabled when team ID is malformed", func(t *testing.T) {
		t.Parallel()

		cfg := &AppleAppSiteAssociationConfig{TeamID: "ABCD1234XY.com.example.ios", BundleID: "com.example.ios"}

		test.False(t, cfg.Enabled())
	})

	T.Run("disabled when bundle ID is malformed", func(t *testing.T) {
		t.Parallel()

		cfg := &AppleAppSiteAssociationConfig{TeamID: "ABCD1234XY", BundleID: `com."example".ios`}

		test.False(t, cfg.Enabled())
	})

	T.Run("disabled when nil", func(t *testing.T) {
		t.Parallel()

		var cfg *AppleAppSiteAssociationConfig

		test.False(t, cfg.Enabled())
	})
}

func TestAppleAppSiteAssociationConfig_ValidateWithContext(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		cfg := &AppleAppSiteAssociationConfig{TeamID: "ABCD1234XY", BundleID: "com.example.ios"}

		test.NoError(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("empty config is valid", func(t *testing.T) {
		t.Parallel()

		cfg := &AppleAppSiteAssociationConfig{}

		test.NoError(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("nil config is valid", func(t *testing.T) {
		t.Parallel()

		var cfg *AppleAppSiteAssociationConfig

		test.NoError(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("returns error with missing bundle ID", func(t *testing.T) {
		t.Parallel()

		cfg := &AppleAppSiteAssociationConfig{TeamID: "ABCD1234XY"}

		test.Error(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("returns error with missing team ID", func(t *testing.T) {
		t.Parallel()

		cfg := &AppleAppSiteAssociationConfig{BundleID: "com.example.ios"}

		test.Error(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("returns error with malformed team ID", func(t *testing.T) {
		t.Parallel()

		cfg := &AppleAppSiteAssociationConfig{TeamID: "ABCD1234XY.com.example.ios", BundleID: "com.example.ios"}

		test.Error(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("returns error with malformed bundle ID", func(t *testing.T) {
		t.Parallel()

		cfg := &AppleAppSiteAssociationConfig{TeamID: "ABCD1234XY", BundleID: "com example ios"}

		test.Error(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("reports the malformed field by name, in the words a consumer reads", func(t *testing.T) {
		t.Parallel()

		// These strings reach whoever is reading a validation failure at
		// startup, so they are part of the interface and not an implementation
		// detail of how the rule is spelled.
		err := (&AppleAppSiteAssociationConfig{TeamID: "nope", BundleID: "com.example.ios"}).
			ValidateWithContext(t.Context())
		must.Error(t, err)
		test.EqOp(t, "teamID: must be ten alphanumeric characters.", err.Error())

		err = (&AppleAppSiteAssociationConfig{TeamID: "ABCD1234XY", BundleID: "com example ios"}).
			ValidateWithContext(t.Context())
		must.Error(t, err)
		test.EqOp(t, "bundleID: must be a bundle identifier.", err.Error())
	})

	T.Run("a period is an ordinary bundle ID character, not a segment separator", func(t *testing.T) {
		t.Parallel()

		// Apple restricts a bundle ID to alphanumerics, hyphens and periods, and
		// says nothing about how the periods are arranged. Reading them as
		// separators — and so rejecting an empty run between two of them — would
		// be this package inventing a rule Apple does not impose.
		for _, bundleID := range []string{"com..example", "com.example.", "c", "a-b.c-d"} {
			cfg := &AppleAppSiteAssociationConfig{TeamID: "ABCD1234XY", BundleID: bundleID}

			test.NoError(t, cfg.ValidateWithContext(t.Context()), test.Sprintf("bundle ID %q", bundleID))
			test.True(t, cfg.Enabled(), test.Sprintf("bundle ID %q", bundleID))
		}
	})

	T.Run("accepts paths and web credentials", func(t *testing.T) {
		t.Parallel()

		cfg := &AppleAppSiteAssociationConfig{
			TeamID:         "ABCD1234XY",
			BundleID:       "com.example.ios",
			Paths:          []string{"/invitations/*", "*"},
			WebCredentials: true,
		}

		test.NoError(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("returns error with paths but no identifiers", func(t *testing.T) {
		t.Parallel()

		cfg := &AppleAppSiteAssociationConfig{Paths: []string{"/invitations/*"}}

		test.Error(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("returns error with web credentials but no identifiers", func(t *testing.T) {
		t.Parallel()

		cfg := &AppleAppSiteAssociationConfig{WebCredentials: true}

		test.Error(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("returns error with a blank path", func(t *testing.T) {
		t.Parallel()

		cfg := &AppleAppSiteAssociationConfig{
			TeamID:   "ABCD1234XY",
			BundleID: "com.example.ios",
			Paths:    []string{"/invitations/*", ""},
		}

		test.Error(t, cfg.ValidateWithContext(t.Context()))
	})
}

func TestAppleAppSiteAssociationHandler(T *testing.T) {
	T.Parallel()

	T.Run("serves the association document", func(t *testing.T) {
		t.Parallel()

		handler := AppleAppSiteAssociationHandler(
			&AppleAppSiteAssociationConfig{TeamID: "ABCD1234XY", BundleID: "com.example.ios"},
			WithLogger(loggingnoop.NewLogger()),
			WithTracerProvider(tracingnoop.NewTracerProvider()),
		)

		req := httptest.NewRequest(http.MethodGet, AppleAppSiteAssociationPath, http.NoBody)
		res := httptest.NewRecorder()

		handler.ServeHTTP(res, req)

		test.EqOp(t, http.StatusOK, res.Code)
		test.StrContains(t, res.Header().Get("Content-Type"), "application/json")

		var doc appleAppSiteAssociation
		must.NoError(t, encoding.DecodeJSON(res.Body.Bytes(), &doc))

		must.SliceLen(t, 1, doc.AppLinks.Details)
		test.Eq(t, []string{"ABCD1234XY.com.example.ios"}, doc.AppLinks.Details[0].AppIDs)
		test.Eq(t, []appleAppLinkComponent{{Path: "*"}}, doc.AppLinks.Details[0].Components)
	})

	T.Run("renders the exact shape Apple expects", func(t *testing.T) {
		t.Parallel()

		handler := AppleAppSiteAssociationHandler(
			&AppleAppSiteAssociationConfig{TeamID: "ABCD1234XY", BundleID: "com.example.ios"},
			WithLogger(loggingnoop.NewLogger()),
			WithTracerProvider(tracingnoop.NewTracerProvider()),
		)

		req := httptest.NewRequest(http.MethodGet, AppleAppSiteAssociationPath, http.NoBody)
		res := httptest.NewRecorder()

		handler.ServeHTTP(res, req)

		test.EqOp(
			t,
			`{"applinks":{"details":[{"appIDs":["ABCD1234XY.com.example.ios"],"components":[{"/":"*"}]}]}}`,
			res.Body.String(),
		)
	})

	T.Run("scopes app links to the configured paths", func(t *testing.T) {
		t.Parallel()

		handler := AppleAppSiteAssociationHandler(
			&AppleAppSiteAssociationConfig{
				TeamID:   "ABCD1234XY",
				BundleID: "com.example.ios",
				Paths:    []string{"/invitations", "/invitations/*"},
			},
		)

		req := httptest.NewRequest(http.MethodGet, AppleAppSiteAssociationPath, http.NoBody)
		res := httptest.NewRecorder()

		handler.ServeHTTP(res, req)

		test.EqOp(t, http.StatusOK, res.Code)

		var doc appleAppSiteAssociation
		must.NoError(t, encoding.DecodeJSON(res.Body.Bytes(), &doc))

		must.SliceLen(t, 1, doc.AppLinks.Details)
		test.Eq(
			t,
			[]appleAppLinkComponent{{Path: "/invitations"}, {Path: "/invitations/*"}},
			doc.AppLinks.Details[0].Components,
		)
	})

	T.Run("claims web credentials when configured", func(t *testing.T) {
		t.Parallel()

		handler := AppleAppSiteAssociationHandler(
			&AppleAppSiteAssociationConfig{
				TeamID:         "ABCD1234XY",
				BundleID:       "com.example.ios",
				WebCredentials: true,
			},
		)

		req := httptest.NewRequest(http.MethodGet, AppleAppSiteAssociationPath, http.NoBody)
		res := httptest.NewRecorder()

		handler.ServeHTTP(res, req)

		test.EqOp(t, http.StatusOK, res.Code)

		var doc appleAppSiteAssociation
		must.NoError(t, encoding.DecodeJSON(res.Body.Bytes(), &doc))

		must.NotNil(t, doc.WebCredentials)
		test.Eq(t, []string{"ABCD1234XY.com.example.ios"}, doc.WebCredentials.Apps)
	})

	T.Run("omits web credentials by default", func(t *testing.T) {
		t.Parallel()

		handler := AppleAppSiteAssociationHandler(
			&AppleAppSiteAssociationConfig{TeamID: "ABCD1234XY", BundleID: "com.example.ios"},
		)

		req := httptest.NewRequest(http.MethodGet, AppleAppSiteAssociationPath, http.NoBody)
		res := httptest.NewRecorder()

		handler.ServeHTTP(res, req)

		// An empty webcredentials object would read to iOS as a claim with no apps, so the
		// key has to be absent rather than present-and-empty.
		test.StrNotContains(t, res.Body.String(), "webcredentials")
	})

	T.Run("renders the exact shape Apple expects with every service claimed", func(t *testing.T) {
		t.Parallel()

		handler := AppleAppSiteAssociationHandler(
			&AppleAppSiteAssociationConfig{
				TeamID:         "ABCD1234XY",
				BundleID:       "com.example.ios",
				Paths:          []string{"/invitations/*"},
				WebCredentials: true,
			},
		)

		req := httptest.NewRequest(http.MethodGet, AppleAppSiteAssociationPath, http.NoBody)
		res := httptest.NewRecorder()

		handler.ServeHTTP(res, req)

		test.EqOp(
			t,
			`{"webcredentials":{"apps":["ABCD1234XY.com.example.ios"]},`+
				`"applinks":{"details":[{"appIDs":["ABCD1234XY.com.example.ios"],"components":[{"/":"/invitations/*"}]}]}}`,
			res.Body.String(),
		)
	})

	T.Run("tolerates a nil logger and tracer provider", func(t *testing.T) {
		t.Parallel()

		handler := AppleAppSiteAssociationHandler(
			&AppleAppSiteAssociationConfig{TeamID: "ABCD1234XY", BundleID: "com.example.ios"},
		)

		req := httptest.NewRequest(http.MethodGet, AppleAppSiteAssociationPath, http.NoBody)
		res := httptest.NewRecorder()

		handler.ServeHTTP(res, req)

		test.EqOp(t, http.StatusOK, res.Code)
	})

	T.Run("returns 404 when disabled", func(t *testing.T) {
		t.Parallel()

		handler := AppleAppSiteAssociationHandler(&AppleAppSiteAssociationConfig{})

		req := httptest.NewRequest(http.MethodGet, AppleAppSiteAssociationPath, http.NoBody)
		res := httptest.NewRecorder()

		handler.ServeHTTP(res, req)

		test.EqOp(t, http.StatusNotFound, res.Code)
	})

	T.Run("returns 404 when malformed", func(t *testing.T) {
		t.Parallel()

		handler := AppleAppSiteAssociationHandler(
			&AppleAppSiteAssociationConfig{TeamID: "too-short", BundleID: "com.example.ios"},
		)

		req := httptest.NewRequest(http.MethodGet, AppleAppSiteAssociationPath, http.NoBody)
		res := httptest.NewRecorder()

		handler.ServeHTTP(res, req)

		test.EqOp(t, http.StatusNotFound, res.Code)
	})

	T.Run("returns 404 when nil", func(t *testing.T) {
		t.Parallel()

		handler := AppleAppSiteAssociationHandler(nil)

		req := httptest.NewRequest(http.MethodGet, AppleAppSiteAssociationPath, http.NoBody)
		res := httptest.NewRecorder()

		handler.ServeHTTP(res, req)

		test.EqOp(t, http.StatusNotFound, res.Code)
	})
}

func TestNewHTTPServer_appleAppSiteAssociation(T *testing.T) {
	T.Parallel()

	T.Run("serves the file when configured", func(t *testing.T) {
		t.Parallel()

		router := testRouter(t)

		_, err := NewHTTPServer(
			t.Context(),
			&Config{
				Port: 8080,
				AppleAppSiteAssociation: &AppleAppSiteAssociationConfig{
					TeamID:   "ABCD1234XY",
					BundleID: "com.example.ios",
				},
			},
			router,
			WithServiceName(t.Name()),
		)
		must.NoError(t, err)

		req := httptest.NewRequest(http.MethodGet, AppleAppSiteAssociationPath, http.NoBody)
		res := httptest.NewRecorder()

		router.Handler().ServeHTTP(res, req)

		test.EqOp(t, http.StatusOK, res.Code)
		test.StrContains(t, res.Body.String(), "ABCD1234XY.com.example.ios")
	})

	T.Run("does not serve the file when unconfigured", func(t *testing.T) {
		t.Parallel()

		router := testRouter(t)

		_, err := NewHTTPServer(t.Context(), &Config{Port: 8080}, router, WithServiceName(t.Name()))
		must.NoError(t, err)

		req := httptest.NewRequest(http.MethodGet, AppleAppSiteAssociationPath, http.NoBody)
		res := httptest.NewRecorder()

		router.Handler().ServeHTTP(res, req)

		test.EqOp(t, http.StatusNotFound, res.Code)
	})

	// A half-filled Apple config is a mistake, and now that NewHTTPServer
	// validates, it is caught at construction rather than serving a file Apple
	// will reject.
	T.Run("rejects a partially configured apple app site association", func(t *testing.T) {
		t.Parallel()

		router := testRouter(t)

		_, err := NewHTTPServer(
			t.Context(),
			&Config{
				Port:                    8080,
				AppleAppSiteAssociation: &AppleAppSiteAssociationConfig{TeamID: "ABCD1234XY"},
			},
			router,
			WithServiceName(t.Name()),
		)
		test.Error(t, err)
	})
}
