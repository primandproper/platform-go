package grpc

import (
	"context"

	"github.com/primandproper/platform-go/v14/settings"
	"github.com/primandproper/platform-go/v14/settings/settingspb"

	"github.com/primandproper/primitives-go/database"
	grpcerrors "github.com/primandproper/primitives-go/errors/grpc"
	filteringgrpc "github.com/primandproper/primitives-go/filtering/grpc"

	"google.golang.org/grpc/codes"
)

// The catalog half of the surface: seven RPCs over the settings defined in the
// caller's scope, each behind a permission.
//
// Every one of them takes the scope off the caller's principal, and none of
// them takes a subject, which is why none of them asks the
// [SubjectAuthorizer]: a definition is nobody's in particular. The four reads
// are one call each; the three writes are one call inside one transaction,
// because settings.Store's writes take a database.Tx and an RPC handler is the
// caller with nothing of its own to join.
//
// None of them switches on a sentinel: the error goes through
// grpcerrors.PrepareAndLogGRPCStatus with codes.Internal as the *default*, and
// the encoding interceptor re-runs the registered mappers over the preserved
// chain, so settings.GRPCMapper wins over the guess made here. The one place a
// code is passed as an answer rather than a default is the request that named
// no definition at all, which nothing maps because it is this transport's
// refusal rather than the store's.

// CreateDefinition adds a setting to the caller's catalog.
//
// The store hands back the row as stored — the id it was minted under and the
// creation time the database assigned — so there is nothing to read back here,
// unlike [Server.UpdateDefinition].
func (s *Server) CreateDefinition(
	ctx context.Context,
	request *settingspb.CreateDefinitionRequest,
) (*settingspb.CreateDefinitionResponse, error) {
	ctx, req, done, err := s.caller(ctx, settingspb.SettingsService_CreateDefinition_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	definition := definitionFromProto(request.GetDefinition(), "")
	if definition == nil {
		err = grpcerrors.PrepareAndLogGRPCStatus(ErrNilDefinitionInput,
			req.op.Logger(), req.op.Span(), codes.InvalidArgument, "creating a setting definition")

		return nil, err
	}

	req.op.Set(definitionKey, definition.Name)

	var created *settings.Definition

	if err = s.client.WithTransaction(ctx, func(tx database.Tx) error {
		stored, createErr := s.store.CreateDefinition(ctx, tx, req.scope, definition)
		if createErr != nil {
			return createErr
		}

		created = stored

		return nil
	}); err != nil {
		err = grpcerrors.PrepareAndLogGRPCStatus(err,
			req.op.Logger(), req.op.Span(), codes.Internal, "creating setting definition %q", definition.Name)

		return nil, err
	}

	req.op.Set(definitionIDKey, created.ID)

	return &settingspb.CreateDefinitionResponse{Result: DefinitionToProto(created)}, nil
}

// GetDefinition reads one live definition in the caller's catalog by id.
//
// A definition in another scope reads as one that does not exist, which is what
// it is from here — the scope is bound into the statement rather than checked
// in front of it.
func (s *Server) GetDefinition(
	ctx context.Context,
	request *settingspb.GetDefinitionRequest,
) (*settingspb.GetDefinitionResponse, error) {
	ctx, req, done, err := s.caller(ctx, settingspb.SettingsService_GetDefinition_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	id := request.GetDefinitionId()
	req.op.Set(definitionIDKey, id)

	definition, err := s.store.GetDefinition(ctx, s.client.Reader(), req.scope, id)
	if err != nil {
		err = grpcerrors.PrepareAndLogGRPCStatus(err,
			req.op.Logger(), req.op.Span(), codes.Internal, "reading setting definition %q", id)

		return nil, err
	}

	return &settingspb.GetDefinitionResponse{Result: DefinitionToProto(definition)}, nil
}

// GetDefinitionByName reads one live definition by the name application code
// spells.
//
// It is the read every value-side call begins with, and it is on the surface in
// its own right because a client rendering a single setting holds the name
// rather than the row id — which is the same reason settings' value methods
// take a name.
func (s *Server) GetDefinitionByName(
	ctx context.Context,
	request *settingspb.GetDefinitionByNameRequest,
) (*settingspb.GetDefinitionByNameResponse, error) {
	ctx, req, done, err := s.caller(ctx, settingspb.SettingsService_GetDefinitionByName_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	name := request.GetName()
	req.op.Set(definitionKey, name)

	definition, err := s.store.GetDefinitionByName(ctx, s.client.Reader(), req.scope, name)
	if err != nil {
		err = grpcerrors.PrepareAndLogGRPCStatus(err,
			req.op.Logger(), req.op.Span(), codes.Internal, "reading setting definition %q", name)

		return nil, err
	}

	return &settingspb.GetDefinitionByNameResponse{Result: DefinitionToProto(definition)}, nil
}

// ListDefinitions pages the caller's catalog.
func (s *Server) ListDefinitions(
	ctx context.Context,
	request *settingspb.ListDefinitionsRequest,
) (*settingspb.ListDefinitionsResponse, error) {
	ctx, req, done, err := s.caller(ctx, settingspb.SettingsService_ListDefinitions_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	filter, err := filteringgrpc.FromProto(request.GetFilter())
	if err != nil {
		err = grpcerrors.PrepareAndLogGRPCStatus(err,
			req.op.Logger(), req.op.Span(), codes.InvalidArgument, "reading the filter of a setting definition page")

		return nil, err
	}

	page, err := s.store.ListDefinitions(ctx, s.client.Reader(), req.scope, filter)
	if err != nil {
		err = grpcerrors.PrepareAndLogGRPCStatus(err,
			req.op.Logger(), req.op.Span(), codes.Internal, "listing setting definitions")

		return nil, err
	}

	return &settingspb.ListDefinitionsResponse{
		Pagination: filteringgrpc.PaginationToProto(page.Pagination),
		Results:    DefinitionsToProto(page.Data),
	}, nil
}

// UpdateDefinition rewrites a definition, enumeration included.
//
// It is the RPC that can be refused for a reason nothing else here has: an edit
// some stored value no longer satisfies is settings.ErrStrandedValues, naming
// the subject and the value, and it is a refusal an operator reads rather than
// a 500. Clearing or migrating those values first is what makes the edit
// possible, and the message names them one at a time so there is always
// something to do next.
//
// The row in the response is the store's own answer, read inside the transaction
// that wrote it. A response assembled from the request would carry the epoch
// where last_updated_at belongs, and a read on Client.Reader() would be a read
// of a database that does not yet hold the edit — which is why this handler owns
// neither read: settings.Store.UpdateDefinition returns what it wrote.
func (s *Server) UpdateDefinition(
	ctx context.Context,
	request *settingspb.UpdateDefinitionRequest,
) (*settingspb.UpdateDefinitionResponse, error) {
	ctx, req, done, err := s.caller(ctx, settingspb.SettingsService_UpdateDefinition_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	id := request.GetDefinitionId()
	req.op.Set(definitionIDKey, id)

	definition := definitionFromProto(request.GetDefinition(), id)
	if definition == nil {
		err = grpcerrors.PrepareAndLogGRPCStatus(ErrNilDefinitionInput,
			req.op.Logger(), req.op.Span(), codes.InvalidArgument, "updating setting definition %q", id)

		return nil, err
	}

	req.op.Set(definitionKey, definition.Name)

	var updated *settings.Definition

	if err = s.client.WithTransaction(ctx, func(tx database.Tx) error {
		edited, updateErr := s.store.UpdateDefinition(ctx, tx, req.scope, definition)
		if updateErr != nil {
			return updateErr
		}

		updated = edited

		return nil
	}); err != nil {
		err = grpcerrors.PrepareAndLogGRPCStatus(err,
			req.op.Logger(), req.op.Span(), codes.Internal, "updating setting definition %q", id)

		return nil, err
	}

	return &settingspb.UpdateDefinitionResponse{Result: DefinitionToProto(updated)}, nil
}

// ArchiveDefinition retires a setting in the caller's catalog.
//
// The values stored against it are left alone and the name stays claimed, which
// is settings.Store.ArchiveDefinition's own decision arriving on the wire:
// archiving is not erasure, and freeing the name would let a second definition
// inherit rows written for the first.
//
// The store answers with the definition it retired and this response does not
// carry it. That is the message's shape rather than an oversight: a retirement
// names a row a client already asked for by id, and the row it wants back is the
// one the store hands its in-process callers for the entry they write beside the
// write. A client that wants the definition reads it before archiving.
func (s *Server) ArchiveDefinition(
	ctx context.Context,
	request *settingspb.ArchiveDefinitionRequest,
) (*settingspb.ArchiveDefinitionResponse, error) {
	ctx, req, done, err := s.caller(ctx, settingspb.SettingsService_ArchiveDefinition_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	id := request.GetDefinitionId()
	req.op.Set(definitionIDKey, id)

	if err = s.client.WithTransaction(ctx, func(tx database.Tx) error {
		_, archiveErr := s.store.ArchiveDefinition(ctx, tx, req.scope, id)

		return archiveErr
	}); err != nil {
		err = grpcerrors.PrepareAndLogGRPCStatus(err,
			req.op.Logger(), req.op.Span(), codes.Internal, "archiving setting definition %q", id)

		return nil, err
	}

	return &settingspb.ArchiveDefinitionResponse{}, nil
}

// ListValuesForDefinition pages everyone who has answered one setting.
//
// It is the administrative read behind "who has overridden this", and it is the
// one value-side RPC that is an operator's rather than a subject's: it names a
// setting and not a subject, so there is no [SubjectAuthorizer] question to ask
// and a grant of its own — PermissionReadAllValues — is what stands in front of
// it.
func (s *Server) ListValuesForDefinition(
	ctx context.Context,
	request *settingspb.ListValuesForDefinitionRequest,
) (*settingspb.ListValuesForDefinitionResponse, error) {
	ctx, req, done, err := s.caller(ctx, settingspb.SettingsService_ListValuesForDefinition_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	name := request.GetName()
	req.op.Set(definitionKey, name)

	filter, err := filteringgrpc.FromProto(request.GetFilter())
	if err != nil {
		err = grpcerrors.PrepareAndLogGRPCStatus(err,
			req.op.Logger(), req.op.Span(), codes.InvalidArgument, "reading the filter of a setting value page")

		return nil, err
	}

	page, err := s.store.ListValuesForDefinition(ctx, s.client.Reader(), req.scope, name, filter)
	if err != nil {
		err = grpcerrors.PrepareAndLogGRPCStatus(err,
			req.op.Logger(), req.op.Span(), codes.Internal, "listing the values of setting %q", name)

		return nil, err
	}

	return &settingspb.ListValuesForDefinitionResponse{
		Pagination: filteringgrpc.PaginationToProto(page.Pagination),
		Results:    ValuesToProto(page.Data),
	}, nil
}
