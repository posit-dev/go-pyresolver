// SPDX-License-Identifier: Apache-2.0 OR MIT

// Command pyresolve inspects a PyPI Repository Snapshot Format (RSF) file: it
// reads package and dependency metadata straight out of a local file on disk.
//
// It never makes a network request. Every subcommand opens the file named by
// --rsf (or the PYRESOLVE_RSF environment variable) and reads from it alone.
//
// Subcommands:
//
//	stats     print package count, dependency-dictionary size, and file size
//	versions  list every version of a package with captured dependency data
//	deps      print the dependency metadata for one version of a package
//	walk      breadth-first walk of the dependency graph, one version per package
//
// walk is not a resolver: see its own --help for what that means.
package main

import (
	"fmt"
	"io"
	"os"
)

const topLevelHelp = `pyresolve inspects a PyPI Repository Snapshot Format (RSF) file.

It reads package and dependency metadata directly out of a local file on
disk. It never makes a network request.

Usage:
  pyresolve <command> --rsf <path> [flags] [args]

Commands:
  stats     package count, dependency-dictionary size, and file size
  versions  every version of a package with captured dependency metadata
  deps      dependency metadata for one version of a package
  walk      breadth-first walk of the dependency graph, one version per package
            (NOT dependency resolution -- see 'pyresolve walk --help')

Global flags (accepted by every command):
  --rsf <path>   path to the RSF file (falls back to $PYRESOLVE_RSF)
  --json         emit JSON instead of text

Run 'pyresolve <command> --help' for details on a specific command.

Exit codes: 0 success, 1 usage or file error, 2 package not found,
3 metadata present but not conforming.
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run is the testable entry point: it takes explicit args and writers instead
// of reading os.Args and writing directly to os.Stdout/os.Stderr.
func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		_, _ = fmt.Fprint(stderr, topLevelHelp)
		return 1
	}

	cmd, rest := args[0], args[1:]

	switch cmd {
	case "-h", "--help", "help":
		_, _ = fmt.Fprint(stdout, topLevelHelp)
		return 0
	}

	var err error
	switch cmd {
	case "stats":
		err = runStats(stdout, rest)
	case "versions":
		err = runVersions(stdout, rest)
	case "deps":
		err = runDeps(stdout, rest)
	case "walk":
		err = runWalk(stdout, rest)
	default:
		_, _ = fmt.Fprintf(stderr, "pyresolve: unknown command %q\n\n", cmd)
		_, _ = fmt.Fprint(stderr, topLevelHelp)
		return 1
	}

	if err == nil {
		return 0
	}
	_, _ = fmt.Fprintln(stderr, "pyresolve:", err)
	return exitCodeFor(err)
}
