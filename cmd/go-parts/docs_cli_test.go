package main

// docs_cli_test.go — the docs-vs-code drift gate (CLI half).
//
// docs/guide/cli.md is the contract for the command surface; this test walks
// the REAL cobra tree (via newRootCmd — construction is side-effect-free) and
// diffs commands and flag names against what the guide documents, in BOTH
// directions: an undocumented command is a finding, and so is a documented
// command or flag the binary no longer has (the --parent/--single-part-only
// class of drift that survived the flat-model pivot for weeks).
//
// The guide's parse contract: every documented LEAF command has an
// "### `go-parts <path>`" header; its flags live in that section's table rows
// as `--name` backtick tokens (multi-flag cells like "`--from`, `--to`" are
// split); the root's persistent flags live under "## Global flags". Fenced
// code blocks and prose are ignored — only table rows count.
//
// Group commands (ones with subcommands, e.g. `locations`) are documented as
// "## <Name>" section intros, never "### `go-parts <name>`" headers: they are
// exempt from the undocumented check, and the ghost check tolerates a group
// header (via the groups set) without requiring one. A tolerated group
// header's flag table documents the GROUP's own flags (typically none) —
// subcommand flags belong under the subcommand's header, and a group table
// listing dead flags still reds (adversary F2, 2026-08-18).
//
// Doc-author rules the parser enforces implicitly: a backticked flag in a
// table row belongs to THAT command — cross-reference other commands' flags
// in prose, never inside a table cell (adversary F11). Flag shorthands and
// Default-column values are unchecked (names only, adversary F13). Command
// paths and flag names may contain [a-z0-9._-] (adversary F5).

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// cliDocPath resolves docs/guide/cli.md relative to this package's dir.
func cliDocPath(t *testing.T) string {
	t.Helper()
	for _, p := range []string{"../../docs/guide/cli.md", "docs/guide/cli.md"} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	t.Fatal("docs/guide/cli.md not found relative to cmd/go-parts")
	return ""
}

var (
	// [a-z0-9-] path segments and [a-z0-9._-] flag names: digits (schema-v2),
	// dots (--log.level) and underscores must document cleanly (adversary F5).
	docCmdHeader = regexp.MustCompile("(?m)^### `go-parts ([a-z0-9-]+(?: [a-z0-9-]+)*)`\\s*$")
	docFlagToken = regexp.MustCompile("`(--[a-z0-9._-]+)`")
)

// parseCLIDoc returns {command path -> flag set} from cli.md, plus the root's
// global-flag set under the "" key.
func parseCLIDoc(t *testing.T, src string) map[string]map[string]bool {
	t.Helper()
	out := map[string]map[string]bool{"": {}}
	lines := strings.Split(src, "\n")
	section := "" // "" = global / pre-command prose
	for _, ln := range lines {
		if m := docCmdHeader.FindStringSubmatch(ln); m != nil {
			section = m[1]
			if _, ok := out[section]; !ok {
				out[section] = map[string]bool{}
			}
			continue
		}
		// A non-command ###/#### subsection (Examples, Notes, …) closes the
		// command's flag-table scope — its table rows must not be
		// misattributed to the command above it.
		if strings.HasPrefix(ln, "###") {
			section = "-"
			continue
		}
		// A new ## section ends the current command's flag table (## Global
		// flags resets to the root key; ## anything else ends table scope).
		if strings.HasPrefix(ln, "## ") {
			if strings.Contains(ln, "Global flags") {
				section = ""
			} else {
				section = "-" // prose-only until the next ### header
			}
			continue
		}
		if !strings.HasPrefix(ln, "|") || section == "-" {
			continue
		}
		for _, tok := range docFlagToken.FindAllStringSubmatch(ln, -1) {
			out[section][tok[1]] = true
		}
	}
	return out
}

// codeCommands walks the cobra tree, returning the leaf commands' flag sets
// and the set of group commands (ones with subcommands, e.g. "locations").
// Cobra's auto help/completion commands and the help flag are excluded (they
// are engine, not surface); --version is included on the root because the
// guide documents it in the global table.
func codeCommands(root *cobra.Command) (leaves map[string]map[string]bool, groups map[string]map[string]bool) {
	leaves = map[string]map[string]bool{"": {}}
	groups = map[string]map[string]bool{}
	root.PersistentFlags().VisitAll(func(f *pflag.Flag) { leaves[""]["--"+f.Name] = true })
	if root.Version != "" {
		leaves[""]["--version"] = true
	}
	var walk func(cmd *cobra.Command, path string)
	walk = func(cmd *cobra.Command, path string) {
		for _, sub := range cmd.Commands() {
			if sub.Name() == "help" || sub.Name() == "completion" {
				continue
			}
			p := path + " " + sub.Name()
			// Inherited persistent flags (--data-dir) are intentionally NOT
			// collected per command: the guide documents them once, in the
			// Global flags table, and the root entry above owns that check.
			if len(sub.Commands()) > 0 {
				// Documented as a ## intro; ghost-tolerated, never required —
				// but a tolerated group header's OWN flag table is still
				// ghost-checked against these real flags (adversary F2).
				gflags := map[string]bool{}
				sub.Flags().VisitAll(func(f *pflag.Flag) {
					if f.Name != "help" {
						gflags["--"+f.Name] = true
					}
				})
				groups[strings.TrimPrefix(p, " ")] = gflags
			} else {
				flags := map[string]bool{}
				sub.Flags().VisitAll(func(f *pflag.Flag) {
					if f.Name != "help" {
						flags["--"+f.Name] = true
					}
				})
				leaves[strings.TrimPrefix(p, " ")] = flags
			}
			walk(sub, p)
		}
	}
	walk(root, "")
	return leaves, groups
}

func TestCLIDocMatchesCommands(t *testing.T) {
	src, err := os.ReadFile(cliDocPath(t))
	if err != nil {
		t.Fatal(err)
	}
	doc := parseCLIDoc(t, string(src))
	root, _ := newRootCmd()
	code, groups := codeCommands(root)

	var undocumented, ghost []string
	for path, flags := range code {
		label := "go-parts"
		if path != "" {
			label += " " + path
		}
		docFlags, ok := doc[path]
		if !ok {
			// "Missing" can also mean a near-miss header the regex can't parse
			// (trailing punctuation, wrong depth) — the header must be exactly
			// "### `go-parts <path>`" (adversary F12).
			undocumented = append(undocumented, label+" (missing from docs/guide/cli.md — or its header is not exactly \"### `go-parts <path>`\")")
			continue
		}
		var missingFlags, ghostFlags []string
		for f := range flags {
			if !docFlags[f] {
				missingFlags = append(missingFlags, f)
			}
		}
		for f := range docFlags {
			if !flags[f] {
				ghostFlags = append(ghostFlags, f)
			}
		}
		if len(missingFlags) > 0 {
			sort.Strings(missingFlags)
			undocumented = append(undocumented, label+" flags not documented: "+strings.Join(missingFlags, ", "))
		}
		if len(ghostFlags) > 0 {
			sort.Strings(ghostFlags)
			ghost = append(ghost, label+" documents removed flags: "+strings.Join(ghostFlags, ", "))
		}
	}
	for path := range doc {
		if path == "" || path == "-" {
			continue
		}
		if _, ok := code[path]; !ok {
			if _, isGroup := groups[path]; !isGroup {
				ghost = append(ghost, "go-parts "+path+" (missing from docs/guide/cli.md — or its header is not exactly `### `+`go-parts <path>`)")
			}
		}
	}
	// A tolerated group header may still carry a flag table — ghost-check it
	// against the group's REAL flags. The 79b1d69 tolerance exempted group
	// tables entirely, which re-opened the gate's founding drift class
	// (--parent/--single-part-only under a group header passed green).
	for path, docFlags := range doc {
		if _, isGroup := groups[path]; !isGroup {
			continue
		}
		for f := range docFlags {
			if !groups[path][f] {
				ghost = append(ghost, "go-parts "+path+" documents removed flags: "+f)
			}
		}
	}
	if len(undocumented) > 0 || len(ghost) > 0 {
		sort.Strings(undocumented)
		sort.Strings(ghost)
		t.Errorf("CLI docs-vs-code drift:\n  UNDOCUMENTED (in binary, not in cli.md):\n    %s\n  GHOST (in cli.md, not in binary):\n    %s",
			strings.Join(undocumented, "\n    "), strings.Join(ghost, "\n    "))
	}
}
