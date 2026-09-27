package settings

import (
	"context"
	"testing"

	"github.com/primandproper/platform-go/v14/conformance"
	"github.com/primandproper/platform-go/v14/settings/settingspb"

	"github.com/primandproper/primitives-go/v2/filtering/filteringpb"

	"github.com/shoenig/test"
	"google.golang.org/grpc"
)

// pagedCatalog answers ListDefinitions two names to a page, the cursor being
// the index of the next row, and holds its retired rows apart so a request
// that does not ask for them never sees them.
type pagedCatalog struct {
	settingspb.SettingsServiceClient

	live, retired []string
	requests      []*settingspb.ListDefinitionsRequest
}

func (c *pagedCatalog) ListDefinitions(
	_ context.Context,
	req *settingspb.ListDefinitionsRequest,
	_ ...grpc.CallOption,
) (*settingspb.ListDefinitionsResponse, error) {
	c.requests = append(c.requests, req)

	rows := c.live
	if req.GetFilter().GetIncludeArchived() {
		rows = append(append([]string{}, c.live...), c.retired...)
	}

	start := 0
	if req.GetFilter().Cursor != nil {
		start = int(req.GetFilter().GetCursor()[0] - '0')
	}

	end := min(start+2, len(rows))

	page := &settingspb.ListDefinitionsResponse{Pagination: &filteringpb.Pagination{}}
	for _, name := range rows[start:end] {
		page.Results = append(page.Results, &settingspb.SettingDefinition{Name: name})
	}

	if end < len(rows) {
		page.Pagination.Cursor = string(rune('0' + end))
	}

	return page, nil
}

func TestCatalogNames(t *testing.T) {
	t.Parallel()

	t.Run("a definition past the first page of a shared catalog is found", func(t *testing.T) {
		t.Parallel()

		catalog := &pagedCatalog{live: []string{"a", "b", "c", "d", "mine"}}
		caller := &conformance.Subject{Surfaces: conformance.Surfaces{Settings: catalog}}

		got := catalogNames(t, caller, false)

		test.Eq(t, []string{"a", "b", "c", "d", "mine"}, got)
		test.SliceLen(t, 3, catalog.requests)

		// The first request carries no cursor at all, because an empty
		// cursor is a cursor.
		test.Nil(t, catalog.requests[0].GetFilter().Cursor)
	})

	t.Run("retired rows are asked for on every page, not the first alone", func(t *testing.T) {
		t.Parallel()

		catalog := &pagedCatalog{live: []string{"a", "b", "c"}, retired: []string{"gone"}}
		caller := &conformance.Subject{Surfaces: conformance.Surfaces{Settings: catalog}}

		test.SliceContains(t, catalogNames(t, caller, true), "gone")
		test.SliceNotContains(t, catalogNames(t, caller, false), "gone")

		for _, req := range catalog.requests[:2] {
			test.True(t, req.GetFilter().GetIncludeArchived())
		}
	})
}
