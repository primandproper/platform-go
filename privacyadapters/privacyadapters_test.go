package privacyadapters_test

import (
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	oauth2clientsmock "github.com/primandproper/platform-go/v14/authentication/oauth2clients/mock"
	passkeysmock "github.com/primandproper/platform-go/v14/authentication/passkeys/mock"
	passwordresetmock "github.com/primandproper/platform-go/v14/authentication/passwordreset/mock"
	billingmock "github.com/primandproper/platform-go/v14/billing/mock"
	billingprivacy "github.com/primandproper/platform-go/v14/billing/privacy"
	commentsmock "github.com/primandproper/platform-go/v14/comments/mock"
	commentsprivacy "github.com/primandproper/platform-go/v14/comments/privacy"
	"github.com/primandproper/platform-go/v14/dataprivacy"
	"github.com/primandproper/platform-go/v14/dataprivacy/auditerasure"
	identitymock "github.com/primandproper/platform-go/v14/identity/mock"
	issuereportsmock "github.com/primandproper/platform-go/v14/issuereports/mock"
	mediaregistrymock "github.com/primandproper/platform-go/v14/mediaregistry/mock"
	notificationsmock "github.com/primandproper/platform-go/v14/notifications/mock"
	notificationsprivacy "github.com/primandproper/platform-go/v14/notifications/privacy"
	"github.com/primandproper/platform-go/v14/privacyadapters"
	settingsmock "github.com/primandproper/platform-go/v14/settings/mock"
	waitlistsmock "github.com/primandproper/platform-go/v14/waitlists/mock"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/database/dialect"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// stubReader is a non-nil database.SQLQueryExecutor that is never called. The
// constructors only check it for nil, and a method call on one of these panics
// rather than passing quietly.
type stubReader struct{ database.SQLQueryExecutor }

// everything names every adapter this module ships, with a mock behind each
// seam. It is what the roster test registers, and the thing that has to be
// edited when a twelfth adapter lands.
func everything() *privacyadapters.Adapters {
	resolve := dataprivacy.FixedScopes(tenancy.Global())

	return &privacyadapters.Adapters{
		Reader:        stubReader{},
		Comments:      &privacyadapters.CommentsAdapter{Store: &commentsmock.StoreMock{}, Resolve: resolve},
		IssueReports:  &privacyadapters.IssueReportsAdapter{Store: &issuereportsmock.StoreMock{}, Resolve: resolve},
		Settings:      &privacyadapters.SettingsAdapter{Store: &settingsmock.ValueStoreMock{}, Resolve: resolve},
		Waitlists:     &privacyadapters.WaitlistsAdapter{Store: &waitlistsmock.SignupStoreMock{}, Resolve: resolve},
		MediaRegistry: &privacyadapters.MediaRegistryAdapter{Store: &mediaregistrymock.StoreMock{}, Resolve: resolve},
		OAuth2Clients: &privacyadapters.OAuth2ClientsAdapter{Store: &oauth2clientsmock.StoreMock{}, Resolve: resolve},
		Passkeys:      &privacyadapters.PasskeysAdapter{Store: &passkeysmock.StoreMock{}, Resolve: resolve},
		PasswordReset: &privacyadapters.PasswordResetAdapter{Store: &passwordresetmock.StoreMock{}, Resolve: resolve},
		Identity:      &privacyadapters.IdentityAdapter{Store: &identitymock.StoreMock{}, Resolve: resolve},
		Notifications: &privacyadapters.NotificationsAdapter{
			Inbox:    &notificationsmock.InboxMock{},
			Registry: &notificationsmock.RegistryMock{},
			Resolve:  resolve,
		},
		Billing: &privacyadapters.BillingAdapter{
			Store:   &billingmock.StoreMock{},
			Resolve: billingprivacy.FixedAccounts(tenancy.Global()),
		},
		AuditErasure: &privacyadapters.AuditErasureAdapter{Dialect: dialect.Postgres},
	}
}

func TestRegisterCoversEveryShippedAdapter(T *testing.T) {
	T.Parallel()

	// The roster check, and the reason this package exists. The keys come from
	// the module's own source — every directory declaring a dataprivacy.Collector
	// or dataprivacy.Eraser, and the Default*Key constants it ships — so a
	// thirteenth adapter landing unlisted fails here rather than as a section
	// missing from somebody's subject access request.
	shipped := shippedAdapters(T)
	must.MapNotEmpty(T, shipped, must.Sprintf("the tree walk found no adapters, which is the walk failing rather than the tree"))

	want := []string{}
	for _, keys := range shipped {
		want = append(want, keys...)
	}

	slices.Sort(want)

	registry := dataprivacy.NewRegistry()

	got, err := privacyadapters.Register(registry, everything())
	must.NoError(T, err)
	test.Eq(T, want, got)
}

func TestRegisterSplitsCollectorsFromErasers(T *testing.T) {
	T.Parallel()

	registry := dataprivacy.NewRegistry()

	_, err := privacyadapters.Register(registry, everything())
	must.NoError(T, err)

	collectors := registry.CollectorKeys()
	erasers := registry.EraserKeys()

	// billing ships a collector and no eraser: a subscription and a ledger row
	// are financial records every jurisdiction requires kept.
	test.SliceContains(T, collectors, billingprivacy.DefaultKey)
	test.SliceNotContains(T, erasers, billingprivacy.DefaultKey)

	// auditerasure is the other asymmetry, the other way round: the hash chain
	// is what it erases whole scopes of, and there is nothing to export.
	test.SliceContains(T, erasers, auditerasure.DefaultKey)
	test.SliceNotContains(T, collectors, auditerasure.DefaultKey)

	// notifications is two pairs under two keys rather than one of either.
	test.SliceContains(T, collectors, notificationsprivacy.DefaultInboxKey)
	test.SliceContains(T, collectors, notificationsprivacy.DefaultDeviceKey)
	test.SliceContains(T, erasers, notificationsprivacy.DefaultInboxKey)
	test.SliceContains(T, erasers, notificationsprivacy.DefaultDeviceKey)
}

func TestDataPrivacyDocNamesEveryAdapter(T *testing.T) {
	T.Parallel()

	// The acceptance criterion's "doc.go names all of them", turned from a list
	// checked against nothing into one checked against source. The prose in
	// dataprivacy's package documentation is what a consumer reads to find out
	// which adapters exist, and it named eleven for as long as it did precisely
	// because nothing could tell it had stopped being true.
	doc, err := os.ReadFile(filepath.Join(moduleRoot(T), "dataprivacy", "doc.go"))
	must.NoError(T, err)

	text := string(doc)

	for dir := range shippedAdapters(T) {
		test.StrContains(T, text, dir,
			test.Sprintf("dataprivacy/doc.go does not name %s: a consumer reading it would not learn the adapter exists", dir))
	}
}

func TestRegisterSkipsWhatADeploymentDoesNotHave(T *testing.T) {
	T.Parallel()

	T.Run("a nil field is a domain this deployment does not run", func(t *testing.T) {
		t.Parallel()

		registry := dataprivacy.NewRegistry()

		keys, err := privacyadapters.Register(registry, &privacyadapters.Adapters{
			Reader:   stubReader{},
			Comments: &privacyadapters.CommentsAdapter{Store: &commentsmock.StoreMock{}, Resolve: dataprivacy.FixedScopes(tenancy.Global())},
		})
		must.NoError(t, err)
		test.Eq(t, []string{commentsprivacy.DefaultKey}, keys)
	})

	T.Run("no adapters at all registers nothing", func(t *testing.T) {
		t.Parallel()

		// A deployment with a privacy pipeline and no domains wired yet is a
		// deployment mid-assembly, not one to refuse. The fulfiller is what
		// refuses an export with no collectors, and it says so by name.
		registry := dataprivacy.NewRegistry()

		keys, err := privacyadapters.Register(registry, &privacyadapters.Adapters{})
		must.NoError(t, err)
		test.SliceEmpty(t, keys)
		test.SliceEmpty(t, registry.CollectorKeys())
	})

	T.Run("one notifications seam without the other", func(t *testing.T) {
		t.Parallel()

		registry := dataprivacy.NewRegistry()

		keys, err := privacyadapters.Register(registry, &privacyadapters.Adapters{
			Reader: stubReader{},
			Notifications: &privacyadapters.NotificationsAdapter{
				Inbox:   &notificationsmock.InboxMock{},
				Resolve: dataprivacy.FixedScopes(tenancy.Global()),
			},
		})
		must.NoError(t, err)
		test.Eq(t, []string{notificationsprivacy.DefaultInboxKey}, keys)
	})
}

func TestRegisterRefuses(T *testing.T) {
	T.Parallel()

	T.Run("a nil registry", func(t *testing.T) {
		t.Parallel()

		_, err := privacyadapters.Register(nil, everything())
		test.ErrorIs(t, err, platformerrors.ErrNilInputParameter)
	})

	T.Run("nil adapters", func(t *testing.T) {
		t.Parallel()

		_, err := privacyadapters.Register(dataprivacy.NewRegistry(), nil)
		test.ErrorIs(t, err, platformerrors.ErrNilInputParameter)
	})

	T.Run("an adapter named without its resolver", func(t *testing.T) {
		t.Parallel()

		// The reason the fields are pointers: "this deployment has no comments"
		// and "this deployment has comments and forgot the resolver" are two
		// different mistakes, and only the second is reachable.
		_, err := privacyadapters.Register(dataprivacy.NewRegistry(), &privacyadapters.Adapters{
			Reader:   stubReader{},
			Comments: &privacyadapters.CommentsAdapter{Store: &commentsmock.StoreMock{}},
		})
		must.Error(t, err)
		test.ErrorIs(t, err, commentsprivacy.ErrNilScopeResolver)
		test.StrContains(t, err.Error(), commentsprivacy.DefaultKey)
	})

	T.Run("nothing is registered when a later adapter refuses", func(t *testing.T) {
		t.Parallel()

		// Every adapter is built before any is registered, so a nil store in the
		// last field does not leave a registry holding ten of eleven domains.
		registry := dataprivacy.NewRegistry()

		adapters := everything()
		adapters.AuditErasure = &privacyadapters.AuditErasureAdapter{
			Dialect: dialect.Postgres,
			Options: []auditerasure.Option{auditerasure.WithTablePrefix("not a prefix")},
		}

		_, err := privacyadapters.Register(registry, adapters)
		must.Error(t, err)
		test.ErrorIs(t, err, auditerasure.ErrInvalidTablePrefix)
		test.SliceEmpty(t, registry.CollectorKeys())
		test.SliceEmpty(t, registry.EraserKeys())
	})

	T.Run("a key the caller registered already", func(t *testing.T) {
		t.Parallel()

		// dataprivacy.Registry refuses a re-registration rather than replacing,
		// because a silent overwrite would drop a domain from every export from
		// then on. It has no unregister, so the collision is checked before the
		// first adapter goes in: the caller is left holding the registry they
		// had, not that one plus whichever adapters were ahead of the clash.
		registry := dataprivacy.NewRegistry()

		must.NoError(t, registry.RegisterCollector(commentsprivacy.DefaultKey,
			dataprivacy.CollectorFunc(func(
				context.Context,
				tenancy.Scope,
				dataprivacy.Subject,
			) (json.RawMessage, error) {
				return nil, nil
			})))

		_, err := privacyadapters.Register(registry, everything())
		must.Error(t, err)
		test.ErrorIs(t, err, dataprivacy.ErrDuplicateKey)
		test.StrContains(t, err.Error(), commentsprivacy.DefaultKey)

		// Nothing else went in. comments is the collision and the first entry
		// built, so under a registration that stopped where it failed this
		// would already be true; what makes it an assertion about the rule
		// rather than about the order is the eraser half — comments ships one,
		// the caller registered only a collector, and the clash is therefore
		// found by a check that read the whole set rather than by the write
		// that would have succeeded.
		test.Eq(t, []string{commentsprivacy.DefaultKey}, registry.CollectorKeys())
		test.SliceEmpty(t, registry.EraserKeys())
	})

	T.Run("a key the caller registered already, on the last adapter built", func(t *testing.T) {
		t.Parallel()

		// The same rule where stopping at the failure would not have been
		// enough: the collision is on a key built last, so a Register that
		// discovered it by trying would have ten domains in the registry by
		// then. dataprivacy.Registry has no unregister, so that registry is
		// unusable and its holder has no way to find out except by asking it
		// for keys it does not have.
		registry := dataprivacy.NewRegistry()

		must.NoError(t, registry.RegisterEraser(auditerasure.DefaultKey,
			dataprivacy.EraserFunc(func(
				context.Context,
				database.Tx,
				tenancy.Scope,
				dataprivacy.Subject,
			) (dataprivacy.ErasureOutcome, error) {
				return dataprivacy.ErasureOutcome{}, nil
			})))

		_, err := privacyadapters.Register(registry, everything())
		must.Error(t, err)
		test.ErrorIs(t, err, dataprivacy.ErrDuplicateKey)
		test.StrContains(t, err.Error(), auditerasure.DefaultKey)

		test.SliceEmpty(t, registry.CollectorKeys())
		test.Eq(t, []string{auditerasure.DefaultKey}, registry.EraserKeys())
	})
}

// shippedAdapters walks the module for every directory that declares a
// dataprivacy.Collector or a dataprivacy.Eraser, and returns the Default*Key
// constants each one ships, keyed by the directory's module-relative path.
//
// The conformance var is the ground truth rather than a list, because it is the
// house convention already — a var _ Iface = (*Impl)(nil) beside the type keeps
// conformance a compile-time fact — and because a package that ships an adapter
// without one is a package whose adapter nothing else can see either.
func shippedAdapters(t *testing.T) map[string][]string {
	t.Helper()

	found, err := adapterWalk()
	must.NoError(t, err)

	return found
}

var adapterWalk = sync.OnceValues(func() (map[string][]string, error) {
	root, err := filepath.Abs("..")
	if err != nil {
		return nil, err
	}

	found := map[string][]string{}
	fileSet := token.NewFileSet()

	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		if entry.IsDir() {
			if name := entry.Name(); name != "." && (strings.HasPrefix(name, ".") || name == "testdata") {
				return fs.SkipDir
			}

			return nil
		}

		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}

		parsed, parseErr := parser.ParseFile(fileSet, path, nil, parser.SkipObjectResolution)
		if parseErr != nil {
			return parseErr
		}

		if !declaresAdapter(parsed) {
			return nil
		}

		rel, relErr := filepath.Rel(root, filepath.Dir(path))
		if relErr != nil {
			return relErr
		}

		dir := filepath.ToSlash(rel)
		found[dir] = append(found[dir], defaultKeys(parsed)...)

		return nil
	})
	if err != nil {
		return nil, err
	}

	for dir, keys := range found {
		slices.Sort(keys)
		found[dir] = slices.Compact(keys)
	}

	return found, nil
})

// declaresAdapter reports whether a file carries a conformance var for either
// half of the dataprivacy contract.
func declaresAdapter(file *ast.File) bool {
	for _, decl := range file.Decls {
		general, ok := decl.(*ast.GenDecl)
		if !ok || general.Tok != token.VAR {
			continue
		}

		for _, spec := range general.Specs {
			value, isValue := spec.(*ast.ValueSpec)
			if !isValue || len(value.Names) != 1 || value.Names[0].Name != "_" {
				continue
			}

			selector, isSelector := value.Type.(*ast.SelectorExpr)
			if !isSelector {
				continue
			}

			pkg, isIdent := selector.X.(*ast.Ident)
			if !isIdent || pkg.Name != "dataprivacy" {
				continue
			}

			if selector.Sel.Name == "Collector" || selector.Sel.Name == "Eraser" {
				return true
			}
		}
	}

	return false
}

// defaultKeys returns the string values of every exported const whose name
// begins with Default and ends with Key.
func defaultKeys(file *ast.File) []string {
	var keys []string

	for _, decl := range file.Decls {
		general, ok := decl.(*ast.GenDecl)
		if !ok || general.Tok != token.CONST {
			continue
		}

		for _, spec := range general.Specs {
			value, isValue := spec.(*ast.ValueSpec)
			if !isValue {
				continue
			}

			for i, name := range value.Names {
				if !strings.HasPrefix(name.Name, "Default") || !strings.HasSuffix(name.Name, "Key") {
					continue
				}

				if i >= len(value.Values) {
					continue
				}

				literal, isLiteral := value.Values[i].(*ast.BasicLit)
				if !isLiteral || literal.Kind != token.STRING {
					continue
				}

				unquoted, err := strconv.Unquote(literal.Value)
				if err != nil {
					continue
				}

				keys = append(keys, unquoted)
			}
		}
	}

	return keys
}

func moduleRoot(t *testing.T) string {
	t.Helper()

	root, err := filepath.Abs("..")
	must.NoError(t, err)

	// The walk above reads the tree relative to this, so a wrong answer would
	// make every roster assertion vacuous rather than wrong.
	_, err = os.Stat(filepath.Join(root, "go.mod"))
	must.NoError(t, err)

	return root
}

func TestWalkFindsEveryAdapterDirectory(T *testing.T) {
	T.Parallel()

	// A guard on the guard: every assertion above is vacuous if the walk comes
	// back empty or short, and a walk that silently found nothing would make
	// this whole file pass while checking nothing.
	found := shippedAdapters(T)

	for _, dir := range []string{
		"authentication/oauth2clients/privacy",
		"authentication/passkeys/privacy",
		"authentication/passwordreset/privacy",
		"billing/privacy",
		"comments/privacy",
		"dataprivacy/auditerasure",
		"identity/privacy",
		"issuereports/privacy",
		"mediaregistry/privacy",
		"notifications/privacy",
		"settings/privacy",
		"waitlists/privacy",
	} {
		_, ok := found[dir]
		test.True(T, ok, test.Sprintf("the walk did not find %s, so nothing in this file asserted about it", dir))
	}

	test.MapLen(T, 12, found)
}
