package grpc_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/primandproper/platform-go/v14/billing/billingpb"
	billinggrpc "github.com/primandproper/platform-go/v14/billing/grpc"

	authzgrpc "github.com/primandproper/primitives-go/v2/authorization/grpc"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// serviceMethods is every RPC the generated service descriptor declares, as the
// full method names the interceptor matches on.
//
// It is read off the descriptor rather than written down, which is the whole
// point: a list here would be a third place to forget an RPC, and the tests
// below exist because the first two are easy to forget.
func serviceMethods() []string {
	prefix := "/" + billingpb.BillingService_ServiceDesc.ServiceName + "/"

	out := make([]string, 0, len(billingpb.BillingService_ServiceDesc.Methods))
	for _, m := range billingpb.BillingService_ServiceDesc.Methods {
		out = append(out, prefix+m.MethodName)
	}

	return out
}

// TestEveryMethodIsPermissioned is the invariant this service chose: there is no
// second set of methods here that require nothing, the catalog reads included.
//
// A method missing from the map is denied by a consumer's fail-closed enforcer,
// in production, for a reason nobody would connect to this package.
func TestEveryMethodIsPermissioned(T *testing.T) {
	T.Parallel()

	methods := serviceMethods()
	must.SliceNotEmpty(T, methods, must.Sprint("no methods on the service descriptor, so this asserted nothing"))

	permissions := billinggrpc.Permissions()

	for _, method := range methods {
		T.Run(method, func(t *testing.T) {
			t.Parallel()

			_, permissioned := permissions[method]
			test.True(t, permissioned, test.Sprintf(
				"%s is not in Permissions, so nothing says who may call it", method))
		})
	}
}

// TestNoDecisionOutlivesItsMethod is the other direction: a decision naming an
// RPC that no longer exists is a permission a consumer is still granting for
// nothing, and a rename would leave the real method undeclared and therefore
// denied.
func TestNoDecisionOutlivesItsMethod(T *testing.T) {
	T.Parallel()

	methods := serviceMethods()

	for method := range billinggrpc.Permissions() {
		test.True(T, slices.Contains(methods, method), test.Sprintf(
			"Permissions names %s, which the service descriptor does not declare", method))
	}
}

// TestNoPermissionIsEmpty catches the decision that looks made and is not: an
// entry mapping a method to an empty slice declares it and requires nothing.
func TestNoPermissionIsEmpty(T *testing.T) {
	T.Parallel()

	for method, permissions := range billinggrpc.Permissions() {
		test.SliceNotEmpty(T, permissions, test.Sprintf(
			"%s is declared with no permissions, which grants it to everybody", method))
	}
}

// TestTheScopeWideReadsHaveTheirOwnGrant is the distinction this file is shaped
// around, asserted rather than described.
//
// Reading your own invoices and enumerating every account's are not the same
// grant. If ListTransactions ever shared a permission with
// ListTransactionsForAccount, a consumer handing a customer portal the second
// would be handing it the ledger — and nothing else in the tree would say so.
func TestTheScopeWideReadsHaveTheirOwnGrant(T *testing.T) {
	T.Parallel()

	permissions := billinggrpc.Permissions()

	for _, pair := range []struct {
		operator string
		own      string
	}{
		{
			operator: billingpb.BillingService_ListSubscriptions_FullMethodName,
			own:      billingpb.BillingService_ListSubscriptionsForAccount_FullMethodName,
		},
		{
			operator: billingpb.BillingService_ListPurchases_FullMethodName,
			own:      billingpb.BillingService_ListPurchasesForAccount_FullMethodName,
		},
		{
			operator: billingpb.BillingService_ListTransactions_FullMethodName,
			own:      billingpb.BillingService_ListTransactionsForAccount_FullMethodName,
		},
	} {
		T.Run(pair.operator, func(t *testing.T) {
			t.Parallel()

			operator, own := permissions[pair.operator], permissions[pair.own]
			must.SliceNotEmpty(t, operator)
			must.SliceNotEmpty(t, own)

			for _, p := range own {
				test.SliceNotContains(t, operator, p, test.Sprintf(
					"%s shares %q with the per-account read, so granting one grants the other", pair.operator, p))
			}

			test.StrContains(t, string(operator[0]), "list_all", test.Sprintf(
				"%s does not require a list_all grant", pair.operator))
		})
	}
}

// TestNoPermissionIsSharedAcrossNouns keeps the catalog's grants out of the
// ledger's and back. A consumer stocking products should not thereby be able to
// archive charges.
func TestNoPermissionIsSharedAcrossNouns(T *testing.T) {
	T.Parallel()

	nouns := map[string]string{
		"products":      "billing.products.",
		"subscriptions": "billing.subscriptions.",
		"purchases":     "billing.purchases.",
		"transactions":  "billing.transactions.",
	}

	for method, permissions := range billinggrpc.Permissions() {
		for _, p := range permissions {
			matched := 0
			for _, prefix := range nouns {
				if strings.HasPrefix(string(p), prefix) {
					matched++
				}
			}

			test.EqOp(T, 1, matched, test.Sprintf(
				"%s requires %q, which names no single billing noun", method, p))
		}
	}
}

// TestPermissionsReturnsAFreshMap keeps a consumer's override from editing every
// other consumer's copy in the same process.
func TestPermissionsReturnsAFreshMap(T *testing.T) {
	T.Parallel()

	first := billinggrpc.Permissions()
	for method := range first {
		delete(first, method)
	}

	test.MapNotEmpty(T, billinggrpc.Permissions(),
		test.Sprint("Permissions handed back a map a caller could empty for everybody"))
}

// TestRequireDeclaresEveryMethod is the check a consumer writing the RequireAll
// themselves would not have. authorization/grpc is fail-closed, so a method
// Require left out is denied, and a denial for want of a declaration looks
// exactly like a policy somebody meant.
func TestRequireDeclaresEveryMethod(T *testing.T) {
	T.Parallel()

	reqs, err := billinggrpc.Require(authzgrpc.NewRequirements()).Build()
	must.NoError(T, err)

	declared := reqs.Methods()

	for _, method := range serviceMethods() {
		test.SliceContains(T, declared, method, test.Sprintf(
			"%s is not declared after Require, so the enforcer will deny it as undeclared", method))
	}
}

// TestRequireToleratesANilBuilder keeps a chain composing several domains'
// fragments from needing a nil check per link.
func TestRequireToleratesANilBuilder(T *testing.T) {
	T.Parallel()

	test.Nil(T, billinggrpc.Require(nil))
}
