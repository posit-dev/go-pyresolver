// SPDX-License-Identifier: Apache-2.0 OR MIT

package index

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/posit-dev/go-python-packaging/version"
)

// An uninitialized version.Version reaching Metadata used to PANIC on
// decoded[ver.String()]. With go-python-packaging v0.3.1 it no longer panics,
// but it would then silently degrade into ErrMetadataUnavailable -- "the RSF has
// no metadata for that version" -- which blames the data for a caller passing a
// zero value. Both outcomes are wrong; this pins the third.
func TestMetadataRejectsUninitializedVersion(t *testing.T) {
	idx := openFixtureIndex(t)

	_, err := idx.Metadata(context.Background(), NewPackageName("flask"), version.Version{})
	if err == nil {
		t.Fatal("expected an error for an uninitialized version")
	}
	if !strings.Contains(err.Error(), "uninitialized") {
		t.Errorf("error should say the version is uninitialized, got: %v", err)
	}

	// ⚠️ Explicitly NOT one of the data-state sentinels. A caller bug must not
	// be mistakable for a fact about the RSF, which is the state-collapse class
	// this package has already had to fix four times.
	for name, sentinel := range map[string]error{
		"ErrMetadataUnavailable": ErrMetadataUnavailable,
		"ErrMetadataUnusable":    ErrMetadataUnusable,
		"ErrPackageNotFound":     ErrPackageNotFound,
		"ErrFilesUnavailable":    ErrFilesUnavailable,
	} {
		if errors.Is(err, sentinel) {
			t.Errorf("uninitialized-version error must not satisfy %s: %v", name, err)
		}
	}
}

func TestFilesRejectsUninitializedVersion(t *testing.T) {
	idx := openFixtureIndex(t)

	_, err := idx.Files(context.Background(), NewPackageName("flask"), version.Version{})
	if err == nil {
		t.Fatal("expected an error for an uninitialized version")
	}
	if !strings.Contains(err.Error(), "uninitialized") {
		t.Errorf("error should say the version is uninitialized, got: %v", err)
	}
	// ⚠️ Files previously returned an ErrFilesUnavailable whose message embedded
	// a RECOVERED panic -- "%!s(PANIC=String method: index out of range...)" --
	// because fmt catches panics raised inside a String method. It looked
	// healthy, which is why the original finding named Files rather than
	// Metadata. Assert the message is clean.
	if strings.Contains(err.Error(), "PANIC") {
		t.Errorf("error message embeds a recovered panic: %v", err)
	}
}

// A real version must still reach the normal paths, so the guard cannot be
// over-broad.
func TestInitializedVersionStillWorks(t *testing.T) {
	idx := openFixtureIndex(t)
	ctx := context.Background()

	meta, err := idx.Metadata(ctx, NewPackageName("flask"), mustVersion(t, "3.0.0"))
	if err != nil {
		t.Fatalf("Metadata with a real version: %v", err)
	}
	if meta.Version.String() != "3.0.0" {
		t.Errorf("Version = %q, want 3.0.0", meta.Version)
	}

	_, err = idx.Files(ctx, NewPackageName("flask"), mustVersion(t, "3.0.0"))
	if !errors.Is(err, ErrFilesUnavailable) {
		t.Errorf("Files with a real version should be ErrFilesUnavailable, got: %v", err)
	}
}
