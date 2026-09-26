package assembled_test

import (
	"context"
	"sync"

	"github.com/primandproper/platform-go/v14/comments"

	"github.com/primandproper/primitives-go/v2/identifiers"
	"github.com/primandproper/primitives-go/v2/tenancy"
)

// thingType is the one comment target type the application declares.
const thingType comments.TargetType = "conformance_thing"

// things are the application's commentable things, which is what a consumer
// keeps in a table of its own — recipes, tickets — and what its comments.Targets
// checks a comment's target against.
//
// The check is here rather than left off because it is the shape a deployment
// that cares about orphaned comments has, and a target the suite minted names
// nothing: a harness that accepted any identifier would never exercise the
// action a checked deployment needs.
type things struct {
	made sync.Map
}

// definition is the target type's catalog entry, existence check included.
func (h *things) definition() comments.TargetDefinition {
	return comments.TargetDefinition{
		Description: "a thing the conformance suite comments on",
		Exists:      h.exists,
	}
}

// exists answers the way a consumer's lookup does: by the scope the comment is
// filed under, so a thing made in one tenant is absent in every other.
func (h *things) exists(_ context.Context, scope tenancy.Scope, targetID string) (bool, error) {
	_, ok := h.made.Load(thingKey{owner: scope.Owner(), id: targetID})
	return ok, nil
}

// bring is the CommentTarget action: what a consumer's application does when
// somebody creates a thing people can then discuss.
func (h *things) bring(_ context.Context, scope tenancy.Scope) (targetType, targetID string, err error) {
	id := identifiers.New()
	h.made.Store(thingKey{owner: scope.Owner(), id: id}, struct{}{})

	return string(thingType), id, nil
}

type thingKey struct {
	owner string
	id    string
}
