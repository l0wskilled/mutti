// Command mutti-docs regenerates docs/reference (make docs).
package main

import (
	"fmt"
	"os"

	"github.com/l0wskilled/mutti/internal/cli"
	"github.com/l0wskilled/mutti/internal/docgen"
)

func main() {
	if err := docgen.Generate(".", "docs", cli.Commands()); err != nil {
		fmt.Fprintln(os.Stderr, "mutti-docs:", err)
		os.Exit(1)
	}
}
