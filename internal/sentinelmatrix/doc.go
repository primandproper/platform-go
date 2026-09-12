/*
Package sentinelmatrix is where this module's domain sentinels are checked
against the statuses they resolve to, and it holds nothing else.

The mappings themselves live beside the sentinels: every package Packages names
exports an HTTPMapper and a GRPCMapper holding the cases for its own errors,
because errors/http and errors/grpc are primitives and cannot import the tier
above them. That is the right place for a mapping and the wrong place for a
roster — each of those packages can see only its own sentinels, and the failure
this package exists to catch is the one nobody is looking at: a sentinel added
to a package months later, mapped nowhere, reaching a client as a 500 or as
codes.Unknown while every test in its own package stays green.

So the roster is here, and it is a decision per sentinel rather than a list of
the mapped ones. Every exported Err in the packages Packages names is one of three
things:

	mapped      the package's own two mappers answer, on both transports
	platform    they are silent and errors/http and errors/grpc answer,
	            because the sentinel wraps a platform one
	unhandled   nobody answers, and a 500 is the honest reply — the sentinel
	            is raised while wiring the component up or inside its own
	            worker, and reaches a client only through a service that
	            shipped broken

A new sentinel is none of the three until somebody says which, which is the
whole mechanism. The names are read out of the packages' own source rather than
listed here, so adding one is what fails this package rather than remembering to
add it to a list.

Both directions are checked, because a roster fails both ways:

	a sentinel in no row     the decision was never made
	a row with no sentinel   a roster outliving its subject, which reads live

And the decision is checked against what the mappers actually do, bare and
wrapped, so a row saying "mapped" for a case somebody deleted fails here rather
than in a consumer's 500.

One check used to live here that no longer can: errors/ was walked for imports
of the packages above, because an external test package under it could import a
domain package freely and that is how the dependency comes back — a test
reaching for a domain sentinel to assert something about, and errors/ quietly
stops being a package that can be lifted out on its own. errors/ has been lifted
out, so the walk moved with it and widened: primitives-go's internal/tierguard
fails on an import of platform-go from anywhere in that module, test files and
go.mod included, and it needs no roster because the answer is the same for every
package there.

There is a second roster, and it names a subset: ClientSafePackages is the
packages that additionally declare a ClientSafeSentinels list, the refusals whose
own wording a gRPC status may quote rather than sending the name of the code. It
is checked against source in both directions too, for the failure that has no
symptom anywhere else — a list declared and registered nowhere changes nothing a
package's own tests can see, and only changes what somebody staring at a browser
is told. Before it, prose was the only thing that said how many there were, and
three passages said four, six and eight while the source held nine and fifteen.

The roster is a package-level var rather than a test fixture, because two other
test binaries need the same expectation. errormappers.Register is the one call
that installs those packages' mappers, service.Register is the caller that
makes it for a service built from a service.Config, and each asserts that what
its registration makes ToAPIError and MapToGRPC say matches what the owning
package's mapper answers — MappedResolutions is that answer, computed here so
that neither can drift from the other or from the mapper. Nothing outside this
module can see any of it: this is internal/.

What this package does not check is the reasons. Each mapper's own comments
carry why a link that has expired is a 410 and a FailedPrecondition; repeating
that here would be a second copy of an answer rather than a check on this one.
*/
package sentinelmatrix
