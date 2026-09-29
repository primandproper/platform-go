package conformance

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/primandproper/primitives-go/v2/database/dialect"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/tenancy"
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
	// mounted is the surfaces the subject mounts, as the caller Run probed
	// with reports them: what a caller built from a connection alone is
	// rebuilt over.
	mounted Surfaces
	seams   Seams
}

// Seams returns what the subject supplied.
func (s *Session) Seams() Seams { return s.seams }

// Dialect is the subject's database, or the zero dialect where it did not say.
func (s *Session) Dialect() dialect.Dialect { return s.seams.Dialect }

// Subject mints a caller, failing the test if the subject factory errors, and
// skipping it if the factory declines.
//
// The caller names what it goes on to make with Making, and that decides who it
// is. Where the subject reserves any of those calls in Seams.OperatorMethods it
// is an administrator, since nobody else may make them; where it reserves none
// it is an ordinary caller, so a deployment that lets its members make a call
// has that promise asserted rather than stepped around. AsAdmin asks for an
// administrator regardless, for the assertions about what administrative
// standing buys, and AsMember for an ordinary caller regardless, skipping
// where the subject reserves a call it names.
//
// Attempting is the one way to put a reserved call in an ordinary caller's
// hands: it mints a member to attempt calls it expects to be refused, for the
// assertion that the deployment refuses them.
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

	req := NewSubjectRequest(opts...)
	if req.member && req.Admin {
		t.Fatal("conformance: a caller was asked for as both a member and an administrator")
	}

	reserved := s.reservedAmong(slices.DeleteFunc(slices.Clone(req.Methods), func(m string) bool {
		return slices.Contains(req.attempting, m)
	}))

	switch {
	case reserved != "" && req.member:
		t.Skipf("conformance: this subject reserves %s to an operator, so no ordinary member makes it and nothing is promised to one about it; skipping", reserved)

		return nil
	case reserved != "":
		opts = append(slices.Clone(opts), AsAdmin())
	}

	subject, err := s.seams.NewSubject(ctx, opts...)
	switch {
	case platformerrors.Is(err, ErrSubjectUnsupported) && reserved != "":
		t.Skipf("conformance: this subject reserves %s to an operator and mints no administrator to make it; skipping", reserved)

		return nil
	case platformerrors.Is(err, ErrSubjectUnsupported):
		t.Skipf("conformance: the subject cannot mint this caller (admin=%t, tenant named=%t, surface=%q); skipping",
			req.Admin, req.Scope != nil, req.Surface)

		return nil
	case err != nil:
		t.Fatalf("conformance: minting a caller: %v", err)

		return nil
	case subject == nil:
		t.Fatal("conformance: the subject factory returned no caller and no error")

		return nil
	}

	return declare(t, subject, req.Methods)
}

// TwoTenants mints two callers in tenants of their own on surface, for an
// assertion that one cannot see what the other did there.
//
// It refuses to proceed if the subject put them in one tenant, and the refusal
// comes in two strengths. Two callers who share a scope other than the global
// one are a factory that ignored its request and handed back one tenant twice,
// which would make every confinement assertion compare a tenant with itself
// and pass — that fails. Two callers who share the global scope are a
// deployment that serves this surface from one directory, as a consumer may
// serve its catalog or its settings; there is no confinement there to observe,
// and the assertion skips with that said rather than failing a deployment for
// a promise it never made.
//
// opts are applied to both, and name with Making the calls each goes on to
// make.
func (s *Session) TwoTenants(t *testing.T, surface string, opts ...SubjectOption) (mine, theirs *Subject) {
	t.Helper()

	mine, theirs = s.Subject(t, opts...), s.Subject(t, opts...)

	switch separation(mine.ScopeFor(surface), theirs.ScopeFor(surface)) {
	case separate:
		return mine, theirs
	case sharedGlobal:
		t.Skipf("conformance: this subject serves %s from the global scope, so no caller's rows there are confined from another's", surface)
	case sharedTenant:
		t.Fatalf("conformance: the subject minted two callers in one %s tenant; the confinement this asserts cannot be observed", surface)
	}

	return nil, nil
}

// apartness is what two callers' scopes on one surface say about asserting a
// confinement between them.
type apartness int

const (
	separate apartness = iota
	sharedGlobal
	sharedTenant
)

func separation(ours, others tenancy.Scope) apartness {
	switch {
	case ours != others:
		return separate
	case ours.IsGlobal():
		return sharedGlobal
	default:
		return sharedTenant
	}
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

// DefaultFulfillmentBudget is how long Session.Await waits where the subject
// named no Seams.FulfillmentBudget: long enough for a worker that polls its
// queue every few seconds to pick work up and finish it several times over.
const DefaultFulfillmentBudget = 2 * time.Minute

const (
	// awaitInterval is how often Await asks again.
	awaitInterval = 250 * time.Millisecond

	// awaitGrace is how far short of the test binary's own deadline Await
	// gives up, so a wait that runs out fails its test by name rather than
	// panicking the whole binary with every other test's goroutines.
	awaitGrace = 5 * time.Second
)

// errBudgetSpent is what poll reports when the deadline came first.
var errBudgetSpent = platformerrors.New("the fulfillment budget was spent first")

// Await asks probe until it reports done, failing the test naming what it was
// waiting for if probe errs or the subject's fulfillment budget runs out first.
//
// It is for the assertions about work the deployment finishes after answering
// — a privacy request fulfilled by a worker, say — and it waits on whatever a
// client can see rather than on the machinery: probe reads the row the caller
// would read, so work that finished without moving it is a timeout here, which
// is the bug it is. The budget is Seams.FulfillmentBudget, or
// DefaultFulfillmentBudget, and never runs past the test's own deadline.
func (s *Session) Await(t *testing.T, what string, probe func() (done bool, err error)) {
	t.Helper()

	testDeadline, bounded := t.Deadline()
	deadline := awaitDeadline(time.Now(), s.seams.FulfillmentBudget, testDeadline, bounded)

	if err := poll(t.Context(), deadline, awaitInterval, probe); err != nil {
		t.Fatalf("conformance: waiting for %s: %v", what, err)
	}
}

// awaitDeadline is when a wait begun at now gives up: the budget from now, or
// awaitGrace short of the test's deadline where that comes first.
func awaitDeadline(now time.Time, budget time.Duration, testDeadline time.Time, bounded bool) time.Time {
	if budget <= 0 {
		budget = DefaultFulfillmentBudget
	}

	deadline := now.Add(budget)
	if bounded && testDeadline.Add(-awaitGrace).Before(deadline) {
		deadline = testDeadline.Add(-awaitGrace)
	}

	return deadline
}

// poll asks probe every interval until it is done, it errs, ctx ends, or the
// deadline passes. probe is always asked at least once, so a deadline already
// behind it still reads the state once rather than failing unseen.
func poll(ctx context.Context, deadline time.Time, interval time.Duration, probe func() (bool, error)) error {
	started := time.Now()

	for {
		done, err := probe()
		if err != nil {
			return err
		}

		if done {
			return nil
		}

		if !time.Now().Before(deadline) {
			return platformerrors.Wrapf(errBudgetSpent, "after %s", time.Since(started).Round(time.Millisecond))
		}

		timer := time.NewTimer(min(interval, time.Until(deadline)))

		select {
		case <-ctx.Done():
			timer.Stop()

			return ctx.Err()
		case <-timer.C:
		}
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

	checkOperatorMethods(t, seams.OperatorMethods)
	checkOperatorRoutes(t, seams.OperatorRoutes)
	checkRoles(t, &seams.Roles)

	probe, err := seams.NewSubject(t.Context())
	if err != nil {
		t.Fatalf("conformance: minting the caller the mounted surfaces are read from: %v", err)
	}

	if probe == nil {
		t.Fatal("conformance: the subject factory returned no caller and no error")
	}

	session := &Session{seams: seams, mounted: probe.Surfaces}

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
