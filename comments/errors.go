package comments

import (
	platformerrors "github.com/primandproper/platform-go/v14/errors"
)

// The sentinels this package returns. They live together because a caller
// deciding what to do next is choosing between them, and a set spread across the
// files that happen to return each one cannot be read as the set it is.
var (
	// ErrNilDatabaseClient indicates a nil database.Client. It wraps
	// errors.ErrNilInputParameter, so a caller may check either.
	ErrNilDatabaseClient = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil comments database client")

	// ErrNilComment indicates a nil *Comment where one was required.
	ErrNilComment = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil comment")

	// ErrNilExecutor indicates a nil executor. Every method here runs on one the
	// caller supplies — a database.Tx for a write, an executor for a read — so
	// there is no method that can fall back to a connection of the store's own.
	ErrNilExecutor = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil comments query executor")

	// ErrScopeMismatch indicates a write whose Comment.Scope names a different
	// tenant than the scope the call named.
	//
	// The argument is what the statement binds, so the two disagreeing is a
	// caller holding a comment from one tenant and writing it into another —
	// either a stale value or a mix-up, and neither is a thing to guess at. It is
	// the same reading ErrTargetMismatch takes of a reply that names a target its
	// parent is not on: an unset field adopts, and a set one that disagrees is
	// refused rather than corrected.
	ErrScopeMismatch = platformerrors.New("comment names a different scope than the write")

	// ErrCommentNotFound indicates a comment that does not exist in the scope
	// that asked. One belonging to another scope reads as absent — which is what
	// it is from here, and is the answer that does not turn a read into an oracle
	// for what other tenants have been saying.
	ErrCommentNotFound = platformerrors.New("comment not found")

	// ErrUnknownTargetType indicates a target type nobody registered.
	//
	// It is the catalog's whole job. A target type is a string underneath, and a
	// comment written under a misspelled one is stored, counted, and listed
	// nowhere — an absence somebody has to notice rather than an error somebody
	// is told about.
	ErrUnknownTargetType = platformerrors.New("unknown comment target type")

	// ErrTargetNotFound indicates a target the consumer's registered existence
	// check could not find.
	//
	// It is distinct from ErrUnknownTargetType: that one is a kind of thing this
	// application does not have, and this one is a kind of thing it has and one
	// of them that is not there. A client shown the first has a bug; a client
	// shown the second has a stale list.
	ErrTargetNotFound = platformerrors.New("comment target not found")

	// ErrEmptyTargetType indicates a target naming no kind of thing.
	ErrEmptyTargetType = platformerrors.New("empty comment target type")

	// ErrEmptyTargetID indicates a target naming a kind of thing and not one of
	// them. It is refused rather than stored, because the empty id is not a
	// wildcard: a comment holding it is about every recipe and no recipe at once.
	ErrEmptyTargetID = platformerrors.New("empty comment target id")

	// ErrEmptyAuthor indicates a comment written by nobody.
	//
	// It is refused rather than stored, because the empty author is not a
	// wildcard and is not "anonymous": a comment written under it is one no
	// author's list can find and one no subject access request can collect or
	// erase.
	ErrEmptyAuthor = platformerrors.New("empty comment author")

	// ErrEmptyBody indicates a comment with nothing in it. The body is the
	// comment; a row without one records that somebody pressed a button.
	ErrEmptyBody = platformerrors.New("empty comment body")

	// ErrEmptyParent indicates a read of one comment's replies that named no
	// comment. It is refused rather than answered with the roots, which is what
	// the empty parent means in the column and would be the wrong half of the
	// discussion returned without anything saying so.
	ErrEmptyParent = platformerrors.New("empty comment parent")

	// ErrParentNotFound indicates a reply to a comment that is not in the scope:
	// absent, archived, or somebody else's.
	//
	// It is distinct from ErrCommentNotFound because the missing row is not the
	// one the caller named — they named a reply, and what is missing is what they
	// are replying to.
	ErrParentNotFound = platformerrors.New("comment parent not found")

	// ErrNestedReply indicates a reply to a reply.
	//
	// Threads here are one level deep. A parent id admits any depth and the
	// reading of it does not: assembling an arbitrarily deep tree is a recursive
	// walk, which is neither one statement nor the same statement on the three
	// engines this package serves. See the package documentation.
	ErrNestedReply = platformerrors.New("a comment reply may not itself be replied to")

	// ErrTargetMismatch indicates a reply that names a different target than the
	// comment it replies to. A reply belongs to its parent's discussion; one that
	// could name another target would be a comment that appears under a thing
	// nobody said it about.
	ErrTargetMismatch = platformerrors.New("comment reply names a different target than its parent")
)
