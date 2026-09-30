// Package routeguard is the permission check in front of an HTTP surface's
// guarded routes, shared by the three surfaces that have them.
//
// It is one package rather than a copy in each because both halves of it can be
// got wrong. The refusal a surface falls back to when nobody configured an
// enforcer is a security default, and three copies of it are three places for
// one to drift open. And the caller a guard resolves to decide whether to check
// a grant is the caller the handler behind it needs, so resolving it once is a
// property of the guard rather than of each surface's discipline.
package routeguard
