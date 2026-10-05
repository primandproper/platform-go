package mcptool_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/primandproper/platform-go/v14/callers"
	"github.com/primandproper/platform-go/v14/internal/mcptool"
	"github.com/primandproper/platform-go/v14/internal/mcptool/fixture"

	"github.com/primandproper/primitives-go/v2/authorization"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/filtering"
	"github.com/primandproper/primitives-go/v2/tenancy"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

const thisPackage = "github.com/primandproper/platform-go/v14/internal/mcptool/fixture"

func TestExtract(T *testing.T) {
	T.Parallel()

	T.Run("reads the first paragraph of each field's doc comment, keyed by json name", func(t *testing.T) {
		t.Parallel()

		docs, err := mcptool.Extract(thisPackage+".Row", thisPackage+".Child")
		must.NoError(t, err)

		test.Eq(t, map[string]string{
			"createdAt": "CreatedAt is when the row was made.",
			"note":      "Note is optional prose.",
			"scope":     "Scope is whose row it is.",
			"id":        "ID identifies the row.",
			"children":  "Children are the row's children.",
		}, docs[thisPackage+".Row"])
		test.Eq(t, map[string]string{"name": "Name is the child's name."}, docs[thisPackage+".Child"])
	})

	T.Run("refuses a type the package does not declare", func(t *testing.T) {
		t.Parallel()

		_, err := mcptool.Extract(thisPackage + ".Absent")
		test.Error(t, err)
	})

	T.Run("refuses a spec naming no type", func(t *testing.T) {
		t.Parallel()

		_, err := mcptool.Extract("nodot")
		test.Error(t, err)
	})
}

func TestOutput(T *testing.T) {
	T.Parallel()

	T.Run("describes every property, nested ones included, from the extracted docs", func(t *testing.T) {
		t.Parallel()

		docs, err := mcptool.Extract(thisPackage+".Row", thisPackage+".Child")
		must.NoError(t, err)

		schema := mcptool.Output[fixture.Row](docs, nil)

		test.SliceEmpty(t, mcptool.Undescribed(schema))
		test.MapNotContainsKey(t, schema.Properties, "Hidden")
		test.MapNotContainsKey(t, schema.Properties, "-")
		test.EqOp(t, "Name is the child's name.", schema.Properties["children"].Items.Properties["name"].Description)
		test.Eq(t, []string{"null", "string"}, schema.Properties["scope"].Types)
	})

	T.Run("reports what it could not describe", func(t *testing.T) {
		t.Parallel()

		schema := mcptool.Output[fixture.Row](mcptool.Docs{}, nil)

		test.Eq(t, []string{"children", "children[].name", "createdAt", "id", "note", "scope"}, mcptool.Undescribed(schema))
	})

	T.Run("describes a paged result from primitives-go's own doc comments", func(t *testing.T) {
		t.Parallel()

		docs, err := mcptool.Extract(thisPackage+".Row", thisPackage+".Child")
		must.NoError(t, err)

		schema := mcptool.Output[filtering.QueryFilteredResult[fixture.Row]](docs, nil)

		test.SliceEmpty(t, mcptool.Undescribed(schema))
		test.MapContainsKey(t, schema.Properties, "cursor")
		test.MapContainsKey(t, schema.Properties["appliedQueryFilter"].Properties, "maxResponseSize")
	})
}

func TestInput(T *testing.T) {
	T.Parallel()

	T.Run("describes a filter with primitives-go's own schema", func(t *testing.T) {
		t.Parallel()

		docs, err := mcptool.Extract(thisPackage + ".Page")
		must.NoError(t, err)

		schema := mcptool.Input[fixture.Page](docs, nil)

		filter := schema.Properties["filter"]
		must.NotNil(t, filter)
		test.EqOp(t, "Filter is the page to read.", filter.Description)

		sortBy := filter.Properties["sortBy"]
		must.NotNil(t, sortBy)
		test.Eq(t, []any{"asc", "desc"}, sortBy.Enum)
		test.NotNil(t, filter.Properties["maxResponseSize"].Maximum)
	})
}

func TestDirectiveSpecs(T *testing.T) {
	T.Parallel()

	T.Run("reads the specs off the mcpdocs directive", func(t *testing.T) {
		t.Parallel()

		file := filepath.Join(t.TempDir(), "tools.go")
		must.NoError(t, os.WriteFile(file, []byte("package mcp\n\n"+
			"//go:generate go run ../../internal/cmd/mcpdocs -pkg mcp -out fielddocs_gen.go example.com/a.Row example.com/b/c.Other\n"), 0o600))

		specs, err := mcptool.DirectiveSpecs(file)
		must.NoError(t, err)
		test.Eq(t, []string{"example.com/a.Row", "example.com/b/c.Other"}, specs)
	})

	T.Run("refuses a file with no directive", func(t *testing.T) {
		t.Parallel()

		file := filepath.Join(t.TempDir(), "tools.go")
		must.NoError(t, os.WriteFile(file, []byte("package mcp\n"), 0o600))

		_, err := mcptool.DirectiveSpecs(file)
		test.Error(t, err)
	})
}

func TestRender(T *testing.T) {
	T.Parallel()

	src, err := mcptool.Render("mcp", "fieldDocs", mcptool.Docs{"example.com/a.Row": {"id": "ID identifies the row."}})
	must.NoError(T, err)

	test.StrContains(T, string(src), "DO NOT EDIT")
	test.StrContains(T, string(src), `"id": "ID identifies the row."`)
	test.StrContains(T, string(src), "mcptool.Docs{")
}

type testPrincipal struct{ scope tenancy.Scope }

func (testPrincipal) UserID() string          { return "user_1" }
func (p testPrincipal) Scope() tenancy.Scope  { return p.scope }
func (testPrincipal) ActiveAccountID() string { return "" }

const readGrant authorization.Permission = "mcptool.things.read"

var errSafe = platformerrors.New("mcptool suite's sentinel a model may read")

func newSurface(t *testing.T, principal callers.Principal, grants ...authorization.Permission) *mcptool.Surface {
	t.Helper()

	surface, err := mcptool.NewSurface("mcptool_test",
		func(ctx context.Context, _ *sdkmcp.CallToolRequest) (context.Context, error) { return ctx, nil },
		func(context.Context) (callers.Principal, bool) { return principal, principal != nil },
		func(context.Context) (authorization.Grants, bool) {
			return authorization.NewGrants(authorization.NewPermissionSet(grants...)), true
		},
		[]error{errSafe}, nil, nil, nil)
	must.NoError(t, err)

	return surface
}

func TestSurface(T *testing.T) {
	T.Parallel()

	T.Run("lets a caller holding the grant through, confined to their scope", func(t *testing.T) {
		t.Parallel()

		scope := tenancy.Of("tenant_1")

		_, call, err := newSurface(t, testPrincipal{scope: scope}, readGrant).Begin(t.Context(), nil, "tool", readGrant)
		must.NoError(t, err)
		test.EqOp(t, scope.String(), call.Scope.String())
		test.NoError(t, call.End(nil))
	})

	T.Run("refuses nobody, and a caller without the grant", func(t *testing.T) {
		t.Parallel()

		_, _, err := newSurface(t, nil).Begin(t.Context(), nil, "tool", readGrant)
		test.ErrorIs(t, err, mcptool.ErrNoPrincipal)

		_, _, err = newSurface(t, testPrincipal{scope: tenancy.Global()}).Begin(t.Context(), nil, "tool", readGrant)
		test.ErrorIs(t, err, mcptool.ErrPermissionDenied)
	})

	T.Run("NoGrant needs no grant, and still a caller", func(t *testing.T) {
		t.Parallel()

		_, call, err := newSurface(t, testPrincipal{scope: tenancy.Global()}).Begin(t.Context(), nil, "tool", mcptool.NoGrant)
		must.NoError(t, err)
		test.NoError(t, call.End(nil))
	})

	T.Run("a safe sentinel is answered as itself, without the context wrapped around it", func(t *testing.T) {
		t.Parallel()

		_, call, err := newSurface(t, testPrincipal{scope: tenancy.Global()}, readGrant).Begin(t.Context(), nil, "tool", readGrant)
		must.NoError(t, err)

		answered := call.End(platformerrors.Wrap(errSafe, "reading row 42 from table secret_internal_name"))
		test.EqOp(t, errSafe, answered)
	})

	T.Run("anything else is ErrToolFailed, and its words are not repeated", func(t *testing.T) {
		t.Parallel()

		_, call, err := newSurface(t, testPrincipal{scope: tenancy.Global()}, readGrant).Begin(t.Context(), nil, "tool", readGrant)
		must.NoError(t, err)

		answered := call.End(platformerrors.New("pq: relation \"secret_internal_name\" does not exist"))
		test.EqOp(t, mcptool.ErrToolFailed, answered)
	})

	T.Run("an authenticator that cannot decide is a failure rather than a refusal", func(t *testing.T) {
		t.Parallel()

		surface, err := mcptool.NewSurface("mcptool_test",
			func(ctx context.Context, _ *sdkmcp.CallToolRequest) (context.Context, error) {
				return ctx, platformerrors.New("the directory is down")
			},
			func(context.Context) (callers.Principal, bool) { return testPrincipal{}, true },
			func(context.Context) (authorization.Grants, bool) { return authorization.Grants{}, true },
			nil, nil, nil, nil)
		must.NoError(t, err)

		_, _, err = surface.Begin(t.Context(), nil, "tool", mcptool.NoGrant)
		test.ErrorIs(t, err, mcptool.ErrToolFailed)
	})

	T.Run("a filter's unrecognized sort direction is refused in its own words", func(t *testing.T) {
		t.Parallel()

		_, call, err := newSurface(t, testPrincipal{scope: tenancy.Global()}, readGrant).Begin(t.Context(), nil, "tool", readGrant)
		must.NoError(t, err)

		sideways := "sideways"

		_, err = call.Filter(t.Context(), &filtering.QueryFilter{SortBy: &sideways}, readGrant, "mcptool_test.cleared")
		answered := call.End(err)

		test.ErrorIs(t, answered, platformerrors.ErrUnrecognizedInputValue)
		test.StrContains(t, answered.Error(), "sideways")
	})
}

func TestNewSurface(T *testing.T) {
	T.Parallel()

	authenticate := func(ctx context.Context, _ *sdkmcp.CallToolRequest) (context.Context, error) { return ctx, nil }
	principals := func(context.Context) (callers.Principal, bool) { return nil, false }
	grants := func(context.Context) (authorization.Grants, bool) { return authorization.Grants{}, false }

	_, err := mcptool.NewSurface("x", nil, principals, grants, nil, nil, nil, nil)
	test.ErrorIs(T, err, mcptool.ErrNilAuthenticator)

	_, err = mcptool.NewSurface("x", authenticate, nil, grants, nil, nil, nil, nil)
	test.ErrorIs(T, err, mcptool.ErrNilPrincipalExtractor)

	_, err = mcptool.NewSurface("x", authenticate, principals, nil, nil, nil, nil, nil)
	test.ErrorIs(T, err, mcptool.ErrNilGrantsExtractor)
}
