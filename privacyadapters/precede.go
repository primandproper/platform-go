package privacyadapters

import (
	"context"
	"maps"

	"github.com/primandproper/platform-go/v14/dataprivacy"

	"github.com/primandproper/primitives-go/v2/database"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/tenancy"
)

// precede composes a consumer's BeforeErase with the domain's own eraser, in
// that order, and is how every adapter here applies that field.
//
// # Why the seam is "before" and not "instead"
//
// The shape a consumer asks for first is a wrapper — hand me the domain's
// eraser and I will return one that calls it. That shape is refused here, and
// the reason is dataprivacy.ErasureOutcome rather than any general dislike of
// decorators. The outcome is not telemetry: Retained carries what was kept and
// the legal basis for keeping it, and it goes into the request record and, in
// practice, in front of a regulator. A wrapper holding the domain's eraser can
// return numbers that eraser never produced — and unlike a wrapper that forgets
// to call it, or one that returns nil, a falsified count is undetectable from
// here. Every check this package has would pass: the key is registered, the
// roster is satisfied, the export is well-formed.
//
// So the domain's eraser is never handed over. The consumer's step runs first,
// on the same transaction, and this package owns what the two of them add up to.
//
// # What a consumer gets in exchange for not holding it
//
// The ordering they actually wanted. A succession rule — move the account to
// somebody else before the person who owned it is erased — has to run ahead of
// the erasure and inside its transaction, and that is exactly what this is.
// dataprivacy.Eraser already promises the transaction: every registered eraser
// for one request shares it, so the consumer's step commits with the erasure or
// not at all.
//
// There is deliberately no AfterErase. Nothing has asked for one, and adding it
// later is another field rather than a different design.
func precede(before, own dataprivacy.Eraser) dataprivacy.Eraser {
	if before == nil {
		return own
	}

	return &precedingEraser{before: before, own: own}
}

// precedingEraser runs the consumer's step, then the domain's, and merges what
// they report.
type precedingEraser struct {
	before dataprivacy.Eraser
	own    dataprivacy.Eraser
}

var _ dataprivacy.Eraser = (*precedingEraser)(nil)

// Erase runs both halves on the caller's transaction and answers with their sum.
//
// The consumer's step goes first and its error takes the whole erasure down
// rather than being logged past. That is the harsher of the two readings and
// the right one: a precondition that failed is a precondition, and dataprivacy
// already promises an erasure is all-or-nothing rather than half-applied across
// eleven domains. What it costs is worth saying plainly — a consumer's buggy
// step blocks erasure for that subject until it is fixed — and the alternative
// costs a subject who was told they were erased and was not.
func (e *precedingEraser) Erase(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	subject dataprivacy.Subject,
) (dataprivacy.ErasureOutcome, error) {
	first, err := e.before.Erase(ctx, tx, scope, subject)
	if err != nil {
		return dataprivacy.ErasureOutcome{}, err
	}

	second, err := e.own.Erase(ctx, tx, scope, subject)
	if err != nil {
		return dataprivacy.ErasureOutcome{}, err
	}

	return mergeOutcomes(first, second)
}

// mergeOutcomes adds two outcomes for one key.
//
// The counts sum, which is the only honest reading: both halves ran on the same
// transaction against the same subject, and the record is of what that erasure
// did rather than of which function did it.
//
// Retained is unioned and a key claimed by both is refused rather than
// overwritten. Retained's values are legal bases, so an overwrite drops the
// reason one of them was kept while leaving the record looking complete — the
// same failure this package exists to close, in miniature. A consumer whose step
// retains something the domain also retains has two accounts of one thing and
// has to say which is true; naming it under a key of their own is how they say
// both.
func mergeOutcomes(first, second dataprivacy.ErasureOutcome) (dataprivacy.ErasureOutcome, error) {
	merged := dataprivacy.ErasureOutcome{
		Deleted:    first.Deleted + second.Deleted,
		Anonymized: first.Anonymized + second.Anonymized,
	}

	if len(first.Retained) == 0 && len(second.Retained) == 0 {
		return merged, nil
	}

	for key := range second.Retained {
		if _, claimed := first.Retained[key]; claimed {
			return dataprivacy.ErasureOutcome{}, platformerrors.Wrapf(dataprivacy.ErrDuplicateKey,
				"retained basis %q is claimed by both a BeforeErase step and the domain's own eraser", key)
		}
	}

	merged.Retained = make(map[string]string, len(first.Retained)+len(second.Retained))
	maps.Copy(merged.Retained, first.Retained)
	maps.Copy(merged.Retained, second.Retained)

	return merged, nil
}
