package conformance

import (
	"slices"
	"strings"
	"testing"
)

// Making names the calls the caller being minted goes on to make, as the full
// method names a client invokes — identitypb.IdentityService_GetUser_FullMethodName
// and its siblings.
//
// Every caller a suite mints names them, and that is what lets a deployment
// reserve any call it likes. Which calls a deployment keeps from its members is
// its product's decision and never this module's: a customer dispute desk may
// reserve commenting to its staff, a household app may let every member stock
// the catalog, and a suite that assumed either would be asserting one
// consumer's access scheme against every other. So the suite says what the
// caller will do, the subject says in Seams.OperatorMethods what it reserves,
// and Session.Subject mints an administrator where the two meet and an ordinary
// caller where they do not.
//
// Naming a call is not a claim that the caller is allowed it. It is what the
// caller is minted to attempt — a refusal a suite asserts is made by a caller
// that names the refused call, so that the code asserted is the handler's
// rather than the reservation's.
func Making(methods ...string) SubjectOption {
	return func(r *SubjectRequest) { r.Methods = append(r.Methods, methods...) }
}

// AsMember asks for a caller holding no administrative standing at all, for an
// assertion about what an ordinary member is refused or is not shown.
//
// Where the subject reserves any call the caller names, no member can make it,
// and the assertion skips with the reservation named: what a deployment that
// keeps a call from its members promises its members about that call is
// nothing, and a suite that minted an administrator anyway would assert the
// refused half against somebody who is not refused. AsMember with AsAdmin is a
// contradiction and fails the test.
func AsMember() SubjectOption {
	return func(r *SubjectRequest) { r.member = true }
}

// Reserves reports whether the subject reserves method to an operator, by
// naming it in Seams.OperatorMethods.
func (s *Session) Reserves(method string) bool {
	return slices.Contains(s.seams.OperatorMethods, method)
}

// NeedsPublic skips the test where the subject reserves any of methods to an
// operator, for an assertion about a call made with nobody on it.
//
// A call a module declares reachable without a caller — a sign-in door, a
// signup page — is still a call a deployment may keep to its staff, and one
// that does has taken it off the public list. What this module promises a
// visitor about that call is then nothing the deployment offers.
func (s *Session) NeedsPublic(t *testing.T, methods ...string) {
	t.Helper()

	if reserved := s.reservedAmong(methods); reserved != "" {
		t.Skipf("conformance: this subject reserves %s to an operator, so nobody reaches it without a caller; skipping", reserved)
	}
}

// reservedAmong is the first of methods the subject reserves, or empty where it
// reserves none of them.
func (s *Session) reservedAmong(methods []string) string {
	for _, method := range methods {
		if s.Reserves(method) {
			return method
		}
	}

	return ""
}

// checkOperatorMethods fails a run whose Seams.OperatorMethods names something
// that is not a full method name, since an entry spelled any other way reserves
// nothing and would leave the suites making that call as an ordinary caller
// while the deployment refuses one.
//
// It checks the spelling and nothing else. Which calls a deployment reserves is
// its own to say, on any service, covered by a suite or not.
func checkOperatorMethods(t *testing.T, methods []string) {
	t.Helper()

	for _, method := range methods {
		if !isFullMethodName(method) {
			t.Fatalf("conformance: Seams.OperatorMethods names %q, which is not a full method name of the form /package.Service/Method", method)
		}
	}
}

// isFullMethodName reports whether method reads as /package.Service/Method.
func isFullMethodName(method string) bool {
	rest, ok := strings.CutPrefix(method, "/")
	if !ok {
		return false
	}

	service, name, ok := strings.Cut(rest, "/")

	return ok && strings.Contains(service, ".") && name != "" && !strings.Contains(name, "/")
}
