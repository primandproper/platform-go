package authorization_test

import (
	"context"
	"fmt"

	"github.com/primandproper/platform-go/v13/authorization"
	"github.com/primandproper/platform-go/v13/authorization/static"
)

// Permissions are ordinary constants in the consuming package. A consumer that
// already has its own Permission type adopts this one with a type alias, which
// leaves every existing constant compiling unchanged.
const (
	readRecipes   authorization.Permission = "read.recipes"
	writeRecipes  authorization.Permission = "write.recipes"
	deleteRecipes authorization.Permission = "delete.recipes"
)

func Example() {
	// Policy declared in code. The same []Role would seed the database backend,
	// which is what keeps the two from drifting apart.
	resolver, err := static.NewResolver([]authorization.Role{
		{Name: "member", Permissions: []authorization.Permission{readRecipes}},
		{Name: "admin", Permissions: []authorization.Permission{writeRecipes}, Inherits: []string{"member"}},
		{Name: "owner", Permissions: []authorization.Permission{deleteRecipes}, Inherits: []string{"admin"}},
	})
	if err != nil {
		panic(err)
	}

	ctx := context.Background()

	// Resolved once, when a session is built — this is the half that may do I/O.
	perms, err := resolver.PermissionsForRoles(ctx, "admin")
	if err != nil {
		panic(err)
	}

	// Checked many times per request, against a value that cannot fail.
	grants := authorization.NewGrants(perms)

	fmt.Println("write:", grants.Has(writeRecipes))
	fmt.Println("read (inherited):", grants.Has(readRecipes))
	fmt.Println("delete:", grants.Has(deleteRecipes))

	// Evaluate answers a batch at once, which is what a client needs to decide
	// which controls to render.
	for _, perm := range []authorization.Permission{readRecipes, writeRecipes, deleteRecipes} {
		fmt.Printf("%s=%t ", perm, grants.Evaluate(readRecipes, writeRecipes, deleteRecipes)[perm])
	}
	fmt.Println()

	// Output:
	// write: true
	// read (inherited): true
	// delete: false
	// read.recipes=true write.recipes=true delete.recipes=false
}

// A principal with authority in more than one scope hands each set to
// NewGrants. Nil sets are dropped, so an administrator acting on a tenant they
// do not belong to needs no special case.
func ExampleNewGrants() {
	serviceWide := authorization.NewPermissionSet(deleteRecipes)

	var tenantScoped *authorization.PermissionSet // no membership in this tenant

	grants := authorization.NewGrants(serviceWide, tenantScoped)

	fmt.Println("delete:", grants.Has(deleteRecipes))
	fmt.Println("read:", grants.Has(readRecipes))

	// Output:
	// delete: true
	// read: false
}

// The zero value denies everything, so authority that was never populated is
// safe by construction rather than by remembering to check.
func ExampleGrants_zeroValue() {
	var grants authorization.Grants

	fmt.Println("read:", grants.Has(readRecipes))
	fmt.Println("empty:", grants.IsEmpty())

	// Output:
	// read: false
	// empty: true
}
