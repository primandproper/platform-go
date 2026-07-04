// Command benchtable turns raw `go test -bench` output into a markdown
// reference table. It reads benchmark output on stdin and writes one markdown
// table per package to the file named by -out (default BENCHMARKS.md).
//
// It is intentionally dependency-free and lives under internal/cmd/ (excluded from
// `make test`). The script .scripts/benchmark.sh drives it; refresh the
// committed table by re-running `make bench`.
package main

import (
	"bufio"
	"flag"
	"io"
	"log"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// modulePrefix is trimmed from package import paths so section headers read as
// repo-relative paths (e.g. "cryptography/hashing").
const modulePrefix = "github.com/primandproper/platform-go/v3/"

// benchLine matches a standard testing.B result line, e.g.
//
//	BenchmarkHash/sha256-10   	 1234567	       123.4 ns/op	      16 B/op	       1 allocs/op
//
// Group 1 is the benchmark name (with the trailing -GOMAXPROCS suffix), group 2
// is the iteration count, and group 3 is the remaining "value unit" metrics.
var benchLine = regexp.MustCompile(`^(Benchmark\S*?)(?:-\d+)?\s+(\d+)\s+(.*)$`)

// metric matches a single "<value> <unit>" pair within a benchmark line, e.g.
// "123.4 ns/op" or "16 B/op".
var metric = regexp.MustCompile(`([0-9.]+)\s+(\S+)`)

// result is one parsed benchmark row.
type result struct {
	name   string
	runs   string
	nsOp   string
	bOp    string
	allocs string
}

// pkgResults groups benchmark rows under their package import path, preserving
// first-seen package order.
type pkgResults struct {
	byPkg map[string][]result
	order []string
}

func main() {
	out := flag.String("out", "BENCHMARKS.md", "path to write the markdown table to")
	containersFlag := flag.String("containers", "", "comma-separated repo-relative package paths whose benchmarks require testcontainers")
	flag.Parse()

	containerPkgs := map[string]bool{}
	for p := range strings.SplitSeq(*containersFlag, ",") {
		if p = strings.TrimSpace(p); p != "" {
			containerPkgs[p] = true
		}
	}

	goos, goarch, cpu, pkgs := parse(os.Stdin)

	var sb strings.Builder
	render(&sb, goos, goarch, cpu, pkgs, containerPkgs)

	if err := os.WriteFile(*out, []byte(sb.String()), 0o600); err != nil {
		log.Fatalf("benchtable: %v", err)
	}

	total := 0
	for _, p := range pkgs.order {
		total += len(pkgs.byPkg[p])
	}
	//nolint:gosec // G706: total/count are locally derived integers, not user-controlled strings
	log.Printf("benchtable: wrote %d benchmark(s) across %d package(s)", total, len(pkgs.order))
}

// parse reads `go test -bench` output and returns the environment header fields
// plus per-package benchmark rows.
func parse(r io.Reader) (goos, goarch, cpu string, pkgs *pkgResults) {
	pkgs = &pkgResults{byPkg: map[string][]result{}}

	var curPkg string
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "goos:"):
			goos = strings.TrimSpace(strings.TrimPrefix(line, "goos:"))
		case strings.HasPrefix(line, "goarch:"):
			goarch = strings.TrimSpace(strings.TrimPrefix(line, "goarch:"))
		case strings.HasPrefix(line, "cpu:"):
			cpu = strings.TrimSpace(strings.TrimPrefix(line, "cpu:"))
		case strings.HasPrefix(line, "pkg:"):
			curPkg = strings.TrimSpace(strings.TrimPrefix(line, "pkg:"))
		default:
			if m := benchLine.FindStringSubmatch(line); m != nil {
				res := result{name: m[1], runs: m[2]}
				for _, pair := range metric.FindAllStringSubmatch(m[3], -1) {
					switch pair[2] {
					case "ns/op":
						res.nsOp = pair[1]
					case "B/op":
						res.bOp = pair[1]
					case "allocs/op":
						res.allocs = pair[1]
					}
				}
				if _, seen := pkgs.byPkg[curPkg]; !seen {
					pkgs.order = append(pkgs.order, curPkg)
				}
				pkgs.byPkg[curPkg] = append(pkgs.byPkg[curPkg], res)
			}
		}
	}
	return goos, goarch, cpu, pkgs
}

// render writes the markdown document into sb. It uses strings.Builder rather
// than fmt.Fprint to a writer so the (always-nil) write errors don't need
// per-call handling.
func render(sb *strings.Builder, goos, goarch, cpu string, pkgs *pkgResults, containerPkgs map[string]bool) {
	date := time.Now().UTC().Format("2006-01-02")
	sb.WriteString("# Benchmarks\n\n")
	sb.WriteString("_Generated " + date + " by `make bench`. Do not edit by hand — re-run to refresh._\n\n")

	env := []string{}
	if goos != "" {
		env = append(env, "goos `"+goos+"`")
	}
	if goarch != "" {
		env = append(env, "goarch `"+goarch+"`")
	}
	if cpu != "" {
		env = append(env, "cpu `"+cpu+"`")
	}
	if len(env) > 0 {
		sb.WriteString("**Environment:** " + strings.Join(env, " · ") + "\n\n")
	}

	if len(pkgs.order) == 0 {
		sb.WriteString("_No benchmarks found._\n")
		return
	}

	sb.WriteString("Times are nanoseconds per operation; lower is better. Run with `make bench` (set `RUN_CONTAINER_TESTS=true` to include infra-backed benchmarks).\n\n")

	names := append([]string(nil), pkgs.order...)
	sort.Strings(names)
	for _, pkg := range names {
		name := strings.TrimPrefix(pkg, modulePrefix)
		sb.WriteString("## " + name + "\n\n")
		if containerPkgs[name] {
			sb.WriteString("> 🐳 Requires testcontainers — run with `RUN_CONTAINER_TESTS=true` (and a running Docker daemon).\n\n")
		}
		sb.WriteString("| Benchmark | Runs | ns/op | B/op | allocs/op |\n")
		sb.WriteString("| --- | ---: | ---: | ---: | ---: |\n")
		rows := pkgs.byPkg[pkg]
		sort.SliceStable(rows, func(i, j int) bool { return rows[i].name < rows[j].name })
		for i := range rows {
			r := &rows[i]
			sb.WriteString("| " + strings.TrimPrefix(r.name, "Benchmark") +
				" | " + group(r.runs) +
				" | " + cell(r.nsOp) +
				" | " + cell(r.bOp) +
				" | " + cell(r.allocs) + " |\n")
		}
		sb.WriteString("\n")
	}
}

// cell renders a metric value, falling back to an em dash when absent.
func cell(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

// group adds thousands separators to an integer-like string for readability.
func group(s string) string {
	n, err := strconv.Atoi(s)
	if err != nil {
		return s
	}
	in := strconv.Itoa(n)
	neg := strings.HasPrefix(in, "-")
	if neg {
		in = in[1:]
	}
	var b strings.Builder
	for i, d := range in {
		if i > 0 && (len(in)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(d)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}
