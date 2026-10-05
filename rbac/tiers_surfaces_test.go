package rbac_test

import (
	"fmt"
	"testing"

	auditgrpc "github.com/primandproper/platform-go/v14/audit/grpc"
	oauth2clientsgrpc "github.com/primandproper/platform-go/v14/authentication/oauth2clients/grpc"
	passkeysgrpc "github.com/primandproper/platform-go/v14/authentication/passkeys/grpc"
	passwordresetgrpc "github.com/primandproper/platform-go/v14/authentication/passwordreset/grpc"
	signingrpc "github.com/primandproper/platform-go/v14/authentication/signin/grpc"
	billinggrpc "github.com/primandproper/platform-go/v14/billing/grpc"
	commentsgrpc "github.com/primandproper/platform-go/v14/comments/grpc"
	identitygrpc "github.com/primandproper/platform-go/v14/identity/grpc"
	issuereportsgrpc "github.com/primandproper/platform-go/v14/issuereports/grpc"
	mediaregistrygrpc "github.com/primandproper/platform-go/v14/mediaregistry/grpc"
	notificationsgrpc "github.com/primandproper/platform-go/v14/notifications/grpc"
	"github.com/primandproper/platform-go/v14/rbac"
	settingsgrpc "github.com/primandproper/platform-go/v14/settings/grpc"
	waitlistsgrpc "github.com/primandproper/platform-go/v14/waitlists/grpc"
	webhooksgrpc "github.com/primandproper/platform-go/v14/webhooks/grpc"

	"github.com/shoenig/test/must"
)

// everySurface is every gRPC surface this module ships, in the order a
// deployment mounting all of them would merge their tiers.
func everySurface() []rbac.Tiers {
	return []rbac.Tiers{
		auditgrpc.Tiers(),
		billinggrpc.Tiers(),
		commentsgrpc.Tiers(),
		identitygrpc.Tiers(),
		issuereportsgrpc.Tiers(),
		mediaregistrygrpc.Tiers(),
		notificationsgrpc.Tiers(),
		settingsgrpc.Tiers(),
		waitlistsgrpc.Tiers(),
		webhooksgrpc.Tiers(),
		oauth2clientsgrpc.Tiers(),
		passkeysgrpc.Tiers(),
		passwordresetgrpc.Tiers(),
		signingrpc.Tiers(),
	}
}

// TestMergeTiers_EverySurface is the composition a deployment mounting every
// surface makes. Each surface's own test holds its tiers to its methods; this
// is what catches two surfaces that share a permission and disagree about
// whose it is.
func TestMergeTiers_EverySurface(T *testing.T) {
	T.Parallel()

	merged, err := rbac.MergeTiers(everySurface()...)
	must.NoError(T, err)

	policy := rbac.PolicyFromTiers(merged, rbac.RoleNames{
		Operator:    "service_admin",
		TenantAdmin: "account_admin",
		Member:      "account_member",
	})
	must.NoError(T, policy.Validate())
}

// A deployment's policy, composed from the surfaces it mounts. The role names
// are its own; which grant belongs to which kind of principal is the surfaces'.
func ExamplePolicyFromTiers() {
	tiers, err := rbac.MergeTiers(
		identitygrpc.Tiers(),
		waitlistsgrpc.Tiers(),
		commentsgrpc.Tiers(),
	)
	if err != nil {
		panic(err)
	}

	policy := rbac.PolicyFromTiers(tiers, rbac.RoleNames{
		Operator:    "staff",
		TenantAdmin: "account_admin",
		Member:      "member",
	})

	for i := range policy.Roles {
		fmt.Println(policy.Roles[i].Name, policy.Roles[i].Inherits)
	}

	// Output:
	// member []
	// account_admin [member]
	// staff [account_admin]
}
