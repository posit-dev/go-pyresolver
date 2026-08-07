// SPDX-License-Identifier: Apache-2.0 OR MIT

package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/posit-dev/go-pyresolver/index"
	"github.com/posit-dev/go-pyresolver/pypirsf"
)

// noNetworkNotice is repeated on every command's help text, per the
// requirement that pyresolve state plainly that it never fetches: it reads
// the local RSF file named by --rsf and nothing else.
const noNetworkNotice = "This command reads a local RSF file only; it never makes a network request."

// cliError carries the process exit code alongside the message, so main can
// map an error to a code without re-deriving it from sentinel comparisons at
// the top level.
type cliError struct {
	code int
	msg  string
}

func (e *cliError) Error() string { return e.msg }

// usageErrorf builds a cliError that exits 1: bad flags, bad arguments, or a
// file-level problem (missing file, no dependency data) that the user must
// fix before retrying.
func usageErrorf(format string, a ...any) error {
	return &cliError{code: 1, msg: fmt.Sprintf(format, a...)}
}

// notFoundErrorf builds a cliError that exits 2: the requested package, or
// the requested version of a known package, is not present in this RSF.
func notFoundErrorf(format string, a ...any) error {
	return &cliError{code: 2, msg: fmt.Sprintf(format, a...)}
}

// unusableErrorf builds a cliError that exits 3: the record is present in this
// RSF, and what it contains does not conform to the spec — in practice a
// Requires-Dist entry PEP 508 rejects.
//
// This gets a code of its own rather than reusing 1 or 2 because it is neither
// of those things. Exiting 1 says "usage or file error", which blames the
// caller for a fact about the data, and a script cannot tell it apart from a
// typo in the arguments. Exiting 2 says the package or version is absent, when
// the record is present and specific. `walk` already keeps this state separate
// in its report; this is the same distinction at the process boundary.
func unusableErrorf(format string, a ...any) error {
	return &cliError{code: 3, msg: fmt.Sprintf(format, a...)}
}

// exitCodeFor maps err to a process exit code. Any error that is not a
// *cliError (for example an I/O failure surfaced from deep inside pypirsf)
// defaults to 1, a generic runtime failure.
func exitCodeFor(err error) int {
	var ce *cliError
	if errors.As(err, &ce) {
		return ce.code
	}
	return 1
}

// globalFlags holds the flags every subcommand accepts.
type globalFlags struct {
	rsfPath string
	json    bool
}

// newFlagSet builds a FlagSet with the shared --rsf and --json flags
// registered, and its own output suppressed: callers decide exactly what to
// print and where, rather than letting the flag package write directly to a
// package-level default.
func newFlagSet(name string) (*flag.FlagSet, *globalFlags) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	g := &globalFlags{}
	fs.StringVar(&g.rsfPath, "rsf", "", "path to the RSF file (or set PYRESOLVE_RSF)")
	fs.BoolVar(&g.json, "json", false, "emit JSON instead of text")
	return fs, g
}

// reorderArgs moves every flag (and its value, if it takes one) registered on
// fs to the front of args, preserving the relative order of the remaining
// positional arguments. The stdlib flag package stops recognizing flags at
// the first positional argument, which would otherwise make `pyresolve walk
// flask --depth 3` fail to see --depth at all -- a shape every one of this
// tool's commands needs to accept, since the positional package name usually
// comes first in how people type the command.
//
// "--" ends flag scanning early, matching flag.Parse's own convention: every
// token after it is treated as positional without inspection.
//
// ⚠️ The "--" token is RE-EMITTED, not swallowed. Stopping our own scan is only
// half the job: flag.Parse does its own scan over whatever we return, and "--"
// is the only thing that stops it (flag/flag.go: `if len(s) == 2` terminates the
// flags). Dropping the token therefore un-protected exactly the arguments it was
// written to protect -- `versions -- --json` came back as ["--json"], which
// flag.Parse read as the --json flag, leaving zero positional arguments and
// reporting "expected exactly one package name argument, got []" about a package
// legitimately named "--json".
//
// It never worked in any position, because positional always follows flagArgs in
// the returned slice: `versions pkg -- --json` came back as ["pkg", "--json"],
// where flag.Parse stops at the non-flag "pkg" and hands back two positional
// arguments instead of one.
func reorderArgs(fs *flag.FlagSet, args []string) []string {
	var flagArgs, positional []string

	for i := 0; i < len(args); i++ {
		a := args[i]

		if a == "--" {
			// Keep the terminator at the head of the positional run so it
			// survives into fs.Parse, which is what actually protects the
			// tokens after it.
			positional = append(positional, args[i:]...)
			break
		}

		if len(a) < 2 || a[0] != '-' {
			positional = append(positional, a)
			continue
		}

		flagArgs = append(flagArgs, a)

		name := strings.TrimLeft(a, "-")
		if strings.Contains(name, "=") {
			// Value is already attached to this token; nothing more to consume.
			continue
		}

		fl := fs.Lookup(name)
		if fl == nil {
			// Unknown flag: leave it for fs.Parse to report, and don't guess
			// whether the next token is its value or a positional argument.
			continue
		}
		if bf, ok := fl.Value.(interface{ IsBoolFlag() bool }); ok && bf.IsBoolFlag() {
			// Bool flags don't consume a following token.
			continue
		}
		if i+1 < len(args) {
			i++
			flagArgs = append(flagArgs, args[i])
		}
	}

	return append(flagArgs, positional...)
}

// resolveRSFPath returns the RSF path from the --rsf flag, falling back to
// PYRESOLVE_RSF when the flag was not given.
func resolveRSFPath(flagVal string) (string, error) {
	if flagVal != "" {
		return flagVal, nil
	}
	if env := os.Getenv("PYRESOLVE_RSF"); env != "" {
		return env, nil
	}
	return "", usageErrorf("missing required --rsf flag (or set PYRESOLVE_RSF)")
}

// openRSF opens the RSF at path and wraps it in an index.RSFIndex. The
// caller must Close the returned file when done; closing the file is
// sufficient, RSFIndex does not own separate resources.
func openRSF(path string) (*pypirsf.File, *index.RSFIndex, error) {
	file, err := pypirsf.Open(path)
	if err != nil {
		return nil, nil, classifyOpenErr(path, err)
	}

	idx, err := index.NewRSFIndex(file, "")
	if err != nil {
		_ = file.Close()
		return nil, nil, usageErrorf("building index over %s: %v", path, err)
	}

	return file, idx, nil
}

// classifyOpenErr turns a pypirsf.Open failure into a clear, actionable
// message instead of a raw Go error dump.
func classifyOpenErr(path string, err error) error {
	if errors.Is(err, pypirsf.ErrNoDependencyData) {
		return usageErrorf(
			"%s carries no dependency data: this RSF predates PyPI dependency capture. "+
				"Use a newer snapshot whose schema carries requires_dist/requires_python/provides_extra.",
			path)
	}
	if errors.Is(err, os.ErrNotExist) {
		return usageErrorf("RSF file not found: %s", path)
	}
	return usageErrorf("failed to open RSF %s: %v", path, err)
}

// errWriter accumulates the first error from a sequence of writes so a
// text-output function can make several Fprint calls in a row and check the
// outcome once at the end, instead of handling each return value inline.
// Once err is set, subsequent writes are no-ops.
type errWriter struct {
	w   io.Writer
	err error
}

func (ew *errWriter) printf(format string, a ...any) {
	if ew.err != nil {
		return
	}
	_, ew.err = fmt.Fprintf(ew.w, format, a...)
}

func (ew *errWriter) println(a ...any) {
	if ew.err != nil {
		return
	}
	_, ew.err = fmt.Fprintln(ew.w, a...)
}

// writeJSON encodes v to w as indented JSON. HTML escaping is disabled: this
// output is for humans and scripts reading a terminal, not an HTML response,
// and requirement strings routinely contain "<" and ">" (e.g. "werkzeug<3"),
// which would otherwise render as unreadable </> escapes.
func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}
