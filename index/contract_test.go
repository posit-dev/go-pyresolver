// SPDX-License-Identifier: Apache-2.0 OR MIT

package index

import (
	"errors"
	"math/rand"
	"reflect"
	"strings"
	"testing"

	"github.com/posit-dev/go-python-packaging/requirement"
	"github.com/posit-dev/go-python-packaging/version"
)

// The tests here exercise the exported contract functions DIRECTLY, rather than
// through Versions and Metadata as dedupe_test.go does. That is the point of
// them: these are the rules a separate implementation over the same bytes has to
// agree with, so the behaviour has to be pinned at the surface a consumer
// actually calls, not only at the index methods that happen to call it today.

func keysOf(classes []EqualityClass) []string {
	out := make([]string, 0, len(classes))
	for _, c := range classes {
		out = append(out, c.Key)
	}
	return out
}

func mustParse(t *testing.T, raw string) version.Version {
	t.Helper()
	v, err := version.Parse(raw)
	if err != nil {
		t.Fatalf("fixture version %q did not parse: %v", raw, err)
	}
	return v
}

// TestDedupeEqualityClassesChoosesOneRepresentative pins both halves of the
// arbitration rule: canonical spelling wins, and between two keys of the same
// canonicality the lexicographically smaller wins.
func TestDedupeEqualityClassesChoosesOneRepresentative(t *testing.T) {
	for _, tc := range []struct {
		name string
		keys []string
		want []string
	}{
		{
			name: "canonical beats non-canonical",
			keys: []string{"01.0.0", "1.0.0"},
			want: []string{"1.0.0"},
		},
		{
			name: "canonical wins regardless of lexicographic order",
			// "1.0" sorts after "01.0" bytewise, so a rule that only compared
			// strings would pick the zero-padded spelling here.
			keys: []string{"01.0", "1.0"},
			want: []string{"1.0"},
		},
		{
			name: "two non-canonical spellings fall back to lexicographic",
			keys: []string{"0.1dev", "0.1.0dev"},
			want: []string{"0.1.0dev"},
		},
		{
			name: "trailing zeros are one class",
			keys: []string{"1.0", "1.0.0", "1.0.0.0"},
			want: []string{"1.0"},
		},
		{
			name: "distinct versions all survive, sorted ascending",
			keys: []string{"2.0", "1.0", "1.5"},
			want: []string{"1.0", "1.5", "2.0"},
		},
		{
			name: "no keys",
			keys: nil,
			want: []string{},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := keysOf(DedupeEqualityClasses(tc.keys).Classes)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("DedupeEqualityClasses(%q) = %q, want %q", tc.keys, got, tc.want)
			}
		})
	}
}

// TestDedupeEqualityClassesReportsRejectedKeys covers the policy that a key PEP
// 440 rejects must not make the rest of the package unreachable, AND that the
// rejected keys come back rather than vanishing -- an empty Classes with no
// Rejected means "nothing was captured", which is a different fact from "every
// key was unreadable".
func TestDedupeEqualityClassesReportsRejectedKeys(t *testing.T) {
	got := DedupeEqualityClasses([]string{"1.0", "0.2.1.Perceval", "2.0"})
	if want := []string{"1.0", "2.0"}; !reflect.DeepEqual(keysOf(got.Classes), want) {
		t.Fatalf("Classes = %q, want %q", keysOf(got.Classes), want)
	}
	if want := []string{"0.2.1.Perceval"}; !reflect.DeepEqual(got.Rejected, want) {
		t.Fatalf("Rejected = %q, want %q", got.Rejected, want)
	}

	// Rejected is sorted, so two runs can be compared.
	multi := DedupeEqualityClasses([]string{"zzz", "0.2.1.Perceval", "aaa"})
	if want := []string{"0.2.1.Perceval", "aaa", "zzz"}; !reflect.DeepEqual(multi.Rejected, want) {
		t.Fatalf("Rejected = %q, want sorted %q", multi.Rejected, want)
	}

	all := DedupeEqualityClasses([]string{"0.2.1.Perceval", "not-a-version"})
	if all.Classes == nil {
		t.Fatal("every key rejected returned a nil Classes; it must be empty but non-nil")
	}
	if len(all.Classes) != 0 {
		t.Fatalf("every key rejected returned %q, want no classes", keysOf(all.Classes))
	}
	if len(all.Rejected) != 2 {
		t.Fatalf("Rejected = %q, want both keys", all.Rejected)
	}

	// Nothing rejected means nil, so "nothing was rejected" stays distinguishable.
	clean := DedupeEqualityClasses([]string{"1.0"})
	if clean.Rejected != nil {
		t.Fatalf("Rejected = %q, want nil when every key parsed", clean.Rejected)
	}
}

// TestDedupeEqualityClassesIsDeterministic is the regression net for the reason
// the lexicographic tiebreak exists. Callers build the key slice by ranging a
// map, so the input order is effectively random; the output must not be.
func TestDedupeEqualityClassesIsDeterministic(t *testing.T) {
	keys := []string{"1.0", "1.0.0", "01.0.0", "0.1dev", "0.1.0dev", "2.0", "bad-key"}

	first := DedupeEqualityClasses(keys)
	want := keysOf(first.Classes)
	if len(want) == 0 {
		t.Fatal("fixture produced no classes; the test would assert nothing")
	}

	rng := rand.New(rand.NewSource(1))
	for i := 0; i < 200; i++ {
		shuffled := append([]string(nil), keys...)
		rng.Shuffle(len(shuffled), func(a, b int) {
			shuffled[a], shuffled[b] = shuffled[b], shuffled[a]
		})
		got := DedupeEqualityClasses(shuffled)
		if !reflect.DeepEqual(keysOf(got.Classes), want) {
			t.Fatalf("input order %q changed Classes: got %q, want %q", shuffled, keysOf(got.Classes), want)
		}
		if !reflect.DeepEqual(got.Rejected, first.Rejected) {
			t.Fatalf("input order %q changed Rejected: got %q, want %q", shuffled, got.Rejected, first.Rejected)
		}
	}
}

// TestDedupeEqualityClassesIsSearchable pins the properties Lookup relies on:
// strictly ascending, no two elements equal, and Version genuinely being Key
// parsed.
//
// The fixture deliberately includes a class whose only member is spelled
// non-canonically ("2015.04.28"), so Canonical() is exercised in BOTH directions.
// Without it every representative is canonical and the false branch is untested.
func TestDedupeEqualityClassesIsSearchable(t *testing.T) {
	classes := DedupeEqualityClasses([]string{"2.0", "1.0", "1.0.0", "1.5", "01.5.0", "2015.04.28", "0.9"}).Classes

	for i := 1; i < len(classes); i++ {
		prev, cur := classes[i-1].Version, classes[i].Version
		if prev.Equal(cur) {
			t.Fatalf("elements %d and %d compare equal (%q, %q)", i-1, i, classes[i-1].Key, classes[i].Key)
		}
		if !prev.LessThan(cur) {
			t.Fatalf("not ascending at %d: %q is not less than %q", i, classes[i-1].Key, classes[i].Key)
		}
	}

	for _, c := range classes {
		if !mustParse(t, c.Key).Equal(c.Version) {
			t.Fatalf("Version %q does not agree with Key %q", c.Version.String(), c.Key)
		}
	}

	// Assert Canonical() against literal expectations, not against its own
	// definition -- restating the implementation can never fail.
	canonical := map[string]bool{}
	for _, c := range classes {
		canonical[c.Key] = c.Canonical()
	}
	for key, want := range map[string]bool{
		"1.0":        true,
		"1.5":        true,
		"2.0":        true,
		"0.9":        true,
		"2015.04.28": false, // renders as "2015.4.28"
	} {
		got, ok := canonical[key]
		if !ok {
			t.Fatalf("expected %q to be a class representative; got %q", key, keysOf(classes))
		}
		if got != want {
			t.Errorf("Canonical() for %q = %v, want %v", key, got, want)
		}
	}
}

// TestLookupResolvesByEqualityNotSpelling is the regression net for the bug that
// motivated exporting any of this: a version PEP 440-equal to a stored key but
// spelled like neither it nor the class representative.
func TestLookupResolvesByEqualityNotSpelling(t *testing.T) {
	// Stored "1.0" only. A request parsed from "1.0.0.0" renders "1.0.0.0", so it
	// matches neither the key nor its canonical rendering -- yet the two versions
	// ARE equal, because trailing zeros are insignificant.
	classes := DedupeEqualityClasses([]string{"1.0", "2.0", "2015.04.28"}).Classes

	for _, spelling := range []string{"1.0", "1.0.0", "1.0.0.0", "1.0.0.0.0"} {
		got, ok := Lookup(classes, mustParse(t, spelling))
		if !ok {
			t.Fatalf("Lookup(%q) reported the version unknown; it is PEP 440-equal to stored \"1.0\"", spelling)
		}
		if got.Key != "1.0" {
			t.Errorf("Lookup(%q).Key = %q, want %q", spelling, got.Key, "1.0")
		}
	}

	// A non-canonically spelled representative is reachable through its canonical
	// rendering, which is the spelling Versions hands out.
	got, ok := Lookup(classes, mustParse(t, "2015.4.28"))
	if !ok || got.Key != "2015.04.28" {
		t.Errorf("Lookup(2015.4.28) = (%q, %v), want (%q, true)", got.Key, ok, "2015.04.28")
	}

	// A version that genuinely is not present is reported absent.
	if got, ok := Lookup(classes, mustParse(t, "3.0")); ok {
		t.Errorf("Lookup(3.0) = (%q, true), want not found", got.Key)
	}
	if _, ok := Lookup(nil, mustParse(t, "1.0")); ok {
		t.Error("Lookup over no classes reported a hit")
	}
}

// TestLookupAgreesWithLinearScan is the differential net for the binary search:
// it must find exactly what an exhaustive equality scan finds.
func TestLookupAgreesWithLinearScan(t *testing.T) {
	classes := DedupeEqualityClasses([]string{"0.9", "1.0", "1.5", "2.0", "2.1", "3.0", "10.0", "2015.04.28"}).Classes

	for _, probe := range []string{
		"0.9", "1.0", "1.0.0.0", "1.5", "2.0", "2.1", "3.0", "10.0", "2015.4.28",
		"0.1", "4.0", "11.0", "99.99",
	} {
		ver := mustParse(t, probe)

		var wantKey string
		var wantOK bool
		for _, c := range classes {
			if c.Version.Equal(ver) {
				wantKey, wantOK = c.Key, true
				break
			}
		}

		got, ok := Lookup(classes, ver)
		if ok != wantOK || got.Key != wantKey {
			t.Errorf("Lookup(%q) = (%q, %v), linear scan says (%q, %v)", probe, got.Key, ok, wantKey, wantOK)
		}
	}
}

// TestParseRecordRefusesUnparseableRequirement pins the fatal half of the
// asymmetry, that the refusal comes back as facts a caller can inspect rather
// than as an opaque message, and that it carries the interface sentinel.
func TestParseRecordRefusesUnparseableRequirement(t *testing.T) {
	bad := "not a requirement!!"

	meta, err := ParseRecord(RawRecord{RequiresDist: []string{"requests>=2", bad}})
	if err == nil {
		t.Fatal("an unparseable requirement must be fatal, got nil error")
	}
	if !reflect.DeepEqual(meta, PackageMetadata{}) {
		t.Fatalf("a refused record must return zero metadata, got %+v", meta)
	}

	var unparseable *UnparseableRequirementError
	if !errors.As(err, &unparseable) {
		t.Fatalf("error is %T, want *UnparseableRequirementError", err)
	}
	if unparseable.Requirement != bad {
		t.Fatalf("Requirement = %q, want %q", unparseable.Requirement, bad)
	}
	if !errors.Is(err, unparseable.Err) {
		t.Fatal("errors.Is must reach the wrapped parse cause")
	}

	// ⚠️ The sentinel is what lets a consumer return this straight out of
	// Metadata: MultiIndex branches on it to decide whether a later source may
	// still answer. Without it, a composed index takes the wrong branch.
	if !errors.Is(err, ErrMetadataUnusable) {
		t.Fatal("the error must satisfy errors.Is(err, ErrMetadataUnusable) so it can be returned from Metadata")
	}

	// It must name the requirement and NOT a version -- one parsed record answers
	// for every spelling in its equality class, so a memoized message carrying a
	// version would report the first caller's request to every later one.
	msg := err.Error()
	if !strings.Contains(msg, bad) {
		t.Errorf("message %q does not name the offending requirement", msg)
	}
	for _, ver := range []string{"1.0", "2.0", "0.0"} {
		if strings.Contains(msg, ver) {
			t.Errorf("message %q names a version (%q); it must carry facts about the record only", msg, ver)
		}
	}
}

// TestParseRecordIsPermissiveOnRequiresPython pins the non-fatal half of the
// asymmetry, and that the permissiveness is recorded rather than merely applied.
func TestParseRecordIsPermissiveOnRequiresPython(t *testing.T) {
	unreadable := ">=!!bogus"

	meta, err := ParseRecord(RawRecord{RequiresPython: unreadable})
	if err != nil {
		t.Fatalf("an unreadable Requires-Python must not be fatal, got %v", err)
	}
	if !meta.RequiresPythonUnreadable {
		t.Fatal("RequiresPythonUnreadable must be set, or a caller cannot tell this apart from an absent constraint")
	}
	if meta.RequiresPythonRaw != unreadable {
		t.Fatalf("RequiresPythonRaw = %q, want the record's own %q", meta.RequiresPythonRaw, unreadable)
	}
	// Unconstrained, so the candidate is over-admitted rather than silently
	// excluded. That direction is the whole reason this case is not fatal.
	if !meta.SupportsPython(mustParse(t, "3.8")) {
		t.Error("an unreadable Requires-Python must leave the version unconstrained, not exclude it")
	}

	// The distinguishing case: a record that declared nothing must NOT look like
	// a record that declared something unreadable.
	absent, err := ParseRecord(RawRecord{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if absent.RequiresPythonUnreadable {
		t.Fatal("an absent Requires-Python must not be flagged unreadable")
	}
	if absent.RequiresPythonRaw != "" {
		t.Fatalf("RequiresPythonRaw = %q, want empty", absent.RequiresPythonRaw)
	}

	// And a READABLE constraint must actually be parsed, not merely not-flagged.
	// Without these two, ParseRecord could return empty Specifiers on the success
	// path and every other assertion here would still pass.
	ok, err := ParseRecord(RawRecord{RequiresPython: ">=3.8"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok.RequiresPythonUnreadable {
		t.Error("a readable Requires-Python must not be flagged unreadable")
	}
	if !ok.SupportsPython(mustParse(t, "3.9")) {
		t.Error(">=3.8 should admit 3.9; the constraint was not parsed")
	}
	if ok.SupportsPython(mustParse(t, "3.7")) {
		t.Error(">=3.8 should exclude 3.7; the constraint was not parsed")
	}
}

// TestParseRecordParsesDistAndNormalizesExtras pins the parsed contents, PEP 685
// normalization, and the purity of the function: it knows nothing about which
// package, version or index it was called for.
func TestParseRecordParsesDistAndNormalizesExtras(t *testing.T) {
	meta, err := ParseRecord(RawRecord{
		RequiresDist:   []string{"requests[socks]>=2", "urllib3"},
		RequiresPython: ">=3.8",
		ProvidesExtra:  []string{"Test-Suite", "docs"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if want := []string{"test-suite", "docs"}; !reflect.DeepEqual(meta.ProvidesExtra, want) {
		t.Fatalf("ProvidesExtra = %q, want %q", meta.ProvidesExtra, want)
	}

	// The CONTENTS, not just the count: a parser that dropped or reordered
	// requirements would satisfy a length check.
	if len(meta.RequiresDist) != 2 {
		t.Fatalf("RequiresDist has %d entries, want 2", len(meta.RequiresDist))
	}
	if got := meta.RequiresDist[0].Name; got != "requests" {
		t.Errorf("RequiresDist[0].Name = %q, want %q", got, "requests")
	}
	if want := []string{"socks"}; !reflect.DeepEqual(meta.RequiresDist[0].Extras, want) {
		t.Errorf("RequiresDist[0].Extras = %q, want %q", meta.RequiresDist[0].Extras, want)
	}
	if got := meta.RequiresDist[1].Name; got != "urllib3" {
		t.Errorf("RequiresDist[1].Name = %q, want %q", got, "urllib3")
	}

	// The zero version renders as "", which is the test checkVersionInitialized
	// applies.
	if meta.Name != "" || meta.Origin != "" || meta.Version.String() != "" {
		t.Fatalf("identity fields must be left to the caller, got Name=%q Origin=%q Version=%q",
			meta.Name, meta.Origin, meta.Version.String())
	}

	// nil in, nil out: "declared no extras" is not "declared an empty list".
	bare, err := ParseRecord(RawRecord{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if bare.RequiresDist != nil || bare.ProvidesExtra != nil {
		t.Fatalf("absent lists must stay nil, got RequiresDist=%v ProvidesExtra=%v", bare.RequiresDist, bare.ProvidesExtra)
	}
}

// TestCloneIsolatesExportedSlices is the regression net for the aliasing bug the
// copy contract exists to prevent, including the nested Extras slice that a
// one-level copy leaves shared.
func TestCloneIsolatesExportedSlices(t *testing.T) {
	req, err := requirement.Parse("requests[socks,use-chardet]>=2")
	if err != nil {
		t.Fatalf("fixture requirement did not parse: %v", err)
	}
	if len(req.Extras) == 0 {
		t.Fatal("fixture requirement carries no extras; the nested-copy assertion would be vacuous")
	}

	original := PackageMetadata{
		RequiresDist:  []requirement.Requirement{req},
		ProvidesExtra: []string{"docs", "tests"},
	}

	clone := original.Clone()

	// Mutating the clone the way a caller legitimately might must not reach the
	// original, which in production is a shared cache entry.
	clone.RequiresDist[0].Extras[0] = "CLOBBERED"
	clone.RequiresDist[0].Name = "clobbered"
	clone.ProvidesExtra[0] = "CLOBBERED"

	if got := original.RequiresDist[0].Extras[0]; got == "CLOBBERED" {
		t.Fatal("Extras is aliased: writing through the clone corrupted the original")
	}
	if got := original.RequiresDist[0].Name; got == "clobbered" {
		t.Fatal("RequiresDist is aliased: writing through the clone corrupted the original")
	}
	if got := original.ProvidesExtra[0]; got == "CLOBBERED" {
		t.Fatal("ProvidesExtra is aliased: writing through the clone corrupted the original")
	}

	// And the reverse direction, since a cache hands out many clones of one entry.
	original.ProvidesExtra[1] = "CLOBBERED"
	if clone.ProvidesExtra[1] == "CLOBBERED" {
		t.Fatal("ProvidesExtra is aliased in the other direction")
	}
}

// TestClonePreservesNilAndValues pins that Clone does not normalize nil to empty
// and carries the scalar fields through untouched.
func TestClonePreservesNilAndValues(t *testing.T) {
	original := PackageMetadata{
		Name:                     NewPackageName("flask"),
		Origin:                   "rsf",
		RequiresPythonRaw:        ">=3.8",
		RequiresPythonUnreadable: true,
	}

	clone := original.Clone()

	if clone.RequiresDist != nil {
		t.Fatalf("a nil RequiresDist must stay nil, got %v", clone.RequiresDist)
	}
	if clone.ProvidesExtra != nil {
		t.Fatalf("a nil ProvidesExtra must stay nil, got %v", clone.ProvidesExtra)
	}
	if clone.Name != original.Name || clone.Origin != original.Origin ||
		clone.RequiresPythonRaw != original.RequiresPythonRaw ||
		clone.RequiresPythonUnreadable != original.RequiresPythonUnreadable {
		t.Fatalf("scalar fields changed: got %+v, want %+v", clone, original)
	}

	// An EMPTY (non-nil) slice must also survive as empty and non-nil.
	empty := PackageMetadata{RequiresDist: []requirement.Requirement{}, ProvidesExtra: []string{}}
	ec := empty.Clone()
	if ec.RequiresDist == nil || len(ec.RequiresDist) != 0 {
		t.Fatalf("an empty RequiresDist must stay empty and non-nil, got %v", ec.RequiresDist)
	}
	if ec.ProvidesExtra == nil || len(ec.ProvidesExtra) != 0 {
		t.Fatalf("an empty ProvidesExtra must stay empty and non-nil, got %v", ec.ProvidesExtra)
	}
}
