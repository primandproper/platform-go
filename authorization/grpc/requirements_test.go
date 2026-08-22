package grpc

import (
	"errors"
	"testing"

	"github.com/primandproper/platform-go/v13/authorization"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

const (
	permRead  authorization.Permission = "read.things"
	permWrite authorization.Permission = "write.things"

	methodRead   = "/things.Things/Read"
	methodWrite  = "/things.Things/Write"
	methodHealth = "/grpc.health.v1.Health/Check"
)

func TestRequirementsBuilder_Build(T *testing.T) {
	T.Parallel()

	T.Run("builds a valid table", func(t *testing.T) {
		t.Parallel()

		reqs, err := NewRequirements().
			Require(methodRead, permRead).
			Public(methodHealth).
			Build()
		must.NoError(t, err)

		test.Eq(t, []string{methodHealth, methodRead}, reqs.Methods())
	})

	T.Run("an empty table is valid", func(t *testing.T) {
		t.Parallel()

		reqs, err := NewRequirements().Build()
		must.NoError(t, err)

		test.SliceEmpty(t, reqs.Methods())
	})

	// A requirement with no permissions reads like a restriction and behaves
	// like an allow, via vacuous truth. Refusing it here is what lets
	// PermissionSet.HasAll keep the mathematically honest semantics.
	T.Run("rejects a method required with no permissions", func(t *testing.T) {
		t.Parallel()

		_, err := NewRequirements().Require(methodRead).Build()

		test.True(t, errors.Is(err, ErrNoPermissionsRequired))
	})

	T.Run("rejects an empty method name", func(t *testing.T) {
		t.Parallel()

		_, err := NewRequirements().Require("", permRead).Build()

		test.True(t, errors.Is(err, ErrEmptyMethod))
	})

	T.Run("rejects an empty permission", func(t *testing.T) {
		t.Parallel()

		_, err := NewRequirements().Require(methodRead, permRead, "").Build()

		test.True(t, errors.Is(err, ErrEmptyPermission))
	})

	// Two service packages contributing overlapping tables is the realistic
	// way this happens, and silently taking the last one is how a method ends
	// up guarded by the wrong permission.
	T.Run("rejects a method declared twice", func(t *testing.T) {
		t.Parallel()

		_, err := NewRequirements().
			Require(methodRead, permRead).
			Require(methodRead, permWrite).
			Build()

		test.True(t, errors.Is(err, ErrDuplicateMethod))
	})

	T.Run("rejects a method both required and public", func(t *testing.T) {
		t.Parallel()

		_, err := NewRequirements().
			Require(methodRead, permRead).
			Public(methodRead).
			Build()

		test.True(t, errors.Is(err, ErrDuplicateMethod))
	})

	// A table assembled from a dozen service packages usually has more than one
	// problem, and fixing them one restart at a time is miserable.
	T.Run("reports every problem at once", func(t *testing.T) {
		t.Parallel()

		_, err := NewRequirements().
			Require(methodRead).
			Require(methodWrite, "").
			Build()

		must.Error(t, err)
		test.True(t, errors.Is(err, ErrNoPermissionsRequired))
		test.True(t, errors.Is(err, ErrEmptyPermission))
	})
}

func TestRequirementsBuilder_RequireAll(T *testing.T) {
	T.Parallel()

	T.Run("merges a method map", func(t *testing.T) {
		t.Parallel()

		reqs, err := NewRequirements().
			RequireAll(map[string][]authorization.Permission{
				methodRead:  {permRead},
				methodWrite: {permWrite},
			}).
			Build()
		must.NoError(t, err)

		test.Eq(t, []string{methodRead, methodWrite}, reqs.Methods())
	})

	T.Run("detects collisions across merged maps", func(t *testing.T) {
		t.Parallel()

		_, err := NewRequirements().
			RequireAll(map[string][]authorization.Permission{methodRead: {permRead}}).
			RequireAll(map[string][]authorization.Permission{methodRead: {permWrite}}).
			Build()

		test.True(t, errors.Is(err, ErrDuplicateMethod))
	})
}

func TestRequirements_Immutability(T *testing.T) {
	T.Parallel()

	// The table is frozen so the interceptors need no lock; that only holds if
	// the builder cannot reach it afterwards.
	T.Run("the builder cannot alter a built table", func(t *testing.T) {
		t.Parallel()

		builder := NewRequirements().Require(methodRead, permRead)

		reqs, err := builder.Build()
		must.NoError(t, err)

		builder.Require(methodWrite, permWrite)

		test.Eq(t, []string{methodRead}, reqs.Methods())
	})

	T.Run("a caller's permission slice cannot alter the table", func(t *testing.T) {
		t.Parallel()

		perms := []authorization.Permission{permRead}

		reqs, err := NewRequirements().Require(methodRead, perms...).Build()
		must.NoError(t, err)

		perms[0] = permWrite

		got, _, _ := reqs.lookup(methodRead)
		test.Eq(t, []authorization.Permission{permRead}, got)
	})
}
