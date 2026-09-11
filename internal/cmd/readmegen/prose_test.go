package main

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// The three tests below are the exception to the file above's rule that nothing
// here reads the module's own README. They do, and what they read is not an
// argument: an ordinal, a line that appears twice, and a table split in half are
// mechanical properties of the "Transports" section that a reader trips over
// before reaching the reasoning, and each of the three was true of it at once —
// four domains each claiming to be "the third across", one sentence written four
// times, and a ten-row table carrying nine packages and one of them twice.
//
// Nothing here asserts what the prose says. Every one of them is satisfiable by
// deleting the claim rather than by updating a fixture, which is why they do not
// become the hand-maintained roster this command replaced.

// transportsSection is the "Transports" section of the module's own README, from
// its heading to the next one of the same depth or shallower.
func transportsSection(t *testing.T) string {
	t.Helper()

	root, err := moduleRoot()
	must.NoError(t, err)

	body, err := os.ReadFile(filepath.Join(root, "README.md"))
	must.NoError(t, err)

	const heading = "\n### Transports\n"

	start := bytes.Index(body, []byte(heading))
	must.NotEq(t, -1, start, must.Sprint(`the README has no "Transports" section`))

	rest := string(body[start+len(heading):])

	if end := regexp.MustCompile(`(?m)^#{1,3} `).FindStringIndex(rest); end != nil {
		rest = rest[:end[0]]
	}

	return rest
}

// ordinalClaim matches a package claiming a place in the queue of domains that
// have crossed onto the wire. There is no such claim to make: the paragraphs are
// in the order they landed and each is named by what its own crossing decided,
// so a domain that crosses next is a paragraph appended rather than ten ordinals
// re-counted. The section carried four "the third across" at once because every
// transport PR wrote its ordinal against the README as it stood.
var ordinalClaim = regexp.MustCompile(
	`is the (second|third|fourth|fifth|sixth|seventh|eighth|ninth|tenth|next)( domain)? across`,
)

func TestTheTransportsProseClaimsNoOrdinal(T *testing.T) {
	T.Parallel()

	for _, claim := range ordinalClaim.FindAllString(transportsSection(T), -1) {
		T.Errorf("the Transports section says %q; the paragraphs are in landing order and "+
			"name what each crossing decided, so there is no ordinal to keep correct", claim)
	}
}

// TestTheTransportsSectionSaysNothingTwice is the merge-resolve check. Every
// paragraph in the section was written on its own branch against a README that
// other branches were also appending to, and a resolve that keeps both sides
// leaves a sentence or a table row written twice with nothing to say so.
func TestTheTransportsSectionSaysNothingTwice(T *testing.T) {
	T.Parallel()

	seen := make(map[string]int)

	for line := range strings.SplitSeq(transportsSection(T), "\n") {
		trimmed := strings.TrimSpace(line)

		// A table's delimiter row and the blank lines between paragraphs carry
		// no claim, and the section holds two tables.
		if trimmed == "" || strings.Trim(trimmed, "|-: ") == "" {
			continue
		}

		seen[trimmed]++
	}

	for line, count := range seen {
		if count > 1 {
			T.Errorf("the Transports section says %q %d times", line, count)
		}
	}
}

// counts are the number words the ruling's table introduces itself with. The
// table is hand-written because it records carve-outs no directory walk derives,
// so the sentence above it and the rows below it are the two places the same
// count is spelled, and they are checked against each other rather than against
// a number kept here.
var counts = map[string]int{
	"Eight": 8, "Nine": 9, "Ten": 10, "Eleven": 11, "Twelve": 12, "Thirteen": 13,
}

// TestTheRuledTableCarriesTheCountItClaims is what a split table and a dropped
// row look like from the outside. The section's verdict table lost `mediaregistry`
// and gained a second `notifications` in the same merge, and blank lines between
// its rows left markdown rendering four of the ten as literal pipes.
func TestTheRuledTableCarriesTheCountItClaims(T *testing.T) {
	T.Parallel()

	section := transportsSection(T)

	intro := regexp.MustCompile(`(?m)^(\w+) more were ruled on together`).FindStringSubmatch(section)
	must.SliceLen(T, 2, intro, must.Sprint("the Transports section does not introduce the ruling's table"))

	claimed, ok := counts[intro[1]]
	must.True(T, ok, must.Sprintf("%q is not a count this test knows; add it to counts", intro[1]))

	rows := ruledRows(T, section)

	test.MapLen(T, claimed, rows, test.Sprintf("the ruling's table says %q and carries %d packages", intro[1], len(rows)))

	for pkg, count := range rows {
		if count > 1 {
			T.Errorf("the ruling's table names %s %d times", pkg, count)
		}
	}
}

// ruledRows reads the package cell of every row of the ruling's table, which is
// the contiguous run of table lines after the sentence that introduces it. A
// blank line ends the run, so a table somebody split in half is a short count
// rather than a passing test over the half that still renders.
func ruledRows(t *testing.T, section string) map[string]int {
	t.Helper()

	const header = "| package | verdict | transport | carved out, and why |"

	start := strings.Index(section, header)
	must.NotEq(t, -1, start, must.Sprint("the ruling's table has lost its header"))

	rows := make(map[string]int)

	body := strings.TrimPrefix(section[start+len(header):], "\n")

	for line := range strings.SplitSeq(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			break
		}

		if !strings.HasPrefix(trimmed, "|") || strings.Trim(trimmed, "|-: ") == "" {
			continue
		}

		cells := strings.Split(strings.Trim(trimmed, "|"), "|")
		rows[strings.Trim(strings.TrimSpace(cells[0]), "`")]++
	}

	return rows
}
