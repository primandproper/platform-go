/*
Package tiercheck is where every package in this module is named with the tier
it belongs to, and it holds nothing else.

The module used to hold two tiers and this package existed to keep them
separable: the primitives were leaving for primitives-go, primitives-go would
import nothing from platform-go, and an import from a primitives-tier package
into a domain-tier one was a package that could not travel — invisible until the
split, at which point it was a build failure in a repository that did not exist
yet. Until this package there was nothing checking it. Two separate audits of
the crossings were published as complete and neither was: the first missed four
config subpackages, and the second — the one that found those four — missed a
roster test that imported fourteen domain packages from a primitive's own test
files. Both were people reading a tree by hand, which is a survey with a shelf
life of one branch.

The split has landed, and what this package checks has inverted with it. There
is no primitives tier here to constrain; every package in this module is the
domain tier or the composition root, and the direction is now primitives-go's to
enforce from its own side, which its internal/tierguard does with no roster at
all — the answer is the same for every package in that module. What is left here
is the half that does not move: the enumeration, so that a package nobody ruled
on is a failing test rather than a silence, and so that the README's table and
the tree cannot drift apart.

Three answers, and one of them is a refusal:

	domain     stays in platform-go — a noun with a table, its lifecycle, its
	           transport, its permissions and its privacy obligations.
	root       neither tier: the composition root that registers both modules'
	           mappers and configs, and the convention tests whose subject is
	           the whole tree. Named separately so that "why is this not a
	           domain" has an answer other than somebody's omission.
	primitive  belongs in primitives-go and therefore not in this repository.
	           No package answers it and the roster refuses one that tries. It
	           is kept because the rule it names is still the rule a new
	           package is measured against — the README's "Primitives and
	           Domains" section states it — and the measurement now decides
	           which repository the package is written for rather than which
	           import path it gets. A contributor who reaches for this answer
	           has found the ticket they meant to open, against primitives-go.

The roster is keyed by directory prefix, longest match wins, which is how the
README's own table is written: everything under `identity` inherits `identity`'s
answer. That is what makes a new subpackage classified by construction —
`links/database/internal/linksdb` is a domain because the store it belongs to is
one, without anybody having to add a row for it.

Five entries name a path whose parent is not in this module at all, and all five
are under `authentication`. `authentication/passwordreset` is one:
`authentication` hashes passwords and issues tokens in primitives-go, and the
table of reset tokens under it is a product's. Those are the straddles the split
left standing, and each says why, because a directory here under a primitives-go
path is the one shape a reader will not predict. A top-level package's tier is
the README's to explain, so an entry that agrees with its path carries no reason.

`authentication` is the only such parent left because it is the only one that
groups. Six others held no Go files and exactly one child apiece — a name a
reader walked through to reach the package, naming a parent this repository does
not hold — and were flattened inside the /v14 major, which is what a package
rename costs before a tag and not after. A seventh cannot appear quietly:
TestNoParentDirectoryOnlyIndirects fails any directory in that shape.

Two directions are checked on the roster, as sqltier checks its own in both: a
package nobody classified fails, and a roster entry naming a directory that no
longer exists fails. A third test reads the README's table and requires that it
and this file say the same thing, so the prose a reader is pointed at cannot
drift from the enumeration a build enforces. The fourth is the shape check above,
which is about the tree rather than the roster and lives here because the
straddles are what made the shape worth ruling on.
*/
package tiercheck
