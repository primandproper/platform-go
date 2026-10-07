// Command mcpdocs writes the field doc comments a package's MCP tool schemas
// describe their properties with, as a generated Go file.
//
// It is run by a `//go:generate` directive in each MCP tool surface, naming
// the structs whose properties that surface's tools describe. A surface in
// another module names this command by its import path:
//
//	//go:generate go run github.com/primandproper/platform-go/v15/mcptool/mcpdocs -pkg tools -out fielddocs_gen.go example.com/app/recipes.Recipe
//
// The generated file declares fieldDocs, which the surface hands to
// mcptool.Input and mcptool.Output. The surface's own test re-extracts the
// same structs — mcptool.DirectiveSpecs reads them back off this directive —
// and compares, so a doc comment edited without regenerating fails there. See
// mcptool.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/primandproper/platform-go/v15/mcptool"
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
