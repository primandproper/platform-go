package conformance

import (
	"testing"

	"github.com/primandproper/platform-go/v14/authentication/signin/signinpb"
)

// SignedIn is the caller a token the sign-in surface issued makes calls as,
// skipping where the subject supplies no Seams.SignedIn.
//
// opts are Making, naming the calls the caller goes on to make, and InTenant,
// naming the tenant the token was issued in, which becomes the caller's Scope.
// Who the caller is — member or administrator — is the token's to say rather
// than the suite's, so AsAdmin and AsMember are refused. The token's own
// Administrative flag decides what a reservation does: a declared call the
// subject reserves in Seams.OperatorMethods skips the test for a token from the
// ordinary door, as AsMember does, since the deployment promised no member
// anything about it.
//
// The caller is held to its declaration on its own connection, as every caller
// Subject mints is, and reaches every surface the subject mounts over it. Its
// UserID is empty, since a token names nobody a client can read; its AccountID
// is the account the token was issued for.
func (s *Session) SignedIn(t *testing.T, issued *signinpb.IssuedToken, opts ...SubjectOption) *Subject {
	t.Helper()

	dial := s.seams.SignedIn
	if dial == nil {
		Skip(t, "conformance: this subject supplies no SignedIn seam, so a token the suite signed in for cannot be called with; skipping")

		return nil
	}

	if issued.GetToken() == "" {
		t.Fatal("conformance: a caller was asked for from a token that carries none")

		return nil
	}

	req := NewSubjectRequest(opts...)
	if req.Admin || req.member {
		t.Fatal("conformance: a signed-in caller's standing is its token's, and cannot be asked for as a member or an administrator")

		return nil
	}

	if reserved := s.reservedAmong(req.Methods); reserved != "" && !issued.GetAdministrative() {
		Skipf(t, "conformance: this subject reserves %s to an operator, and this caller signed in through the ordinary door; skipping", reserved)

		return nil
	}

	conn, err := dial(t.Context(), issued)
	switch {
	case err != nil:
		t.Fatalf("conformance: turning an issued token into a caller: %v", err)

		return nil
	case conn == nil:
		t.Fatal("conformance: the SignedIn seam returned no connection and no error")

		return nil
	}

	sub := &Subject{
		Conn:      conn,
		Surfaces:  s.mounted,
		AccountID: issued.GetActiveAccountId(),
	}
	if req.Scope != nil {
		sub.Scope = *req.Scope
	}

	return declare(t, sub, req.Methods)
}
