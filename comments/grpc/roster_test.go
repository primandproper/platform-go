package grpc_test

import (
	"reflect"
	"slices"
	"testing"

	"github.com/primandproper/platform-go/v14/comments"
	"github.com/primandproper/platform-go/v14/comments/commentspb"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// This file is the roster of which store methods cross onto the wire, and it is
// the ruling rather than a description of it. comments.Store has ten methods and
// this service serves eight; the two absences are each a decision, and an
// eleventh method is in neither list until somebody says which.
//
// The mechanism is billing/grpc's and webhooks/grpc's, adopted rather than
// re-derived, and it is internal/sentinelmatrix's applied to methods instead of
// sentinels — for the same reason in all of them: the failure worth catching is
// not a wrong row, it is a method added later that nobody classified. On this
// surface that means either an RPC nobody meant to publish, or a write whose
// whole property is that it commits inside its caller's transaction.

// absent is every comments.Store method this service deliberately does not
// serve, with the reason recorded beside it so the roster is the argument and
// not the paperwork.
//
// Each reason is the short form of one the Store method itself carries; that is
// where the long version lives, because a reader of the Store should find the
// answer where they are standing.
var absent = map[string]string{
	// The dangling-target sweep. Its tx parameter is the point: it is called
	// from the transaction that removes the thing being discussed, and outside
	// one it leaves a window — sometimes a permanent one — in which the target
	// is gone and its comments are live, listable and about nothing.
	"DeleteCommentsForTarget": "the sweep that commits with the delete that removed the target",

	// The erasure path. comments/privacy's dataprivacy.Eraser calls it inside
	// the transaction destroying the rest of a subject's footprint, and a
	// separate commit means either these rows survive an erasure that rolled
	// back or they go when the rest of it did not.
	"DeleteCommentsByAuthor": "the erasure that commits with the rest of a subject's footprint",
}

// storeMethods is every method on comments.Store, read off the interface rather
// than listed, so a method added to it fails the two tests below.
func storeMethods() []string {
	t := reflect.TypeFor[comments.Store]()

	out := make([]string, 0, t.NumMethod())
	for method := range t.Methods() {
		out = append(out, method.Name)
	}

	return out
}

// rpcNames is every RPC on the service, by the bare method name the store method
// it serves also carries.
func rpcNames() []string {
	out := make([]string, 0, len(commentspb.CommentsService_ServiceDesc.Methods))
	for _, m := range commentspb.CommentsService_ServiceDesc.Methods {
		out = append(out, m.MethodName)
	}

	return out
}

// TestEveryStoreMethodIsEitherServedOrRuledOut is the roster's forward
// direction: a store method in neither list is a decision nobody made.
//
// The expensive direction to be wrong in is publishing something. A method added
// to the store and reflexively given an RPC is how a write that has to commit
// with its caller's transaction arrives on a wire without anybody arguing for
// it.
func TestEveryStoreMethodIsEitherServedOrRuledOut(T *testing.T) {
	T.Parallel()

	methods := storeMethods()
	must.SliceNotEmpty(T, methods, must.Sprint("read no methods off comments.Store, so this asserted nothing"))

	served := rpcNames()

	for _, method := range methods {
		T.Run(method, func(t *testing.T) {
			t.Parallel()

			_, ruledOut := absent[method]
			onTheWire := slices.Contains(served, method)

			test.True(t, ruledOut != onTheWire, test.Sprintf(
				"comments.Store.%s is %s; it has to be exactly one of served and ruled out",
				method, servedAndRuledOut(onTheWire, ruledOut)))
		})
	}
}

func servedAndRuledOut(onTheWire, ruledOut bool) string {
	switch {
	case onTheWire && ruledOut:
		return "both an RPC and recorded as absent"
	default:
		return "neither an RPC nor recorded as absent"
	}
}

// TestNoRulingOutlivesItsMethod is the other direction: a row naming a store
// method that no longer exists is a roster describing a tree that has moved, and
// it reads live.
func TestNoRulingOutlivesItsMethod(T *testing.T) {
	T.Parallel()

	methods := storeMethods()

	for method := range absent {
		test.SliceContains(T, methods, method, test.Sprintf(
			"the roster rules out comments.Store.%s, which comments.Store does not declare", method))
	}
}

// TestNeitherBulkDeleteIsOnTheWire names the two the lane's ruling for this
// package turned on, because a roster that only counted would let one of them be
// swapped for something else.
//
// Both look serviceable — a moderation console has a "delete everything about
// this" button, and a privacy team has a "forget this person" one — which is why
// they are the two somebody would add in good faith.
func TestNeitherBulkDeleteIsOnTheWire(T *testing.T) {
	T.Parallel()

	served := rpcNames()

	for _, method := range []string{"DeleteCommentsForTarget", "DeleteCommentsByAuthor"} {
		T.Run(method, func(t *testing.T) {
			t.Parallel()

			test.SliceNotContains(t, served, method, test.Sprintf(
				"%s is an RPC; its write exists to commit inside the caller's transaction", method))

			_, ruledOut := absent[method]
			test.True(t, ruledOut, test.Sprintf("%s is not recorded as absent", method))
		})
	}
}

// TestTheEightAreTheEight is the acceptance list read back: each caller-facing
// store method has an RPC of the same name.
func TestTheEightAreTheEight(T *testing.T) {
	T.Parallel()

	want := []string{
		"CreateComment", "GetComment", "ListRootComments", "ListReplies",
		"ListCommentsByTargetType", "ListCommentsByAuthor", "UpdateComment", "ArchiveComment",
	}

	served := rpcNames()

	test.SliceLen(T, len(want), served)

	for _, method := range want {
		test.SliceContains(T, served, method,
			test.Sprintf("comments.Store.%s has a plausibly remote caller and no RPC", method))
	}
}
