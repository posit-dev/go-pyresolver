// SPDX-License-Identifier: Apache-2.0 OR MIT

package pypirsf

import (
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// The tests here pin the RSF record shape this package reads. They exist because
// the shape is a CROSS-REPO contract with no shared definition: three
// repositories declare the same wire layout independently, and agreement is
// enforced only by hand-maintained literals like the ones below.
//
//	producer  rstudio/pypi-manifest    internal/manifest/metadata/shape_test.go
//	server    rstudio/package-manager  src/rsf/types/shape_test.go
//	resolver  this file
//
// Until this file existed, this repo was the one leg with no such test, so a
// one-sided field insertion or reorder stayed green in its own CI and was only
// discovered as wrong data at read time. Nothing here can detect that the OTHER
// two repos changed; what it does is make a change to THIS declaration
// impossible to land silently, which is the half that was missing.
//
// If a test here fails, do not just update the literal. Check the sibling tests
// above first: the fields are positional on the wire, so an insertion anywhere
// but the end changes what every later field means.

// TestPackageRecordShape pins the exact field list and order of PackageRecord.
//
// Order is load-bearing, and it is the `rsf` tag sequence -- not the Go names or
// types -- that has to match the other two repos. Trailing fields may be
// APPENDED: the RSF reader skips fields it does not know via the record size
// prefix, which is how `license`/`licensedict` arrived, and how `tagsdict`
// arrives for the server without this package needing to declare it. Anything
// other than an append is a wire break.
func TestPackageRecordShape(t *testing.T) {
	typ := reflect.TypeOf(PackageRecord{})
	var b strings.Builder
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		b.WriteString(f.Name + " " + f.Type.String() + " rsf:" + strconv.Quote(f.Tag.Get("rsf")) + "\n")
	}
	got := b.String()
	want := `CanonicalName string rsf:"cname"
ProjectName string rsf:"pname"
Snapshots []pypirsf.SnapshotRecord rsf:"snapshots,index:snapshot"
Deps string rsf:"deps"
Depsdict string rsf:"depsdict"
License string rsf:"license"
Licensedict string rsf:"licensedict"
`
	if got != want {
		t.Fatalf("PackageRecord shape drifted:\n got=\n%s\nwant=\n%s", got, want)
	}
}

// TestPackageRecordRSFTagOrder states the cross-repo contract on its own, as the
// bare tag sequence, so it can be compared with the sibling repos by eye even
// though their Go field names and types differ from this package's.
func TestPackageRecordRSFTagOrder(t *testing.T) {
	typ := reflect.TypeOf(PackageRecord{})
	got := make([]string, 0, typ.NumField())
	for i := 0; i < typ.NumField(); i++ {
		got = append(got, typ.Field(i).Tag.Get("rsf"))
	}
	want := []string{
		"cname",
		"pname",
		"snapshots,index:snapshot",
		"deps",
		"depsdict",
		"license",
		"licensedict",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("PackageRecord rsf tag order drifted:\n got=%q\nwant=%q", got, want)
	}
}

// TestSnapshotRecordShape pins the snapshot element's SUBFIELD order, which is
// load-bearing in a way the record's is not: these are fixed-width and read
// positionally within each element, so `reldate` moving by one field misreads
// every following byte rather than failing.
//
// CanonicalName and ProjectName carry rsf:"-": they are filled in from the
// enclosing record and are NOT on the wire. They still belong in this literal --
// dropping them from the struct would change nothing on disk but would change
// this package's API, and the test should say which fields are which.
func TestSnapshotRecordShape(t *testing.T) {
	typ := reflect.TypeOf(SnapshotRecord{})
	var b strings.Builder
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		b.WriteString(f.Name + " " + f.Type.String() + " rsf:" + strconv.Quote(f.Tag.Get("rsf")) + "\n")
	}
	got := b.String()
	want := `CanonicalName string rsf:"-"
ProjectName string rsf:"-"
Deleted bool rsf:"deleted"
Snapshot string rsf:"snapshot,skip,fixed:10"
Version string rsf:"version"
ReleaseDate string rsf:"reldate,fixed:2"
Summary string rsf:"summary"
`
	if got != want {
		t.Fatalf("SnapshotRecord shape drifted:\n got=\n%s\nwant=\n%s", got, want)
	}
}
