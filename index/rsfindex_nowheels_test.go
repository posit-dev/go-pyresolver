// SPDX-License-Identifier: Apache-2.0 OR MIT

package index

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"sort"
	"testing"

	rsf "github.com/rstudio/repository-snapshot-format"

	"github.com/posit-dev/go-pyresolver/pypirsf"
)

// TestKnownNoWheelsMatchesFullMetadata runs the producer's wheel-tag fixture
// (sdist-only, no file at all, only non-matching wheels, uncaptured, wheels)
// through a real RSF and checks the cheap answer equals the full-decode answer
// for every version.
func TestKnownNoWheelsMatchesFullMetadata(t *testing.T) {
	td := "../pypirsf/testdata/"
	tags, err := os.ReadFile(td + "crossrepo_tags_tagsdict.bin")
	if err != nil {
		t.Fatal(err)
	}
	blob, err := os.ReadFile(td + "crossrepo_tags_blob.bin")
	if err != nil {
		t.Fatal(err)
	}

	var dict bytes.Buffer
	dict.WriteByte(0x01)
	putUvarint(&dict, 2)
	putStr(&dict, "flask")
	putStr(&dict, "numpy")
	putUvarint(&dict, 0)

	versions := []string{"1.0.0", "1.0.1", "1.1.0", "2.0.0", "3.0.0", "4.0.0", "5.0.0"}
	var snaps []pypirsf.SnapshotRecord
	for _, v := range versions {
		snaps = append(snaps, pypirsf.SnapshotRecord{Snapshot: "2026080100", Version: v, ReleaseDate: "\x00\x01", Summary: "x"})
	}
	rec := pypirsf.PackageRecord{
		CanonicalName: "tagged",
		ProjectName:   "Tagged",
		Snapshots:     snaps,
		Deps:          string(append([]byte{0x02}, blob...)),
		Depsdict:      dict.String(),
		Tagsdict:      string(tags),
	}

	path := filepath.Join(t.TempDir(), "tags.rsf")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rsf.NewWriter(f).WriteObject(rec); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	file, err := pypirsf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	idx, err := NewRSFIndex(file, "tags-rsf")
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	var hidden []string
	for _, v := range versions {
		ver := mustVersion(t, v)
		meta, err := idx.Metadata(ctx, NewPackageName("tagged"), ver)
		if err != nil {
			t.Fatalf("Metadata %s: %v", v, err)
		}
		full := meta.TagsCaptured && len(meta.WheelTags) == 0
		cheap, err := idx.KnownNoWheels(ctx, NewPackageName("tagged"), ver)
		if err != nil {
			t.Fatalf("KnownNoWheels %s: %v", v, err)
		}
		if cheap != full {
			t.Errorf("%s: cheap=%v full=%v", v, cheap, full)
		}
		if cheap {
			hidden = append(hidden, v)
		}
	}
	sort.Strings(hidden)
	if got := len(hidden); got != 2 || hidden[0] != "3.0.0" || hidden[1] != "4.0.0" {
		t.Errorf("hidden set = %v, want [3.0.0 4.0.0]", hidden)
	}
}
