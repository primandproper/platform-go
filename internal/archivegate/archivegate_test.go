package archivegate_test

import (
	"context"
	"testing"

	"github.com/primandproper/platform-go/v15/internal/archivegate"

	"github.com/primandproper/primitives-go/v2/authorization"
	"github.com/primandproper/primitives-go/v2/filtering"
	"github.com/primandproper/primitives-go/v2/filtering/filteringpb"
	"github.com/primandproper/primitives-go/v2/observability"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	archiveGrant authorization.Permission = "archivegate.things.archive"
	readGrant    authorization.Permission = "archivegate.things.read"
	clearedKey                            = "archivegate.include_archived_cleared"
)

// granting is an extractor answering with exactly perms.
func granting(perms ...authorization.Permission) authorization.GrantsExtractor {
	return func(context.Context) (authorization.Grants, bool) {
		return authorization.NewGrants(authorization.NewPermissionSet(perms...)), true
	}
}

// asking is a wire filter that asked, or did not, for the archived rows.
func asking(include bool) *filteringpb.QueryFilter {
	return &filteringpb.QueryFilter{IncludeArchived: &include}
}

// cleared reports whether the operation recorded a clearing.
func cleared(o *observability.RecordingObserver) bool {
	stream := o.Stream()
	for i := range stream {
		if stream[i].Key == clearedKey {
			return true
		}
	}

	return false
}

func TestFilter(T *testing.T) {
	T.Parallel()

	T.Run("honors the field for a caller holding the archive grant", func(t *testing.T) {
		t.Parallel()

		o := observability.NewRecordingObserver()
		ctx, op := o.Begin(t.Context())

		filter, err := archivegate.Filter(ctx, op, asking(true), granting(readGrant, archiveGrant),
			archiveGrant, clearedKey, "reading")
		must.NoError(t, err)
		must.NotNil(t, filter.IncludeArchived)
		test.True(t, *filter.IncludeArchived)
		test.False(t, cleared(o), test.Sprint("an honored request was recorded as cleared"))
	})

	// Every way of not being entitled ends in the same filter: the one a caller
	// who never asked would have sent, with the clearing on the span.
	for name, grants := range map[string]authorization.GrantsExtractor{
		"a caller holding the read grant alone": granting(readGrant),
		"a server built with no extractor":      nil,
		"an extractor that cannot say who is calling": func(context.Context) (authorization.Grants, bool) {
			return authorization.AllowAll(), false
		},
	} {
		T.Run("clears the field for "+name, func(t *testing.T) {
			t.Parallel()

			o := observability.NewRecordingObserver()
			ctx, op := o.Begin(t.Context())

			filter, err := archivegate.Filter(ctx, op, asking(true), grants, archiveGrant, clearedKey, "reading")
			must.NoError(t, err)
			test.Nil(t, filter.IncludeArchived, test.Sprint("the field was set to a value rather than cleared"))
			test.True(t, cleared(o), test.Sprint("a clearing was not recorded on the span"))
		})
	}

	// The empty permission is nobody's grant, including a grants value that
	// says yes to everything — which is the case that matters, since that is
	// what an operator's allow-all role reads as.
	T.Run("clears the field for everybody on a read with nothing archived", func(t *testing.T) {
		t.Parallel()

		o := observability.NewRecordingObserver()
		ctx, op := o.Begin(t.Context())

		allowAll := func(context.Context) (authorization.Grants, bool) { return authorization.AllowAll(), true }

		filter, err := archivegate.Filter(ctx, op, asking(true), allowAll,
			archivegate.NothingArchived, clearedKey, "reading")
		must.NoError(t, err)
		test.Nil(t, filter.IncludeArchived)
		test.True(t, cleared(o))
	})

	// A request that did not ask is not a clearing, so the span says nothing
	// and a false is passed through as sent.
	for name, in := range map[string]*filteringpb.QueryFilter{
		"no filter at all":         nil,
		"a filter that never asks": {},
		"a filter that says false": asking(false),
	} {
		T.Run("records nothing for "+name, func(t *testing.T) {
			t.Parallel()

			o := observability.NewRecordingObserver()
			ctx, op := o.Begin(t.Context())

			filter, err := archivegate.Filter(ctx, op, in, granting(readGrant), archiveGrant, clearedKey, "reading")
			must.NoError(t, err)
			must.NotNil(t, filter)
			test.EqOp(t, in.GetIncludeArchived(), filter.IncludeArchived != nil && *filter.IncludeArchived)
			test.False(t, cleared(o))
		})
	}

	T.Run("refuses a filter it cannot read as the caller's to fix", func(t *testing.T) {
		t.Parallel()

		o := observability.NewRecordingObserver()
		ctx, op := o.Begin(t.Context())

		sideways := "sideways"

		filter, err := archivegate.Filter(ctx, op, &filteringpb.QueryFilter{SortBy: &sideways},
			granting(archiveGrant), archiveGrant, clearedKey, "reading")
		must.Error(t, err)
		test.Nil(t, filter)
		test.EqOp(t, codes.InvalidArgument, status.Code(err))
	})
}

func TestNarrow(T *testing.T) {
	T.Parallel()

	include := true

	T.Run("honors the field for a caller holding the archive grant", func(t *testing.T) {
		t.Parallel()

		o := observability.NewRecordingObserver()
		ctx, op := o.Begin(t.Context())

		filter := archivegate.Narrow(ctx, op, &filtering.QueryFilter{IncludeArchived: &include},
			granting(archiveGrant), archiveGrant, clearedKey)
		must.NotNil(t, filter.IncludeArchived)
		test.True(t, *filter.IncludeArchived)
		test.False(t, cleared(o))
	})

	T.Run("clears the field for a caller without it, and records that", func(t *testing.T) {
		t.Parallel()

		o := observability.NewRecordingObserver()
		ctx, op := o.Begin(t.Context())

		filter := archivegate.Narrow(ctx, op, &filtering.QueryFilter{IncludeArchived: &include},
			granting(readGrant), archiveGrant, clearedKey)
		test.Nil(t, filter.IncludeArchived)
		test.True(t, cleared(o))
	})

	T.Run("passes a nil filter through", func(t *testing.T) {
		t.Parallel()

		o := observability.NewRecordingObserver()
		ctx, op := o.Begin(t.Context())

		test.Nil(t, archivegate.Narrow(ctx, op, nil, granting(archiveGrant), archiveGrant, clearedKey))
	})
}
