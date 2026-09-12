package comments

import (
	"context"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/filtering"
	"github.com/primandproper/primitives-go/v2/tenancy"
)

// Store is the persistence seam for comments.
//
// This package ships a SQL implementation ([NewSQLStore]) together with the DDL
// it needs (comments/migrations), so adopting it does not mean writing this. The
// interface exists because a discussion and its storage are genuinely separable,
// and an application with its own schema conventions should not have to fork the
// package to keep them.
//
// # The transaction is the caller's
//
// Every write takes a database.Tx and every read takes the wider
// database.SQLQueryExecutor, which is the module's store convention rather than
// anything this package invented. There is no form of any write that opens a
// transaction of its own, and that absence is the point: a comment is rarely the
// only row a consumer writes. An audit entry naming who said it and a data change
// event on an outbox somebody fans out are the ordinary companions, and a
// companion written after the comment's own write has committed is a companion
// that can go missing while the comment stays. A signature that cannot express
// that is better than a doc that warns against it.
//
// The read takes the wider type so that one method serves both moments. A
// consumer listing a discussion for a page holds no transaction and passes
// Client.Reader(); a consumer that has just written a comment passes the Tx it
// wrote through, and sees it. A read narrowed to Tx would have forced the first
// caller into a transaction it has no use for, and one narrowed to Client.Reader()
// would have read a database that does not yet hold the row its caller just wrote.
//
// A caller with genuinely nothing to join opens one with Client.WithTransaction
// and passes the Tx it is handed. A Store that is not a SQL store still takes
// these types; an implementation with no transaction of its own ignores the
// executor, and the seam stays one signature rather than one per backing.
//
// # Every write that names one comment answers with it
//
// [Store.CreateComment], [Store.UpdateComment] and [Store.ArchiveComment] each
// return the row the statement left, read back on the caller's transaction.
// Returning is the spelling rather than writing the answer onto the argument:
// the two deliver the same guarantee, and one of them is available to a write
// that takes an id rather than an entity, so this interface spells it one way.
// None of the three modifies the value it was handed.
//
// The reading is not "a write returns". It is that the row is what the caller
// acts on next and no read of theirs can reach it as the statement left it. A
// create mints the id and the creation time is the database's. An edit moves
// last_updated_at, which is the database's, and leaves the target, the parent
// and the author as the row already held them rather than as the argument named
// them. An archive hides the row from every keyed read this interface has, so
// the entry naming what was removed is written from the returned row or from a
// read taken before the write, describing a comment that was still in the
// discussion.
//
// [Store.DeleteCommentsForTarget] and [Store.DeleteCommentsByAuthor] are the two
// writes that do not, and the reason is the shape of the answer rather than an
// exception to the rule: each destroys a set rather than moving a row, and what
// a caller reports is how many went. Handing back the rows would be handing back
// the free text the erasure exists to remove.
//
// # The scope is an argument, on every method
//
// Every method takes a tenancy.Scope, and none of them offers a variant that
// omits it — an implementation must filter on it rather than treat it as a hint.
// A deployment with one tenant passes tenancy.Global() everywhere and behaves
// exactly as it would have without the column.
//
// That includes the two writes that take a whole [Comment]. They read the scope
// off the argument rather than off Comment.Scope, and the alternative — letting
// an entity that carries a scope supply its own, so that the explicit argument
// appears only where there is no entity — was considered and rejected. The
// module's rule is that a scope goes into the query bound as a tenancy.Scope
// rather than derived from some other value, and an entity field is exactly the
// derivation that rule exists to rule out: it makes "which tenant is this write
// for" answerable only by reading a struct the caller assembled somewhere else.
// A Comment.Scope that disagrees with the argument is [ErrScopeMismatch] rather
// than either value quietly winning; an unset one adopts the argument, the same
// way a reply that names no target adopts its parent's.
//
// There is deliberately no cross-scope listing, and it is worth being clear
// about what that costs. An operator moderating every tenant's comments from one
// console is a real thing to want, and this interface will not answer it in one
// call: they list the scopes they administer and page each. The alternative is a
// read that omits the scope, which is the one read that cannot tell an
// operator's caller from a tenant's — and a paged list cannot bind a set of
// scopes either, because a bound set may not sit in a statement that also binds
// a cursor and a page size on two of the three dialects this package serves.
//
// # Eight of these are on the wire and two are not
//
// comments/grpc serves CreateComment, GetComment, ListRootComments,
// ListReplies, ListCommentsByTargetType, ListCommentsByAuthor, UpdateComment
// and ArchiveComment. They are the whole lifecycle of the noun, and each of
// them has a caller who is somewhere else.
//
// [Store.DeleteCommentsForTarget] and [Store.DeleteCommentsByAuthor] are the
// two that stay off it, and each says why on its own method. They are one case:
// bulk erasure that exists to commit inside somebody else's transaction.
//
// # The catalog gates writes, not reads
//
// A comment names a target type, and the catalog the store was built with is
// what says which types exist. [Store.CreateComment] refuses one the catalog
// does not hold; no read does.
//
// That is not an oversight. The catalog exists to stop a comment being written
// where nothing will ever list it — the misspelling that produces rows no view
// shows — and that failure is at the write. A read of a type the catalog does
// not hold answers with the rows that are there, which is nothing at all, since
// the write gate is what stopped any from being written. The exception is the
// type that was withdrawn, and there the rows are exactly what the operator who
// withdrew it needs to reach: gating the read would make the catalog a mechanism
// for hiding rows, which is not what it is for.
type Store interface {
	// CreateComment writes one comment through the caller's transaction, so the
	// comment commits with whatever the caller writes beside it, and answers with
	// the row it wrote. It assigns the id where the caller left it empty. A nil
	// tx is an error wrapping ErrNilExecutor.
	//
	// The target is checked against the catalog the store was built with, and
	// against that definition's existence hook where one is registered: an
	// unregistered type is an error wrapping ErrUnknownTargetType, and a target
	// the hook cannot find is ErrTargetNotFound.
	//
	// A comment with a ParentID is a reply, and three things are true of one. Its
	// parent must be a live comment in the same scope, or the write is
	// ErrParentNotFound. Its parent must itself be a root, or the write is
	// ErrNestedReply. And it belongs to its parent's discussion: a reply that
	// names no target adopts the parent's, and one that names a different target
	// is ErrTargetMismatch.
	//
	// The comment handed in is not modified. Everything the write settled — the
	// id it minted, the scope it bound, the target a reply adopted, the stamp the
	// database wrote — is on the value returned, so a caller reads them from one
	// place rather than from an argument that changed under them.
	//
	// Every one of those checks runs on tx, and so does the read-back: a reply
	// whose parent was written earlier in the same transaction resolves its
	// parent, and the creation time read back is the one this transaction just
	// wrote rather than a read of a row nothing else can see yet.
	//
	// One check does not run there, and it is worth stating rather than leaving
	// to be found. A TargetExistsFunc is handed a scope and a target id and no
	// executor — the row it answers for lives in a table this package has never
	// seen, sometimes in another database — so it reads on whatever connection
	// the consumer built it over, which is not this one. A comment filed in the
	// same transaction that creates the thing it is about is therefore
	// ErrTargetNotFound where that target type registers a hook, and is written
	// where it does not. The alternative is an executor on the hook's signature,
	// which is a connection to a table most hooks do not read.
	CreateComment(ctx context.Context, tx database.Tx, scope tenancy.Scope, comment *Comment) (*Comment, error)

	// GetComment reads one of the scope's live comments. It returns an error
	// wrapping ErrCommentNotFound when the comment does not exist, has been
	// archived, or belongs to another scope — which are the same answer from
	// here. A nil q is an error wrapping ErrNilExecutor.
	GetComment(ctx context.Context, q database.SQLQueryExecutor, scope tenancy.Scope, commentID string) (*Comment, error)

	// ListRootComments pages the top level of one target's discussion: the
	// comments that reply to nothing.
	//
	// It is the read a discussion opens with, and it is a separate method from
	// ListReplies rather than a parent argument a caller leaves empty, because
	// "the roots" and "this comment's replies" are two questions somebody asks
	// deliberately. Underneath they are one statement with a different bound
	// value — see comments/internal/queries — which is what keeps them from
	// answering differently.
	//
	// The count a client wants beside the discussion is on the result's
	// pagination: the filtered count is of the target's roots, not of the page.
	ListRootComments(ctx context.Context, q database.SQLQueryExecutor, scope tenancy.Scope, target Target, filter *filtering.QueryFilter) (*filtering.QueryFilteredResult[Comment], error)

	// ListReplies pages one root comment's replies.
	//
	// The target is a parameter as well as the parent because a reply carries
	// both and the statement keys on both: a reply's target is its parent's, so
	// naming it costs the caller nothing and buys the read the index it was
	// written for.
	//
	// An empty parent is ErrEmptyParent rather than the roots, which is what the
	// empty parent means in the column — returning them would be the wrong half
	// of the discussion, with nothing about the rows saying so.
	//
	// A parent that is no longer there is not an error. A reply outlives the
	// comment it replies to — archived, or erased with its author — and it is
	// still a reply; see the package documentation.
	ListReplies(ctx context.Context, q database.SQLQueryExecutor, scope tenancy.Scope, target Target, parentID string, filter *filtering.QueryFilter) (*filtering.QueryFilteredResult[Comment], error)

	// ListCommentsByTargetType pages every comment about one kind of thing —
	// "everything anybody has said about recipes", roots and replies alike.
	//
	// It is the moderation read, and it is the read an operator withdrawing a
	// target type runs first, to see what withdrawing it would strand. It does
	// not gate on the catalog, and that is why.
	ListCommentsByTargetType(ctx context.Context, q database.SQLQueryExecutor, scope tenancy.Scope, targetType TargetType, filter *filtering.QueryFilter) (*filtering.QueryFilteredResult[Comment], error)

	// ListCommentsByAuthor pages what one person wrote within the scope. It is
	// what a "your comments" view reads, and what the subject access request
	// collector pages through.
	ListCommentsByAuthor(ctx context.Context, q database.SQLQueryExecutor, scope tenancy.Scope, author string, filter *filtering.QueryFilter) (*filtering.QueryFilteredResult[Comment], error)

	// UpdateComment revises what the author said, and only that, through the
	// caller's transaction — so the revision and whatever the caller records
	// about it commit together or not at all — and answers with the revised row.
	// An edit is a moderation event as much as it is a write: who changed what,
	// and when, and the entry recording it describes the row the statement left
	// rather than the one the caller read before it. A nil tx is an error
	// wrapping ErrNilExecutor.
	//
	// It does not move the comment: the target is what the comment is about and
	// was checked against the catalog when it was written, the parent is which
	// conversation it is in, and the author is who said it. A whole-row write
	// that assigned any of the three would be an edit that silently moved
	// somebody else's words. The row returned is what makes that visible rather
	// than assumed — it carries the three as the table holds them, not as the
	// argument named them.
	//
	// The comment handed in is not modified, and last_updated_at is the
	// database's, so a response assembled from the argument would say the row was
	// last touched at the epoch.
	//
	// A comment that is not in the scope — absent, archived, or somebody else's —
	// is an error wrapping ErrCommentNotFound.
	UpdateComment(ctx context.Context, tx database.Tx, scope tenancy.Scope, comment *Comment) (*Comment, error)

	// ArchiveComment removes one comment from the discussion through the caller's
	// transaction, leaving the row for whoever asks later what was said, and
	// answers with the row it hid. It is the write a moderation action reaches
	// for: the comment leaves the discussion and the entry naming who removed it
	// land together, or neither does. A nil tx is an error wrapping
	// ErrNilExecutor.
	//
	// It archives exactly the comment named. A root's replies stay where they
	// are, which is deliberate: a moderator removing an off-topic root has not
	// removed the answers to it, and a reply whose parent is gone is what every
	// discussion UI already renders as a reply to a removed comment. A consumer
	// that wants the whole subtree gone archives the replies too, which
	// ListReplies enumerates.
	//
	// A comment already archived is an error wrapping ErrCommentNotFound,
	// because an archived comment is not in the discussion and this method
	// addresses the discussion.
	//
	// It returns the row because this is the write whose result no ordinary read
	// here can reach. GetComment cannot see an archived comment at all — that is
	// what archiving means — and the three lists reach it only for a caller who
	// set QueryFilter.IncludeArchived and then pages for it, which is a different
	// question from "what did I just remove". The words somebody wrote are what a
	// moderator's entry has to name, and the alternative is the read a consumer
	// takes a statement earlier, describing the comment as it stood before the
	// write rather than as the write left it.
	//
	// The read-back runs on tx and only after the guard has matched, so a write
	// that moved nothing is ErrCommentNotFound rather than somebody else's
	// archive reported as this one's.
	ArchiveComment(ctx context.Context, tx database.Tx, scope tenancy.Scope, commentID string) (*Comment, error)

	// DeleteCommentsForTarget destroys every comment about one thing — replies
	// and archived rows included — and reports how many that was.
	//
	// It is the sweep the package documentation's dangling-target ruling names.
	// A comment's target lives in a table this package has never seen, so nothing
	// here cascades from that table's delete; the consumer calls this from the
	// transaction that removes the target, which is what the tx it takes is for.
	//
	// Zero is not an error: a thing nobody commented on is a thing with nothing
	// here to sweep.
	//
	// It is erasure machinery and has no RPC in comments/grpc, which is the same
	// reading the tx it takes already states. The whole property of this write is
	// that it commits with the delete that removed the target; an RPC moves it
	// into a transaction of its own, at a moment the caller does not choose, and
	// what is left is a window — sometimes a permanent one, if the second call
	// never happens — in which the target is gone and its comments are live,
	// listable, and about nothing. A moderator removing one comment is not this
	// case and has ArchiveComment.
	DeleteCommentsForTarget(ctx context.Context, tx database.Tx, scope tenancy.Scope, target Target) (int64, error)

	// DeleteCommentsByAuthor destroys everything one person wrote within the
	// scope, archived comments included, and reports how many that was.
	//
	// It is a hard delete and it is the erasure path: the body is free text
	// somebody wrote, so what a right-to-be-forgotten request has to remove is
	// the words rather than a flag beside them. comments/privacy is the
	// dataprivacy.Eraser built on this.
	//
	// It runs inside the caller's transaction and must use the executor it is
	// given, so that a subject's comments and the rest of their footprint commit
	// or roll back together.
	//
	// That sentence is also why it is erasure machinery with no RPC in
	// comments/grpc. Its caller is comments/privacy's dataprivacy.Eraser, inside
	// the transaction destroying everything else a subject left behind; reached
	// over a wire it would commit separately from that transaction, so a rolled
	// back erasure would have taken these rows with it or left them when the rest
	// went. Neither is recoverable by trying again, because the request naming
	// the subject is the thing that has already returned.
	DeleteCommentsByAuthor(ctx context.Context, tx database.Tx, scope tenancy.Scope, author string) (int64, error)
}
