// SPDX-License-Identifier: Apache-2.0 OR MIT

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/posit-dev/go-pyresolver/pypirsf"
)

// The tests here pin behaviours of this command that a mutation of the source
// could change without any existing test noticing
// (rstudio/package-manager#19466's surviving mutations). Each was verified by
// applying the mutation it names, watching this test fail, and reverting.
//
// Two mutations are deliberately NOT pinned; they are recorded at the bottom of
// the file with the reason no test can distinguish them.

// sharedDirectURLFixture builds a graph where TWO packages carry the SAME
// direct-URL requirement.
//
// directURLFixture has one such requirement reached once, so it cannot see
// duplicate reporting. A direct reference reached from several packages is the
// common case in real data — a pinned fork of a common library — and listing it
// once per edge says nothing extra while making the count wrong.
func sharedDirectURLFixture(t *testing.T) string {
	t.Helper()

	const urlReq = "durlabel @ git+https://github.com/example/Other@main"

	root := pypirsf.PackageRecord{
		CanonicalName: "sharedroot",
		ProjectName:   "SharedRoot",
		Snapshots: []pypirsf.SnapshotRecord{
			{Snapshot: "2026080100", Version: "1.0", ReleaseDate: "\x00\x01", Summary: "x"},
		},
		Deps: buildStoredDepsField([]fixtureVersion{
			{version: "1.0", requiresDist: []string{"sharedone", "sharedtwo", urlReq}},
		}),
		Depsdict: buildDepsdictField(),
	}
	one := pypirsf.PackageRecord{
		CanonicalName: "sharedone",
		ProjectName:   "SharedOne",
		Snapshots: []pypirsf.SnapshotRecord{
			{Snapshot: "2026080100", Version: "1.0", ReleaseDate: "\x00\x01", Summary: "x"},
		},
		Deps: buildStoredDepsField([]fixtureVersion{
			{version: "1.0", requiresDist: []string{urlReq}},
		}),
	}
	two := pypirsf.PackageRecord{
		CanonicalName: "sharedtwo",
		ProjectName:   "SharedTwo",
		Snapshots: []pypirsf.SnapshotRecord{
			{Snapshot: "2026080100", Version: "1.0", ReleaseDate: "\x00\x01", Summary: "x"},
		},
		Deps: buildStoredDepsField([]fixtureVersion{
			{version: "1.0", requiresDist: []string{urlReq}},
		}),
	}

	return writeFixtureRSF(t, []pypirsf.PackageRecord{root, one, two})
}

// TestWalkReportsARepeatedDirectURLOnce pins the deduplication of
// direct_url_requirements.
//
// Mutation pinned: dropping slices.Compact, which reports the same requirement
// once per edge that reached it.
func TestWalkReportsARepeatedDirectURLOnce(t *testing.T) {
	path := sharedDirectURLFixture(t)

	var buf bytes.Buffer
	if err := walkCmd(&buf, path, true, "sharedroot", 5); err != nil {
		t.Fatalf("walkCmd: %v", err)
	}

	var result walkResult
	if err := json.Unmarshal(buf.Bytes(), &result); err != nil {
		t.Fatalf("unmarshaling JSON: %v\noutput: %s", err, buf.String())
	}

	if len(result.DirectURLRequirements) != 1 {
		t.Errorf("the same direct reference was reached from three packages and reported %d "+
			"times, want once: %v", len(result.DirectURLRequirements), result.DirectURLRequirements)
	}
}

// TestWalkUncapturedRootIsNotFoundNotUsageError pins the EXIT CODE for a root
// that is present in the file with nothing captured.
//
// The message was already asserted; the code was not, and it is the part a
// script reads. Exiting 1 says "usage or file error", blaming the caller for a
// fact about the data — the same collapse this CLI has had to fix in `deps`.
//
// Mutation pinned: notFoundErrorf -> usageErrorf on walk's empty-version-list
// path.
func TestWalkUncapturedRootIsNotFoundNotUsageError(t *testing.T) {
	path := standardFixture(t)

	var buf bytes.Buffer
	err := walkCmd(&buf, path, false, "nodeps", 3)
	if err == nil {
		t.Fatal("expected an error for a root with no captured dependency versions")
	}
	if got := exitCodeFor(err); got != 2 {
		t.Errorf("exit code = %d, want 2 (package or version not present); got %v", got, err)
	}
}

// TestDepsJSONOmitsTheRawConstraintWhenItIsReadable pins the ABSENCE of
// requires_python_raw for a constraint that parsed.
//
// The pair (raw, unreadable) exists to say "the publisher declared this and we
// could not read it". Emitting raw for a constraint that parsed fine makes the
// field meaningless as a signal, since a consumer can no longer use its presence
// to tell the two states apart.
//
// Mutation pinned: setting result.RequiresPythonRaw unconditionally rather than
// only when RequiresPythonUnreadable.
func TestDepsJSONOmitsTheRawConstraintWhenItIsReadable(t *testing.T) {
	path := standardFixture(t)

	var buf bytes.Buffer
	if err := depsCmd(&buf, path, true, "flask", "3.0.0"); err != nil {
		t.Fatalf("depsCmd: %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(buf.Bytes(), &decoded); err != nil {
		t.Fatalf("unmarshaling JSON: %v\noutput: %s", err, buf.String())
	}
	if decoded["requires_python"] != ">=3.8" {
		t.Fatalf("requires_python = %v, want >=3.8; the fixture's constraint must be readable "+
			"for this test to mean anything", decoded["requires_python"])
	}
	if raw, ok := decoded["requires_python_raw"]; ok {
		t.Errorf("requires_python_raw = %v was emitted for a constraint that parsed; its "+
			"presence is what tells a consumer the constraint was unreadable", raw)
	}
	if unreadable, ok := decoded["requires_python_unreadable"]; ok {
		t.Errorf("requires_python_unreadable = %v was emitted for a readable constraint", unreadable)
	}
}

// TestJSONOutputDoesNotEscapeRequirementOperators pins that JSON output leaves
// "<" and ">" alone.
//
// Requirement strings are full of them — "werkzeug>=3.0" — and Go's encoder
// escapes both by default, so the field comes out as "werkzeug\u003e=3.0". That
// is valid JSON and unreadable in a terminal, and it is not what a consumer
// piping this into jq expects to see.
//
// Mutation pinned: enc.SetEscapeHTML(true) in writeJSON.
func TestJSONOutputDoesNotEscapeRequirementOperators(t *testing.T) {
	path := standardFixture(t)

	var buf bytes.Buffer
	if err := depsCmd(&buf, path, true, "flask", "3.0.0"); err != nil {
		t.Fatalf("depsCmd: %v", err)
	}

	out := buf.String()
	if strings.Contains(out, `\u003e`) || strings.Contains(out, `\u003c`) {
		t.Errorf("JSON output escaped a requirement operator as \\u003e/\\u003c:\n%s", out)
	}
	if !strings.Contains(out, "werkzeug>=3.0") {
		t.Errorf("expected the requirement to appear verbatim, got:\n%s", out)
	}
}

// TestNoDependencyDataSaysWhatToDoAboutIt pins the guidance on the one error a
// user cannot diagnose from the raw failure.
//
// An RSF that predates dependency capture is not corrupt and not missing; it is
// the wrong snapshot. The generic "failed to open" message sends someone
// checking permissions and file paths, so the message names the cause and the
// fix.
//
// Mutation pinned: skipping the ErrNoDependencyData branch in classifyOpenErr so
// the failure falls through to the generic message.
func TestNoDependencyDataSaysWhatToDoAboutIt(t *testing.T) {
	path := noDepsDataFixture(t)

	var buf bytes.Buffer
	err := versionsCmd(&buf, path, false, "flask")
	if err == nil {
		t.Fatal("expected an error opening an RSF with no dependency fields")
	}
	msg := err.Error()
	for _, want := range []string{"carries no dependency data", "newer snapshot"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error message does not mention %q, so it does not say what to do about it: %s",
				want, msg)
		}
	}
	if got := exitCodeFor(err); got != 1 {
		t.Errorf("exit code = %d, want 1 (a file the user must replace)", got)
	}
}

// TestExitCodeForUnclassifiedErrorIsFailure pins the fallback in exitCodeFor.
//
// Errors surfaced from deep inside pypirsf — an I/O failure mid-read — are not
// *cliError, and mapping them to 0 would report success on a run that produced
// no answer. That is the worst available failure mode for a tool a script drives,
// and nothing else in the suite constrains this branch.
//
// Mutation pinned: `return 1` -> `return 0` in exitCodeFor.
func TestExitCodeForUnclassifiedErrorIsFailure(t *testing.T) {
	if got := exitCodeFor(errors.New("read /dev/x: input/output error")); got != 1 {
		t.Errorf("exitCodeFor(a plain error) = %d, want 1; a run that failed must not report success", got)
	}
	if got := exitCodeFor(usageErrorf("bad flag")); got != 1 {
		t.Errorf("exitCodeFor(usage error) = %d, want 1", got)
	}
	if got := exitCodeFor(notFoundErrorf("absent")); got != 2 {
		t.Errorf("exitCodeFor(not-found error) = %d, want 2", got)
	}
	if got := exitCodeFor(unusableErrorf("does not conform")); got != 3 {
		t.Errorf("exitCodeFor(unusable error) = %d, want 3", got)
	}
}

// TestReorderArgsBoolFlagLeavesAFollowingPositionalAlone pins that a bool flag
// leaves the token after it alone.
//
// `pyresolve versions --json flask` is the ordinary shape for this tool, and a
// bool flag that swallows its successor turns the package name into a flag value:
// the command then reports "expected exactly one package name argument, got []"
// for a command line that named one.
//
// ⚠️ NEITHER of the two obvious shapes can see this, which is why the branch went
// unpinned:
//
//   - `{"flask", "--json"}` (TestReorderArgsBoolFlagDoesNotConsumeNextToken) puts
//     the bool flag LAST, so there is no following token and the branch decides
//     nothing.
//   - `{"--json", "flask"}` looks right and is also blind. Consuming "flask" as a
//     flag value moves it from the positional run to the flag run, and since the
//     flags are emitted first, the concatenation comes out byte-identical.
//
// A SECOND flag after the positional is what separates them: the swallowed
// positional then sits between the two flags rather than after both.
//
// Mutation pinned: inverting the IsBoolFlag test in reorderArgs.
func TestReorderArgsBoolFlagLeavesAFollowingPositionalAlone(t *testing.T) {
	fs, _ := newFlagSet("versions")

	got := reorderArgs(fs, []string{"--json", "flask", "--rsf", "/tmp/x.rsf"})
	want := []string{"--json", "--rsf", "/tmp/x.rsf", "flask"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("reorderArgs = %v, want %v (a bool flag must not consume the package name)", got, want)
	}

	if err := fs.Parse(got); err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if fs.NArg() != 1 || fs.Arg(0) != "flask" {
		t.Errorf("after parsing, positional args = %v, want exactly [flask]", fs.Args())
	}
}

// TestBoolFlagBeforePositionalReachesTheCommand is the end-to-end half: the
// reordering above has to survive into a real command.
func TestBoolFlagBeforePositionalReachesTheCommand(t *testing.T) {
	path := standardFixture(t)

	var stdout, stderr bytes.Buffer
	code := run([]string{"versions", "--json", "flask", "--rsf", path}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr: %s", code, stderr.String())
	}

	var result versionsResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("unmarshaling JSON: %v\noutput: %s", err, stdout.String())
	}
	if result.Package != "flask" {
		t.Errorf("package = %q, want flask", result.Package)
	}
}

// Guard against the assumption above going stale: this only tests anything while
// --json is registered as a bool flag.
func TestJSONFlagIsABoolFlag(t *testing.T) {
	fs, _ := newFlagSet("versions")
	fl := fs.Lookup("json")
	if fl == nil {
		// The return is redundant after t.Fatal, and is here because CI's
		// staticcheck does not always resolve Fatal as terminating and then
		// reports SA5011 on the dereference below. Reproduces only on the
		// GitHub runner, not under golangci-lint v2.11.2 locally on either
		// Go 1.25 or 1.26, so this removes the ambiguity rather than
		// suppressing the check.
		t.Fatal("--json is not registered")
		return
	}
	bf, ok := fl.Value.(interface{ IsBoolFlag() bool })
	if !ok || !bf.IsBoolFlag() {
		t.Errorf("--json is no longer a bool flag (%T), so the reorderArgs bool-flag path is "+
			"untested by TestReorderArgsBoolFlagLeavesAFollowingPositionalAlone", fl.Value)
	}
}

// --- mutations in this command that no test can pin, and why ---
//
//  1. walk's ErrMetadataUnavailable branch (reclassifying it, or deleting it).
//     It is UNREACHABLE for *index.RSFIndex, which is the concrete type walkCmd
//     holds: Versions and Metadata are built from the same decoded map and
//     selectHighest only returns a member of what Versions just returned, so
//     Metadata always finds a record — an invariant
//     index/dedupe_test.go's TestVersionsAndMetadataAgreeOnEveryVersion pins
//     directly. The branch is kept because the MetadataIndex CONTRACT permits
//     ErrMetadataUnavailable for a known version, and MockIndex returns it, so
//     any non-RSF index reaches it. Killing this mutation needs walkCmd to accept
//     an index.MetadataIndex rather than a path, which is a production change and
//     out of scope here. See walk.go's comment on the branch.
//
//  2. `versions`' own sort.Sort call. Removing it changes nothing observable,
//     because RSFIndex.Versions already returns ascending order -- and REVERSING
//     that internal order is equally invisible, because this command re-sorts.
//     The two are a mutually masking pair: measured, each alone leaves the suite
//     green and applying both fails TestVersionsCmdSortedAscending. The sort stays
//     because MetadataIndex.Versions explicitly promises no order, so this command
//     must not rely on getting one. See index/pinned_mutations_test.go.
//
//  3. Restricting selected_versions to names that appear in Packages. The filter
//     is provably a no-op: a package only enters `selected` after Versions
//     returned at least one version for it, which means it is not absent, and
//     `names` is every visited name minus the absent ones. So the two sets
//     coincide and no fixture can separate them. It is a guard against a future
//     change to either set, not a behaviour with an observable alternative.
