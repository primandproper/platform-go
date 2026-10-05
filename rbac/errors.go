package rbac

import (
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
)

var (
	// ErrInvalidTablePrefix indicates a prefix that is not a plain SQL
	// identifier fragment. Prefixes are interpolated into queries rather than
	// bound, so they are restricted rather than escaped.
	ErrInvalidTablePrefix = platformerrors.New("invalid authorization table prefix")
	// ErrNilExecutor indicates a query executor was required and not supplied.
	// It wraps errors.ErrNilInputParameter, so a caller may check either.
	ErrNilExecutor = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil authorization database query executor")
	// ErrWrittenNameMissing indicates a role or permission that was written by
	// name inside the caller's transaction and then could not be read back in
	// that same transaction. Every dialect this package runs against shows a
	// transaction its own writes, so this is a broken invariant rather than a
	// state a caller can reach or recover from by retrying; it exists so that
	// the failure names itself instead of surfacing later as a foreign-key
	// violation on a grant.
	ErrWrittenNameMissing = platformerrors.New("a role or permission written by name could not be read back")

	// ErrPermissionInTwoTiers indicates a permission placed in more than one
	// tier of a Tiers, or by two surfaces MergeTiers was handed.
	ErrPermissionInTwoTiers = platformerrors.New("permission placed in more than one tier")
	// ErrUntieredPermission indicates a permission a surface requires or
	// consults that its Tiers places nowhere.
	ErrUntieredPermission = platformerrors.New("permission a surface checks is in no tier")
	// ErrUncheckedPermission indicates a permission a Tiers places that the
	// surface neither requires on a method nor consults in a handler, so a
	// role holding it is granted something nothing checks.
	ErrUncheckedPermission = platformerrors.New("tiered permission is checked by nothing")
	// ErrInvalidNarrowing indicates a narrowing that names a method requiring
	// no permission, names no authorizer, is not below its permission's tier,
	// or is stated two different ways.
	ErrInvalidNarrowing = platformerrors.New("invalid tier narrowing")
	// ErrUnknownTier indicates a Tier that is none of TierMember,
	// TierTenantAdmin and TierOperator.
	ErrUnknownTier = platformerrors.New("unknown permission tier")
)
