// Command mcpdocs writes the field doc comments a package's MCP tool schemas
// describe their properties with, as a generated Go file.
//
// It is run by a `//go:generate` directive in each MCP tool surface, naming
// the structs whose properties that surface's tools describe:
//
//	//go:generate go run ../../internal/cmd/mcpdocs -pkg mcp -out fielddocs_gen.go example.com/pkg.Report
//
// The surface's own test re-extracts the same structs and compares, so a doc
// comment edited without regenerating fails there. See internal/mcptool.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/primandproper/platform-go/v14/internal/mcptool"
)

func main() {
	pkg := flag.String("pkg", "", "the package the generated file declares")
	out := flag.String("out", "", "the file to write")
	flag.Parse()

	if err := run(*pkg, *out, flag.Args()); err != nil {
		fmt.Fprintln(os.Stderr, "mcpdocs:", err)
		os.Exit(1)
	}
}

func run(pkg, out string, specs []string) error {
	if pkg == "" || out == "" || len(specs) == 0 {
		return errors.New("usage: mcpdocs -pkg <name> -out <file> <import path>.<Type> [<import path>.<Type>]")
	}

	docs, err := mcptool.Extract(specs...)
	if err != nil {
		return err
	}

	src, err := mcptool.Render(pkg, "fieldDocs", docs)
	if err != nil {
		return err
	}

	return os.WriteFile(out, src, 0o600)
}
