package grpc

import (
	"context"

	"github.com/primandproper/platform-go/v14/settings"

	platformerrors "github.com/primandproper/primitives-go/v2/errors"
)

// A setting the catalog reserved is behind a grant of its own, on both writes
// this surface serves.
//
// # What the flag means, and why the method map cannot carry it
//
// settings.Definition.AdminOnly "marks a setting only an administrator may
// write", and the store records it rather than enforcing it — deliberately, and
// it says so: that package has no notion of who is calling, and a store that
// pretended to would be an authorization check in the wrong layer. This is the
// layer it defers to. It is the one that holds a callers.Principal, and until
// this file it read the flag off a row, wrote it onto the wire, and never asked
// anything about it.
//
// [Permissions] cannot close it. That map assigns a grant per method, and
// SetValue and ClearValue are each one method serving two kinds of setting:
// whether this one is reserved is a fact about the definition the request
// names, which is in the body and not in the method name. Both available
// assignments are wrong. Under [PermissionWriteValues] alone — where they were
// — every member who may set their own preferences may set a reserved one,
// because that grant is what self-service means and every ordinary member holds
// it. Under an administrative grant instead, no ordinary member may set any of
// their own settings, which removes the feature the surface exists for. A
// consumer overriding the map per method reaches neither, because the method is
// not where the two answers differ.
//
// Nor can [SubjectAuthorizer]. It is handed a caller and a settings.Subject and
// never learns which definition was named — which is right, because it answers
// a different question. This is asked in addition to it and not instead of it:
// the authorizer says whose settings these are, and this says whether this
// setting is one an ordinary member may answer at all.
//
// # It is a refusal and not a narrowing
//
// archived.go rules the other way about include_archived, and the difference is
// what the caller asked for. A read that requested archived rows has a legal
// answer under a lighter grant — the live rows — so it is narrowed to that, and
// the page is the one the caller would have received had they never asked. A
// write has no such answer. There is no lesser value to store, and storing
// nothing while replying success is the failure mode this whole file exists to
// end: the setting would read back unchanged and the client would have been
// told it changed.
//
// # Clearing is writing
//
// ClearValue is here for the same reason SetValue is. Taking an administrator's
// answer back returns the setting to its default, which decides it for the
// subject exactly as naming a value does — and [PermissionWriteValues]'s own
// documentation already reads set and clear as one capability. A rule that
// reserved the set and not the clear would let an ordinary member undo every
// reserved answer in the scope while being unable to make one.
//
// It costs ClearValue a read it did not make, inside the transaction that
// clears. That is the price of the check and it is stated rather than hidden:
// the definition has to be in hand to know whether the flag is set, and reading
// it after the write in order to save a statement would mean deciding the
// refusal from a transaction that had already done the thing.
//
// # What this does not reach
//
// settings.Store.SetValue and ClearValue are untouched, and a Go caller holding
// the store is inside the trust boundary exactly as archived.go says of
// IncludeArchived. This is a ruling about the wire.

// confineToAdmin refuses a write to a reserved setting by a caller who does not
// hold [PermissionWriteAdminValues].
//
// It returns the sentinel rather than a prepared status, because both callers
// raise it from inside Client.WithTransaction and that error block is what
// assigns the code — a status prepared here is overwritten by the Internal
// default on the way out, which is what the first version of this did and what
// its test caught.
//
// A definition that is not reserved is every setting in the ordinary case, and
// costs one field read. A server built with no [WithGrantsExtractor] refuses
// every reserved write, which is [Server.callerGrants]'s fail-closed default
// read at this door: a surface that cannot see what the caller may do cannot
// tell an administrator from anybody else, and guessing "administrator" is the
// expensive way to be wrong.
func (s *Server) confineToAdmin(ctx context.Context, req *request, definition *settings.Definition, name string) error {
	if definition == nil || !definition.AdminOnly {
		return nil
	}

	if grants, ok := s.callerGrants(ctx); ok && grants.Has(PermissionWriteAdminValues) {
		return nil
	}

	return platformerrors.Wrapf(ErrAdminOnlySetting, "setting %q", name)
}
