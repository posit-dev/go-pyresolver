// SPDX-License-Identifier: Apache-2.0 OR MIT

package main

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
)

// TestReorderArgsFlagsAfterPositional pins the shape every command needs to
// accept: `pyresolve walk flask --depth 3` puts the flag after the
// positional package name, which the stdlib flag package alone would refuse
// to see (it stops scanning for flags at the first non-flag argument).
func TestReorderArgsFlagsAfterPositional(t *testing.T) {
	fs, _ := newFlagSet("walk")
	depth := fs.Int("depth", defaultWalkDepth, "")
	_ = depth

	got := reorderArgs(fs, []string{"flask", "--depth", "3"})
	want := []string{"--depth", "3", "flask"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("reorderArgs = %v, want %v", got, want)
	}
}

func TestReorderArgsBoolFlagDoesNotConsumeNextToken(t *testing.T) {
	fs, _ := newFlagSet("versions")

	got := reorderArgs(fs, []string{"flask", "--json"})
	want := []string{"--json", "flask"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("reorderArgs = %v, want %v", got, want)
	}
}

func TestReorderArgsEqualsForm(t *testing.T) {
	fs, _ := newFlagSet("stats")

	got := reorderArgs(fs, []string{"--rsf=/tmp/x.rsf", "extra"})
	want := []string{"--rsf=/tmp/x.rsf", "extra"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("reorderArgs = %v, want %v", got, want)
	}
}

// The "--" terminator must survive into the returned slice. This test
// previously asserted the opposite -- want was []string{"pkg", "--json"}, the
// terminator-stripped output -- which pinned the bug rather than the behavior:
// fs.Parse does its own scan over whatever reorderArgs returns, and "--" is the
// only token that stops it, so dropping it un-protected exactly the arguments
// the terminator exists to protect.
func TestReorderArgsDoubleDashStopsScanning(t *testing.T) {
	fs, _ := newFlagSet("versions")

	got := reorderArgs(fs, []string{"pkg", "--", "--json"})
	want := []string{"pkg", "--", "--json"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("reorderArgs = %v, want %v", got, want)
	}
}

func TestReorderArgsDoubleDashBeforePositional(t *testing.T) {
	fs, _ := newFlagSet("versions")

	// A real flag ahead of the terminator still gets hoisted; everything from
	// "--" onward is positional and keeps its order.
	got := reorderArgs(fs, []string{"--json", "--", "-weird-package"})
	want := []string{"--json", "--", "-weird-package"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("reorderArgs = %v, want %v", got, want)
	}
}

// TestRunWalkAcceptsFlagAfterPositional is the end-to-end version of the
// above, run through the actual command dispatcher.
func TestRunWalkAcceptsFlagAfterPositional(t *testing.T) {
	path := standardFixture(t)

	var stdout, stderr bytes.Buffer
	code := run([]string{"walk", "--rsf", path, "flask", "--depth", "1"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr:\n%s", code, stderr.String())
	}
}

// The unit tests above cover reorderArgs in isolation, but the terminator's
// whole purpose is end-to-end: it must reach fs.Parse. There was no test at this
// level, which is how a reorderArgs test asserting the stripped output could
// pass while "--" did not work in any position.
//
// A package literally named "--json" is not in the fixture, so the right answer
// is exit 2, "package not found". Before the fix, "--json" was consumed as the
// --json flag, leaving no positional argument, and the command exited 1 with
// "expected exactly one package name argument, got []" -- blaming the caller for
// a name it had silently eaten.
func TestRunDoubleDashProtectsDashLeadingPackageName(t *testing.T) {
	path := standardFixture(t)

	var stdout, stderr bytes.Buffer
	code := run([]string{"versions", "--rsf", path, "--", "--json"}, &stdout, &stderr)

	if code == 1 {
		t.Fatalf("exit code = 1 (usage error): the terminator did not protect the "+
			"argument, so %q was parsed as a flag; stderr:\n%s", "--json", stderr.String())
	}
	if code != 2 {
		t.Fatalf("exit code = %d, want 2 (package not found); stderr:\n%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "--json") {
		t.Errorf("error should name the package it looked for, got:\n%s", stderr.String())
	}
}

// The same, with a real flag present, to prove the terminator does not disable
// legitimate flag parsing ahead of it.
func TestRunDoubleDashCoexistsWithFlags(t *testing.T) {
	path := standardFixture(t)

	var stdout, stderr bytes.Buffer
	code := run([]string{"versions", "--rsf", path, "--json", "--", "flask"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr:\n%s", code, stderr.String())
	}
	if !strings.HasPrefix(strings.TrimSpace(stdout.String()), "{") {
		t.Errorf("--json before the terminator should still take effect, got:\n%s", stdout.String())
	}
}
