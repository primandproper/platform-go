// Command conformancesummary reads `go test -json` output for the conformance
// tree on stdin and reports, per suite and per subject, how many assertions
// passed, skipped and failed.
//
// It exists because `go test` credits a suite's assertions to the package that
// ran them rather than to the suite. The suites are library code, so every one
// of them reports "[no test files]", and all of their assertions run — and are
// reported — inside conformance/assembled. From that output a suite that ran
// and a suite that never ran look identical.
//
// So this counts them, and fails when a suite that is registered — one that
// appears as a subtest of the assembled subject — passed no assertion on any
// subject at all. That is the silent-degradation case the conformance ruling
// forbids: a suite whose every assertion skipped, or that stopped being called,
// is not a green result. A suite that skips entirely on one dialect and passes
// on another (dataprivacy needs Postgres) is fine, and the table shows why.
//
// It also fails when it finds no suites at all, because that is what a run
// whose assembled subject never started looks like.
//
// Failing tests' output is echoed, and package results are passed through, so
// a CI log piped through this still shows why something failed. When
// GITHUB_STEP_SUMMARY names a file the table is appended to it as markdown.
//
// It lives under internal/cmd/ beside benchtable and is driven by the
// conformance workflow; `go test`'s own exit status is preserved there by
// pipefail, so this only ever adds a failure.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"slices"
	"strings"
)

// assembledPackage is the package the assembled subject runs in, which is
// where every suite's assertions are reported.
const assembledPackage = "github.com/primandproper/platform-go/v14/conformance/assembled"

// The test2json actions this reads.
const (
	actionOutput = "output"
	actionPass   = "pass"
	actionSkip   = "skip"
	actionFail   = "fail"
)

// event is the part of a test2json event this reads.
type event struct {
	Action  string `json:"Action"`
	Package string `json:"Package"`
	Test    string `json:"Test"`
	Output  string `json:"Output"`
}

// tally is one suite's results on one subject.
type tally struct {
	pass, skip, fail int
}

func main() {
	summary, failures, err := summarize(os.Stdin, os.Stdout)
	if err != nil {
		log.Fatal(err)
	}

	fmt.Print(summary)

	if path := os.Getenv("GITHUB_STEP_SUMMARY"); path != "" {
		if writeErr := appendFile(path, summary); writeErr != nil {
			log.Printf("writing the step summary: %v", writeErr)
		}
	}

	if len(failures) > 0 {
		for _, f := range failures {
			log.Print(f)
		}

		os.Exit(1)
	}
}

func appendFile(path, content string) error {
	//nolint:gosec // G703: the path is GITHUB_STEP_SUMMARY, which the Actions runner sets for this step to write to.
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}

	if _, err = f.WriteString(content); err != nil {
		return err
	}

	return f.Close()
}

// summarize reads the event stream, echoes what a reader of the log needs, and
// returns the markdown table and the reasons, if any, to fail.
func summarize(in io.Reader, echo io.Writer) (summary string, failures []string, err error) {
	var (
		results = map[string]string{}   // test name -> final action, assembled package only
		outputs = map[string][]string{} // test name -> output lines, any package
	)

	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 1024*1024), 16*1024*1024)

	for scanner.Scan() {
		var e event
		if jsonErr := json.Unmarshal(scanner.Bytes(), &e); jsonErr != nil {
			// Not an event: a build failure prints plain text. Pass it on.
			if _, err = fmt.Fprintln(echo, scanner.Text()); err != nil {
				return "", nil, err
			}

			continue
		}

		switch {
		case e.Action == actionOutput && e.Test != "":
			key := e.Package + " " + e.Test
			outputs[key] = append(outputs[key], e.Output)
		case e.Action == actionOutput:
			// Package-level lines: "ok", "FAIL", "[no test files]", panics.
			if _, err = io.WriteString(echo, e.Output); err != nil {
				return "", nil, err
			}
		case e.Action == actionFail && e.Test != "":
			for _, line := range outputs[e.Package+" "+e.Test] {
				if _, err = io.WriteString(echo, line); err != nil {
					return "", nil, err
				}
			}
		}

		if e.Package == assembledPackage && e.Test != "" && isResult(e.Action) {
			results[e.Test] = e.Action
		}
	}

	if err = scanner.Err(); err != nil {
		return "", nil, err
	}

	tallies, subjects := count(results)
	summary = render(tallies, subjects)

	if len(tallies) == 0 {
		failures = append(failures, "conformancesummary: no suite ran under the assembled subject; it did not start, or ran with no suites")
	}

	for _, suite := range sortedKeys(tallies) {
		passed := 0
		for _, t := range tallies[suite] {
			passed += t.pass
		}

		if passed == 0 {
			failures = append(failures, fmt.Sprintf(
				"conformancesummary: suite %q is registered and passed no assertion on any subject", suite))
		}
	}

	return summary, failures, nil
}

func isResult(action string) bool {
	return action == actionPass || action == actionSkip || action == actionFail
}

// count tallies leaf assertions per suite and subject.
//
// The assembled subject's tests are TestConformance_AssembledSQLite/<suite>/...
// and TestConformance_AssembledRealServers/<dialect>/<suite>/.... A suite is
// registered if its node appears at all, even skipped; an assertion is a test
// with no subtests of its own.
func count(results map[string]string) (tallies map[string]map[string]*tally, subjects []string) {
	tallies = map[string]map[string]*tally{}
	seen := map[string]bool{}

	names := make([]string, 0, len(results))
	for name := range results {
		names = append(names, name)
	}

	slices.Sort(names)

	isLeaf := func(i int) bool {
		return i+1 >= len(names) || !strings.HasPrefix(names[i+1], names[i]+"/")
	}

	for i, name := range names {
		parts := strings.Split(name, "/")

		var subject, suite string

		switch {
		case parts[0] == "TestConformance_AssembledSQLite" && len(parts) >= 2:
			subject, suite = "sqlite", parts[1]
		case parts[0] == "TestConformance_AssembledRealServers" && len(parts) >= 3:
			subject, suite = parts[1], parts[2]
		default:
			continue
		}

		if !seen[subject] {
			seen[subject] = true
			subjects = append(subjects, subject)
		}

		if tallies[suite] == nil {
			tallies[suite] = map[string]*tally{}
		}

		if tallies[suite][subject] == nil {
			tallies[suite][subject] = &tally{}
		}

		if !isLeaf(i) {
			continue
		}

		t := tallies[suite][subject]

		switch results[name] {
		case actionPass:
			t.pass++
		case actionSkip:
			t.skip++
		case actionFail:
			t.fail++
		}
	}

	slices.Sort(subjects)

	return tallies, subjects
}

func render(tallies map[string]map[string]*tally, subjects []string) string {
	var b strings.Builder

	b.WriteString("\n### Conformance assertions by suite (passed / skipped / failed)\n\n| suite |")

	for _, s := range subjects {
		b.WriteString(" " + s + " |")
	}

	b.WriteString("\n| --- |")

	for range subjects {
		b.WriteString(" --- |")
	}

	b.WriteString("\n")

	for _, suite := range sortedKeys(tallies) {
		b.WriteString("| " + suite + " |")

		for _, s := range subjects {
			t := tallies[suite][s]
			if t == nil {
				b.WriteString(" — |")

				continue
			}

			fmt.Fprintf(&b, " %d / %d / %d |", t.pass, t.skip, t.fail)
		}

		b.WriteString("\n")
	}

	return b.String()
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}

	slices.Sort(keys)

	return keys
}
