package migrate

import (
	"fmt"
	"io/fs"
	"path"
	"strconv"
	"strings"
	"testing/fstest"

	"github.com/primandproper/platform-go/v13/charset"
	"github.com/primandproper/platform-go/v13/errors"
)

// generatedMigration is a migration supplied as text rather than as a file on
// disk. Platform packages that own a table — outbox is the first — render their
// DDL from code, and the consumer places it in their own sequence by choosing
// its version.
type generatedMigration struct {
	name    string
	body    string
	version uint64
}

// filename renders the goose filename for this migration. The width matches the
// 00001_description.sql convention and widens naturally for larger versions.
func (g *generatedMigration) filename() string {
	return fmt.Sprintf("%05d_%s.sql", g.version, g.name)
}

// validate rejects a generated migration that could not produce a usable file.
func (g *generatedMigration) validate() error {
	if g.version == 0 {
		return errors.New("generated migration version must be greater than zero")
	}
	if g.name == "" {
		return errors.New("generated migration name is required")
	}
	if !migrationName.Valid(g.name) {
		return errors.Newf(
			"generated migration name %q must contain only letters, digits and underscores", g.name,
		)
	}
	if strings.TrimSpace(g.body) == "" {
		return errors.Newf("generated migration %q has no SQL", g.name)
	}

	return nil
}

// migrationName matches a name that is safe to place in a filename. The name
// lands between the version prefix and the .sql suffix, so anything that could
// introduce a path separator or a second extension is refused.
//
// Deliberately ASCII rather than unicode.IsLetter: the job is filename safety,
// not language coverage. Admitting the full letter category would let two names
// that render identically — homoglyphs, or the same string in NFC and NFD —
// claim two different files, which is worse than refusing both. charset admits
// only ASCII for that reason, so the rule is now stated by the alphabet rather
// than argued for beside it.
//
// A digit may lead, unlike the identifier rules elsewhere in this module: this
// name is a filename fragment and never a bare SQL identifier, so there is
// nothing here for a leading digit to be mistaken for.
var migrationName = charset.New(charset.ASCIIAlphanumeric.Union(charset.Bytes('_')))

// mergeGenerated adds the generated migrations to an already-annotated
// filesystem, failing on any version that a file on disk already claims.
//
// The collision check is the whole reason this is not just "write another file
// into the map": goose keys applied migrations by version, and two migrations
// sharing one version is a corrupt sequence. Catching it here means a bad
// version fails New — at service construction — rather than mid-deploy on the
// first Migrate.
func mergeGenerated(annotated fs.FS, generated []generatedMigration) (fs.FS, error) {
	if len(generated) == 0 {
		return annotated, nil
	}

	entries, err := fs.ReadDir(annotated, ".")
	if err != nil {
		return nil, errors.Wrap(err, "listing annotated migrations")
	}

	out := make(fstest.MapFS, len(entries)+len(generated))
	claimed := map[uint64]string{}

	for _, entry := range entries {
		name := entry.Name()

		content, readErr := fs.ReadFile(annotated, name)
		if readErr != nil {
			return nil, errors.Wrapf(readErr, "reading migration %q", name)
		}

		out[name] = &fstest.MapFile{Data: content}

		if version, ok := migrationVersion(name); ok {
			claimed[version] = name
		}
	}

	for i := range generated {
		g := &generated[i]

		if err = g.validate(); err != nil {
			return nil, err
		}

		if existing, taken := claimed[g.version]; taken {
			return nil, errors.Newf(
				"generated migration %q wants version %d, which %q already uses; pick an unused version",
				g.name, g.version, existing,
			)
		}

		name := g.filename()

		// Routed through the same annotator as a file on disk, so a generated
		// migration gets the Up annotation and the dollar-quote check on
		// identical terms.
		content, annotateErr := annotateSQL(name, []byte(g.body))
		if annotateErr != nil {
			return nil, annotateErr
		}

		out[name] = &fstest.MapFile{Data: content}
		claimed[g.version] = name
	}

	return out, nil
}

// migrationVersion extracts the leading numeric version from a migration
// filename, the way goose orders them. It reports false for a name goose would
// ignore anyway, and deliberately mirrors goose.NumericComponent rule for rule:
// a .sql extension, a version delimited by the first underscore, and a value of
// at least one. Divergence here would mean this package's collision check
// reasons about a version goose never assigns.
//
// The bit size is 63, not 64: goose holds versions in an int64, so refusing
// anything above MaxInt64 keeps "parsed here" equivalent to "loadable there"
// rather than deferring the failure to the first Migrate.
func migrationVersion(name string) (uint64, bool) {
	if path.Ext(name) != ".sql" {
		return 0, false
	}

	before, _, found := strings.Cut(name, "_")
	if !found {
		return 0, false
	}

	version, err := strconv.ParseUint(before, 10, 63)
	if err != nil || version == 0 {
		return 0, false
	}

	return version, true
}
