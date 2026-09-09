/*
Package grpc is settings on the wire: the gRPC service over
[github.com/primandproper/platform-go/v14/settings]'s Store, the converters
between the generated messages and its types, a typed client, and the default
permission fragment a consumer composes into its policy.

It is imported as settingsgrpc.

	srv, _ := settingsgrpc.NewServer(store, db, extractPrincipal, subjectAuthorizer)
	// []grpcserver.RegistrationFunc{srv.RegisterOn}

It follows the pattern identity/grpc's documentation states rather than
restating it: who is calling is an interface a consumer's own authentication
interceptor satisfies, what each method requires is a default map a consumer
composes into their own, the scope binds off the connection, a consumer's
vocabulary stays an opaque string, and a write whose caller is already inside
the process's own transaction is not an RPC. What is below is where settings
lands on each and where it diverges.

# Thirteen RPCs, two audiences, and one absence

The catalog half is an operator's: CreateDefinition, GetDefinition,
GetDefinitionByName, ListDefinitions, UpdateDefinition, ArchiveDefinition, and
ListValuesForDefinition, which is "who has overridden this". The value half is a
person acting on themselves: SetValue, GetValue, ClearValue,
ListValuesForSubject, Resolve and ResolveAll. That second half is the settings
screen every consumer ships, and [Server.Resolve] is the method this package
exists for — a stored value falling back to the definition's default, so that
anyone who has not chosen gets an answer rather than a missing row.

The fourteenth method of settings.Store is DeleteValuesForSubject and it is not
here. It destroys everything one subject answered, cleared answers included, and
it is erasure machinery — a dataprivacy.Eraser or a retention sweep calling on a
subject's behalf, from inside the transaction that removes the rest of them.
Moved onto a wire the write lands in a transaction of its own, at a moment the
caller does not choose, and what is left is a person erased from one table and
present in the others. The absence is recorded three times, which is once per
reader: on settings.Store's own method, in settings.proto's service comment, and
in the roster in this package's tests, which fails if a fifteenth store method
is in neither the service nor the list of absences.

# The typed value, which is this surface's one real design decision

settings stores every value as text and parses it against the kind its
definition declares. That parse is the thing a hand-written service gets wrong,
so putting the text on the wire and letting each consumer's generated client
parse it would be shipping the bug rather than the fix.

So a value is typed where this package parses one, and a string where it
compares bytes. [settingspb.ResolvedSetting] carries a
[settingspb.TypedValue] — a oneof of the four kinds — and so does a SetValue
request. A stored row, a definition's default and its enumeration carry the text
as stored, because those are what a write is checked against by string equality:
a definition read as typed values and written back unchanged would round-trip
through a parse and a format, and an edit that changed nothing could refuse
itself with settings.ErrStrandedValues.

The oneof is also what carries presence. A KindString value of "" is a value
somebody chose and a request that named no value is not, and a proto3 string
cannot hold that difference — which is the same reason settings.Definition.Default
is a *string and default_value on the wire is `optional`.

Both refusals the typed encoding has to keep expressible stay expressible, and
the first one only exists because of it. [Server.SetValue] reads the definition
inside the transaction it is about to write in, and a case that is not the
setting's kind is settings.ErrKindMismatch there and then — where a raw string
would have been stored without complaint, since an integer written to a text
setting is text. A value of the right kind that the setting does not admit is
settings.ErrMalformedValue or settings.ErrNotEnumerated from the store's own
check, which runs on its own read of the definition: the rule belongs to the
store, and this surface reading the definition first does not make it the
transport's.

The store checks the definition twice per write, once for each of those, and
that is the price of the boundary refusal being about what the client sent.

# The unset answer is data, not an error

A setting the subject has not answered and that has no default resolves to
VALUE_SOURCE_UNSET with no typed value. It is settings' tri-state arriving
unchanged: the third case is an answer — nobody has decided, and the caller's
own policy applies — rather than a failure, and a getter that took a fallback
could not express it.

settings.ErrSettingUnset is what the Go accessors report for the same state, and
it is mapped and client-safe even though no RPC here returns it, because a
consumer's own handler over a resolution will.

# Two writes read their answer back inside their own transaction

SetValue and ClearValue answer with a resolution rather than with a row, and
both read it on the database.Tx they wrote through. That is the property
settings.Store's reads taking a database.SQLQueryExecutor rather than a reader
exists for, and settings' own documentation names this exact caller: a service
that saves somebody's preference and returns the new effective value in the same
response is resolving a row it has written and not yet committed. On a
connection of its own it would answer with the value the subject had before the
request, and nothing would report an error.

UpdateDefinition reads the definition back for the plainer reason: the store's
update answers with an error and nothing else, so a response assembled from the
request would carry the epoch where last_updated_at belongs.

Every write here opens its own transaction with Client.WithTransaction, because
settings.Store's writes take a database.Tx and an RPC handler is precisely the
caller that method's documentation describes: one with nothing of its own to
join.

# Authorization has two halves, and the second one is required

[Permissions] is the first: a grant on the method, evaluated by
authorization/grpc's interceptor from the full method name and the caller's
grants, before the request body has been looked at.

[SubjectAuthorizer] is the second, and it is here because it cannot be there.
Six of these RPCs take a settings.Subject out of the request, and a grant on
SetValue is not a grant to write anybody's settings — within one tenant that
would make "may change their own notification preferences" mean "may change
everybody's". So the question is asked inside the handler, after the request has
been found well formed and before anything reads or writes a row.

Unlike identity/grpc's TargetAuthorizer it has no default and is positional.
settings.SubjectType is a bare string with two suggested constants, deliberately
open so that a deployment whose settings hang off a device or a workspace can
say so, and a default rule about an open vocabulary would be right for one
member of it and silently closed for the rest. See [SubjectAuthorizer] for the
two-line self-service rule and why each available default is wrong.

The seventh value-side RPC, ListValuesForDefinition, names no subject and asks
nothing of the authorizer. It pages every subject's answer to one setting, which
is why it has a grant of its own — [PermissionReadAllValues] — rather than
sharing the one that reads a subject's own.

# The scope comes off the connection, and the schema enforces it

Every statement behind these RPCs binds the scope the caller's principal names,
and no message in settings.proto has a scope field. That second half is the
schema's rather than this package's: the name is reserved on every request and
on the four messages a response is built from, so protoc refuses a scope field
being added — here and in a consumer's fork of the file alike. It is audit/grpc's
pattern, adopted rather than re-derived.

It matters as much here as anywhere. A definition and the values stored against
it share a scope, so a scope a client could name would be a read of another
tenant's catalog and a write into it.

# Errors

This package registers nothing. settings.HTTPMapper and settings.GRPCMapper live
beside the sentinels they map, and installing them is the composition root's one
call — errormappers.Register, which service.Register makes for a service built
from a service.Config and a service assembled by hand makes itself. Without it
every sentinel this service returns arrives as codes.Unknown.

Every failure here is one grpcerrors.PrepareAndLogGRPCStatus with codes.Internal
as the *default*. The encoding interceptor re-runs the registered mappers over
the preserved chain, so the mapper wins over the guess made at the call site,
which is why no handler on this surface switches on a sentinel. The exceptions
pass a code because nothing maps what they raise: a request that named no
definition, a SetValue that named no value, a filter that will not parse, and a
subject the authorizer refused.
*/
package grpc

//platform:transport resource surface: the catalog, the answers stored against it, and what a setting resolves to — over `settings.Store`
