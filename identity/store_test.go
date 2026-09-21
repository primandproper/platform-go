package identity

import (
	"reflect"
	"testing"

	"github.com/shoenig/test"
)

// TestStore_PartitionsItsMethods holds Store's nine constituent interfaces to a
// partition: every method in exactly one of them.
//
// Go permits overlapping embedded interfaces, so a method added to two of these
// compiles and the duplication is invisible — until a caller reaching for the
// narrow interface finds it can do something the name does not say. Comparing
// the summed method counts against Store's is the cheapest thing that notices,
// and it also fails when a method is added to Store's union without landing in
// any of them.
func TestStore_PartitionsItsMethods(t *testing.T) {
	t.Parallel()

	parts := []struct {
		typ  reflect.Type
		name string
	}{
		{reflect.TypeFor[Registrar](), "Registrar"},
		{reflect.TypeFor[CredentialStore](), "CredentialStore"},
		{reflect.TypeFor[SignInReader](), "SignInReader"},
		{reflect.TypeFor[DirectoryReader](), "DirectoryReader"},
		{reflect.TypeFor[ProfileWriter](), "ProfileWriter"},
		{reflect.TypeFor[MembershipWriter](), "MembershipWriter"},
		{reflect.TypeFor[AdminWriter](), "AdminWriter"},
		{reflect.TypeFor[BillingWriter](), "BillingWriter"},
		{reflect.TypeFor[InvitationStore](), "InvitationStore"},
		{reflect.TypeFor[SearchIndexWriter](), "SearchIndexWriter"},
	}

	seen := map[string]string{}
	total := 0

	for _, part := range parts {
		total += part.typ.NumMethod()

		for method := range part.typ.Methods() {
			method := method.Name
			if other, ok := seen[method]; ok {
				t.Errorf("%s is in both %s and %s", method, other, part.name)
			}

			seen[method] = part.name
		}
	}

	storeType := reflect.TypeFor[Store]()
	test.EqOp(t, storeType.NumMethod(), total)

	// Named separately from the count so a method that exists on Store and in
	// none of the nine is reported as the missing name rather than as a number
	// that is one too small.
	for method := range storeType.Methods() {
		if method := method.Name; seen[method] == "" {
			t.Errorf("Store.%s is in none of the ten interfaces", method)
		}
	}
}

// storeMethodCount is how many methods Store carries, read off the interface
// rather than written down.
//
// The nil-executor case in the caller-transaction suite compares its table
// against this, so a method added to Store and not to that table fails as a
// count rather than as an absence nobody notices.
func storeMethodCount() int { return reflect.TypeFor[Store]().NumMethod() }

// TestSQLStore_SQLite runs the behavioral suite against SQLite, which every
// developer has and every CI run executes. The same suite runs against real
// servers in containers_test.go.
func TestSQLStore_SQLite(T *testing.T) {
	T.Parallel()

	runStoreSuite(T, newSQLiteEnv(T))
}

// runStoreSuite is the whole behavioral contract, run once per dialect.
//
// It is one function rather than a file of top-level tests so that the
// Postgres, MySQL, and SQLite runs cannot drift: a case added for one dialect
// is a case added for all three, which is the only way the ON CONFLICT/ON
// DUPLICATE KEY split and the partial-index difference stay honest.
//
// One sub-suite per interface, each in the file that holds the methods it
// exercises, so a method and its cases move together.
func runStoreSuite(t *testing.T, env *storeEnv) {
	t.Helper()

	suites := []struct {
		run  func(*testing.T, *storeEnv)
		name string
	}{
		{name: "registrar", run: runRegistrarSuite},
		{name: "credentials", run: runCredentialStoreSuite},
		{name: "sign-in", run: runSignInReaderSuite},
		{name: "directory", run: runDirectoryReaderSuite},
		{name: "profiles", run: runProfileWriterSuite},
		{name: "memberships", run: runMembershipWriterSuite},
		{name: "admin", run: runAdminWriterSuite},
		{name: "billing", run: runBillingWriterSuite},
		{name: "invitations", run: runInvitationStoreSuite},
		{name: "handle folding", run: runHandleFoldingSuite},
		{name: "token digests", run: runTokenDigestSuite},
		{name: "service", run: runServiceSuite},
		{name: "credential service", run: runCredentialServiceSuite},
		{name: "transactions", run: runCallerTransactionSuite},
		{name: "search index", run: runSearchIndexSuite},
		{name: "timestamps", run: runClockSuite},
	}

	for _, suite := range suites {
		t.Run(suite.name, func(t *testing.T) {
			t.Parallel()
			suite.run(t, env)
		})
	}
}
