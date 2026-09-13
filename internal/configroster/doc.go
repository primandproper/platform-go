// Package configroster holds the tests that walk every config subpackage in
// the module at once: the one that says a zero-valued config either validates or
// says why it cannot, the one that says a config naming a provider must carry
// that provider's block, and the one that says the first of those names every
// config subpackage this module ships.
//
// That last one is what keeps the other two from being lists. A roster of
// packages is complete on the day it is written and never again, so the one here
// is checked against the directories rather than against its own comment — the
// same reading service's TestEveryConfigPackageHasAField takes of the fields on
// its Config.
//
// They have no production code of their own, and the package exists so that they
// have somewhere to live that is honest about what they are.
//
// # Why they are not in cfgnorm
//
// They were, because cfgnorm is the package whose helpers the rules are about —
// Provider, ZeroToNil, EnsureSweepInterval, SweepIntervalRule. But what those
// tests actually assert is a property of every config in the tree, so between
// them they import roughly fifty config subpackages, from both tiers.
//
// cfgnorm is a primitive: a handful of normalizations over struct tags, with no
// notion of what is being configured. It goes to primitives-go, and primitives-go
// cannot import platform-go — not from a test file either, because a test file
// is compiled by the module that ships it. A roster of every config in a module
// belongs to the module that has all of them, which is this one.
//
// The rule this is an instance of: a test whose subject is one package tests
// that package, and a test whose subject is the whole tree is a
// composition-root test wherever its helpers happen to live. cfgnorm keeps the
// tests of its own functions, which name no config subpackage at all.
package configroster
