package migrate

import (
	"bytes"
	"io/fs"
	"path"
	"regexp"
	"strings"
	"testing/fstest"

	"github.com/primandproper/platform-go/v13/errors"
)

// gooseUpAnnotation opens the Up section of a goose SQL migration.
//
// This Migrator is forward-only — Migrate calls the provider's Up and nothing
// exposes Down — so the annotation carries no information here: every file
// would have it, in the same place, always. New inserts it rather than making
// callers remember it, which is the only piece of goose's file format that is
// goose-specific rather than an industry convention. The numbered filename
// (00001_description.sql) is still the caller's job.
const gooseUpAnnotation = "-- +goose Up"

// dollarQuote matches the opener of a Postgres dollar-quoted string ($$ or
// $tag$), which in a migration almost always means a function or DO body.
var dollarQuote = regexp.MustCompile(`\$\$|\$[A-Za-z_][A-Za-z0-9_]*\$`)

// annotateMigrations copies migrations into an in-memory filesystem, inserting
// the Up annotation into every top-level .sql file that lacks one. Non-.sql
// entries are copied through untouched, and directories are skipped, because
// goose only globs the top level.
//
// The copy is eager: migrations are normally an embed.FS of a few small files,
// and doing the work at construction means a malformed one fails New rather
// than the first Migrate.
func annotateMigrations(migrations fs.FS) (fs.FS, error) {
	entries, err := fs.ReadDir(migrations, ".")
	if err != nil {
		return nil, errors.Wrap(err, "listing migrations")
	}

	// fstest.MapFS is used as a plain read-only in-memory fs.FS. Despite
	// living under testing/, it imports no testing machinery, and it gets
	// Open/ReadDir/Glob semantics right — which goose depends on — for free.
	out := make(fstest.MapFS, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		name := entry.Name()

		content, readErr := fs.ReadFile(migrations, name)
		if readErr != nil {
			return nil, errors.Wrapf(readErr, "reading migration %q", name)
		}

		if path.Ext(name) == ".sql" {
			if content, err = annotateSQL(name, content); err != nil {
				return nil, err
			}
		}

		out[name] = &fstest.MapFile{Data: content}
	}

	return out, nil
}

// annotateSQL returns content with the Up annotation inserted, or unchanged if
// it already carries a direction annotation.
func annotateSQL(name string, content []byte) ([]byte, error) {
	lines := strings.Split(string(content), "\n")

	// Only the section-independent annotations may precede Up, so the
	// insertion point is after any leading run of them and of blank lines.
	// Everything else ends the run and gets the annotation above it —
	// StatementBegin especially, which goose rejects unless it *follows* a
	// direction annotation.
	insertAt := 0
scan:
	for i, line := range lines {
		if strings.TrimSpace(line) == "" {
			insertAt = i + 1
			continue
		}

		switch gooseAnnotationOf(line) {
		case annotationUp, annotationDown:
			// Already annotated. A file whose first direction annotation is
			// Down is malformed, but that is goose's error to report clearly,
			// not ours to paper over.
			return content, nil
		case annotationNoTransaction, annotationEnvsubOn, annotationEnvsubOff:
			insertAt = i + 1
		default:
			break scan
		}
	}

	if err := checkStatementSplitting(name, content); err != nil {
		return nil, err
	}

	annotated := make([]string, 0, len(lines)+1)
	annotated = append(annotated, lines[:insertAt]...)
	annotated = append(annotated, gooseUpAnnotation)
	annotated = append(annotated, lines[insertAt:]...)

	return []byte(strings.Join(annotated, "\n")), nil
}

// checkStatementSplitting rejects an unannotated migration that goose would
// accept but mis-execute once the Up annotation is inserted.
//
// goose splits a section into statements on semicolons unless the statement is
// fenced by StatementBegin/StatementEnd. A dollar-quoted function body is full
// of semicolons that are not statement terminators, so without the fence it is
// torn into fragments. Before the annotation was inserted the file failed
// loudly for want of an Up; inserting one silently would trade that clear
// error for a confusing one at apply time, and someone who did not know to
// write "-- +goose Up" almost certainly does not know about the fence either.
//
// The check is deliberately narrow. It looks only for dollar quoting, not for
// BEGIN/END blocks, which cannot be recognized without false positives on
// ordinary SQL — so it is a guard against the common case, not a proof.
func checkStatementSplitting(name string, content []byte) error {
	if !dollarQuote.Match(content) {
		return nil
	}
	if bytes.Contains(content, []byte("+goose StatementBegin")) {
		return nil
	}

	return errors.Newf(
		"migration %q contains a dollar-quoted body but no %q fence: its inner semicolons would be split into separate statements. Wrap the body in %q and %q",
		name, "-- +goose StatementBegin", "-- +goose StatementBegin", "-- +goose StatementEnd",
	)
}

// The goose annotations this package reasons about. annotationNone means the
// line is not an annotation at all; annotationOther covers the ones whose
// placement relative to Up is fixed by goose (StatementBegin/StatementEnd) or
// that goose does not recognize.
const (
	annotationNone = iota
	annotationOther
	annotationUp
	annotationDown
	annotationNoTransaction
	annotationEnvsubOn
	annotationEnvsubOff
)

// gooseAnnotationOf classifies one line the way goose's parser does: an
// annotation is a comment line mentioning +goose, and the command is what
// remains after stripping both markers.
//
// Matching is case-insensitive, which is deliberately looser than goose. A
// miscased "-- +goose up" is an author trying to annotate; treating it as a
// direction annotation leaves the file alone so goose's own "unknown
// annotation" error surfaces, instead of this package injecting a second Up
// beside it.
func gooseAnnotationOf(line string) int {
	if !strings.HasPrefix(strings.TrimSpace(line), "--") || !strings.Contains(line, "+goose") {
		return annotationNone
	}

	cmd := strings.ReplaceAll(line, "--", "")
	cmd = strings.Replace(cmd, "+goose", "", 1)
	cmd = strings.TrimSpace(cmd)

	switch {
	case strings.EqualFold(cmd, "Up"):
		return annotationUp
	case strings.EqualFold(cmd, "Down"):
		return annotationDown
	case strings.EqualFold(cmd, "NO TRANSACTION"):
		return annotationNoTransaction
	case strings.EqualFold(cmd, "ENVSUB ON"):
		return annotationEnvsubOn
	case strings.EqualFold(cmd, "ENVSUB OFF"):
		return annotationEnvsubOff
	default:
		return annotationOther
	}
}
