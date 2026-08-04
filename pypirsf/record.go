// SPDX-License-Identifier: Apache-2.0 OR MIT

package pypirsf

// The rsf struct tags below drive the format library's reflection-based
// WRITER, not its reader. There is no tag-driven Unmarshal: reading is manual
// field navigation against the schema each file carries (see file.go). These
// structs therefore serve two purposes — they document the layout, and they
// let a test generate a real RSF with the real writer.
//
// A consequence worth knowing: this declares ONE snapshot element layout, and
// three have existed historically. Package Manager's reader detects which a
// given file uses from that file's own schema. This package sidesteps the whole
// question by never traversing the snapshots array; if you ever need to, do the
// detection rather than trusting this declaration.

// SnapshotRecord is one snapshot entry within a PyPI package record.
//
// # Field order is load-bearing — do not reorder
//
// ReleaseDate MUST stay between Version and Summary, and Summary MUST remain
// last. Released readers navigate these subfields by name using the RSF
// reader's forward-only skip, so they advance from "summary" straight to the
// next element's "deleted" without skipping anything in between — which means
// they silently depend on "summary" being last. Placing a field after Summary
// leaks its bytes into the next element and desynchronizes every shipped
// reader with an unexpected EOF.
//
// This is not hypothetical: it happened in production. Treat the ordering as
// part of the wire format rather than as a struct-layout detail.
type SnapshotRecord struct {
	// CanonicalName and ProjectName are populated by the caller from the
	// enclosing record, not read from the snapshot element itself.
	CanonicalName string `json:"-" rsf:"-"`
	ProjectName   string `json:"-" rsf:"-"`

	Deleted  bool   `json:"d,omitempty" rsf:"deleted"`
	Snapshot string `json:"s" rsf:"snapshot,skip,fixed:10"`
	Version  string `json:"v,omitempty" rsf:"version"`

	// ReleaseDate is a 2-byte big-endian uint16 of epoch-days, always exactly
	// 2 bytes on the wire. See the type comment before changing its position.
	ReleaseDate string `json:"r,omitempty" rsf:"reldate,fixed:2"`

	Summary string `json:"u,omitempty" rsf:"summary"`
}

// PackageRecord is one PyPI package record in an RSF.
//
// Deps carries this package's dependency blob; Depsdict carries the global
// dictionary and is populated only on the FIRST record of the file. Decode
// Depsdict once with ParseDepsdictField and reuse the resulting Dict for every
// package's Deps.
//
// The trailing blob fields were appended additively so that a reader which does
// not know about them skips them via the record size prefix. That is why the
// order here matters in one direction only: Deps and Depsdict precede License
// and Licensedict, so a reader that wants dependency data never has to traverse
// the license fields.
//
// License and Licensedict are declared for completeness of the layout. This
// package does not decode them — license derivation is a Package Manager
// concern, and the two blob formats are independent.
type PackageRecord struct {
	CanonicalName string           `rsf:"cname"`
	ProjectName   string           `rsf:"pname"`
	Snapshots     []SnapshotRecord `rsf:"snapshots,index:snapshot"`
	Deps          string           `json:"-" rsf:"deps"`
	Depsdict      string           `json:"-" rsf:"depsdict"`
	License       string           `json:"-" rsf:"license"`
	Licensedict   string           `json:"-" rsf:"licensedict"`
}
