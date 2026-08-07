// Package main is the go-parts entry point.
//
// go-parts is a single-binary, embedded-storage electronics parts database.
// It is scaffolded here with a minimal version-printing CLI; the Phase 1
// daemon lifecycle (start/stop/status), REST API, and Pebble store arrive as
// implementation work against docs/internals/go-parts-prd.md.
package main

import (
	"fmt"
	"os"
)

// version is set at build time via -ldflags "-X main.version=..."; defaults to dev.
var version = "dev"

func main() {
	if len(os.Args) > 1 && (os.Args[1] == "-v" || os.Args[1] == "--version" || os.Args[1] == "version") {
		fmt.Println("go-parts", version)
		return
	}
	fmt.Fprintf(os.Stderr, "go-parts %s — scaffolded. See docs/internals/go-parts-prd.md.\n", version)
	fmt.Fprintln(os.Stderr, "Phase 1 daemon (start/stop/status) not yet implemented.")
	os.Exit(1)
}
