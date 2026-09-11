/*
Command readmegen writes the two README sections that are rosters of the tree —
"Stores and Transports" and "SQL Dialect Support" — from the tree itself.

It is run by `make generate` and the sections it writes are checked in, so a
package that gains or loses an http subpackage, or a migrations directory that
gains or loses a dialect, changes README.md on the next generate and reds the
generated-files workflow until that change is committed.

# Why generated rather than checked

Both sections used to be typed by hand and policed by a test — internal/dialectmatrix
and internal/transportmatrix, 821 lines between them — that walked the tree,
computed what the table should say, and then asserted that somebody had said it.
Computing it is the hard half and both already did it; emitting the row is
strictly less work than diffing it against a row a person typed, and it makes
the tables unable to rot rather than caught rotting.

# Where the human half lives

A roster is a tree fact and this command derives it. What a roster carries
beside the fact is not: whether operations/http is a middleware or a resource
surface, what shape sessions/http is standing in for, and why workqueue ships
Postgres alone are judgements no directory walk settles.

Those live in the doc.go of the package they are about, as directives, because
that is the file the next author of that package opens and the only place a
judgement about it can be revised in the same change that revises it. A
central manifest would put them one edit away from the decision they describe,
which is the arrangement this command exists to end.

	//platform:transport <kind>: <whose shape it is>
	//platform:narrowing <why this package ships fewer than three dialects>

Both are read only from a package's own doc.go, and both are required rather
than optional: a directory named http or grpc with no transport directive, or a
migrations directory shipping fewer than three dialects with no narrowing
directive, is a failed generate rather than a row emitted with a blank cell.
That is the forcing function the old tests were — a package cannot quietly grow
handlers or quietly drop a dialect — moved onto the event that causes it.

The kind is one of a closed set (see kinds). It is closed on purpose: a new kind
is a new category of thing this module ships across the store/transport
boundary, which is a decision to write into the section's prose rather than a
word to invent in a cell.

# What it does not generate

The prose around each table, including the paragraph naming the packages that
ship a store and no handlers, and the long form of why the three narrow. Those
are arguments rather than rosters, and nothing checks what they argue,
deliberately: a package that crossed the boundary shows up as a new row needing
a new directive, which is where the author is asked to think.

Three mechanical properties of the "Transports" section are checked, and they
are not the argument. An ordinal ("the third across"), a line written twice, and
a table split by a blank line are what a reader trips over before reaching the
reasoning, and the section carried all three at once: four domains each claiming
to be the third across, one sentence written four times, and a ten-row table
holding nine packages with one of them twice. Each of the three arrived the same
way — a transport PR appending its paragraph to a README two other branches were
also appending to — so the forcing function the directives are cannot reach
them. See prose_test.go; every one of the three is satisfiable by deleting a
claim rather than by updating a fixture.

	go generate ./internal/cmd/readmegen   # rewrite the generated regions in README.md
	go run ./internal/cmd/readmegen -root . -out README.md
*/
package main

//go:generate go run .
