package grpc_test

import (
	"testing"

	"github.com/primandproper/platform-go/v14/identity/identitypb"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TestRecordAgreementStampsTheDocumentsNamed: the subject is the caller, and
// naming several stamps them with one clock read — accepting two records one
// moment rather than two a later comparison could order.
func TestRecordAgreementStampsTheDocumentsNamed(T *testing.T) {
	T.Parallel()

	h := newHarness(T)

	user := h.seedUser(T, testScope, "somebody")
	ctx := h.as(&testPrincipal{userID: user.ID, scope: testScope})

	response, err := h.client.RecordAgreement(ctx, &identitypb.RecordAgreementRequest{
		Agreements: []identitypb.Agreement{
			identitypb.Agreement_AGREEMENT_TERMS_OF_SERVICE,
			identitypb.Agreement_AGREEMENT_PRIVACY_POLICY,
		},
	})
	must.NoError(T, err)

	accepted := response.GetUser()
	must.NotNil(T, accepted.GetLastAcceptedTermsOfService())
	must.NotNil(T, accepted.GetLastAcceptedPrivacyPolicy())
	test.EqOp(T,
		accepted.GetLastAcceptedTermsOfService().AsTime(),
		accepted.GetLastAcceptedPrivacyPolicy().AsTime(),
		test.Sprint("two documents accepted in one call were stamped at two moments"))
}

// TestRecordAgreementRefusesAnUnsetDocument is the reason AgreementFromProto
// treats UNSPECIFIED as an error rather than defaulting: this enum decides which
// compliance column gets stamped, and stamping the wrong one is a record that
// says somebody agreed to something they did not read.
func TestRecordAgreementRefusesAnUnsetDocument(T *testing.T) {
	T.Parallel()

	h := newHarness(T)

	user := h.seedUser(T, testScope, "somebody")
	ctx := h.as(&testPrincipal{userID: user.ID, scope: testScope})

	_, err := h.client.RecordAgreement(ctx, &identitypb.RecordAgreementRequest{
		Agreements: []identitypb.Agreement{
			identitypb.Agreement_AGREEMENT_TERMS_OF_SERVICE,
			identitypb.Agreement_AGREEMENT_UNSPECIFIED,
		},
	})
	must.Error(T, err)
	test.EqOp(T, codes.InvalidArgument, status.Code(err))

	// The whole list is refused for one bad entry, so nothing was stamped.
	read, err := h.client.GetUser(h.ctx(), &identitypb.GetUserRequest{UserId: user.ID})
	must.NoError(T, err)
	test.Nil(T, read.GetUser().GetLastAcceptedTermsOfService(),
		test.Sprint("a refused list stamped the entries it had read before the bad one"))
}

// TestSetUserServiceRolesReplacesAndCanWithdraw is the write that grants and
// withdraws operator access. An empty set is allowed here, deliberately: it is
// how the access is taken away.
func TestSetUserServiceRolesReplacesAndCanWithdraw(T *testing.T) {
	T.Parallel()

	h := newHarness(T)

	user := h.seedUser(T, testScope, "somebody")

	granted, err := h.client.SetUserServiceRoles(h.ctx(), &identitypb.SetUserServiceRolesRequest{
		UserId: user.ID,
		Roles:  []string{"service_admin"},
	})
	must.NoError(T, err)
	test.Eq(T, []string{"service_admin"}, granted.GetUser().GetServiceRoles())

	withdrawn, err := h.client.SetUserServiceRoles(h.ctx(), &identitypb.SetUserServiceRolesRequest{
		UserId: user.ID,
		Roles:  nil,
	})
	must.NoError(T, err)
	test.SliceEmpty(T, withdrawn.GetUser().GetServiceRoles(),
		test.Sprint("a merging setter cannot revoke, and this one has to"))
}

func TestSearchUsersByUsernameMatchesThePrefixInTheCallersDirectory(T *testing.T) {
	T.Parallel()

	h := newHarness(T)

	h.seedUser(T, testScope, "alexander")
	h.seedUser(T, testScope, "alexandra")
	h.seedUser(T, testScope, "bartholomew")
	h.seedUser(T, otherScope, "alexis")

	page, err := h.client.SearchUsersByUsername(h.ctx(),
		&identitypb.SearchUsersByUsernameRequest{Prefix: "alex"})
	must.NoError(T, err)
	test.NotNil(T, page.GetPagination(), test.Sprint("a paged read answered with no pagination"))

	usernames := make([]string, 0, len(page.GetResults()))
	for _, u := range page.GetResults() {
		usernames = append(usernames, u.GetUsername())
	}

	test.SliceContains(T, usernames, "alexander")
	test.SliceContains(T, usernames, "alexandra")
	test.SliceNotContains(T, usernames, "bartholomew")

	// The prefix is not a way out of the directory: the neighbor matches it and
	// is not here.
	test.SliceNotContains(T, usernames, "alexis")
}
