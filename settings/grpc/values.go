package grpc

import (
	"context"
	"errors"

	"github.com/primandproper/platform-go/v14/settings"
	"github.com/primandproper/platform-go/v14/settings/settingspb"

	"github.com/primandproper/primitives-go/database"
	grpcerrors "github.com/primandproper/primitives-go/errors/grpc"
	filteringgrpc "github.com/primandproper/primitives-go/filtering/grpc"

	"google.golang.org/grpc/codes"
)

// The subject half of the surface: six RPCs about what one person answered and
// what a setting resolves to for them, each behind a permission and each gated
// by the [SubjectAuthorizer].
//
// The gate is the difference between this file and the catalog next door. Every
// one of these RPCs takes a settings.Subject out of the request, and a grant on
// the method says only that this caller may make this kind of call — so the
// authorizer is asked after the subject has been read and before anything
// reads or writes a row. A refusal is codes.PermissionDenied and discloses
// nothing: it is decided before any statement runs, so a subject who has never
// answered anything is refused by exactly the rule one who belongs to somebody
// else is.
//
// Two of the six write, and both open their own transaction and read the
// resolution back inside it. That is the property settings.Store's reads taking
// a database.SQLQueryExecutor rather than a reader exists for, and it is the
// package's own worked example: a service that saves somebody's preference and
// answers with the new effective value is resolving a row it has written and
// not yet committed, and on a connection of its own it would answer with what
// the subject had before the request.

// SetValue stores a subject's answer and answers with what the setting now
// resolves to for them.
//
// The definition is read first, inside the transaction that will hold the
// write, and that read is what turns the request's typed value into the text
// the store stores. It is also what makes a wrong case a refusal: an int_value
// written to a text setting would otherwise be stored as "1" with nothing
// complaining, because settings.KindString admits any text. See
// rawFromTypedValue.
//
// The store reads the definition again for its own check, which is a read
// rather than a second decision — the rule that a value is of its definition's
// kind and in its enumeration is the store's, and it must not become a rule
// this transport is trusted to have applied.
func (s *Server) SetValue(
	ctx context.Context,
	request *settingspb.SetValueRequest,
) (*settingspb.SetValueResponse, error) {
	ctx, req, done, err := s.caller(ctx, settingspb.SettingsService_SetValue_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	subject := subjectFromProto(request.GetSubject())
	name := request.GetName()

	req.setSubject(subject)
	req.op.Set(definitionKey, name)

	if err = s.authorizeSubject(ctx, req, subject); err != nil {
		return nil, err
	}

	var resolution *settings.Resolution

	if err = s.client.WithTransaction(ctx, func(tx database.Tx) error {
		definition, readErr := s.store.GetDefinitionByName(ctx, tx, req.scope, name)
		if readErr != nil {
			return readErr
		}

		raw, rawErr := rawFromTypedValue(definition.Kind, request.GetValue())
		if rawErr != nil {
			return rawErr
		}

		if _, writeErr := s.store.SetValue(ctx, tx, req.scope, subject, name, raw); writeErr != nil {
			return writeErr
		}

		resolved, resolveErr := s.store.Resolve(ctx, tx, req.scope, subject, name)
		if resolveErr != nil {
			return resolveErr
		}

		resolution = resolved

		return nil
	}); err != nil {
		// codes.Internal is the default, and the registered mapper answers over
		// it for everything settings decides — a kind mismatch, a value outside
		// the enumeration, a setting that does not exist. The one refusal raised
		// here rather than there is a request that named no value at all, and
		// InvalidArgument is what a mapper would say about it if the sentinel
		// were the store's.
		code := codes.Internal
		if errors.Is(err, ErrNoValueNamed) {
			code = codes.InvalidArgument
		}

		err = grpcerrors.PrepareAndLogGRPCStatus(err,
			req.op.Logger(), req.op.Span(), code, "setting %q", name)

		return nil, err
	}

	rendered, err := s.renderResolution(req, name, resolution)
	if err != nil {
		return nil, err
	}

	return &settingspb.SetValueResponse{Resolution: rendered}, nil
}

// GetValue reads the answer a subject stored, unparsed.
//
// It is the raw row and it applies no default: a subject who has not answered
// is settings.ErrValueNotFound rather than an empty value, which is the
// distinction [Server.Resolve] exists to make on the other side.
func (s *Server) GetValue(
	ctx context.Context,
	request *settingspb.GetValueRequest,
) (*settingspb.GetValueResponse, error) {
	ctx, req, done, err := s.caller(ctx, settingspb.SettingsService_GetValue_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	subject := subjectFromProto(request.GetSubject())
	name := request.GetName()

	req.setSubject(subject)
	req.op.Set(definitionKey, name)

	if err = s.authorizeSubject(ctx, req, subject); err != nil {
		return nil, err
	}

	value, err := s.store.GetValue(ctx, s.client.Reader(), req.scope, subject, name)
	if err != nil {
		err = grpcerrors.PrepareAndLogGRPCStatus(err,
			req.op.Logger(), req.op.Span(), codes.Internal, "reading the value of setting %q", name)

		return nil, err
	}

	return &settingspb.GetValueResponse{Result: ValueToProto(value)}, nil
}

// ClearValue takes a subject's answer back and answers with what the setting
// resolves to without it.
//
// The resolution is the point of the response. What a screen that has just
// shown a "reset to default" button has to render next is the default — or the
// third answer, that nobody has decided — and reading it back inside the
// transaction that cleared the row is the only place that answer is the one the
// caller just brought about.
func (s *Server) ClearValue(
	ctx context.Context,
	request *settingspb.ClearValueRequest,
) (*settingspb.ClearValueResponse, error) {
	ctx, req, done, err := s.caller(ctx, settingspb.SettingsService_ClearValue_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	subject := subjectFromProto(request.GetSubject())
	name := request.GetName()

	req.setSubject(subject)
	req.op.Set(definitionKey, name)

	if err = s.authorizeSubject(ctx, req, subject); err != nil {
		return nil, err
	}

	var resolution *settings.Resolution

	if err = s.client.WithTransaction(ctx, func(tx database.Tx) error {
		if clearErr := s.store.ClearValue(ctx, tx, req.scope, subject, name); clearErr != nil {
			return clearErr
		}

		resolved, resolveErr := s.store.Resolve(ctx, tx, req.scope, subject, name)
		if resolveErr != nil {
			return resolveErr
		}

		resolution = resolved

		return nil
	}); err != nil {
		err = grpcerrors.PrepareAndLogGRPCStatus(err,
			req.op.Logger(), req.op.Span(), codes.Internal, "clearing the value of setting %q", name)

		return nil, err
	}

	rendered, err := s.renderResolution(req, name, resolution)
	if err != nil {
		return nil, err
	}

	return &settingspb.ClearValueResponse{Resolution: rendered}, nil
}

// ListValuesForSubject pages everything one subject has answered.
//
// It is an inventory of overrides rather than the settings screen's read: the
// settings a subject has *not* answered are not here, and [Server.ResolveAll]
// is what includes them at their default.
func (s *Server) ListValuesForSubject(
	ctx context.Context,
	request *settingspb.ListValuesForSubjectRequest,
) (*settingspb.ListValuesForSubjectResponse, error) {
	ctx, req, done, err := s.caller(ctx, settingspb.SettingsService_ListValuesForSubject_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	subject := subjectFromProto(request.GetSubject())
	req.setSubject(subject)

	if err = s.authorizeSubject(ctx, req, subject); err != nil {
		return nil, err
	}

	filter, err := filteringgrpc.FromProto(request.GetFilter())
	if err != nil {
		err = grpcerrors.PrepareAndLogGRPCStatus(err,
			req.op.Logger(), req.op.Span(), codes.InvalidArgument, "reading the filter of a setting value page")

		return nil, err
	}

	page, err := s.store.ListValuesForSubject(ctx, s.client.Reader(), req.scope, subject, filter)
	if err != nil {
		err = grpcerrors.PrepareAndLogGRPCStatus(err,
			req.op.Logger(), req.op.Span(), codes.Internal, "listing the values of %s %q", subject.Type, subject.ID)

		return nil, err
	}

	return &settingspb.ListValuesForSubjectResponse{
		Pagination: filteringgrpc.PaginationToProto(page.Pagination),
		Results:    ValuesToProto(page.Data),
	}, nil
}

// Resolve answers one setting for one subject: their value, else the
// definition's default, else neither.
//
// This is the method the package exists for, and the third case is why it is
// not a read of a row. A setting the subject has not answered and that has no
// default resolves to VALUE_SOURCE_UNSET with no typed value — an answer,
// "nobody has decided", after which the caller's own policy applies — rather
// than an error or a zero somebody could mistake for a choice.
func (s *Server) Resolve(
	ctx context.Context,
	request *settingspb.ResolveRequest,
) (*settingspb.ResolveResponse, error) {
	ctx, req, done, err := s.caller(ctx, settingspb.SettingsService_Resolve_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	subject := subjectFromProto(request.GetSubject())
	name := request.GetName()

	req.setSubject(subject)
	req.op.Set(definitionKey, name)

	if err = s.authorizeSubject(ctx, req, subject); err != nil {
		return nil, err
	}

	resolution, err := s.store.Resolve(ctx, s.client.Reader(), req.scope, subject, name)
	if err != nil {
		err = grpcerrors.PrepareAndLogGRPCStatus(err,
			req.op.Logger(), req.op.Span(), codes.Internal, "resolving setting %q", name)

		return nil, err
	}

	rendered, err := s.renderResolution(req, name, resolution)
	if err != nil {
		return nil, err
	}

	return &settingspb.ResolveResponse{Resolution: rendered}, nil
}

// ResolveAll answers every live setting in the scope for one subject, sorted by
// name.
//
// It is the read a settings page makes, and it is one call rather than one per
// setting: the settings nobody has answered are in the answer too, at their
// default or as unset, because a page rendering "your preferences" wants the
// ones nobody has touched.
//
// It is deliberately not paged, which is the store's shape arriving unchanged.
// A catalog is a deployment's decision and is small in the way a set of columns
// is small; a page over it would be a cursor a client had to walk to render one
// screen.
func (s *Server) ResolveAll(
	ctx context.Context,
	request *settingspb.ResolveAllRequest,
) (*settingspb.ResolveAllResponse, error) {
	ctx, req, done, err := s.caller(ctx, settingspb.SettingsService_ResolveAll_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	subject := subjectFromProto(request.GetSubject())
	req.setSubject(subject)

	if err = s.authorizeSubject(ctx, req, subject); err != nil {
		return nil, err
	}

	resolutions, err := s.store.ResolveAll(ctx, s.client.Reader(), req.scope, subject)
	if err != nil {
		err = grpcerrors.PrepareAndLogGRPCStatus(err,
			req.op.Logger(), req.op.Span(), codes.Internal, "resolving the settings of %s %q", subject.Type, subject.ID)

		return nil, err
	}

	req.op.Set(countKey, len(resolutions))

	rendered, err := ResolutionsToProto(resolutions)
	if err != nil {
		err = grpcerrors.PrepareAndLogGRPCStatus(err,
			req.op.Logger(), req.op.Span(), codes.Internal,
			"rendering the settings of %s %q", subject.Type, subject.ID)

		return nil, err
	}

	return &settingspb.ResolveAllResponse{Resolutions: rendered}, nil
}

// setSubject records whose settings a request is about, in the two fields the
// tenancy doctrine keeps apart. A composite "user:abc123" would be one span
// attribute nobody can filter on as the two facts it is.
func (r *request) setSubject(subject settings.Subject) {
	r.op.Set(subjectTypeKey, subject.Type.String()).Set(subjectIDKey, subject.ID)
}

// renderResolution renders a resolution for a response.
//
// Reaching its failure means a stored row that is not of its definition's kind,
// which this surface cannot have written — a value set through it is checked
// against the definition twice — so it is a row something else wrote, and
// settings.ErrMalformedValue is what says so. The alternative, answering with
// the resolution and no value, is the silent one: a settings screen showing a
// default the subject did not choose.
//
// It is a helper over three call sites because the part that can be got wrong
// is not the conversion, it is failing the call when the conversion fails.
func (s *Server) renderResolution(
	req *request,
	name string,
	resolution *settings.Resolution,
) (*settingspb.ResolvedSetting, error) {
	rendered, err := ResolutionToProto(resolution)
	if err != nil {
		return nil, grpcerrors.PrepareAndLogGRPCStatus(err,
			req.op.Logger(), req.op.Span(), codes.Internal, "rendering setting %q", name)
	}

	return rendered, nil
}
