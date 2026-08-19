// SPDX-License-Identifier: Apache-2.0 OR MIT

package index

import (
	"errors"
	"math/rand"
	"reflect"
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
			got := keysOf(DedupeEqualityClasses(tc.keys))
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("DedupeEqualityClasses(%q) = %q, want %q", tc.keys, got, tc.want)
			}
		})
	}
}

// TestDedupeEqualityClassesSkipsUnparseableKeys covers the policy that a key PEP
// 440 rejects must not make the rest of the package unreachable, and that the
// all-rejected case still returns a non-nil empty slice.
func TestDedupeEqualityClassesSkipsUnparseableKeys(t *testing.T) {
	got := DedupeEqualityClasses([]string{"1.0", "0.2.1.Perceval", "2.0"})
	if want := []string{"1.0", "2.0"}; !reflect.DeepEqual(keysOf(got), want) {
		t.Fatalf("got %q, want %q", keysOf(got), want)
	}

	all := DedupeEqualityClasses([]string{"0.2.1.Perceval", "not-a-version"})
	if all == nil {
		t.Fatal("every key rejected returned a nil slice; it must be empty but non-nil")
	}
	if len(all) != 0 {
		t.Fatalf("every key rejected returned %q, want no elements", keysOf(all))
	}
}

// TestDedupeEqualityClassesIsDeterministic is the regression net for the reason
// the lexicographic tiebreak exists. Callers build the key slice by ranging a
// map, so the input order is effectively random; the output must not be.
func TestDedupeEqualityClassesIsDeterministic(t *testing.T) {
	keys := []string{"1.0", "1.0.0", "01.0.0", "0.1dev", "0.1.0dev", "2.0", "bad-key"}

	want := keysOf(DedupeEqualityClasses(keys))
	if len(want) == 0 {
		t.Fatal("fixture produced no classes; the test would assert nothing")
	}

	rng := rand.New(rand.NewSource(1))
	for i := 0; i < 200; i++ {
		shuffled := append([]string(nil), keys...)
		rng.Shuffle(len(shuffled), func(a, b int) {
			shuffled[a], shuffled[b] = shuffled[b], shuffled[a]
		})
		if got := keysOf(DedupeEqualityClasses(shuffled)); !reflect.DeepEqual(got, want) {
			t.Fatalf("input order %q changed the result: got %q, want %q", shuffled, got, want)
		}
	}
}

// TestDedupeEqualityClassesIsSearchable pins the two properties a caller relies
// on to binary-search the result: strictly ascending, and Version agrees with
// Key.
func TestDedupeEqualityClassesIsSearchable(t *testing.T) {
	classes := DedupeEqualityClasses([]string{"2.0", "1.0", "1.0.0", "1.5", "01.5.0", "0.9"})

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
		parsed, err := version.Parse(c.Key)
		if err != nil {
			t.Fatalf("representative key %q does not parse; it must never have been admitted", c.Key)
		}
		if !parsed.Equal(c.Version) {
			t.Fatalf("Version %q does not agree with Key %q", c.Version.String(), c.Key)
		}
		if want := c.Version.String() == c.Key; c.Canonical() != want {
			t.Fatalf("Canonical() = %v for key %q rendering as %q", c.Canonical(), c.Key, c.Version.String())
		}
	}
}

// TestParseRecordRefusesUnparseableRequirement pins the fatal half of the
// asymmetry, and that the refusal comes back as facts a caller can inspect
// rather than as an opaque message.
func TestParseRecordRefusesUnparseableRequirement(t *testing.T) {
	bad := "not a requirement!!"

	meta, err := ParseRecord([]string{"requests>=2", bad}, "", nil)
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
	if unparseable.Unwrap() == nil {
		t.Fatal("the underlying parse error must stay in the chain for diagnostics")
	}

	// It must name the requirement and NOT a version: the same parsed record
	// answers for every spelling in its equality class, so a memoized message
	// carrying one caller's version would be reported to every later caller.
	if !errors.Is(err, unparseable.Err) {
		t.Fatal("errors.Is must reach the wrapped cause")
	}
}

// TestParseRecordIsPermissiveOnRequiresPython pins the non-fatal half of the
// asymmetry, and that the permissiveness is recorded rather than merely applied.
func TestParseRecordIsPermissiveOnRequiresPython(t *testing.T) {
	unreadable := ">=!!bogus"

	meta, err := ParseRecord(nil, unreadable, nil)
	if err != nil {
		t.Fatalf("an unreadable Requires-Python must not be fatal, got %v", err)
	}
	if !meta.RequiresPythonUnreadable {
		t.Fatal("RequiresPythonUnreadable must be set, or a caller cannot tell this apart from an absent constraint")
	}
	if meta.RequiresPythonRaw != unreadable {
		t.Fatalf("RequiresPythonRaw = %q, want the record's own %q", meta.RequiresPythonRaw, unreadable)
	}

	// The distinguishing case: a record that declared nothing must NOT look like
	// a record that declared something unreadable.
	absent, err := ParseRecord(nil, "", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if absent.RequiresPythonUnreadable {
		t.Fatal("an absent Requires-Python must not be flagged unreadable")
	}
	if absent.RequiresPythonRaw != "" {
		t.Fatalf("RequiresPythonRaw = %q, want empty", absent.RequiresPythonRaw)
	}
}

// TestParseRecordNormalizesExtrasAndLeavesIdentityZero pins PEP 685
// normalization and the purity of the function: it knows nothing about which
// package, version or index it was called for.
func TestParseRecordNormalizesExtrasAndLeavesIdentityZero(t *testing.T) {
	meta, err := ParseRecord([]string{"requests>=2"}, ">=3.8", []string{"Test-Suite", "docs"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if want := []string{"test-suite", "docs"}; !reflect.DeepEqual(meta.ProvidesExtra, want) {
		t.Fatalf("ProvidesExtra = %q, want %q", meta.ProvidesExtra, want)
	}
	if meta.RequiresPythonUnreadable {
		t.Fatal("a readable Requires-Python must not be flagged")
	}
	if len(meta.RequiresDist) != 1 {
		t.Fatalf("RequiresDist has %d entries, want 1", len(meta.RequiresDist))
	}

	// The zero version renders as "", which is the same test checkVersionInitialized
	// applies.
	if meta.Name != "" || meta.Origin != "" || meta.Version.String() != "" {
		t.Fatalf("identity fields must be left to the caller, got Name=%q Origin=%q Version=%q",
			meta.Name, meta.Origin, meta.Version.String())
	}

	// nil in, nil out: "declared no extras" is not "declared an empty list".
	bare, err := ParseRecord(nil, "", nil)
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
