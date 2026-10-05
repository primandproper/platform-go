package audit

import (
	"github.com/primandproper/platform-go/v15/callers"

	"github.com/primandproper/primitives-go/v2/authorization"
)

// The metadata keys an [EventOperatorBypass] entry carries.
const (
	// MetadataOperatorPermission is the permission the caller was admitted on.
	MetadataOperatorPermission = "operator_permission"

	// MetadataOperatorMethod is the full method name of the call the caller
	// was admitted to, where the surface knows it.
	MetadataOperatorMethod = "operator_method"
)

// OperatorBypassEntry is the entry a surface files when it admits a caller to
// somebody else's row because they hold an operator permission — the row rule
// refused them, and the permission is what let them through.
//
// It is one function rather than an entry each surface assembles, because the
// entry is only worth recording if it reads the same wherever it came from: a
// query for everything an operator was let into has to find identity's and
// audit's under one event type and one pair of keys. The actor is
// [PrincipalActor], so an operator acting through somebody else is recorded
// as both.
//
// It is recorded when the permission decided, and only then. A caller the row
// rule already admitted was not bypassing anything, and an entry for every
// ordinary read would bury the ones this exists to show.
func OperatorBypassEntry(
	p callers.Principal,
	permission authorization.Permission,
	method, resourceType, resourceID string,
) *Entry {
	metadata := map[string]string{MetadataOperatorPermission: string(permission)}
	if method != "" {
		metadata[MetadataOperatorMethod] = method
	}

	return &Entry{
		EventType:    EventOperatorBypass,
		ResourceType: resourceType,
		ResourceID:   resourceID,
		Actor:        PrincipalActor(p),
		Metadata:     metadata,
	}
}
