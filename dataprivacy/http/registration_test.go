package http

import (
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// TestNoMapperRegistration pins the acceptance criterion that this surface
// registers nothing with the error registries.
//
// operations/http.New registers its own HTTP mapper and is the module's single
// exception; this package sits beside it and does not follow it there, because
// one door stays one door — errormappers.Register is where the domain tier's
// mappers are installed, and a second surface registering for itself makes
// "which mappers does this process answer with" a question about which handlers
// happen to have been constructed.
//
// It reads the package's own source because the property is an absence, and an
// absence cannot be observed from a request: the registries are process-global
// and additive, so a test asserting that a status is a 500 would be asserting
// that nothing else in the binary had registered the mapper yet.
func TestNoMapperRegistration(T *testing.T) {
	T.Parallel()

	forbiddenImports := []string{
		"github.com/primandproper/primitives-go/v2/errors/http",
		"github.com/primandproper/primitives-go/v2/errors/grpc",
	}

	forbiddenCalls := []string{
		"RegisterHTTPErrorMapper",
		"RegisterGRPCErrorMapper",
		"RegisterClientSafeSentinels",
	}

	entries, err := os.ReadDir(".")
	must.NoError(T, err)

	sourceFiles := 0

	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}

		sourceFiles++

		contents, readErr := os.ReadFile(name)
		must.NoError(T, readErr)

		for _, call := range forbiddenCalls {
			test.StrNotContains(T, string(contents), call,
				test.Sprintf("%s calls %s; registration is errormappers.Register's", name, call))
		}

		file, parseErr := parser.ParseFile(token.NewFileSet(), name, contents, 0)
		must.NoError(T, parseErr)

		for _, imported := range file.Imports {
			path, unquoteErr := strconv.Unquote(imported.Path.Value)
			must.NoError(T, unquoteErr)

			for _, forbidden := range forbiddenImports {
				test.NotEqOp(T, forbidden, path,
					test.Sprintf("%s imports %s; this surface maps nothing itself", name, forbidden))
			}
		}
	}

	// A walk that found nothing would pass every assertion above.
	test.Greater(T, 0, sourceFiles)
}
