package settings

import (
	"context"
	"database/sql"
	"errors"
	"slices"

	"github.com/primandproper/platform-go/v14/settings/internal/settingsdb"

	"github.com/primandproper/primitives-go/database"
	platformerrors "github.com/primandproper/primitives-go/errors"
	"github.com/primandproper/primitives-go/filtering"
	"github.com/primandproper/primitives-go/identifiers"
	"github.com/primandproper/primitives-go/observability"
	"github.com/primandproper/primitives-go/tenancy"
)

// The SQLStore's DefinitionStore: the catalog, written by whoever administers
// a deployment and read by everything else.
var _ DefinitionStore = (*SQLStore)(nil)

// CreateDefinition adds a setting to the scope's catalog, inside the caller's
// transaction.
//
// The definition, its enumeration and the read-back of the creation time all run
// on tx, along with whatever the caller is writing beside them. Without one
// transaction over the three, a definition could exist with half its enumeration
// written — which is the state that makes every value legal or no value legal
// depending on which half landed, and an enumeration is what every subsequent
// write is checked against. See [Store] for why the transaction is the caller's.
func (s *SQLStore) CreateDefinition(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	definition *Definition,
) (*Definition, error) {
	ctx, op := s.o11y.Begin(ctx, observability.WithValue(scopeKey, scope.String()))
	defer op.End()

	if tx == nil {
		return nil, op.Error(ErrNilExecutor, "creating setting definition")
	}

	if definition == nil {
		return nil, op.Error(ErrNilDefinition, "creating setting definition")
	}

	op.Set(definitionKey, definition.Name)

	if err := scope.Validate(); err != nil {
		return nil, op.Error(err, "creating setting definition %q", definition.Name)
	}

	if err := definition.validate(); err != nil {
		return nil, op.Error(err, "creating setting definition %q", definition.Name)
	}

	// A copy of the caller's, under the scope the argument names, with its
	// enumeration sorted and its id minted where the caller left it empty.
	created := *definition
	created.Scope = scope
	created.Enumeration = sortedEnumeration(definition.Enumeration)

	if created.ID == "" {
		created.ID = identifiers.New()
	}

	if err := s.insertDefinition(ctx, tx, scope, &created); err != nil {
		return nil, op.Error(err, "creating setting definition %q", created.Name)
	}

	return &created, nil
}

// insertDefinition is the statements the create runs: the name collision check,
// the row, its enumeration, and the read-back of the creation time onto created.
func (s *SQLStore) insertDefinition(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	created *Definition,
) error {
	if err := s.refuseTakenName(ctx, q, scope, created.Name, nil); err != nil {
		return err
	}

	if err := s.q.CreateDefinition(ctx, q, createDefinitionParams(created, scope)); err != nil {
		return platformerrors.Wrap(err, "creating setting definition")
	}

	if err := s.writeEnumeration(ctx, q, created.ID, created.Enumeration); err != nil {
		return err
	}

	return s.stampCreatedAt(ctx, q, created)
}

// GetDefinition reads one of the scope's live definitions by id, on the caller's
// executor.
func (s *SQLStore) GetDefinition(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	definitionID string,
) (*Definition, error) {
	ctx, op := s.o11y.Begin(ctx,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(definitionIDKey, definitionID),
	)
	defer op.End()

	if q == nil {
		return nil, op.Error(ErrNilExecutor, "reading setting definition %q", definitionID)
	}

	if err := scope.Validate(); err != nil {
		return nil, op.Error(err, "reading setting definition %q", definitionID)
	}

	definition, err := s.readDefinition(ctx, q, scope, definitionID)
	if err != nil {
		return nil, op.Error(err, "reading setting definition %q", definitionID)
	}

	return definition, nil
}

// GetDefinitionByName reads one of the scope's live definitions by the name
// application code spells, on the caller's executor.
func (s *SQLStore) GetDefinitionByName(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	name string,
) (*Definition, error) {
	ctx, op := s.o11y.Begin(ctx,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(definitionKey, name),
	)
	defer op.End()

	if q == nil {
		return nil, op.Error(ErrNilExecutor, "reading setting definition %q", name)
	}

	if err := scope.Validate(); err != nil {
		return nil, op.Error(err, "reading setting definition %q", name)
	}

	definition, err := s.readDefinitionByName(ctx, q, scope, name)
	if err != nil {
		return nil, op.Error(err, "reading setting definition %q", name)
	}

	return definition, nil
}

// ListDefinitions pages the scope's catalog, in the direction the filter
// names, on the caller's executor.
//
// The direction is a choice between two generated statements rather than an
// argument either of them binds — see sortedRows — so what this method does with
// filter.SortBy is pick the one whose ORDER BY and cursor comparison agree with
// it.
func (s *SQLStore) ListDefinitions(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	filter *filtering.QueryFilter,
) (*filtering.QueryFilteredResult[Definition], error) {
	ctx, op := s.o11y.Begin(ctx, observability.WithValue(scopeKey, scope.String()))
	defer op.End()

	if q == nil {
		return nil, op.Error(ErrNilExecutor, "listing setting definitions")
	}

	if err := scope.Validate(); err != nil {
		return nil, op.Error(err, "listing setting definitions")
	}

	page, err := s.listDefinitions(ctx, q, scope, filter)
	if err != nil {
		return nil, op.Error(err, "listing setting definitions")
	}

	op.SpanOnly(countKey, len(page.Data))

	return page, nil
}

// listDefinitions is the paged read behind the exported method and behind
// ResolveAll's first walk, on whatever executor the caller is holding.
func (s *SQLStore) listDefinitions(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	filter *filtering.QueryFilter,
) (*filtering.QueryFilteredResult[Definition], error) {
	filter = pageFilter(filter)

	listRows, err := sortedRows(filter,
		func() ([]settingsdb.ListDefinitionsRow, error) {
			return s.q.ListDefinitions(ctx, q, listDefinitionsParams(scope, filter))
		},
		func() ([]settingsdb.ListDefinitionsDescendingRow, error) {
			return s.q.ListDefinitionsDescending(ctx, q,
				settingsdb.ListDefinitionsDescendingParams(listDefinitionsParams(scope, filter)))
		},
		func(r settingsdb.ListDefinitionsDescendingRow) settingsdb.ListDefinitionsRow {
			return settingsdb.ListDefinitionsRow(r)
		})
	if err != nil {
		return nil, err
	}

	rows := make([]pageRow[Definition], 0, len(listRows))
	for i := range listRows {
		rows = append(rows, definitionPageRow(&listRows[i]))
	}

	// One batched read for the whole page's enumerations rather than one per
	// definition — see querygen.Generator.SetReadQuery, and settings/migrations
	// for why an enumeration is its own table.
	if err = s.hydrateEnumerations(ctx, q, pageValues(rows)); err != nil {
		return nil, err
	}

	// The cursor is the id, because the statement orders by it. A cursor naming
	// a position in an order the query does not use is a page that skips rows
	// and repeats others, with nothing reporting an error.
	return filtering.Drain(rows, pageValue, pageCounts,
		func(d *Definition) string { return d.ID }, filter), nil
}

// UpdateDefinition rewrites a definition, enumeration included, inside the
// caller's transaction, refusing an edit that some stored value no longer
// satisfies.
//
// The refusal is the rule this store owns. A narrowed enumeration or a changed
// kind decides how every value already written against the definition is read,
// and an edit applied over them leaves rows that exist, resolve, and fail to
// parse — a setting that works for most subjects and is broken for whoever chose
// the value somebody just made illegal. So the values are walked first, in the
// caller's transaction, and the first one the new definition would not admit
// stops it.
//
// The walk reading through tx is what lets an administrator clear the offending
// values and narrow the enumeration together: a value cleared earlier in the
// transaction is already not live, and does not strand the edit.
//
// Only live values are checked. A cleared value resolves to the default rather
// than to itself, and setting it again goes through the write path with the new
// definition in hand.
//
// The walk is skipped where the edit cannot strand anything: renaming a setting,
// rewording it, changing its default or its admin flag leaves every stored value
// exactly as legal as it was.
//
// What comes back is the definition as the edit left it, read on tx after the
// write. The caller's argument is not mutated and is not what is returned: the
// row carries a last_updated_at the server stamped and this call never held, so
// a value assembled from the argument would say a definition edited a moment ago
// has never been edited. See [DefinitionStore.UpdateDefinition] for what an
// audit entry beside the write does with it.
func (s *SQLStore) UpdateDefinition(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	definition *Definition,
) (*Definition, error) {
	ctx, op := s.o11y.Begin(ctx, observability.WithValue(scopeKey, scope.String()))
	defer op.End()

	if tx == nil {
		return nil, op.Error(ErrNilExecutor, "updating setting definition")
	}

	if definition == nil {
		return nil, op.Error(ErrNilDefinition, "updating setting definition")
	}

	op.Set(definitionIDKey, definition.ID)
	op.Set(definitionKey, definition.Name)

	if err := scope.Validate(); err != nil {
		return nil, op.Error(err, "updating setting definition %q", definition.Name)
	}

	if definition.ID == "" {
		return nil, op.Error(platformerrors.ErrInvalidIDProvided, "updating setting definition %q", definition.Name)
	}

	if err := definition.validate(); err != nil {
		return nil, op.Error(err, "updating setting definition %q", definition.Name)
	}

	updated := *definition
	updated.Scope = scope
	updated.Enumeration = sortedEnumeration(definition.Enumeration)

	edited, err := s.rewriteDefinition(ctx, tx, scope, &updated)
	if err != nil {
		return nil, op.Error(err, "updating setting definition %q", updated.Name)
	}

	return edited, nil
}

// rewriteDefinition is the statements the update runs: the read of what is
// there, the name collision check, the stranded-value walk where the edit
// reinterprets stored values, the row, its enumeration, and the read-back of
// what all of that left.
//
// The read-back is the same read every caller of GetDefinition makes, on the
// transaction the edit was written in — so it sees the row and the enumeration
// this call has just written rather than the ones the last commit left, and the
// definition it returns carries the stamp the server assigned. The edit is not
// archived, so it is the live read rather than readArchivedDefinition.
func (s *SQLStore) rewriteDefinition(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	updated *Definition,
) (*Definition, error) {
	existing, err := s.readDefinition(ctx, q, scope, updated.ID)
	if err != nil {
		return nil, err
	}

	if err = s.refuseTakenName(ctx, q, scope, updated.Name, &updated.ID); err != nil {
		return nil, err
	}

	if reinterprets(existing, updated) {
		if err = s.refuseStrandedValues(ctx, q, scope, updated); err != nil {
			return nil, err
		}
	}

	count, err := s.q.UpdateDefinition(ctx, q, updateDefinitionParams(updated, scope))
	if err = guardCount(count, err, ErrDefinitionNotFound, "updating setting definition"); err != nil {
		return nil, err
	}

	if err = s.writeEnumeration(ctx, q, updated.ID, updated.Enumeration); err != nil {
		return nil, err
	}

	return s.readDefinition(ctx, q, scope, updated.ID)
}

// ArchiveDefinition retires one of the scope's settings, inside the caller's
// transaction, and answers with the definition it retired.
//
// It takes the caller's transaction rather than a writer of the store's own:
// retiring a setting is an administrative act with an audit entry beside it, and
// the entry is worth what its atomicity with the retirement is worth. The row it
// hands back is what that entry describes — the name, the kind and the
// enumeration a caller holding only an id never had, and the retirement stamp
// nothing outside the server could supply.
//
// Two statements rather than one, and the second cannot be the read every other
// caller makes: the row this one just archived is exactly the row a live read
// excludes. See readArchivedDefinition.
func (s *SQLStore) ArchiveDefinition(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	definitionID string,
) (*Definition, error) {
	ctx, op := s.o11y.Begin(ctx,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(definitionIDKey, definitionID),
	)
	defer op.End()

	if tx == nil {
		return nil, op.Error(ErrNilExecutor, "archiving setting definition %q", definitionID)
	}

	if err := scope.Validate(); err != nil {
		return nil, op.Error(err, "archiving setting definition %q", definitionID)
	}

	count, err := s.q.ArchiveDefinition(ctx, tx, settingsdb.ArchiveDefinitionParams{ID: definitionID, Scope: scope})
	if err = guardCount(count, err, ErrDefinitionNotFound, "archiving setting definition"); err != nil {
		return nil, op.Error(err, "archiving setting definition %q", definitionID)
	}

	archived, err := s.readArchivedDefinition(ctx, tx, scope, definitionID)
	if err != nil {
		return nil, op.Error(err, "reading back the archived setting definition %q", definitionID)
	}

	return archived, nil
}

// readDefinition is the read by id, through whatever executor the caller is
// holding, with its enumeration attached.
func (s *SQLStore) readDefinition(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	definitionID string,
) (*Definition, error) {
	row, err := s.q.GetDefinition(ctx, q, settingsdb.GetDefinitionParams{ID: definitionID, Scope: scope})
	if err != nil {
		return nil, notFound(err, ErrDefinitionNotFound)
	}

	definition := definitionFromRow(&row)

	if err = s.hydrateEnumerations(ctx, q, []*Definition{definition}); err != nil {
		return nil, err
	}

	return definition, nil
}

// readArchivedDefinition is the archive's read-back: the row ArchiveDefinition
// has just retired, on the transaction that retired it.
//
// It is a second statement rather than readDefinition because the two ask
// opposite questions. Every live read here filters archived_at IS NULL, which is
// what makes a retired setting absent from a catalog, so the read that would
// describe what an archive did is the one read that cannot see it. The statement
// this runs carries the complement — see settings/internal/queries — so a row
// the archive did not move is not a row it answers with either.
//
// The enumeration is attached the same way every other definition read attaches
// it. Archiving a definition leaves its options alone, and a caller recording
// what was retired wants the values it admitted.
func (s *SQLStore) readArchivedDefinition(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	definitionID string,
) (*Definition, error) {
	row, err := s.q.GetArchivedDefinition(ctx, q,
		settingsdb.GetArchivedDefinitionParams{ID: definitionID, Scope: scope})
	if err != nil {
		return nil, notFound(err, ErrDefinitionNotFound)
	}

	// The two statements project the same columns in the same order, so the
	// conversion is the whole of what they have to say to each other. A second
	// hand-written converter beside definitionFromRow could assign a field wrong
	// and nothing would report it; this fails to compile the day the projections
	// diverge.
	live := settingsdb.GetDefinitionRow(row)

	definition := definitionFromRow(&live)

	if err = s.hydrateEnumerations(ctx, q, []*Definition{definition}); err != nil {
		return nil, err
	}

	return definition, nil
}

// readDefinitionByName is the read every value-side method begins with, through
// whatever executor the caller is holding.
func (s *SQLStore) readDefinitionByName(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	name string,
) (*Definition, error) {
	if name == "" {
		return nil, ErrEmptyDefinitionName
	}

	row, err := s.q.GetDefinitionByName(ctx, q, settingsdb.GetDefinitionByNameParams{Name: name, Scope: scope})
	if err != nil {
		return nil, notFound(err, ErrDefinitionNotFound)
	}

	definition := definitionFromNameRow(&row)

	if err = s.hydrateEnumerations(ctx, q, []*Definition{definition}); err != nil {
		return nil, err
	}

	return definition, nil
}

// hydrateEnumerations attaches each definition's enumeration in one query.
//
// It is batched for the reason every N+1 read in this module is: a page of
// definitions whose options are read inside the loop that converts rows is one
// round trip per definition. The empty batch is answered here rather than sent,
// because the statement has no rendering of an empty set and what the generated
// code substitutes is a predicate matching no row — a round trip whose answer
// was known before it left, on the read that exists to save round trips.
//
// Every definition comes back with a non-nil enumeration, empty where it has
// none. A nil one would be indistinguishable from a definition this never
// reached, and that reading fails open: an enumeration nobody attached admits
// every value.
func (s *SQLStore) hydrateEnumerations(
	ctx context.Context,
	q database.SQLQueryExecutor,
	definitions []*Definition,
) error {
	if len(definitions) == 0 {
		return nil
	}

	ids := make([]string, 0, len(definitions))
	for _, definition := range definitions {
		definition.Enumeration = []string{}
		ids = append(ids, definition.ID)
	}

	rows, err := s.q.ListDefinitionOptionsByDefinitionIDs(ctx, q,
		settingsdb.ListDefinitionOptionsByDefinitionIDsParams{IDs: ids})
	if err != nil {
		return platformerrors.Wrap(err, "reading setting enumerations")
	}

	byDefinition := make(map[string][]string, len(definitions))
	for i := range rows {
		byDefinition[rows[i].DefinitionID] = append(byDefinition[rows[i].DefinitionID], rows[i].Value)
	}

	for _, definition := range definitions {
		if options, ok := byDefinition[definition.ID]; ok {
			definition.Enumeration = options
		}
	}

	return nil
}

// writeEnumeration replaces a definition's option set wholesale.
//
// Replaced rather than diffed: diffing means reading the current set first and
// computing two statements from it, which is three round trips to express "these
// are the legal values now" and a read-modify-write besides. One statement per
// option rather than one multi-row INSERT, because the multi-row form's shape is
// the caller's cardinality — no static text for sqlc to check — and the
// cardinalities are single-digit inside the transaction the definition's own
// write is already in.
func (s *SQLStore) writeEnumeration(
	ctx context.Context,
	q database.SQLQueryExecutor,
	definitionID string,
	enumeration []string,
) error {
	if _, err := s.q.DeleteDefinitionOptions(ctx, q,
		settingsdb.DeleteDefinitionOptionsParams{DefinitionID: definitionID}); err != nil {
		return platformerrors.Wrap(err, "clearing setting enumeration")
	}

	for _, option := range enumeration {
		if err := s.q.InsertDefinitionOption(ctx, q, settingsdb.InsertDefinitionOptionParams{
			DefinitionID: definitionID,
			Value:        option,
		}); err != nil {
			return platformerrors.Wrap(err, "writing setting enumeration")
		}
	}

	return nil
}

// refuseTakenName reports ErrDefinitionNameTaken when the scope already defines
// the name, excluding the row being updated where there is one.
//
// The check runs inside the caller's transaction, and it is a check rather than
// a reliance on the unique index because a constraint violation reaches a caller
// as a driver error naming an index — which the caller cannot tell apart from
// the database being unwell, and cannot show to a person. The index is still
// what makes it true under a concurrent write.
func (s *SQLStore) refuseTakenName(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	name string,
	exceptID *string,
) error {
	_, err := s.q.GetDefinitionIDByName(ctx, q, settingsdb.GetDefinitionIDByNameParams{
		Name:               name,
		Scope:              scope,
		ExceptDefinitionID: exceptID,
	})

	switch {
	case err == nil:
		return platformerrors.Wrapf(ErrDefinitionNameTaken, "setting %q", name)
	case errors.Is(err, sql.ErrNoRows):
		return nil
	default:
		return platformerrors.Wrap(err, "checking setting name")
	}
}

// refuseStrandedValues walks every live value stored against the definition and
// refuses the edit at the first one the new definition would not admit.
//
// A page walk rather than one read of everything: the number of subjects that
// have answered a setting is the number of subjects, and an administrator's edit
// is not the place to read all of them into memory at once.
func (s *SQLStore) refuseStrandedValues(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	definition *Definition,
) error {
	return walkPages(
		func(filter *filtering.QueryFilter) (*filtering.QueryFilteredResult[Value], error) {
			return s.listValuesForDefinition(ctx, q, scope, definition.ID, filter)
		},
		func(value *Value) error {
			if err := definition.admits(value.Raw); err != nil {
				return platformerrors.Wrapf(ErrStrandedValues,
					"%s %q holds %q for setting %q: %s",
					value.Subject.Type, value.Subject.ID, value.Raw, definition.Name, err)
			}

			return nil
		})
}

// stampCreatedAt writes the creation time the database assigned onto the value
// the caller handed over.
//
// The column is the database's — see settings/internal/queries — so the create
// does not carry it, and the alternative to this read is a caller whose struct
// says 0001-01-01 for a row written a moment ago. CreatedAt is exported and a
// service serializes the value it just created straight into a response, where a
// zero time renders as a date rather than reading as an absence. It costs one
// round trip inside the transaction the write is already in, and it reads its own
// uncommitted row on all three servers.
func (s *SQLStore) stampCreatedAt(ctx context.Context, q database.SQLQueryExecutor, definition *Definition) error {
	row, err := s.q.GetDefinitionCreatedAt(ctx, q, settingsdb.GetDefinitionCreatedAtParams{ID: definition.ID})
	if err != nil {
		return platformerrors.Wrap(err, "reading back the setting definition's creation time")
	}

	definition.CreatedAt = row.CreatedAt.UTC()

	return nil
}

// reinterprets reports whether an edit changes how stored values are read: the
// kind they parse as, or the set they have to be in.
//
// A narrowed enumeration and a widened one are both reported, because the cheap
// comparison is equality and the expensive one is a walk — and an edit that adds
// an option runs a walk that approves everything, which costs a query and
// cannot be wrong. Testing for narrowing specifically would be the subset check
// this is trying to enforce, written twice.
func reinterprets(existing, updated *Definition) bool {
	return existing.Kind != updated.Kind || !slices.Equal(existing.Enumeration, updated.Enumeration)
}

// sortedEnumeration returns the enumeration as it is stored and read back:
// sorted, and a copy, so the caller's slice is not reordered under them.
//
// The sort is what makes the value a caller holds after a write the same value
// the next read hands them. An enumeration is a set — see settings/migrations —
// and the batched read comes back ordered by the option, so a struct that kept
// the caller's order would disagree with the row the moment it was re-read.
func sortedEnumeration(enumeration []string) []string {
	sorted := slices.Clone(enumeration)
	slices.Sort(sorted)

	if sorted == nil {
		return []string{}
	}

	return sorted
}
