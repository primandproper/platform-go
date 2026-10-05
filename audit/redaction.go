package audit

import (
	"maps"
	"slices"
)

// Redaction declares what happens to named fields of one resource type on the
// way into the log.
//
// It exists because the decision belongs next to the write. A password hash or
// a bearer token that reaches the audit table is in the one table designed to
// be immutable and retained, and filtering it out at query time does not
// un-write it. Deciding here means the value never becomes durable at all.
//
// A field named in both lists is dropped: the stricter disposition wins, so
// widening one list can never accidentally narrow the other.
type Redaction struct {
	// Drop omits the field entirely. Use it when even the fact that a value
	// changed is not worth recording.
	Drop []string
	// Hash records "sha256:" followed by the digest of the value in place of the
	// value. Use it when the audit question is "did this change, and is it the
	// same value as that other one" rather than "what is it" — rotating a secret
	// is a real event and the new secret is not a thing to write down.
	Hash []string
}

// merge combines two Redactions, so registering the same resource type twice
// accumulates rather than replaces.
func (r Redaction) merge(other Redaction) Redaction {
	return Redaction{
		Drop: append(slices.Clone(r.Drop), other.Drop...),
		Hash: append(slices.Clone(r.Hash), other.Hash...),
	}
}

// Apply returns changes with r's rules applied, for a writer whose redaction
// travels with the type rather than with a deployment's recorder.
//
// A store's RecordingHooks is that writer. A field its own privacy adapter
// treats as the subject's — what somebody wrote, what an operator wrote about
// them — is the module's obligation and not a deployment's policy, so the hook
// redacts it before the entry reaches any recorder, and a deployment that never
// heard of WithRedaction still keeps it out of the log. Hashing there rather
// than tagging the field audit:"-" keeps its name in the diff: the entry still
// says the field changed, and so does an event built from the same diff.
//
// The digest is the one the recorder writes for a Hash rule, so a value is
// spelled the same whichever of the two redacted it. A recorder rule naming the
// same field afterwards applies to the digest: Hash digests it again and Drop
// removes it, which is still the stricter disposition winning. changes itself
// is never mutated.
func (r Redaction) Apply(changes map[string]Change) (map[string]Change, error) {
	return redactChanges(changes, r.rules())
}

// rules resolves one Redaction into a lookup. Hash is applied first and Drop
// second, so a field named in both ends up dropped.
func (r Redaction) rules() map[string]disposition {
	out := make(map[string]disposition, len(r.Hash)+len(r.Drop))

	for _, field := range r.Hash {
		out[field] = hashField
	}

	for _, field := range r.Drop {
		out[field] = dropField
	}

	return out
}

// disposition is what happens to one field.
type disposition uint8

const (
	// keepField records the value as it was given.
	keepField disposition = iota
	// hashField records a digest of the value.
	hashField
	// dropField records nothing.
	dropField
)

// dispositions resolves a resource type's rules into a lookup, folding in the
// rules registered under the empty resource type.
//
// The catch-all is applied to every resource type rather than only to those
// with no rules of their own, which is what makes it useful: "never record a
// field called password, anywhere" is a policy about the word, not about one
// table. A resource type's own rules can only add to it — there is no way to
// opt one resource type back out of the catch-all, deliberately, since that is
// the shape of every redaction bug worth having.
func (r *ChainRecorder) dispositions(resourceType string) map[string]disposition {
	if len(r.redactions) == 0 {
		return nil
	}

	global, hasGlobal := r.redactions[""]
	specific, hasSpecific := r.redactions[resourceType]

	if !hasGlobal && !hasSpecific {
		return nil
	}

	return global.merge(specific).rules()
}

// redact applies the entry's resource type's rules to its changes and metadata,
// returning what should actually be written.
//
// Both maps are filtered against the same field names. Metadata is free-form
// context rather than field values, but a token dropped in there is exactly as
// durable as one dropped in Changes, and a redaction rule that covered only
// half of the entry would be an invitation to route around it.
//
// The originals are never mutated: the caller may still be holding the maps it
// passed, and quietly emptying them would be a surprising thing for a write to
// do.
func (r *ChainRecorder) redact(entry *Entry) (changes map[string]Change, metadata map[string]string, err error) {
	rules := r.dispositions(entry.ResourceType)
	if len(rules) == 0 {
		return entry.Changes, entry.Metadata, nil
	}

	if changes, err = redactChanges(entry.Changes, rules); err != nil {
		return nil, nil, err
	}

	if metadata, err = redactMetadata(entry.Metadata, rules); err != nil {
		return nil, nil, err
	}

	return changes, metadata, nil
}

// redactChanges applies rules to a change map.
func redactChanges(changes map[string]Change, rules map[string]disposition) (map[string]Change, error) {
	if len(changes) == 0 {
		return changes, nil
	}

	out := make(map[string]Change, len(changes))

	for _, field := range slices.Sorted(maps.Keys(changes)) {
		change := changes[field]

		switch rules[field] {
		case dropField:
			continue
		case hashField:
			hashed, err := hashChange(change)
			if err != nil {
				return nil, err
			}
			out[field] = *hashed
		case keepField:
			out[field] = change
		}
	}

	return out, nil
}

// hashChange digests both halves of a change, leaving an absent half absent —
// so a creation still reads as a creation and a deletion as a deletion, rather
// than both growing a digest of nothing.
func hashChange(change Change) (*Change, error) {
	out := Change{}

	if change.Old != nil {
		hashed, err := hashValue(change.Old)
		if err != nil {
			return nil, err
		}
		out.Old = hashed
	}

	if change.New != nil {
		hashed, err := hashValue(change.New)
		if err != nil {
			return nil, err
		}
		out.New = hashed
	}

	return &out, nil
}

// redactMetadata applies rules to a metadata map.
func redactMetadata(metadata map[string]string, rules map[string]disposition) (map[string]string, error) {
	if len(metadata) == 0 {
		return metadata, nil
	}

	out := make(map[string]string, len(metadata))

	for _, field := range slices.Sorted(maps.Keys(metadata)) {
		value := metadata[field]

		switch rules[field] {
		case dropField:
			continue
		case hashField:
			hashed, err := hashValue(value)
			if err != nil {
				return nil, err
			}
			out[field] = hashed
		case keepField:
			out[field] = value
		}
	}

	return out, nil
}
