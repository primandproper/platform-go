/*
Package countwidth is where the width of every exported count in this module is
checked against one rule, and it holds nothing else.

The rule is that a count answers in int64. A count's type is exported API in the
one way its value never shows: nothing a caller writes against Flushed or
Checked reads differently at int than at int64, so a narrowing is invisible
right up until the day the field has to widen — and widening an exported field
after the tag is a major version, for a change no caller can see coming and none
would have written differently against.

# Why the check is not per-package

It was, in two copies. Each named the same nine disallowed reflect kinds beside
its own result type, in metering's test file and in audit's, and a list spelled
twice is a list that can drift: the day one copy learns about a kind the other
does not, nothing says so, and the package with the older copy goes on passing.
That is the shape this module extracts — not what is merely written twice, but
what can be wrong twice — so the kinds are named once here and the result types
come to them.

Naming the roster is also what makes "every exported count" a claim rather than
a hope. A result type added to a package that has no convention test of its own
is a type nothing checks, and it stays that way until somebody remembers; here
it is a missing row, in the same file as the rows that are already right.

# What is checked

Every exported field of every rostered type, against the disallowed integer
kinds rather than against int64 outright. Listing what a count may not be leaves
a field that is not an integer at all alone — a time, a scope, a reason, a break
— so a result type is rostered whole rather than field by field, and a count
added to one later is covered by the row that is already there.
*/
package countwidth
