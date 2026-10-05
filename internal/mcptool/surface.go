package mcptool

import (
	"context"
	"errors"

	"github.com/primandproper/platform-go/v14/callers"
	"github.com/primandproper/platform-go/v14/internal/archivegate"

	"github.com/primandproper/primitives-go/v2/authorization"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/filtering"
	"github.com/primandproper/primitives-go/v2/observability"
	"github.com/primandproper/primitives-go/v2/observability/logging"
	"github.com/primandproper/primitives-go/v2/observability/metrics"
	"github.com/primandproper/primitives-go/v2/observability/tracing"
	"github.com/primandproper/primitives-go/v2/tenancy"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// Authenticator turns one tool call into the context a consumer's
// callers.PrincipalExtractor and authorization.GrantsExtractor read: it is
// the part of a consumer's authentication interceptor that a tool call has no
// interceptor to run.
//
// It is asked per call, with the call's request, because the context the MCP
// SDK hands a tool handler is not the request's. Over streamable HTTP it is
// the session's, detached from whichever request opened the session, so a
// principal a consumer's HTTP middleware put on a request context is either
// absent there or, worse, the first request's caller answering for every call
// that session makes. The verified token arrives on each call as
// req.Extra.TokenInfo — primitives-go's authentication/oauth2server/mcp puts
// it there, and reads it back with AccessTokenFrom — so that is what an
// Authenticator reads.
//
// A call carrying no credential is answered by returning ctx unchanged: the
// extractor then finds nobody, and the call is refused as unauthenticated. An
// error is a failure to decide — a directory that would not answer — and is
// logged and answered as a failed call rather than as a refusal.
type Authenticator = func(ctx context.Context, req *sdkmcp.CallToolRequest) (context.Context, error)

// NoGrant is the permission of a tool every caller may call — one whose gRPC
// counterpart is public. Naming it is the decision; a tool cannot reach
// [Surface.Begin] without naming some permission.
const NoGrant authorization.Permission = ""

var (
	// ErrNilAuthenticator is a tool surface built with no way to read a call's
	// credential. See [Authenticator] for why the handler's own context is not
	// one.
	ErrNilAuthenticator = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil authenticator for an MCP tool surface")

	// ErrNilPrincipalExtractor is a tool surface built with no way to tell who
	// is calling.
	ErrNilPrincipalExtractor = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil principal extractor for an MCP tool surface")

	// ErrNilGrantsExtractor is a tool surface built with no way to tell what the
	// caller may do. There is no default: one granting nothing refuses every
	// call and reads as a broken deployment, and one granting everything hands
	// the triage queue to anybody holding a token.
	ErrNilGrantsExtractor = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil grants extractor for an MCP tool surface")

	// ErrNoPrincipal is a tool call that arrived with no caller on its context.
	ErrNoPrincipal = platformerrors.New("no principal on the MCP tool call context")

	// ErrPermissionDenied is a tool call by a caller who does not hold the
	// permission the tool's gRPC counterpart requires.
	ErrPermissionDenied = platformerrors.New("the caller does not hold the permission this tool requires")

	// ErrToolFailed is what a model is told in place of a failure whose own text
	// was not written for it. The cause is on the call's log line and span.
	ErrToolFailed = platformerrors.New("the tool failed; the server's log has the cause")
)

// Surface is one package's set of tools: who is calling, what they may do,
// which failures may be repeated to a model, and the observability every call
// records on.
type Surface struct {
	o11y         observability.Observer
	authenticate Authenticator
	principals   callers.PrincipalExtractor
	grants       authorization.GrantsExtractor
	instruments  *metrics.OperationSet
	toolKey      string
	scopeKey     string
	userIDKey    string
	safe         []error
}

// NewSurface builds a tool surface named name — the instrument prefix and the
// span scope, as a gRPC surface's serverName is.
//
// safe are the sentinels whose own text a model may be told: a surface passes
// its package's ClientSafeSentinels, or the not-found refusal its reads answer
// with. Anything else a call fails with reaches the model as [ErrToolFailed].
func NewSurface(
	name string,
	authenticate Authenticator,
	principals callers.PrincipalExtractor,
	grants authorization.GrantsExtractor,
	safe []error,
	logger logging.Logger,
	tracerProvider tracing.Provider,
	metricsProvider metrics.Provider,
) (*Surface, error) {
	if authenticate == nil {
		return nil, ErrNilAuthenticator
	}

	if principals == nil {
		return nil, ErrNilPrincipalExtractor
	}

	if grants == nil {
		return nil, ErrNilGrantsExtractor
	}

	instruments, err := metrics.NewOperationSet(metricsProvider, name)
	if err != nil {
		return nil, platformerrors.Wrapf(err, "creating %s instruments", name)
	}

	return &Surface{
		authenticate: authenticate,
		principals:   principals,
		grants:       grants,
		o11y:         observability.NewObserver(name, logger, tracerProvider),
		instruments:  instruments,
		safe:         safe,
		toolKey:      name + ".tool",
		scopeKey:     name + ".scope",
		userIDKey:    name + ".user_id",
	}, nil
}

// Grants is the extractor the surface checks permissions with, for a tool
// that has a second decision to make off the same authority.
func (s *Surface) Grants() authorization.GrantsExtractor { return s.grants }

// Call is one tool invocation that has been let through: who is calling, the
// tenant their reads are confined to, and the operation to record on.
type Call struct {
	Op        observability.Operation
	Principal callers.Principal

	surface *Surface
	done    func(error)
	Scope   tenancy.Scope
}

// Begin authenticates the call, resolves the caller and checks grant, in the
// one place every tool starts.
//
// The scope comes off the principal and never off the tool's arguments: a
// tenant a model could name would be a cross-tenant read hiding behind an
// argument.
//
// A refusal is returned ready to hand back to the SDK, and has already been
// recorded; a call that was let through must be finished with [Call.End].
func (s *Surface) Begin(
	ctx context.Context,
	req *sdkmcp.CallToolRequest,
	tool string,
	grant authorization.Permission,
) (context.Context, *Call, error) {
	ctx, op := s.o11y.Begin(ctx)

	attr := metric.WithAttributes(attribute.String(s.toolKey, tool))
	s.instruments.Attempt(ctx, attr)

	stop := op.Time(ctx, nil, s.instruments.Latency, attr)
	op.Set(s.toolKey, tool)

	call := &Call{Op: op, surface: s}
	call.done = func(err error) {
		if err != nil {
			s.instruments.Failed(ctx, attr)
		}

		stop()
		op.End()
	}

	ctx, err := s.authenticate(ctx, req)
	if err != nil {
		return ctx, nil, call.End(platformerrors.Wrap(err, "authenticating an MCP tool call"))
	}

	principal, ok := s.principals(ctx)
	if !ok || principal == nil {
		return ctx, nil, call.End(Refuse(ErrNoPrincipal))
	}

	call.Principal = principal
	call.Scope = principal.Scope()

	op.Set(s.scopeKey, call.Scope.String())
	op.Set(s.userIDKey, principal.UserID())

	if grant != NoGrant {
		grants, found := s.grants(ctx)
		if !found || !grants.Has(grant) {
			return ctx, nil, call.End(Refuse(ErrPermissionDenied))
		}
	}

	return ctx, call, nil
}

// Filter normalizes a paged read's filter and narrows include_archived to a
// caller holding archiveGrant, through the same door every gRPC surface's
// paged read passes. A sort direction the filter does not recognize is refused
// in its own words, because it is the model's argument that was wrong.
func (c *Call) Filter(
	ctx context.Context,
	in *filtering.QueryFilter,
	archiveGrant authorization.Permission,
	clearedKey string,
) (*filtering.QueryFilter, error) {
	if err := in.Normalize(); err != nil {
		return nil, Refuse(err)
	}

	return archivegate.Narrow(ctx, c.Op, in, c.surface.grants, archiveGrant, clearedKey), nil
}

// End finishes the call and returns what the model is told: nil for a call that
// succeeded, a refusal in its own words, the safe sentinel err matches, or
// [ErrToolFailed] with the cause logged.
func (c *Call) End(err error) error {
	defer c.done(err)

	if err == nil {
		return nil
	}

	if r, ok := errors.AsType[*refusal](err); ok {
		c.Op.Acknowledge(r.err, "refusing an MCP tool call")

		return r.err
	}

	for _, sentinel := range c.surface.safe {
		if errors.Is(err, sentinel) {
			c.Op.Acknowledge(err, "refusing an MCP tool call")

			return sentinel
		}
	}

	c.Op.Acknowledge(err, "serving an MCP tool call")

	return ErrToolFailed
}

// Refuse marks err as a refusal whose own text a model may be told: the
// caller's standing, or an argument the model supplied.
func Refuse(err error) error { return &refusal{err: err} }

type refusal struct{ err error }

func (r *refusal) Error() string { return r.err.Error() }
func (r *refusal) Unwrap() error { return r.err }
