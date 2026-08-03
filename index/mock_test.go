// SPDX-License-Identifier: Apache-2.0 OR MIT

package index

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/posit-dev/go-python-packaging/version"
)

func mustVersion(t *testing.T, s string) version.Version {
	t.Helper()
	v, err := version.Parse(s)
	if err != nil {
		t.Fatalf("version.Parse(%q): %v", s, err)
	}
	return v
}

func TestMockIndexUnknownPackageIsNotFound(t *testing.T) {
	idx := NewMockIndex("test")
	ctx := context.Background()

	if _, err := idx.Versions(ctx, NewPackageName("nope")); !errors.Is(err, ErrPackageNotFound) {
		t.Errorf("Versions on unknown package: got %v, want ErrPackageNotFound", err)
	}
	if _, err := idx.Metadata(ctx, NewPackageName("nope"), mustVersion(t, "1.0")); !errors.Is(err, ErrPackageNotFound) {
		t.Errorf("Metadata on unknown package: got %v, want ErrPackageNotFound", err)
	}
	if _, err := idx.Files(ctx, NewPackageName("nope"), mustVersion(t, "1.0")); !errors.Is(err, ErrPackageNotFound) {
		t.Errorf("Files on unknown package: got %v, want ErrPackageNotFound", err)
	}
}

// TestMockIndexKnownPackageWithNoVersions covers the distinction the interface
// makes and a resolver depends on: a package that exists but offers nothing is
// an empty slice and a nil error, NOT ErrPackageNotFound.
func TestMockIndexKnownPackageWithNoVersions(t *testing.T) {
	idx := NewMockIndex("test").AddPackage("lonely")

	versions, err := idx.Versions(context.Background(), NewPackageName("lonely"))
	if err != nil {
		t.Fatalf("Versions on known empty package: unexpected error %v", err)
	}
	if len(versions) != 0 {
		t.Errorf("got %d versions, want 0", len(versions))
	}
}

// TestMockIndexVersionsAreNotSorted pins the documented contract. If this ever
// starts returning ascending order, consumers that assume sorting will pass
// here and fail against a real index.
func TestMockIndexVersionsAreNotSorted(t *testing.T) {
	idx := NewMockIndex("test").
		AddVersion("pkg", "1.0").
		AddVersion("pkg", "2.0").
		AddVersion("pkg", "3.0")

	versions, err := idx.Versions(context.Background(), NewPackageName("pkg"))
	if err != nil {
		t.Fatalf("Versions: %v", err)
	}

	want := []string{"3.0", "2.0", "1.0"} // reverse insertion order
	if len(versions) != len(want) {
		t.Fatalf("got %d versions, want %d", len(versions), len(want))
	}
	for i, w := range want {
		if got := versions[i].String(); got != w {
			t.Errorf("versions[%d] = %q, want %q", i, got, w)
		}
	}
}

func TestMockIndexReAddingVersionKeepsOrderAndReplacesMetadata(t *testing.T) {
	idx := NewMockIndex("test").
		AddVersion("pkg", "1.0", "a").
		AddVersion("pkg", "2.0").
		AddVersion("pkg", "1.0", "b>=2")

	versions, err := idx.Versions(context.Background(), NewPackageName("pkg"))
	if err != nil {
		t.Fatalf("Versions: %v", err)
	}
	if len(versions) != 2 {
		t.Fatalf("got %d versions, want 2 (re-adding must not duplicate)", len(versions))
	}

	meta, err := idx.Metadata(context.Background(), NewPackageName("pkg"), mustVersion(t, "1.0"))
	if err != nil {
		t.Fatalf("Metadata: %v", err)
	}
	if len(meta.RequiresDist) != 1 || meta.RequiresDist[0].Name != "b" {
		t.Errorf("RequiresDist = %v, want the replacement [b>=2]", meta.RequiresDist)
	}
}

func TestMockIndexMetadataParsesRequirements(t *testing.T) {
	idx := NewMockIndex("origin-a").
		AddVersion("flask", "3.0.0", "werkzeug>=3.0", `importlib-metadata>=3.6; python_version < "3.10"`)

	meta, err := idx.Metadata(context.Background(), NewPackageName("Flask"), mustVersion(t, "3.0.0"))
	if err != nil {
		t.Fatalf("Metadata: %v", err)
	}

	if meta.Name != "flask" {
		t.Errorf("Name = %q, want %q", meta.Name, "flask")
	}
	if meta.Version.String() != "3.0.0" {
		t.Errorf("Version = %q, want %q", meta.Version, "3.0.0")
	}
	if meta.Origin != "origin-a" {
		t.Errorf("Origin = %q, want %q", meta.Origin, "origin-a")
	}
	if len(meta.RequiresDist) != 2 {
		t.Fatalf("got %d requirements, want 2", len(meta.RequiresDist))
	}
	if meta.RequiresDist[0].Specifiers.String() != ">=3.0" {
		t.Errorf("first requirement specifiers = %q, want %q", meta.RequiresDist[0].Specifiers, ">=3.0")
	}
	// The conditional dependency must keep its marker, or the resolver would
	// apply it unconditionally.
	if meta.RequiresDist[1].Marker.IsEmpty() {
		t.Error("second requirement lost its environment marker")
	}
}

func TestMockIndexUnavailableMetadata(t *testing.T) {
	idx := NewMockIndex("test").
		AddVersion("pkg", "1.0").
		SetUnavailable("pkg", "2.0")

	// The version still exists...
	versions, err := idx.Versions(context.Background(), NewPackageName("pkg"))
	if err != nil {
		t.Fatalf("Versions: %v", err)
	}
	if len(versions) != 2 {
		t.Fatalf("got %d versions, want 2 -- an unavailable version still exists", len(versions))
	}

	// ...but its metadata is not retrievable, and that is NOT "not found".
	_, err = idx.Metadata(context.Background(), NewPackageName("pkg"), mustVersion(t, "2.0"))
	if !errors.Is(err, ErrMetadataUnavailable) {
		t.Errorf("got %v, want ErrMetadataUnavailable", err)
	}
	if errors.Is(err, ErrPackageNotFound) {
		t.Error("unavailable metadata must not also report ErrPackageNotFound: " +
			"conflating them lets a resolver silently fall back to an older version")
	}
}

func TestMockIndexSetUnavailableDiscardsExistingMetadata(t *testing.T) {
	idx := NewMockIndex("test").
		AddVersion("pkg", "1.0", "dep").
		SetUnavailable("pkg", "1.0")

	if _, err := idx.Metadata(context.Background(), NewPackageName("pkg"), mustVersion(t, "1.0")); !errors.Is(err, ErrMetadataUnavailable) {
		t.Errorf("got %v, want ErrMetadataUnavailable", err)
	}
}

// TestMockIndexAddFilesRegistersVersion guards the footgun that AddFiles
// without AddVersion would otherwise create: Files reporting ErrPackageNotFound
// looks like a bug in the code under test rather than in the setup.
func TestMockIndexAddFilesRegistersVersion(t *testing.T) {
	idx := NewMockIndex("test").AddFiles("pkg", "1.0", DistFile{
		Filename: "pkg-1.0-py3-none-any.whl",
		Location: "https://example.com/pkg-1.0-py3-none-any.whl",
		Kind:     DistKindWheel,
	})

	files, err := idx.Files(context.Background(), NewPackageName("pkg"), mustVersion(t, "1.0"))
	if err != nil {
		t.Fatalf("Files after AddFiles alone: unexpected error %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("got %d files, want 1", len(files))
	}

	// And metadata is legitimately unavailable for such a version.
	if _, err := idx.Metadata(context.Background(), NewPackageName("pkg"), mustVersion(t, "1.0")); !errors.Is(err, ErrMetadataUnavailable) {
		t.Errorf("Metadata for a files-only version: got %v, want ErrMetadataUnavailable", err)
	}
}

func TestMockIndexFilesReturnsWheelsAndSdists(t *testing.T) {
	idx := NewMockIndex("test").
		AddVersion("pkg", "1.0").
		AddFiles("pkg", "1.0",
			DistFile{Filename: "pkg-1.0-py3-none-any.whl", Kind: DistKindWheel},
			DistFile{Filename: "pkg-1.0-cp312-cp312-manylinux_2_17_x86_64.whl", Kind: DistKindWheel},
			DistFile{Filename: "pkg-1.0.tar.gz", Kind: DistKindSDist},
		)

	files, err := idx.Files(context.Background(), NewPackageName("pkg"), mustVersion(t, "1.0"))
	if err != nil {
		t.Fatalf("Files: %v", err)
	}

	// Per RFD 0001 Section 16, Files returns ALL wheels with no platform
	// filtering -- including a manylinux wheel that this test process could
	// not itself install. The consumer decides.
	if len(files) != 3 {
		t.Fatalf("got %d files, want 3 (no filtering at this layer)", len(files))
	}

	wheels := 0
	for _, f := range files {
		if f.IsWheel() {
			wheels++
		}
	}
	if wheels != 2 {
		t.Errorf("got %d wheels, want 2", wheels)
	}
}

// TestMockIndexReturnsCopies matters because a resolver is entitled to sort or
// otherwise mutate what it is handed. If the mock leaked its own slices, one
// test could corrupt the fixture for the assertions that follow it.
func TestMockIndexReturnsCopies(t *testing.T) {
	idx := NewMockIndex("test").
		AddVersion("pkg", "1.0", "a", "b").
		AddFiles("pkg", "1.0", DistFile{Filename: "one.whl"}, DistFile{Filename: "two.whl"})

	ctx := context.Background()
	pkg := NewPackageName("pkg")
	v := mustVersion(t, "1.0")

	meta, err := idx.Metadata(ctx, pkg, v)
	if err != nil {
		t.Fatalf("Metadata: %v", err)
	}
	meta.RequiresDist[0], meta.RequiresDist[1] = meta.RequiresDist[1], meta.RequiresDist[0]

	again, err := idx.Metadata(ctx, pkg, v)
	if err != nil {
		t.Fatalf("Metadata (second call): %v", err)
	}
	if again.RequiresDist[0].Name != "a" {
		t.Errorf("mutating returned RequiresDist changed the mock's state: got %q, want %q",
			again.RequiresDist[0].Name, "a")
	}

	files, err := idx.Files(ctx, pkg, v)
	if err != nil {
		t.Fatalf("Files: %v", err)
	}
	files[0].Filename = "mutated.whl"

	filesAgain, err := idx.Files(ctx, pkg, v)
	if err != nil {
		t.Fatalf("Files (second call): %v", err)
	}
	if filesAgain[0].Filename != "one.whl" {
		t.Errorf("mutating returned files changed the mock's state: got %q, want %q",
			filesAgain[0].Filename, "one.whl")
	}

	versions, err := idx.Versions(ctx, pkg)
	if err != nil {
		t.Fatalf("Versions: %v", err)
	}
	if len(versions) > 0 {
		versions[0] = mustVersion(t, "99.0")
	}
	versionsAgain, err := idx.Versions(ctx, pkg)
	if err != nil {
		t.Fatalf("Versions (second call): %v", err)
	}
	if versionsAgain[0].String() != "1.0" {
		t.Errorf("mutating returned versions changed the mock's state: got %q", versionsAgain[0])
	}
}

func TestMockIndexSetMetadataForcesIdentityAndDefaultsOrigin(t *testing.T) {
	reqPython, err := version.NewSpecifiers(">=3.9")
	if err != nil {
		t.Fatalf("NewSpecifiers: %v", err)
	}

	idx := NewMockIndex("default-origin").SetMetadata("Zope.Interface", "6.1", PackageMetadata{
		// Deliberately lying about identity: the mock must overwrite these
		// with the key the entry is filed under.
		Name:           NewPackageName("something-else"),
		Version:        mustVersion(t, "99.0"),
		RequiresPython: reqPython,
		ProvidesExtra:  []string{"Test-Suite"},
	})

	meta, err := idx.Metadata(context.Background(), NewPackageName("zope-interface"), mustVersion(t, "6.1"))
	if err != nil {
		t.Fatalf("Metadata: %v", err)
	}

	if meta.Name != "zope-interface" {
		t.Errorf("Name = %q, want the normalized key %q", meta.Name, "zope-interface")
	}
	if meta.Version.String() != "6.1" {
		t.Errorf("Version = %q, want %q", meta.Version, "6.1")
	}
	if meta.Origin != "default-origin" {
		t.Errorf("Origin = %q, want the index default %q", meta.Origin, "default-origin")
	}
	if meta.RequiresPython.String() != ">=3.9" {
		t.Errorf("RequiresPython = %q, want %q", meta.RequiresPython, ">=3.9")
	}
	if len(meta.ProvidesExtra) != 1 || meta.ProvidesExtra[0] != "Test-Suite" {
		t.Errorf("ProvidesExtra = %v, want it preserved verbatim", meta.ProvidesExtra)
	}
}

func TestMockIndexSetMetadataKeepsExplicitOrigin(t *testing.T) {
	idx := NewMockIndex("default").SetMetadata("pkg", "1.0", PackageMetadata{Origin: "explicit"})

	meta, err := idx.Metadata(context.Background(), NewPackageName("pkg"), mustVersion(t, "1.0"))
	if err != nil {
		t.Fatalf("Metadata: %v", err)
	}
	if meta.Origin != "explicit" {
		t.Errorf("Origin = %q, want %q", meta.Origin, "explicit")
	}
}

// TestMockIndexNormalizesNames checks the correctness property that motivates
// the PackageName type: differently-spelled names must land on one identity, or
// the solver builds duplicate nodes and can invent a conflict.
func TestMockIndexNormalizesNames(t *testing.T) {
	idx := NewMockIndex("test").AddVersion("Zope.Interface", "6.1")

	for _, spelling := range []string{
		"zope-interface", "Zope.Interface", "ZOPE_INTERFACE", "zope..interface", "zope-_-interface",
	} {
		if _, err := idx.Versions(context.Background(), NewPackageName(spelling)); err != nil {
			t.Errorf("Versions(%q): unexpected error %v", spelling, err)
		}
	}
}

func TestMockIndexHonorsContextCancellation(t *testing.T) {
	idx := NewMockIndex("test").AddVersion("pkg", "1.0")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	pkg := NewPackageName("pkg")
	v := mustVersion(t, "1.0")

	if _, err := idx.Versions(ctx, pkg); !errors.Is(err, context.Canceled) {
		t.Errorf("Versions: got %v, want context.Canceled", err)
	}
	if _, err := idx.Metadata(ctx, pkg, v); !errors.Is(err, context.Canceled) {
		t.Errorf("Metadata: got %v, want context.Canceled", err)
	}
	if _, err := idx.Files(ctx, pkg, v); !errors.Is(err, context.Canceled) {
		t.Errorf("Files: got %v, want context.Canceled", err)
	}
}

// TestMockIndexConcurrentUse exists to be run under -race. The interface
// promises implementations are safe for concurrent use, and a resolver that
// looks ahead in parallel will rely on it.
func TestMockIndexConcurrentUse(t *testing.T) {
	idx := NewMockIndex("test")
	for i := range 20 {
		idx.AddVersion("pkg", fmtVersion(i), "dep>=1.0")
	}

	ctx := context.Background()
	pkg := NewPackageName("pkg")

	var wg sync.WaitGroup
	for i := range 16 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if _, err := idx.Versions(ctx, pkg); err != nil {
				t.Errorf("Versions: %v", err)
				return
			}
			v, err := version.Parse(fmtVersion(i % 20))
			if err != nil {
				t.Errorf("Parse: %v", err)
				return
			}
			if _, err := idx.Metadata(ctx, pkg, v); err != nil {
				t.Errorf("Metadata: %v", err)
			}
			if _, err := idx.Files(ctx, pkg, v); err != nil {
				t.Errorf("Files: %v", err)
			}
		}(i)
	}
	wg.Wait()
}

func fmtVersion(i int) string {
	return fmt.Sprintf("1.%d.%d", i/10, i%10)
}

func TestMockIndexPanicsOnBadTestSetup(t *testing.T) {
	for _, tc := range []struct {
		name string
		fn   func()
	}{
		{"bad version", func() { NewMockIndex("t").AddVersion("pkg", "not-a-version") }},
		{"bad requirement", func() { NewMockIndex("t").AddVersion("pkg", "1.0", "!!!broken") }},
		{"bad version in AddFiles", func() { NewMockIndex("t").AddFiles("pkg", "nope") }},
		{"bad version in SetMetadata", func() { NewMockIndex("t").SetMetadata("pkg", "nope", PackageMetadata{}) }},
		{"bad version in SetUnavailable", func() { NewMockIndex("t").SetUnavailable("pkg", "nope") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Error("expected a panic: bad test setup must fail loudly, " +
						"since an error return would be ignored and leave a silently empty index")
				}
			}()
			tc.fn()
		})
	}
}

func TestDistFileFieldsRoundTrip(t *testing.T) {
	uploaded := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
	reqPython, err := version.NewSpecifiers(">=3.8")
	if err != nil {
		t.Fatalf("NewSpecifiers: %v", err)
	}

	want := DistFile{
		Filename:       "pkg-1.0-py3-none-any.whl",
		Location:       "file:///var/lib/ppm/pkg-1.0-py3-none-any.whl",
		Kind:           DistKindWheel,
		Size:           4096,
		Hashes:         map[string]string{"sha256": "abc123"},
		UploadTime:     uploaded,
		RequiresPython: reqPython,
		Yanked:         true,
		YankedReason:   "broken metadata",
	}

	idx := NewMockIndex("test").AddVersion("pkg", "1.0").AddFiles("pkg", "1.0", want)

	files, err := idx.Files(context.Background(), NewPackageName("pkg"), mustVersion(t, "1.0"))
	if err != nil {
		t.Fatalf("Files: %v", err)
	}
	got := files[0]

	if got.Filename != want.Filename || got.Location != want.Location {
		t.Errorf("Filename/Location = %q/%q, want %q/%q", got.Filename, got.Location, want.Filename, want.Location)
	}
	// A file:// Location must survive unchanged: the interface treats Location
	// as opaque, which is what lets an air-gapped index return local paths
	// through the same type a connected index uses for https URLs.
	if got.Size != want.Size || got.Hashes["sha256"] != "abc123" {
		t.Errorf("Size/Hashes = %d/%v, want %d/%v", got.Size, got.Hashes, want.Size, want.Hashes)
	}
	if !got.UploadTime.Equal(uploaded) {
		t.Errorf("UploadTime = %v, want %v", got.UploadTime, uploaded)
	}
	if got.RequiresPython.String() != ">=3.8" {
		t.Errorf("RequiresPython = %q, want %q", got.RequiresPython, ">=3.8")
	}
	if !got.Yanked || got.YankedReason != "broken metadata" {
		t.Errorf("Yanked/YankedReason = %v/%q, want true/%q", got.Yanked, got.YankedReason, "broken metadata")
	}
}

func TestDistKindString(t *testing.T) {
	for _, tc := range []struct {
		kind DistKind
		want string
	}{
		{DistKindWheel, "wheel"},
		{DistKindSDist, "sdist"},
		{DistKindUnknown, "unknown"},
		{DistKind(99), "unknown"},
	} {
		if got := tc.kind.String(); got != tc.want {
			t.Errorf("DistKind(%d).String() = %q, want %q", tc.kind, got, tc.want)
		}
	}
}

// TestDistKindZeroValueIsUnknown pins the ordering choice in the iota block: if
// DistKindWheel were the zero value, a DistFile built by an incomplete
// implementation would silently claim to be an installable wheel.
func TestDistKindZeroValueIsUnknown(t *testing.T) {
	var zero DistKind
	if zero != DistKindUnknown {
		t.Errorf("zero DistKind = %v, want DistKindUnknown", zero)
	}
	if (DistFile{}).IsWheel() {
		t.Error("a zero DistFile must not report itself as a wheel")
	}
}
