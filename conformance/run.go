package conformance

import (
	"context"
	"testing"

	"github.com/primandproper/primitives-go/v2/database/dialect"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
)

// ErrSubjectUnsupported is what a subject factory returns for a caller it
// cannot mint — an administrative one where the subject has no notion of a
// service role, or a second member of a tenant it cannot add one to.
//
// It is a decline rather than a failure, and the assertions that asked for that
// caller skip with the reason printed. A factory that returns any other error
// has failed, and the suite reports it as one: the distinction is between "this
// deployment does not have that idea" and "this deployment is broken".
var ErrSubjectUnsupported = platformerrors.New("conformance subject cannot mint a caller of that kind")

// Suite is one surface's behavioral assertions, and what it needs to make
// them.
//
// A suite is a value rather than a registration, so linking one is an import a
// consumer writes rather than an init this package performs. It is the same
// argument errormappers makes for having no init of its own: a thing that
// installs itself by being linked in is a side effect nobody can opt out of.
type Suite struct {

	// Mounted reports whether the subject mounted this surface, by reading the
	// one field of Surfaces the suite calls through.
	Mounted func(Surfaces) bool

	// Run holds the assertions.
	Run func(t *testing.T, s *Session)
	// Name is the surface, as it appears in a subtest and in a skip.
	Name string
}

// Session is what a suite's assertions are handed: the seams, resolved, plus
// the few things every suite does the same way.
type Session struct {
	seams Seams
}

// Seams returns what the subject supplied.
func (s *Session) Seams() Seams { return s.seams }

// Dialect is the subject's database, or the zero dialect where it did not say.
func (s *Session) Dialect() dialect.Dialect { return s.seams.Dialect }

// Subject mints a caller, failing the test if the subject factory errors, and
// skipping it if the factory declines.
//
// The skip is the load-bearing half. A deployment with no administrative role
// is not a deployment that fails this suite; it is one that does not have the
// idea the assertion was about, and a suite that could not tell the two apart
// would push consumers towards stubbing a seam to make a red go away.
func (s *Session) Subject(t *testing.T, opts ...SubjectOption) *Subject {
	t.Helper()

	return s.subject(t, t.Context(), opts...)
}

// SubjectWithContext is Subject against a caller-supplied context, for the
// assertions that mint inside a deadline of their own.
func (s *Session) SubjectWithContext(t *testing.T, ctx context.Context, opts ...SubjectOption) *Subject {
	t.Helper()

	return s.subject(t, ctx, opts...)
}

func (s *Session) subject(t *testing.T, ctx context.Context, opts ...SubjectOption) *Subject {
	t.Helper()

	if s.seams.NewSubject == nil {
		t.Fatal("conformance: Seams.NewSubject is required")
	}

	subject, err := s.seams.NewSubject(ctx, opts...)
	switch {
	case platformerrors.Is(err, ErrSubjectUnsupported):
		req := NewSubjectRequest(opts...)
		t.Skipf("conformance: the subject cannot mint this caller (admin=%t, tenant named=%t); skipping",
			req.Admin, req.Scope != nil)

		return nil
	case err != nil:
		t.Fatalf("conformance: minting a caller: %v", err)

		return nil
	case subject == nil:
		t.Fatal("conformance: the subject factory returned no caller and no error")

		return nil
	}

	return subject
}

// NeedsAction skips the test unless the subject can bring about the state it
// needs, printing what was missing.
//
// The skip names the action rather than the row, because that is what a subject
// implements: a consumer reading this skip should be able to go and write the
// function it names, not go looking for a table.
func (s *Session) NeedsAction(t *testing.T, present bool, what string) {
	t.Helper()

	if !present {
		t.Skipf("conformance: this subject supplies no %s action, and no client can bring that state about on its own", what)
	}
}

// NeedsExclusiveDatabase skips the test unless the suite is the only writer.
func (s *Session) NeedsExclusiveDatabase(t *testing.T, what string) {
	t.Helper()

	if !s.seams.ExclusiveDatabase {
		t.Skipf("conformance: %s can only be asserted against a database this run owns, and this subject did not claim one", what)
	}
}

// NeedsControlledTime skips the test unless the subject's clock can be moved.
func (s *Session) NeedsControlledTime(t *testing.T, what string) {
	t.Helper()

	if !s.seams.ControlledTime {
		t.Skipf("conformance: %s needs a clock this run can move, and a deployed service has none", what)
	}
}

// Run asserts every suite the subject mounted a surface for.
//
// A suite whose surface is absent is skipped and said so, which is the
// difference between a consumer who did not wire something and a consumer whose
// wiring is wrong. Suites run as parallel subtests: each mints its own callers
// in tenants of their own, so they share a database without sharing rows.
//
//nolint:gocritic // hugeParam: Seams is taken by value so a subject cannot change what a run holds after handing it over
func Run(t *testing.T, seams Seams, suites ...Suite) {
	t.Helper()

	if seams.NewSubject == nil {
		t.Fatal("conformance: Seams.NewSubject is required")
	}

	if len(suites) == 0 {
		t.Fatal("conformance: no suites were named; import conformance/all to run every one")
	}

	session := &Session{seams: seams}

	probe, err := seams.NewSubject(t.Context())
	if err != nil {
		t.Fatalf("conformance: minting the caller the mounted surfaces are read from: %v", err)
	}

	if probe == nil {
		t.Fatal("conformance: the subject factory returned no caller and no error")
	}

	for i := range suites {
		suite := &suites[i]

		t.Run(suite.Name, func(t *testing.T) {
			t.Parallel()

			if suite.Mounted != nil && !suite.Mounted(probe.Surfaces) {
				t.Skipf("conformance: this subject mounts no %s surface", suite.Name)
			}

			suite.Run(t, session)
		})
	}
}
