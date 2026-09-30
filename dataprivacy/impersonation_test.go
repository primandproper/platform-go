package dataprivacy

import (
	"context"
	"testing"

	"github.com/primandproper/platform-go/v14/audit"
	auditmigrations "github.com/primandproper/platform-go/v14/audit/migrations"
	"github.com/primandproper/platform-go/v14/callers"

	"github.com/primandproper/primitives-go/v2/filtering"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// operatorPrincipal is an operator signed in as a customer: the request is the
// customer's, and the operator is who made it.
type operatorPrincipal struct {
	subject, operator string
}

var _ callers.Delegated = (*operatorPrincipal)(nil)

func (p *operatorPrincipal) UserID() string          { return p.subject }
func (p *operatorPrincipal) Scope() tenancy.Scope    { return testScope }
func (p *operatorPrincipal) ActiveAccountID() string { return "" }
func (p *operatorPrincipal) ActorID() string         { return p.operator }

type principalKey struct{}

// fromContext is the consumer's extractor: whatever their interceptor put on
// the request.
func fromContext(ctx context.Context) (callers.Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(callers.Principal)

	return p, ok
}

func asOperator(ctx context.Context) context.Context {
	return context.WithValue(ctx, principalKey{}, &operatorPrincipal{subject: "customer_1", operator: "operator_1"})
}

func TestPrincipalActorResolver(T *testing.T) {
	T.Parallel()

	resolve := PrincipalActorResolver(fromContext)

	T.Run("files a delegated request under its user and names the operator", func(t *testing.T) {
		t.Parallel()

		test.Eq(t, audit.Actor{ID: "customer_1", Type: audit.ActorUser, Impersonator: "operator_1"},
			resolve(asOperator(t.Context())))
	})

	T.Run("attributes a request with nobody on it to the system", func(t *testing.T) {
		t.Parallel()

		test.Eq(t, audit.Actor{ID: serviceName, Type: audit.ActorSystem}, resolve(t.Context()))
		test.Eq(t, audit.Actor{ID: serviceName, Type: audit.ActorSystem}, PrincipalActorResolver(nil)(t.Context()))
	})
}

// TestService_ImpersonatedSubmission is an impersonated write through a
// platform surface, end to end: the operator submits an export while signed in
// as a customer, and the audit log it lands in — the real recorder, on the same
// database — files it under the customer and names the operator.
func TestService_ImpersonatedSubmission(T *testing.T) {
	T.Parallel()

	env := newSQLiteEnv(T)
	store := env.newStore(T)

	stmts, err := auditmigrations.Statements(env.dialect, audit.DefaultTablePrefix)
	must.NoError(T, err)

	for _, stmt := range stmts {
		_, execErr := env.client.Writer().ExecContext(T.Context(), stmt)
		must.NoError(T, execErr, must.Sprintf("executing %q", stmt))
	}

	recorder, err := audit.NewRecorder(env.dialect)
	must.NoError(T, err)

	reader, err := audit.NewReader(env.dialect)
	must.NoError(T, err)

	svc, err := NewService(T.Context(), &ServiceConfig{}, env.client, store, newStubOperations(),
		WithServiceClock(newStubClock()),
		WithServiceAuditRecorder(recorder),
		WithActorResolver(PrincipalActorResolver(fromContext)),
	)
	must.NoError(T, err)

	req, err := svc.Submit(asOperator(T.Context()), testScope, testSubject, RequestExport)
	must.NoError(T, err)

	scope := testScope
	page, err := reader.List(T.Context(), env.client.Reader(), &audit.Query{Scope: &scope, ResourceID: req.ID},
		filtering.DefaultQueryFilter())
	must.NoError(T, err)
	must.SliceLen(T, 1, page.Data)

	entry := page.Data[0]
	test.EqOp(T, "customer_1", entry.Actor.ID, test.Sprint("the request is the customer's"))
	test.EqOp(T, "operator_1", entry.Actor.Impersonator, test.Sprint("and the operator made it"))

	byOperator, err := reader.List(T.Context(), env.client.Reader(), &audit.Query{Scope: &scope, ImpersonatorID: "operator_1"},
		filtering.DefaultQueryFilter())
	must.NoError(T, err)
	must.SliceLen(T, 1, byOperator.Data)
	test.EqOp(T, entry.ID, byOperator.Data[0].ID)
}

func TestFulfiller_ImpersonatedActor(T *testing.T) {
	T.Parallel()

	recorder := newRecordingAudit()

	// The worker has no request behind it; this resolver answers as though an
	// operator's context had reached it, which is what a deployment driving
	// the fulfiller from an operator tool would hand it.
	always := func(context.Context) (callers.Principal, bool) {
		return &operatorPrincipal{subject: "customer_1", operator: "operator_1"}, true
	}

	env := newFulfillerEnv(T, func(r *Registry) {
		must.NoError(T, r.RegisterEraser("identity", countingEraser(1, 0, nil, nil)))
	},
		WithFulfillerAuditRecorder(recorder),
		WithFulfillerActorResolver(PrincipalActorResolver(always)),
	)

	env.submitAndRun(T, RequestErasure)

	entry := recorder.last()
	must.NotNil(T, entry)
	test.EqOp(T, "customer_1", entry.Actor.ID)
	test.EqOp(T, "operator_1", entry.Actor.Impersonator)
}
