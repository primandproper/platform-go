package settings

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/primandproper/platform-go/v14/settings/internal/settingsdb"

	"github.com/primandproper/primitives-go/database"
	platformerrors "github.com/primandproper/primitives-go/errors"
	"github.com/primandproper/primitives-go/filtering"
	"github.com/primandproper/primitives-go/identifiers"
	"github.com/primandproper/primitives-go/observability"
	"github.com/primandproper/primitives-go/tenancy"
)

// The SQLStore's ValueStore: what subjects answered, and what a setting resolves
// to for them.
var _ ValueStore = (*SQLStore)(nil)

// SetValue stores a subject's answer to one setting, inside the caller's
// transaction.
//
// The definition read runs on tx along with the write, and that is what makes
// the write checkable: raw has to be of the definition's kind and in its
// enumeration, and both of those are facts about a row that another transaction
// could be editing. Read outside the write, a value could be validated against
// an enumeration that no longer holds by the time it lands — which is the same
// stranded row [SQLStore.UpdateDefinition] refuses to create, reached from the
// other side. It also means a definition the caller created earlier in the same
// transaction is one this can set a value against.
//
// The write converges rather than inserts: the (scope, subject, definition)
// quadruple is unique across live and archived rows alike, so a subject setting
// a value they had cleared revives the row they cleared, keeping the creation
// time that records when they first answered.
func (s *SQLStore) SetValue(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	subject Subject,
	name, raw string,
) (*Value, error) {
	ctx, op := s.o11y.Begin(ctx,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(definitionKey, name),
		observability.WithValue(subjectTypeKey, subject.Type.String()),
		observability.WithValue(subjectIDKey, subject.ID),
	)
	defer op.End()

	if tx == nil {
		return nil, op.Error(ErrNilExecutor, "setting %q", name)
	}

	if err := s.addressable(scope, subject); err != nil {
		return nil, op.Error(err, "setting %q", name)
	}

	definition, err := s.readDefinitionByName(ctx, tx, scope, name)
	if err != nil {
		return nil, op.Error(err, "setting %q", name)
	}

	if err = definition.admits(raw); err != nil {
		return nil, op.Error(err, "setting %q", name)
	}

	if err = s.q.UpsertValue(ctx, tx,
		upsertValueParams(identifiers.New(), scope, subject, definition.ID, raw)); err != nil {
		return nil, op.Error(platformerrors.Wrap(err, "writing setting value"), "setting %q", name)
	}

	// Read back rather than assembled from what went in. The row the write
	// converged on may be one that already existed, so its id and its creation
	// time are the database's answer and not this call's — a caller handed the
	// id minted above would be holding one that names no row whenever the
	// subject had answered before.
	value, err := s.readValue(ctx, tx, scope, subject, definition.ID)
	if err != nil {
		return nil, op.Error(err, "setting %q", name)
	}

	return value, nil
}

// GetValue reads the answer a subject stored, on the caller's executor, without
// applying the definition's default. Resolve is what applies it.
func (s *SQLStore) GetValue(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	subject Subject,
	name string,
) (*Value, error) {
	ctx, op := s.o11y.Begin(ctx,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(definitionKey, name),
		observability.WithValue(subjectTypeKey, subject.Type.String()),
		observability.WithValue(subjectIDKey, subject.ID),
	)
	defer op.End()

	if q == nil {
		return nil, op.Error(ErrNilExecutor, "reading setting %q", name)
	}

	if err := s.addressable(scope, subject); err != nil {
		return nil, op.Error(err, "reading setting %q", name)
	}

	definition, err := s.readDefinitionByName(ctx, q, scope, name)
	if err != nil {
		return nil, op.Error(err, "reading setting %q", name)
	}

	value, err := s.readValue(ctx, q, scope, subject, definition.ID)
	if err != nil {
		return nil, op.Error(err, "reading setting %q", name)
	}

	return value, nil
}

// ClearValue takes a subject's answer back inside the caller's transaction,
// leaving them on the definition's default.
//
// The row is archived rather than deleted. What a subject answered is worth
// keeping — it is what a later restore restores and what an audit of a
// preference change reads — and the row is the thing the unique key is about, so
// archiving leaves the key claimed and the next write converging on the same
// row.
func (s *SQLStore) ClearValue(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	subject Subject,
	name string,
) error {
	ctx, op := s.o11y.Begin(ctx,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(definitionKey, name),
		observability.WithValue(subjectTypeKey, subject.Type.String()),
		observability.WithValue(subjectIDKey, subject.ID),
	)
	defer op.End()

	if tx == nil {
		return op.Error(ErrNilExecutor, "clearing setting %q", name)
	}

	if err := s.addressable(scope, subject); err != nil {
		return op.Error(err, "clearing setting %q", name)
	}

	definition, err := s.readDefinitionByName(ctx, tx, scope, name)
	if err != nil {
		return op.Error(err, "clearing setting %q", name)
	}

	count, err := s.q.ArchiveValue(ctx, tx, settingsdb.ArchiveValueParams{
		Scope:        scope,
		SubjectType:  string(subject.Type),
		SubjectID:    subject.ID,
		DefinitionID: definition.ID,
	})
	if err = guardCount(count, err, ErrValueNotFound, "clearing setting value"); err != nil {
		return op.Error(err, "clearing setting %q", name)
	}

	return nil
}

// DeleteValuesForSubject destroys everything one subject answered within the
// scope, cleared answers included, and reports how many rows that was.
//
// Zero is not an error. An erasure runs against whatever the subject actually
// left behind, and a subject who never answered is a subject with nothing here
// to erase — reporting that as a failure would fail an erasure that succeeded.
//
// A subject's values are never the only thing an erasure removes, so a delete
// that committed on its own would be the one row set gone when the rest of the
// erasure rolled back.
func (s *SQLStore) DeleteValuesForSubject(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	subject Subject,
) (int64, error) {
	ctx, op := s.o11y.Begin(ctx,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(subjectTypeKey, subject.Type.String()),
		observability.WithValue(subjectIDKey, subject.ID),
	)
	defer op.End()

	if tx == nil {
		return 0, op.Error(ErrNilExecutor, "erasing setting values")
	}

	if err := s.addressable(scope, subject); err != nil {
		return 0, op.Error(err, "erasing setting values")
	}

	deleted, err := s.q.DeleteValuesForSubject(ctx, tx, settingsdb.DeleteValuesForSubjectParams{
		Scope:       scope,
		SubjectType: string(subject.Type),
		SubjectID:   subject.ID,
	})
	if err != nil {
		return 0, op.Error(err, "erasing setting values")
	}

	op.Set(countKey, deleted)

	return deleted, nil
}

// ListValuesForSubject pages everything one subject has answered, on the
// caller's executor.
func (s *SQLStore) ListValuesForSubject(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	subject Subject,
	filter *filtering.QueryFilter,
) (*filtering.QueryFilteredResult[Value], error) {
	ctx, op := s.o11y.Begin(ctx,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(subjectTypeKey, subject.Type.String()),
		observability.WithValue(subjectIDKey, subject.ID),
	)
	defer op.End()

	if q == nil {
		return nil, op.Error(ErrNilExecutor, "listing setting values")
	}

	if err := s.addressable(scope, subject); err != nil {
		return nil, op.Error(err, "listing setting values")
	}

	page, err := s.listValuesForSubject(ctx, q, scope, subject, filter)
	if err != nil {
		return nil, op.Error(err, "listing setting values")
	}

	op.SpanOnly(countKey, len(page.Data))

	return page, nil
}

// ListValuesForDefinition pages everyone who has answered one setting, on the
// caller's executor.
func (s *SQLStore) ListValuesForDefinition(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	name string,
	filter *filtering.QueryFilter,
) (*filtering.QueryFilteredResult[Value], error) {
	ctx, op := s.o11y.Begin(ctx,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(definitionKey, name),
	)
	defer op.End()

	if q == nil {
		return nil, op.Error(ErrNilExecutor, "listing values for setting %q", name)
	}

	if err := scope.Validate(); err != nil {
		return nil, op.Error(err, "listing values for setting %q", name)
	}

	definition, err := s.readDefinitionByName(ctx, q, scope, name)
	if err != nil {
		return nil, op.Error(err, "listing values for setting %q", name)
	}

	page, err := s.listValuesForDefinition(ctx, q, scope, definition.ID, filter)
	if err != nil {
		return nil, op.Error(err, "listing values for setting %q", name)
	}

	op.SpanOnly(countKey, len(page.Data))

	return page, nil
}

// Resolve answers one setting for one subject, on the caller's executor: their
// value, else the definition's default, else neither.
//
// The executor is what lets a service that has just written an override answer
// with it. Handed the transaction that wrote it, both reads here see it; handed
// the client's reader from inside that transaction, they would see what the
// subject had before.
func (s *SQLStore) Resolve(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	subject Subject,
	name string,
) (*Resolution, error) {
	ctx, op := s.o11y.Begin(ctx,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(definitionKey, name),
		observability.WithValue(subjectTypeKey, subject.Type.String()),
		observability.WithValue(subjectIDKey, subject.ID),
	)
	defer op.End()

	if q == nil {
		return nil, op.Error(ErrNilExecutor, "resolving setting %q", name)
	}

	if err := s.addressable(scope, subject); err != nil {
		return nil, op.Error(err, "resolving setting %q", name)
	}

	definition, err := s.readDefinitionByName(ctx, q, scope, name)
	if err != nil {
		return nil, op.Error(err, "resolving setting %q", name)
	}

	value, err := s.readValue(ctx, q, scope, subject, definition.ID)
	if err != nil && !errors.Is(err, ErrValueNotFound) {
		return nil, op.Error(err, "resolving setting %q", name)
	}

	resolution := resolve(definition, value)

	op.SpanOnly(sourceKey, resolution.Source.String())
	s.countResolution(ctx, resolution.Source)

	return resolution, nil
}

// ResolveAll answers every live setting in the scope for one subject, sorted by
// name, on the caller's executor.
//
// Two walks rather than one resolution per setting: the catalog, and the
// subject's own answers. Both are bounded by the size of the catalog — a
// subject can have answered at most one of each — and reading them separately is
// what lets a setting nobody has answered appear in the result at its default,
// which is exactly what a preferences page renders.
//
// Both run on q, so a transaction that has just defined a setting and answered
// it resolves both rather than waiting on its own commit.
func (s *SQLStore) ResolveAll(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	subject Subject,
) ([]*Resolution, error) {
	ctx, op := s.o11y.Begin(ctx,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(subjectTypeKey, subject.Type.String()),
		observability.WithValue(subjectIDKey, subject.ID),
	)
	defer op.End()

	if q == nil {
		return nil, op.Error(ErrNilExecutor, "resolving settings")
	}

	if err := s.addressable(scope, subject); err != nil {
		return nil, op.Error(err, "resolving settings")
	}

	var definitions []*Definition

	if err := walkPages(
		func(filter *filtering.QueryFilter) (*filtering.QueryFilteredResult[Definition], error) {
			return s.listDefinitions(ctx, q, scope, filter)
		},
		func(definition *Definition) error {
			definitions = append(definitions, definition)

			return nil
		}); err != nil {
		return nil, op.Error(err, "resolving settings")
	}

	answers := map[string]*Value{}

	if err := walkPages(
		func(filter *filtering.QueryFilter) (*filtering.QueryFilteredResult[Value], error) {
			return s.listValuesForSubject(ctx, q, scope, subject, filter)
		},
		func(value *Value) error {
			answers[value.DefinitionID] = value

			return nil
		}); err != nil {
		return nil, op.Error(err, "resolving settings")
	}

	resolutions := make([]*Resolution, 0, len(definitions))
	for _, definition := range definitions {
		resolution := resolve(definition, answers[definition.ID])
		s.countResolution(ctx, resolution.Source)
		resolutions = append(resolutions, resolution)
	}

	// Sorted by name rather than by the id the pages walked, because the name is
	// what a caller has and the id is a mint order nobody chose. A caller
	// rendering these wants the same order twice.
	slices.SortFunc(resolutions, func(a, b *Resolution) int {
		return strings.Compare(a.Definition.Name, b.Definition.Name)
	})

	op.SpanOnly(countKey, len(resolutions))

	return resolutions, nil
}

// resolve is the value-else-default-else-neither rule, in one place.
//
// It is a function rather than a method because both callers have already done
// the reading, and because it is the whole of what "resolution" means here: a
// stored value wins, a default answers when there is none, and a setting with
// neither is reported as unset rather than as its kind's zero.
func resolve(definition *Definition, value *Value) *Resolution {
	switch {
	case value != nil:
		return &Resolution{Definition: definition, Value: value, Raw: value.Raw, Source: SourceSubject}
	case definition.Default != nil:
		return &Resolution{Definition: definition, Raw: *definition.Default, Source: SourceDefault}
	default:
		return &Resolution{Definition: definition, Source: SourceUnset}
	}
}

// readValue is the read of one subject's answer, keyed on the natural key rather
// than on the id the row carries.
func (s *SQLStore) readValue(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	subject Subject,
	definitionID string,
) (*Value, error) {
	row, err := s.q.GetValue(ctx, q, settingsdb.GetValueParams{
		Scope:        scope,
		SubjectType:  string(subject.Type),
		SubjectID:    subject.ID,
		DefinitionID: definitionID,
	})
	if err != nil {
		return nil, notFound(err, ErrValueNotFound)
	}

	return valueFromRow(&row), nil
}

// listValuesForSubject is the paged read behind the exported method and behind
// ResolveAll's second walk.
func (s *SQLStore) listValuesForSubject(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	subject Subject,
	filter *filtering.QueryFilter,
) (*filtering.QueryFilteredResult[Value], error) {
	filter = pageFilter(filter)

	listRows, err := sortedRows(filter,
		func() ([]settingsdb.ListValuesForSubjectRow, error) {
			return s.q.ListValuesForSubject(ctx, q, listValuesForSubjectParams(scope, subject, filter))
		},
		func() ([]settingsdb.ListValuesForSubjectDescendingRow, error) {
			return s.q.ListValuesForSubjectDescending(ctx, q,
				settingsdb.ListValuesForSubjectDescendingParams(listValuesForSubjectParams(scope, subject, filter)))
		},
		func(r settingsdb.ListValuesForSubjectDescendingRow) settingsdb.ListValuesForSubjectRow {
			return settingsdb.ListValuesForSubjectRow(r)
		})
	if err != nil {
		return nil, platformerrors.Wrap(err, "listing setting values")
	}

	return drainValues(listRows, valuePageRowForSubject, filter), nil
}

// listValuesForDefinition is the paged read behind the exported method and
// behind the walk UpdateDefinition runs before it changes a kind or an
// enumeration.
//
// Both callers hold an executor already: the exported read is handed one, and
// the walk runs on the transaction the edit is being written in — where reading
// through a connection of the store's own would check the edit against a
// snapshot the write is not part of.
func (s *SQLStore) listValuesForDefinition(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	definitionID string,
	filter *filtering.QueryFilter,
) (*filtering.QueryFilteredResult[Value], error) {
	filter = pageFilter(filter)

	listRows, err := sortedRows(filter,
		func() ([]settingsdb.ListValuesForDefinitionRow, error) {
			return s.q.ListValuesForDefinition(ctx, q, listValuesForDefinitionParams(scope, definitionID, filter))
		},
		func() ([]settingsdb.ListValuesForDefinitionDescendingRow, error) {
			return s.q.ListValuesForDefinitionDescending(ctx, q,
				settingsdb.ListValuesForDefinitionDescendingParams(
					listValuesForDefinitionParams(scope, definitionID, filter)))
		},
		func(r settingsdb.ListValuesForDefinitionDescendingRow) settingsdb.ListValuesForDefinitionRow {
			return settingsdb.ListValuesForDefinitionRow(r)
		})
	if err != nil {
		return nil, platformerrors.Wrap(err, "listing values for setting")
	}

	return drainValues(listRows, valuePageRowForDefinition, filter), nil
}

// drainValues is the last two steps both value pages share: convert the rows,
// and cut the page.
//
// The cursor is the id, because both statements order by it. A cursor naming a
// position in an order the query does not use is a page that skips rows and
// repeats others, with nothing reporting an error.
func drainValues[Row any](
	rows []Row,
	convert func(*Row) pageRow[Value],
	filter *filtering.QueryFilter,
) *filtering.QueryFilteredResult[Value] {
	page := make([]pageRow[Value], 0, len(rows))
	for i := range rows {
		page = append(page, convert(&rows[i]))
	}

	return filtering.Drain(page, pageValue, pageCounts, func(v *Value) string { return v.ID }, filter)
}

// addressable is the guard every value-side method opens with: a scope that
// names a tenancy, and a subject that names somebody.
//
// Both fail before any query rather than as a driver error or an empty page. An
// unset scope is caught by tenancy.Scope itself; a subject with no type or no id
// would otherwise be a legal-looking read of every row whose columns happen to
// be empty.
func (s *SQLStore) addressable(scope tenancy.Scope, subject Subject) error {
	if err := scope.Validate(); err != nil {
		return err
	}

	return subject.Validate()
}
