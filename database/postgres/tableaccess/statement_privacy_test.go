package tableaccess

import (
	"context"
	"database/sql"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/primandproper/platform-go/v13/testutils/containers/pgtest"

	"github.com/XSAM/otelsql"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// spanRecorder keeps every span it is exported, so a test can read back the
// attributes otelsql actually emitted rather than reasoning about what it would
// have emitted.
type spanRecorder struct {
	spans []sdktrace.ReadOnlySpan
	mu    sync.Mutex
}

func (r *spanRecorder) ExportSpans(_ context.Context, spans []sdktrace.ReadOnlySpan) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.spans = append(r.spans, spans...)

	return nil
}

func (r *spanRecorder) Shutdown(context.Context) error { return nil }

// stringValues returns every string-valued attribute across every recorded span.
// Which key statement text lands under depends on the semantic-convention opt-in
// otelsql was built with, so this looks at all of them rather than naming one.
func (r *spanRecorder) stringValues() []string {
	r.mu.Lock()
	defer r.mu.Unlock()

	var values []string

	for _, span := range r.spans {
		attrs := span.Attributes()
		for i := range attrs {
			values = append(values, attrs[i].Value.String())
		}
	}

	return values
}

// TestManager_CreateUser_keepsPasswordOffSpans runs CreateUser under the
// configuration that makes the leak possible — otelsql recording statement text,
// which is what LOG_QUERIES=true turns on — and asserts the password is in none
// of the attributes that came out. A fix that only holds while query logging is
// off would not be a fix, so the test refuses to run with it off.
func TestManager_CreateUser_keepsPasswordOffSpans(T *testing.T) {
	T.Parallel()

	T.Run("password reaches no span attribute", func(t *testing.T) {
		t.Parallel()

		runWithTestPostgres(t, func(ctx context.Context, pg *pgtest.Instance) {
			// secret is the part of the password that survives any quoting scheme
			// unaltered: SQL escaping doubles quotes and backslashes, so a password
			// made only of those would not appear verbatim in statement text even
			// when it had plainly been interpolated into it. Searching for a run of
			// characters that nothing escapes is what makes a miss meaningful. The
			// quoting of the awkward characters is the round-trip test's job.
			const (
				username = "spanprivacyuser"
				secret   = "un1qu3-sp4n-s3cr3t"
				password = secret + `'"\%`
			)

			recorder := new(spanRecorder)
			tracerProvider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(recorder))
			t.Cleanup(func() { _ = tracerProvider.Shutdown(context.WithoutCancel(ctx)) })

			db, err := otelsql.Open(pgtest.DriverName, pg.ConnectionString, otelsql.WithTracerProvider(tracerProvider))
			must.NoError(t, err)
			t.Cleanup(func() { _ = db.Close() })

			must.NoError(t, NewManager(db).CreateUser(ctx, username, password))

			values := recorder.stringValues()
			must.SliceNotEmpty(t, values)

			// Statement text really is being recorded, so the absence below is the
			// statements not carrying the password rather than nothing being watched.
			test.True(t, strings.Contains(strings.Join(values, "\n"), "CREATE USER"),
				test.Sprintf("no span carried statement text; the test is watching nothing"))

			for _, value := range values {
				test.StrNotContains(t, value, secret,
					test.Sprintf("span attribute carried the password: %q", value))
			}
		})
	})
}

// TestManager_CreateUser_passwordRoundTrips is the other half of the same
// property. Keeping the password out of statement text is only worth anything if
// the role can still be authenticated with it afterwards — server-side quoting
// that mangled the credential would leave CreateUser reporting success and the
// password silently wrong, which no assertion on the CREATE USER statement itself
// would catch. So this one logs in.
func TestManager_CreateUser_passwordRoundTrips(T *testing.T) {
	T.Parallel()

	T.Run("awkward password authenticates", func(t *testing.T) {
		t.Parallel()

		runWithTestPostgres(t, func(ctx context.Context, pg *pgtest.Instance) {
			const (
				username = "roundtripuser"
				password = `p'a\ss"w%rd`
			)

			must.NoError(t, NewManager(pg.DB).CreateUser(ctx, username, password))

			// Build the DSN through net/url so the awkward characters are
			// percent-encoded on the way in; what is being tested is the server's
			// copy of the password, not this test's ability to spell a URL.
			dsn, err := url.Parse(pg.ConnectionString)
			must.NoError(t, err)
			dsn.User = url.UserPassword(username, password)

			db, err := sql.Open(pgtest.DriverName, dsn.String())
			must.NoError(t, err)
			t.Cleanup(func() { _ = db.Close() })

			must.NoError(t, db.PingContext(ctx))
		})
	})
}
