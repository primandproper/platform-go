package settings

import (
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/primandproper/platform-go/v14/settings/migrations"

	"github.com/primandproper/primitives-go/v2/database/dialect"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/pointer"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestKind(T *testing.T) {
	T.Parallel()

	T.Run("the set is closed", func(t *testing.T) {
		t.Parallel()

		for _, kind := range []Kind{KindString, KindBool, KindInt, KindFloat} {
			test.True(t, kind.Valid(), test.Sprintf("%s", kind))
			test.EqOp(t, string(kind), kind.String())
		}

		test.False(t, Kind("").Valid())
		test.False(t, Kind("date").Valid())
		test.False(t, Kind("Boolean").Valid())
	})

	T.Run("what each kind parses", func(t *testing.T) {
		t.Parallel()

		legal := map[Kind][]string{
			// The empty string is a string and nothing else, which is the one
			// asymmetry in this table: a text setting answered with nothing has
			// still been answered.
			KindString: {"", "anything", "  "},
			KindBool:   {"true", "false", "TRUE", "1", "0"},
			KindInt:    {"0", "-1", "9223372036854775807"},
			KindFloat:  {"0", "-1.5", "1e3"},
		}

		for kind, values := range legal {
			for _, value := range values {
				test.NoError(t, kind.parses(value), test.Sprintf("%s %q", kind, value))
			}
		}

		illegal := map[Kind][]string{
			KindBool:  {"", "yes", "on", "2"},
			KindInt:   {"", "1.5", "one", "9223372036854775808"},
			KindFloat: {"", "half", "1,5"},
		}

		for kind, values := range illegal {
			for _, value := range values {
				test.ErrorIs(t, kind.parses(value), ErrMalformedValue, test.Sprintf("%s %q", kind, value))
			}
		}

		test.ErrorIs(t, Kind("date").parses("2026-08-28"), ErrUnknownKind)
	})
}

func TestSubject_Validate(T *testing.T) {
	T.Parallel()

	test.NoError(T, Subject{Type: SubjectUser, ID: "u"}.Validate())
	test.ErrorIs(T, Subject{ID: "u"}.Validate(), ErrEmptySubjectType)
	test.ErrorIs(T, Subject{Type: SubjectUser}.Validate(), ErrEmptySubjectID)
	test.EqOp(T, "user", SubjectUser.String())
	test.EqOp(T, "account", SubjectAccount.String())

	// The bound is what keeps a server not in strict mode from truncating a
	// subject onto another principal's row — see MaxSubjectTypeLength. Each case
	// is one byte over its column, because one byte over is the case a limit
	// written down as the wrong number still passes, and each is paired with the
	// limit itself, which must be accepted.
	T.Run("each stored string is bounded by its column", func(t *testing.T) {
		t.Parallel()

		for _, tc := range []struct {
			subject Subject
			name    string
		}{
			{
				name: "subject type",
				subject: Subject{
					Type: SubjectType(strings.Repeat("t", MaxSubjectTypeLength+1)),
					ID:   "u",
				},
			},
			{
				name: "subject id",
				subject: Subject{
					Type: SubjectUser,
					ID:   strings.Repeat("i", MaxSubjectIDLength+1),
				},
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()

				err := tc.subject.Validate()

				test.ErrorIs(t, err, ErrSubjectValueTooLong)

				// Answered as a bad request by the platform mapper rather than
				// by a case of this package's own, which is the whole reason it
				// wraps this sentinel — see internal/sentinelmatrix.
				test.ErrorIs(t, err, platformerrors.ErrUnrecognizedInputValue)

				// The message says which of the two it was, because "too long"
				// with two candidates is a refusal the caller has to guess at.
				test.StrContains(t, err.Error(), tc.name)
			})
		}

		test.NoError(t, Subject{
			Type: SubjectType(strings.Repeat("t", MaxSubjectTypeLength)),
			ID:   strings.Repeat("i", MaxSubjectIDLength),
		}.Validate())
	})

	// An empty subject is reported as empty rather than as a length: the two
	// refusals are different remedies, and a caller that sent nothing is not a
	// caller that sent too much.
	T.Run("emptiness is reported before length", func(t *testing.T) {
		t.Parallel()

		test.ErrorIs(t, Subject{Type: "", ID: strings.Repeat("i", MaxSubjectIDLength+1)}.Validate(),
			ErrEmptySubjectType)
	})
}

func TestDefinition_admits(T *testing.T) {
	T.Parallel()

	T.Run("a setting with no enumeration admits any value of its kind", func(t *testing.T) {
		t.Parallel()

		d := &Definition{Name: "n", Kind: KindInt}
		test.NoError(t, d.admits("42"))
		test.ErrorIs(t, d.admits("many"), ErrMalformedValue)
	})

	T.Run("an enumerated setting admits what it lists", func(t *testing.T) {
		t.Parallel()

		d := &Definition{Name: "n", Kind: KindString, Enumeration: []string{"daily", "weekly"}}
		test.NoError(t, d.admits("daily"))
		test.ErrorIs(t, d.admits("hourly"), ErrNotEnumerated)

		// The kind is checked first, so a value that is neither reports the
		// reason a caller can act on: a number the setting does not take is a
		// number, not an unlisted option.
		typed := &Definition{Name: "n", Kind: KindInt, Enumeration: []string{"1", "2"}}
		test.ErrorIs(t, typed.admits("three"), ErrMalformedValue)
		test.ErrorIs(t, typed.admits("3"), ErrNotEnumerated)
	})
}

func TestResolution(T *testing.T) {
	T.Parallel()

	T.Run("an accessor refuses the wrong kind before it refuses an absence", func(t *testing.T) {
		t.Parallel()

		// Both are true of this resolution, and the kind is the one the caller
		// can fix in their own code.
		unset := &Resolution{Definition: &Definition{Name: "n", Kind: KindString}, Source: SourceUnset}

		_, err := unset.Bool()
		test.ErrorIs(t, err, ErrKindMismatch)

		_, err = unset.String()
		test.ErrorIs(t, err, ErrSettingUnset)
	})

	T.Run("a malformed row is reported rather than coerced", func(t *testing.T) {
		t.Parallel()

		// Every write goes through Definition.admits, so this is a row somebody
		// wrote around the store. It is still not a false.
		r := &Resolution{
			Definition: &Definition{Name: "n", Kind: KindBool},
			Raw:        "affirmative",
			Source:     SourceSubject,
		}

		_, err := r.Bool()
		test.ErrorIs(t, err, ErrMalformedValue)
	})

	T.Run("a nil resolution answers rather than panics", func(t *testing.T) {
		t.Parallel()

		var r *Resolution

		test.False(t, r.Set())

		_, err := r.String()
		test.Error(t, err)

		_, err = (&Resolution{}).Int()
		test.Error(t, err)
	})

	T.Run("every kind reads back", func(t *testing.T) {
		t.Parallel()

		text, err := (&Resolution{
			Definition: &Definition{Kind: KindString}, Raw: "hello", Source: SourceDefault,
		}).String()
		must.NoError(t, err)
		test.EqOp(t, "hello", text)

		number, err := (&Resolution{
			Definition: &Definition{Kind: KindInt}, Raw: "-3", Source: SourceSubject,
		}).Int()
		must.NoError(t, err)
		test.EqOp(t, int64(-3), number)

		fraction, err := (&Resolution{
			Definition: &Definition{Kind: KindFloat}, Raw: "2.5", Source: SourceSubject,
		}).Float()
		must.NoError(t, err)
		test.EqOp(t, 2.5, fraction)

		test.EqOp(t, "subject", SourceSubject.String())
	})
}

func TestSortedEnumeration(T *testing.T) {
	T.Parallel()

	T.Run("it sorts a copy", func(t *testing.T) {
		t.Parallel()

		original := []string{"weekly", "daily"}
		sorted := sortedEnumeration(original)

		test.Eq(t, []string{"daily", "weekly"}, sorted)
		test.Eq(t, []string{"weekly", "daily"}, original)
	})

	T.Run("nothing becomes an empty set rather than nil", func(t *testing.T) {
		t.Parallel()

		test.NotNil(t, sortedEnumeration(nil))
		test.SliceEmpty(t, sortedEnumeration(nil))
	})
}

func TestReinterprets(T *testing.T) {
	T.Parallel()

	base := &Definition{Name: "n", Kind: KindString, Enumeration: []string{"a", "b"}}

	cases := map[string]struct {
		updated *Definition
		expect  bool
	}{
		"the same definition":   {&Definition{Kind: KindString, Enumeration: []string{"a", "b"}}, false},
		"a new name":            {&Definition{Kind: KindString, Enumeration: []string{"a", "b"}, Name: "m"}, false},
		"a new default":         {&Definition{Kind: KindString, Enumeration: []string{"a", "b"}, Default: pointer.To("a")}, false},
		"a narrowed set":        {&Definition{Kind: KindString, Enumeration: []string{"a"}}, true},
		"a widened set":         {&Definition{Kind: KindString, Enumeration: []string{"a", "b", "c"}}, true},
		"a different kind":      {&Definition{Kind: KindInt, Enumeration: []string{"a", "b"}}, true},
		"no enumeration at all": {&Definition{Kind: KindString}, true},
	}

	for name, c := range cases {
		T.Run(name, func(t *testing.T) {
			t.Parallel()

			test.EqOp(t, c.expect, reinterprets(base, c.updated))
		})
	}
}

func TestDefinition_withinBounds(T *testing.T) {
	T.Parallel()

	// The bound is what keeps MySQL's INSERT IGNORE from storing a truncated
	// definition and reporting success. Each case is one byte over its column,
	// because one byte over is the case a limit written down as the wrong number
	// still passes.
	T.Run("each stored string is bounded by its column", func(t *testing.T) {
		t.Parallel()

		for _, tc := range []struct {
			mangle func(*Definition)
			name   string
		}{
			{
				name:   "id",
				mangle: func(d *Definition) { d.ID = strings.Repeat("i", MaxDefinitionIDLength+1) },
			},
			{
				name:   "name",
				mangle: func(d *Definition) { d.Name = strings.Repeat("n", MaxDefinitionNameLength+1) },
			},
			{
				name:   "description",
				mangle: func(d *Definition) { d.Description = strings.Repeat("d", MaxDefinitionDescriptionLength+1) },
			},
			{
				name: "default",
				mangle: func(d *Definition) {
					d.Default = pointer.To(strings.Repeat("v", MaxDefinitionDefaultLength+1))
				},
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()

				definition := &Definition{Name: "theme", Kind: KindString}
				tc.mangle(definition)

				err := definition.validate()

				test.ErrorIs(t, err, ErrDefinitionValueTooLong)

				// Answered as a bad request by the platform mapper rather than
				// by a case of this package's own, which is the whole reason it
				// wraps this sentinel — see internal/sentinelmatrix.
				test.ErrorIs(t, err, platformerrors.ErrUnrecognizedInputValue)

				// The message says which string it was, because "too long" with
				// four candidates is a refusal the caller has to guess at.
				test.StrContains(t, err.Error(), tc.name)
			})
		}
	})

	T.Run("a definition exactly at each limit is admitted", func(t *testing.T) {
		t.Parallel()

		definition := &Definition{
			ID:          strings.Repeat("i", MaxDefinitionIDLength),
			Name:        strings.Repeat("n", MaxDefinitionNameLength),
			Description: strings.Repeat("d", MaxDefinitionDescriptionLength),
			Kind:        KindString,
			Default:     pointer.To(strings.Repeat("v", MaxDefinitionDefaultLength)),
		}

		must.NoError(t, definition.validate())
	})

	// An absent default is not a zero-length one, and the bound must not turn
	// the nil into a dereference.
	T.Run("a definition with no default is admitted", func(t *testing.T) {
		t.Parallel()

		must.NoError(t, (&Definition{Name: "theme", Kind: KindString}).validate())
	})
}

// innoDBKeyLimit is the widest key InnoDB will build, in bytes, and
// utf8mb4Bytes is what MySQL charges per character of a utf8mb4 VARCHAR. The
// product of a key's declared widths against them is what decides how wide
// subject_type and subject_id can be — see settings/migrations.
const (
	innoDBKeyLimit = 3072
	utf8mb4Bytes   = 4
)

// varcharColumn matches a MySQL column declaration wide enough to bound, which
// is every column of the uniqueness this test measures.
var varcharColumn = regexp.MustCompile(`(?m)^\s+(\w+)\s+VARCHAR\((\d+)\)`)

// uniqueKeyColumns matches the settings_values uniqueness and captures the
// column list it is built from, so the test measures the key the schema
// declares rather than a list copied out of it.
var uniqueKeyColumns = regexp.MustCompile(`UNIQUE KEY settings_values_subject_uniq \(([^)]+)\)`)

// TestSubjectBoundsMatchTheirColumns is the half of the bound that
// TestSubject_Validate cannot see.
//
// MaxSubjectTypeLength and MaxSubjectIDLength are only a promise about
// truncation if they are the widths the MySQL schema actually declares, and the
// two live in different files in different languages — the drift this pins is
// the one that reports nothing until a subject that Go accepted is truncated
// onto another principal's row.
func TestSubjectBoundsMatchTheirColumns(T *testing.T) {
	T.Parallel()

	values := mustValuesTable(T)

	T.Run("the Go bounds are the declared widths", func(t *testing.T) {
		t.Parallel()

		widths := varcharWidths(t, values)

		test.EqOp(t, MaxSubjectTypeLength, widths["subject_type"])
		test.EqOp(t, MaxSubjectIDLength, widths["subject_id"])
	})

	// Widening either column to the 255 the rest of this schema reaches for puts
	// the key over the budget, and the two servers that answer to this dialect
	// disagree about what that means: MySQL refuses the CREATE TABLE outright,
	// and MariaDB silently rewrites the unique key USING HASH. This is the only
	// place the budget is checked, because the container suite runs MariaDB,
	// where the over-wide key creates and the rewrite goes unreported.
	T.Run("the uniqueness fits InnoDB's key budget", func(t *testing.T) {
		t.Parallel()

		widths := varcharWidths(t, values)

		key := uniqueKeyColumns.FindStringSubmatch(values)
		must.SliceLen(t, 2, key)

		total := 0
		for column := range strings.SplitSeq(key[1], ",") {
			column = strings.TrimSpace(column)
			width, ok := widths[column]
			must.True(t, ok, must.Sprintf("%s is in the key and is not a VARCHAR", column))

			total += width * utf8mb4Bytes
		}

		test.LessEq(t, innoDBKeyLimit, total,
			test.Sprintf("the subject uniqueness is %d bytes wide", total))
	})
}

// mustValuesTable is the settings_values CREATE TABLE as MySQL renders it,
// read out of the DDL the package ships rather than restated here.
func mustValuesTable(t *testing.T) string {
	t.Helper()

	stmts, err := migrations.Statements(dialect.MySQL, "")
	must.NoError(t, err)

	for _, stmt := range stmts {
		if strings.Contains(stmt, "CREATE TABLE IF NOT EXISTS settings_values ") {
			return stmt
		}
	}

	t.Fatal("the MySQL schema creates no settings_values table")

	return ""
}

// varcharWidths is every bounded column of a CREATE TABLE, by name.
func varcharWidths(t *testing.T, stmt string) map[string]int {
	t.Helper()

	widths := map[string]int{}

	for _, match := range varcharColumn.FindAllStringSubmatch(stmt, -1) {
		width, err := strconv.Atoi(match[2])
		must.NoError(t, err)

		widths[match[1]] = width
	}

	must.MapNotEmpty(t, widths)

	return widths
}
