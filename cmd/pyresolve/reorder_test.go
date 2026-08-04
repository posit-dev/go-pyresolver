// SPDX-License-Identifier: Apache-2.0 OR MIT

package main

import (
	"bytes"
	"reflect"
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

func TestReorderArgsDoubleDashStopsScanning(t *testing.T) {
	fs, _ := newFlagSet("versions")

	got := reorderArgs(fs, []string{"pkg", "--", "--json"})
	want := []string{"pkg", "--json"}
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
