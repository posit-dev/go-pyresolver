# index/testdata

## `pypi-trimmed.rsf`

An excerpt of a real production PyPI Repository Snapshot Format file, committed so
that the tests which exercise **producer output** run on every pull request rather
than only when a maintainer happens to have a 1 GB snapshot on disk.

`TestRSFIndexAgainstRealFile`, `TestRealFileContentIsDecodedCorrectly`,
`TestRealFileAwkwardShapes`, `TestRealFileErrorTaxonomy` and
`TestRealFileCarriesATrainedDictionary` all read it. They **fail** rather than skip
if it is missing: those tests were env-var gated for months, so CI never ran the
only tests that read what the producer actually emits
(rstudio/package-manager#19466). A silent skip is the defect, not the fallback.

### What is in it

| | |
|---|---|
| Records | 139 |
| Size | ~988 KB, of which ~297 KB is the first record's shared dependency dictionary (4096 trained names) |
| Source | `~/.cache/ppm-rsf/prod.rsf`, a full snapshot of 932,861 packages, dated 2026-08-04 |

Two groups of packages:

1. **The closure `TestRSFIndexAgainstRealFile` walks** — `flask` plus every
   package reachable in 4 breadth-first steps through the newest version's
   `Requires-Dist`, which is 131 packages. All of them are needed: that test
   asserts every discovered name resolves, so a missing one fails the run.

2. **Seven packages included for the SHAPE they carry**, each a state that was an
   actual defect in this module. They are listed in `fixtureExtras` in
   `index/fixture_gen_test.go` with the state each one demonstrates, and each has
   an assertion in `TestRealFileAwkwardShapes`. `flask`'s closure is well-behaved,
   which is why the walk alone could pass while telling us very little.

### Bytes are copied, not re-encoded

Records are copied out of the source verbatim. The point of this fixture is to
read what the producer wrote: the schema it declares, the per-package snapshots
array this reader deliberately skips wholesale, and dependency blobs compressed
against a trained zstd dictionary. Re-encoding through this module's own writer
would prove only that it agrees with itself, which the synthetic fixtures in
`rsfindex_test.go` already do.

Two consequences worth knowing:

- The source's **first record is always kept**, whatever package it is, because
  the global dependency dictionary is carried there and nowhere else.
- The excerpt is **not** a valid snapshot in any other sense. It has no
  checkpoint identity, its package set is arbitrary, and nothing should treat it
  as representative of PyPI.

### Regenerating

```sh
PYPIRSF_TRIM_SRC=/path/to/pypi.rsf go test ./index/ -run TestGenerateTrimmedFixture -v
```

Full snapshots come from `https://rspm-sync.rstudio.com/pypi/manifest/v2/1/rsf/<checkpoint>.rsf`,
with checkpoint ids in `.../pypi/manifest/v2/1/checkpoints.json`. The generator
verifies its own output: it re-walks the excerpt and fails if the closure differs
from the source's.

Regenerating from a newer snapshot is a deliberate act, not routine hygiene. It
changes what CI asserts against, and packages can be deleted from PyPI — if one of
the shape extras disappears,
`TestRealFileAwkwardShapes` will say so by name rather than quietly losing the
case.

### Provenance and licensing

The content is PyPI metadata — project names, versions, `Requires-Dist`,
`Requires-Python`, `Provides-Extra`, release summaries — which is public. The
*packaging* of it is not: the RSF artifact and its trained compression dictionary
are produced by Posit's manifest tooling, and full snapshots are distributed under
terms for Package Manager customers.

⚠️ This module is a **public** repository. Committing an excerpt of a licensed
artifact is a deliberate trade — repository weight and provenance versus a CI
network dependency — and it should have a human sign-off rather than being
inherited from this file. If that sign-off does not come, the alternative that
keeps most of the value is to decode each kept package's dependency blob and
re-encode it as a *stored* blob, which drops the trained dictionary and the zstd
path along with it.
