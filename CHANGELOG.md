# Changelog

All notable changes to this module are documented here.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).
While the major version is `0`, breaking changes ship in a **minor** bump rather
than a major one, and are always listed under a `Breaking` heading so they are not
mistaken for a safe patch upgrade.

Entries go under `## [Unreleased]`, never into a dated section. A dated section may
already be tagged, and a Go module tag cannot be moved once the module proxy has
served it.

## [Unreleased]

## [0.7.0] - 2026-08-14

### Changed

- **`pep440set` no longer renders a version back to text to find its release
  group.** Warm resolution is **1.19x to 1.39x faster and allocates 1.25x to
  2.27x less** on the six corpus entries that resolve anything; cold is 1.11x to
  1.28x. The seventh, `unsatisfiable`, does not move — see below. No resolution
  this module produces changes.

  `Contains` needs each candidate version's `(epoch, release)` position, and it
  derived that by calling `BaseVersion()` — which renders the version through a
  `bytes.Buffer` with one `math/big` decimal conversion **per segment** — and
  then splitting the result back into digit runs. Once per candidate version per
  `Contains` call. It now asks go-python-packaging for
  `version.ReleaseKey` instead, which reads the parsed `Version`'s own epoch and
  release fields: **no allocation, against 10** for the render-and-split, and
  roughly an order of magnitude less time.

  ⚠️ The allocation counts are exact and machine-independent; the times are
  not. gpp's `BenchmarkReleaseKeyVsBaseVersionSplit` reads 16 ns vs 213 ns on
  an idle M4 Max and 23 ns vs 520 ns on a loaded one — the ratio survives, a
  quoted nanosecond figure does not. Run the benchmark rather than trusting a
  number written down here.

  Medians of three interleaved rounds, ten iterations, against the production
  snapshot (932,861 packages, dated 2026-08-04), on an Apple M4 Max:

  | entry | warm before | warm after | | warm allocs before | after | |
  |---|---|---|---|---|---|---|
  | `single-no-deps` | 0.21 ms | 0.15 ms | 1.38x | 3,637 | 2,260 | 1.61x |
  | `small-tree` | 1.06 ms | 0.80 ms | 1.32x | 19,393 | 10,908 | 1.78x |
  | `extras` | 1.45 ms | 1.08 ms | 1.35x | 29,391 | 15,102 | 1.95x |
  | `app-set` | 4.54 ms | 3.27 ms | 1.39x | 96,318 | 42,415 | 2.27x |
  | `wide-versions` | 8.11 ms | 6.74 ms | 1.20x | 197,228 | 127,659 | 1.54x |
  | `backtracking` | 2.78 ms | 2.33 ms | 1.19x | 43,304 | 21,015 | 2.06x |
  | `unsatisfiable` | 0.33 ms | 0.31 ms | 1.07x | 6,448 | 5,168 | 1.25x |

  | entry | cold before | cold after | | cold allocs before | after | |
  |---|---|---|---|---|---|---|
  | `single-no-deps` | 0.27 ms | 0.23 ms | 1.17x | 4,365 | 2,982 | 1.46x |
  | `small-tree` | 1.41 ms | 1.10 ms | 1.28x | 23,609 | 15,107 | 1.56x |
  | `extras` | 1.82 ms | 1.46 ms | 1.25x | 34,630 | 20,354 | 1.70x |
  | `app-set` | 5.94 ms | 4.70 ms | 1.26x | 118,465 | 64,533 | 1.84x |
  | `wide-versions` | 13.14 ms | 11.86 ms | 1.11x | 276,776 | 207,206 | 1.34x |
  | `backtracking` | 6.22 ms | 5.26 ms | 1.18x | 89,097 | 66,832 | 1.33x |
  | `unsatisfiable` | 0.55 ms | 0.51 ms | 1.08x | 9,256 | 7,974 | 1.16x |

  **The allocation column is the one to read.** The saving is not the arithmetic
  — it is the garbage. `app-set` allocates 2.27x less warm while running 1.39x
  faster.

  In a CPU profile of the warm resolution, `Set.Contains` falls from **41% of
  `resolver.Resolve` to 24%** on `app-set`, and from 33% to 14% on
  `wide-versions` — 3.1x and 3.8x fewer samples in absolute terms. Measured
  against `Resolve` rather than against total samples deliberately: removing
  this allocation shrinks the profile's garbage-collection share too, so a
  "% of samples" figure would credit the change with that as well.

  `strings.Split` leaves the profile entirely. `writeRelease` and
  `math/big.nat.itoa` do **not** — `ensurePub` still renders the public
  spelling, which this change deliberately does not touch — but they fall from
  2.3% and 1.6% of samples under `Contains` to below the sampling floor.

  ⚠️ **`unsatisfiable` is not measurably faster.** Its 1.07x is the median of
  three rounds, and a fourth confirmation round put it at **0.96x** — slower.
  Read it as no change, not as a small win. That is the predicted result rather
  than a disappointment: it fails before enumerating many candidates, so it
  makes the fewest `Contains` calls of the corpus and has the least to gain.
  Its allocations do fall, by 1.25x, which is the effect showing up where the
  mechanism says it should.

  ⚠️ The measurement is against `v0.6.0` **without** the parsed-version memo. The
  two are not additive — after this change the largest remaining warm cost is
  `version.Parse` inside `index.Versions` (16.9% of samples on `wide-versions`),
  which is exactly what that memo removes.

- **go-python-packaging is now `v0.7.0`**, for `version.ReleaseKey`. Purely
  additive; nothing else in this module changed with the pin.

## [0.6.0] - 2026-08-14

### Changed

- **go-python-packaging is now `v0.6.0`**, which brings a packed integer version
  comparison key. **Cold resolution is 2.0x to 13.4x faster and warm 1.2x to 1.9x**,
  with no change to any resolution this module produces.

  Nothing in this repository changed but the dependency pin. `version.Version`
  comparison now runs off a packed 4×uint64 order-preserving key rather than a
  field-by-field walk, for the 97.3% of real versions that fit one.

  Medians of five interleaved rounds, ten iterations, against the production
  snapshot (932,861 packages, dated 2026-08-04), on an Apple M4 Max:

  | entry | cold before | cold after | | warm before | warm after | |
  |---|---|---|---|---|---|---|
  | `single-no-deps` | 1.18 ms | 0.24 ms | 4.9x | 0.24 ms | 0.17 ms | 1.42x |
  | `small-tree` | 4.87 ms | 1.21 ms | 4.0x | 1.22 ms | 0.91 ms | 1.34x |
  | `extras` | 6.47 ms | 1.65 ms | 3.9x | 1.71 ms | 1.32 ms | 1.29x |
  | `app-set` | 20.67 ms | 5.50 ms | 3.8x | 5.40 ms | 4.22 ms | 1.28x |
  | `wide-versions` | 167.35 ms | 12.46 ms | 13.4x | 14.15 ms | 7.61 ms | 1.86x |
  | `backtracking` | 11.16 ms | 5.61 ms | 2.0x | 3.10 ms | 2.59 ms | 1.20x |
  | `unsatisfiable` | 2.33 ms | 0.47 ms | 4.9x | 0.45 ms | 0.30 ms | 1.50x |

  **Cold gains far exceed warm, and that asymmetry is the whole story.** Building a
  package's sorted version order is comparison-bound, and it happens once per package
  per index — so it lands entirely in cold, where `wide-versions` (botocore, over ten
  thousand releases) drops 13.4x. Warm reuses that order through the memo added in
  0.5.0, so what the packed key can still reach warm is only the residual comparison
  inside `pep440set` containment and candidate filtering. That the warm figure is
  1.2–1.9x rather than flat says those residual comparisons were still material after
  #39 and #40; that it is not larger says #40 had already removed most of them.

  ⚠️ **This does not compose multiplicatively with #40 — the two CONTEND**, and that is
  measured rather than assumed. #40 removed most comparisons; the packed key makes the
  remainder cheap. Both attack the same cost, so each is worth less once the other has
  landed. A 2×2 in one interleaved session, warm, medians of three:

  | entry | packed gain BEFORE #40 | packed gain AFTER #40 | #40 gain at v0.5.0 | #40 gain at v0.6.0 |
  |---|---|---|---|---|
  | `single-no-deps` | 5.04x | 1.42x | 6.14x | 1.73x |
  | `small-tree` | 2.37x | 1.32x | 3.26x | 1.81x |
  | `extras` | 3.10x | 1.30x | 5.44x | 2.28x |
  | `app-set` | 5.33x | 1.28x | 12.34x | 2.95x |
  | `wide-versions` | 5.93x | 1.88x | 5.42x | 1.72x |
  | `backtracking` | 4.01x | 1.23x | 7.49x | 2.29x |
  | `unsatisfiable` | 1.99x | 1.48x | 1.43x | 1.07x |

  Both directions shrink, which is what "substitutes" means. On `app-set`, multiplying
  the two isolated headlines predicts 12.34 × 5.33 = **65.8x**; the measured end-to-end
  from `73d820a` + v0.5.0 to `276fe91` + v0.6.0 is **15.7x**. ⚠️ This is the opposite of
  the #39/#40 interaction, where `Contains` became *more* valuable after the rank memo
  (1.02x → 1.29x) — so the sign of these interactions cannot be guessed and has to be
  measured each time.

  It also explains an apparent discrepancy with go-python-packaging's own release notes,
  which claim 2.1–5.2x warm on this benchmark: that was measured at `73d820a`, before
  #40 landed 46 minutes later. The pre-#40 column above reproduces it (1.99–5.93x). The
  figures in the table at the top of this entry are the ones that apply to `main`.

  `candvers`, `metadata` and the pin set are **identical** on every entry, as they must
  be: this changes what a comparison costs, not what it answers. Allocation churn falls
  with the wall clock (`app-set` cold 23.1 MB / 475,428 allocs → 10.9 MB / 118,447).
  Peak heap is flat to better — `wide-versions` 18.7 MB → 12.3 MB over baseline — with
  one exception measured and kept rather than dropped: `backtracking` retains slightly
  *more* (6.0 MB → 6.5 MB) and allocates slightly more bytes warm, while still
  allocating fewer objects and finishing faster.

  `pypirsf.Open` plus `NewRSFIndex` over the same snapshot is unchanged at ~220 ms
  (220.0 ms → 216.6 ms, within noise): it builds a name-to-offset table and parses no
  versions, so the key cannot reach it.

  **Equivalence, since a comparison key change touches every ordering decision.**
  4,007 resolutions against the production snapshot — the seven corpus entries plus
  4,000 sampled package names, seed 1 — produce byte-identical transcripts before and
  after: identical pins, identical decision ORDER, identical activated extras, and
  identical failure report text on the 1,607 that fail. 36 cases where either side hit
  an 8-second wall-clock deadline are excluded (35 on both sides, 1 on the baseline
  only, 0 on the bumped side only).

- **Resolution is 1.4x to 11.8x faster warm**, with no change to any resolution it
  produces. `Provider.Candidates` no longer re-ranks a package's versions on every
  call, and `candidate.Rank` no longer sorts a list that is already ordered.

  Ranking was **83–86% of a `Candidates` call** — measured warm against a production
  snapshot, against 1.7% and 0.6% for the usability walk the previous release had just
  optimized. Two independent causes:

  - The solver re-asks about a package on every round it reconsiders it, and every
    call re-sorted from scratch. `Provider` now memoizes the ranked **full** version
    list per package for the life of one resolution. Ranking the superset rather than
    the in-range list is what makes it memoizable at all: the in-range list is a
    function of the caller's allowed set, and a memo keyed by that would be keyed on
    caller-supplied input and grow without bound.
  - `index.RSFIndex` returns versions **ascending** while the default `Newest` policy
    wants them descending, so `sort.SliceStable` was handed its worst case on
    essentially every call. `candidate.Rank` now classifies the input in one linear
    pass and reverses it, or returns it untouched, when it can.

  Warm, ten iterations, medians of three interleaved runs, against base `73d820a`:

  | entry | warm before | warm after | |
  |---|---|---|---|
  | `single-no-deps` | 1.77 ms | 0.28 ms | 6.3x |
  | `small-tree` | 4.31 ms | 1.39 ms | 3.1x |
  | `extras` | 9.68 ms | 1.90 ms | 5.1x |
  | `app-set` | 67.10 ms | 5.67 ms | 11.8x |
  | `wide-versions` | 80.33 ms | 15.02 ms | 5.3x |
  | `backtracking` | 24.51 ms | 3.29 ms | 7.5x |
  | `unsatisfiable` | 0.73 ms | 0.51 ms | 1.4x |

  `Metadata` calls are **unchanged**: this reads no less data, it stops re-sorting it.
  What falls is `candvers` — 6,040 to 943 on `app-set`, 2,495 to 351 on `backtracking`
  — which the previous release's change did not move at all. Allocations fall with it,
  `app-set` from 67.2 MB / 1,444,306 to 8.7 MB / 123,869.

  **Peak heap falls too, 3.0x to 7.3x**, which is the number `B/op` cannot give:
  `app-set`'s peak-over-baseline goes 53.9 MB → 7.4 MB and `wide-versions`' 56.2 MB →
  18.6 MB, reproducible across three runs a side. ⚠️ This was worth measuring rather
  than assuming, and it contradicts the obvious prediction: a memo holds version lists
  for the whole resolve where they used to be garbage, so it *looks* like churn traded
  for retention. It is not, because the churn was never short-lived — the old path
  allocated a fresh in-range slice and a fresh `Rank` copy on each of `app-set`'s 87
  calls, which pile up within a GC cycle, against 18 retained lists now and no in-range
  slice at all. See `resolver.TestPeakHeapDuringOneResolve` (a *sampled* maximum, so a
  floor on the true peak).

  ⚠️ This is a delta from `73d820a`, which already contains the `pep440set.Contains`
  change below, and the two are **not** independent — `Contains` sits inside
  `Candidates`. Measured as a 2×2 in one interleaved session on the three largest
  entries: `Contains` alone is 1.02x/1.12x/1.02x, this change alone is
  9.19x/5.43x/6.90x, and `Contains` **on top of** this change is 1.29x/1.12x/1.11x.
  It became more valuable, not less: `Contains` was 4.4% of a `Candidates` call before
  the ranking work and 28.6% after, so the same absolute saving is a larger share of a
  smaller total. Neither entry should be read as containing the other's win; the
  decomposition is in `resolver/bench_test.go`.

  ⚠️ The memo lives on the `Provider`, **not** on the index, and that is
  correctness-bearing rather than stylistic. A `version.Version` cannot be shared
  between goroutines even for reads — `Version.Compare` pads a release segment with
  `append` into spare capacity a by-value copy still shares, the upstream defect in
  `rstudio/go-version` that `index.RSFIndex` documents and declines to memoize parsed
  versions because of. A `Provider` serves one resolution and is documented as unsafe
  for concurrent use, so nothing here is read from two goroutines; concurrent
  resolutions get one `Provider` and therefore one memo each. On a shared index this
  would reintroduce that race in full.

  ⚠️ `candidate.Rank`'s fast path **leans on** `Policy.Less` being transitive where
  `sort.SliceStable` merely benefits from it. `Policy` already requires transitivity in
  as many words, so this is not a new demand on an embedder — but it is a new place
  where breaking it goes unnoticed, because an intransitive `Less` yields the input
  order or its reverse instead of an arbitrary sort. `Newest` is verified to be a
  genuine strict weak ordering on production data: 124,918 ordered triples and 96,989
  genuine incomparability witnesses, no violation — at `GPR_SAMPLE=3000
  GPR_TRIPLES=3000000`, which is the configuration those figures come from and which
  the default run (300,000 triples) does not reproduce.

  Equivalence is measured, not argued: **4,007 resolutions against the production
  snapshot produce byte-identical transcripts** — same pins, same decision order, same
  activated extras, and the same failure report text on the 1,605 that fail. A second
  run of 1,007 against the current base agrees, 996 of 996 compared.

  ⚠️ Every corpus figure in this entry comes from a **local run against the 981 MB
  production snapshot**, which CI does not have. The snapshot-backed tests fall back to
  the committed 1 MB excerpt (139 packages) when `PYPIRSF_TEST_FILE` is unset, and CI's
  anti-skip guard covers `./index/` only — so CI verifies these tests *run and pass*, at
  roughly 139 packages, not at the scale quoted here. The transcript harness does not
  run in CI at all, by design.

- The benchmark corpus's `backtracking` entry is `pandas, numpy<1.26` rather than
  `pandas, numpy<2`, and **actually backtracks again**. Under the old bound pandas had
  relaxed its floor, so the newest pandas satisfied `numpy<2` on its own: the entry
  pinned pandas 3.0.5, backed out of nothing, and measured an ordinary resolve while
  still being named `backtracking`. It stayed that way across at least one release and
  the cost analysis written on top of it implied coverage the corpus did not have.

  The corrected entry costs 42 `Metadata` and 30 `Versions` calls for 6 pins, against
  13 and 9 for 4 pins. ⚠️ Most of that 3.2x is a **bigger closure**, not backtracking:
  pandas 2.x pulls `pytz` and `tzdata` that pandas 3.x does not. Pinning
  `pandas==2.3.3` with the same numpy bound — identical closure, nothing to back out
  of — costs 26 and 20. So 13 → 26 is the closure and only 26 → 42, about **1.6x**, is
  the repeated asking.

  ⚠️ An entry defined by "the newest version cannot be used" is inherently perishable,
  because the packages it names keep releasing. So `benchEntry.MustNotBeNewest` now
  asserts the property instead of assuming it: if the driver package can be taken at
  its newest version, the benchmark **fails** rather than reporting a comfortable
  number. Verified by putting the stale bound back and watching it fail.

- `pep440set.Set.Contains` probes a set's spans with a stack-held position
  instead of materializing a bound. Building the bound cost a public-spelling
  render, a possible re-parse, and a heap-allocated sort key per membership
  test, and the resolver tests membership once per candidate version per
  `Candidates` call. The public spelling is now derived lazily, only when a
  comparison descends into a release group — cross-group probes, the common
  case, never pay it — and the scan stops at the first span whose floor is
  above the probe, which the old path did not (spans are sorted and
  disjoint, so nothing later can match; below-range and `!=`-hole probes,
  the shapes a backtracking solve produces, stop early).

  Measured on this code (Apple M4 Max, go1.26.4, production snapshot of
  932,861 packages). Micro, `BenchmarkContains`, same-session pairs,
  medians of 5: the cross-group probe — the common case, which never
  renders — 588 → 329 ns/op, 800 → 232 B/op, 16 → 8 allocs/op; the
  same-group probe — the worst case, which pays the render and the full
  ladder — 1612 → 1559 ns/op (flat within noise), 1582 → 1169 B/op.
  End-to-end, `BenchmarkResolveWarm` interleaved A/B ×3 rounds, medians
  of 3, against the corrected `backtracking` corpus entry: warm resolves
  allocate 5–9% fewer bytes on every corpus entry (deterministic), and
  wall-clock is 3–5% faster on the allocation-heavy entries
  (`small-tree`, `extras`, `app-set`) and flat within noise on the rest;
  with the early exit added, the branch won all 21 of 21 within-round
  entry comparisons, but the exit's own end-to-end contribution is below
  this machine's noise floor and is kept for the micro win. Answers are
  unchanged: the fast path is held to the reference path by two agreement
  tests, a 33.9-million-pair differential against
  `version.Specifiers.Check` over 20,000 production packages, and 8.6M
  fuzz executions.

### Fixed

- **Sharing one parsed `version.Version` between goroutines is no longer a data
  race**, which this module inherits from the go-python-packaging v0.6.0 bump above
  rather than fixing itself. Upstream's `Compare` now pads into a fresh slice instead
  of appending into spare capacity a by-value copy shares.

  This matters here because the restriction was **documented on an exported API**:
  `index.PackageMetadata.SupportsPython` told callers "DO NOT SHARE ONE PARSED target
  BETWEEN GOROUTINES … give each goroutine its own `version.Parse`", and that guidance
  is now unnecessary work. That doc and the canonical account in `RSFIndex.Versions`
  are corrected; the historical explanation is kept, since it is still why several
  types memoize version KEYS rather than parsed values.

  Re-verified against both pins with the exact eight-goroutine repro those docs
  specify (target `3.11.0`, constraint `>3.9.1`): v0.5.0 reports `WARNING: DATA RACE`,
  v0.6.0 is clean.

  ⚠️ **No behaviour in this module changed.** Memoizing parsed versions is now
  *available* but is not taken here — that is a performance change owing its own
  measurement, not something to ride along with a dependency bump. The remaining
  internal rationales in `provider`, `index/mock.go` and `resolver/bench_test.go`
  still describe the constraint as current and are swept with that work.

### Added

- `TestEveryReasonIsRecordedWhenNOTHINGIsUsable`, pinning the claim the whole
  shrinking of `Unusable()` rests on: when a package has nothing usable, establishing
  that requires examining all of it, so every reason is still recorded. That is the
  case a failure report needs, it is asserted in three doc comments and a changelog
  entry, and until now it was an argument rather than a test.

- `TestCandidatesAgreeAcrossRepeatedCallsWithDifferentRanges`, the differential for the
  ranked-list memo. The existing `TestCandidatesAgreeWithAnExactCountOnTheRealIndex`
  structurally cannot reach it: it asks each package **once**, always with
  `pep440set.All()`, and a memo only does anything on the second call with a
  **different** allowed set. This asks each package many times with ranges built from
  its own published versions — 109,564 calls over 14,540 production packages — against
  a reference that re-reads the index and sorts from scratch each time.

  It reads through a **counting index** and asserts that the provider made exactly one
  `Versions()` call per distinct package: 14,379 for 14,379, so 95,185 of the 109,564
  calls (86.9%) were served from the memo.

  ⚠️ Its default sample is capped at 20,000 packages, unlike its siblings. Uncapped
  against a full snapshot it swept 680,711 packages and ran 34 minutes without reaching
  an assertion — wedged in its **own fixture builder**, not in the code under test:
  `rangesOver` was unioning per-version singletons, which is quadratic in `cmpBound`.
  Ranges are now built through specifiers (one or two spans regardless of version
  count), the union-built gappy shape is kept only for lists of ≤64, and a bare
  full-snapshot run finishes in well under a minute. A knob whose documented use hangs
  is worse than no knob.

  ⚠️ That assertion replaced a derived one, and the distinction is the point. An earlier
  version reported the memoized-call count as calls-minus-packages — arithmetic over the
  test's own loop shape. With the memo lookup neutered so every call missed, it still
  passed and still printed the same figure, and that figure had been quoted here as a
  measurement. Counting index calls is what distinguishes a memo that is *read* from one
  that is merely *written*.

  ⚠️ It earns its place by mutation, twice over. Poisoning the memo with the
  range-filtered list — the "keyed by caller-supplied input" bug — fails **this test and
  no other in the module**; the pre-existing differential passes, and so does the entire
  resolver suite. Making the memo lookup always miss fails it at 940 reads for 136
  packages, which is the mutation the derived version slept through.

- `candidate` differentials for `Rank`'s fast path against a sort-only reference: 13
  hand-built shapes including the tie cases the reversed branch claims are impossible,
  20,000 random lists, and every version list of 200,000 production packages
  (1,623,115 versions; 103,358 lists take the reversed branch, 42,609 the ordered one,
  none fall through to the sort). Plus `TestNewestIsAStrictWeakOrdering`, which checks
  all four properties on real published version strings.

  ⚠️ Its equivalence half needs **injected** equal spellings, **biased** sampling, and
  classes of at least **three** members to be non-vacuous. `index.RSFIndex.Versions`
  collapses each PEP 440 equality class to one representative, so on real index output
  no two versions are ever equivalent: under the default policy the order is **total**
  and the stability `Rank` provides is vacuous there. Three million uniformly drawn
  triples produced zero equivalent ones.

  And two injected spellings per class is not enough either — with classes of size 2,
  every "mutually equivalent triple" must repeat an element, so the conclusion is
  reflexive or the antecedent merely restated. 749,465 satisfied antecedents contained
  **2** genuine witnesses while the vacuity guard reported 749,465. The guard now counts
  witnesses (three pairwise-distinct spellings) rather than satisfied antecedents, and
  a third spelling per class takes it to 96,989.

- `provider.TestInRangeIsNotContiguous` and `TestRSFIndexVersionsAreAscending`, which
  measure rather than assume the two facts the design rests on. Over the **whole**
  snapshot — 680,711 packages, 7,666,753 versions — 12.14% of versions are pre-releases
  and **4.23% of packages have their admitted set split** by one, so an intersection by
  binary search would need a fallback; and across 481,998 multi-version packages
  `Versions` has 0 adjacent inversions and 0 adjacent PEP 440-equal pairs, so the fast
  path's branch is the one real data takes. ⚠️ The second is a check on the
  **implementation** — `MetadataIndex` promises no ordering, which is why `Rank`
  detects the shape rather than assuming it — and exists so that if `RSFIndex` ever
  stops being sorted, the reason the fast path went quiet is discoverable rather than
  mysterious.

## [0.5.0] - 2026-08-13

### Breaking

- Requires **go-pubgrub v0.2.0**, whose `Provider.Candidates` returns
  `(best, found, rank, err)` instead of `(best, count, err)`. `provider.Provider`
  implements the new signature, so any caller that invoked `Candidates` directly —
  which is the solver's job, not a caller's — must be updated.

- `provider.Provider.Unusable()` and `resolver.ResolutionError.Unusable` return
  **fewer entries**, and their documented meaning changed. They now hold the versions
  the resolution actually examined rather than every published version it could have
  set aside: candidate selection stops at the first usable version, so a version
  ranked below the chosen one is never looked at and never recorded.

  Filed as breaking rather than as a change because it alters what an exported method
  returns for callers who render it, and `ResolutionError` is part of the surface
  Package Manager consumes. ⚠️ Anything presenting this as "every version we set
  aside" is now presenting an incomplete list. The entries that explain a *failure*
  are still all present — a package with nothing usable is examined exhaustively,
  because that is what establishing "nothing" requires.

### Changed

- `provider.Candidates` answers **existence** rather than cardinality. It walks the
  in-range versions in ranked order and stops at the first usable one, instead of
  testing every version in range and reporting an exact count.

  Index calls previously scaled with candidate *versions* rather than with the
  closure. `certifi` has 65 published versions and no dependencies at all, and an
  exact count read every one of them on each of the two rounds the solver asked
  about it — 131 `Metadata` calls. Answering existence takes 3.

  Measured on this code against the production snapshot (932,861 packages), per
  corpus entry, `Metadata` calls before → after:

  | entry | before | after | |
  |---|---:|---:|---:|
  | `single-no-deps` (certifi) | 131 | 3 | 43.7x |
  | `small-tree` (flask) | 290 | 30 | 9.7x |
  | `extras` (flask[async]) | 661 | 43 | 15.4x |
  | `backtracking` (pandas, numpy<2) | 435 | 13 | 33.5x |
  | `app-set` (5 packages) | 4,750 | 105 | 45.2x |
  | `wide-versions` (boto3) | 4,549 | 24 | 189.5x |
  | `unsatisfiable` | 37 | 3 | 12.3x |

  So **9.7x to 189.5x**, and 5.9x to 111x counting all index calls rather than
  metadata reads alone. Pin *counts* are unchanged on all seven entries.

  ⚠️ **In wall-clock terms this is much smaller, and the <1 ms warm target did not
  move.** Measured in one session on one machine, warm, before and after:

  | entry | warm before | warm after | |
  |---|---:|---:|---:|
  | `single-no-deps` | 1.79 ms | 1.74 ms | 1.03x |
  | `small-tree` | 6.47 ms | 4.54 ms | 1.43x |
  | `extras` | 13.39 ms | 10.03 ms | 1.33x |
  | `backtracking` | 9.85 ms | 7.00 ms | 1.41x |
  | `app-set` | 257.52 ms | 70.76 ms | 3.64x |
  | `wide-versions` | 181.02 ms | 82.18 ms | 2.20x |
  | `unsatisfiable` | 0.88 ms | 0.80 ms | 1.09x |

  The warm gate is met by exactly one entry, the same one that met it before. So the
  index call count was **not** the binding constraint on that target — the remaining
  cost is walking and intersecting version lists (set algebra and version
  comparison), not reading metadata. `candvers` is unchanged, which is where that
  shows.

  ⚠️ **Backtracking is unmeasured.** The `backtracking` corpus entry pins pandas
  3.0.5 — the newest published pandas — so nothing is backed out of and it is an
  ordinary resolve. No claim about this change's effect on backtracking cost is
  supported by these numbers.

  `rank`, which only orders which package the solver works on next, is the count of
  versions in range taken *before* usability is tested. That is free, since the list
  has to be built to walk it, and go-pubgrub documents `rank` as a hint it only ever
  compares. ⚠️ It may therefore over-count the usable versions, deliberately.

  ⚠️ Only a **negative** answer still walks everything, and that is irreducible:
  proving nothing in range is usable means checking all of it. What this removes is
  paying that walk on every package instead of only where the answer really is
  "nothing".

  ⚠️ Ranking happens **before** the usability walk, which is what makes the chosen
  version identical to what filter-then-rank produced. `candidate.Rank` is a stable
  sort over a pairwise `Less`, so it orders a subset consistently with its superset.
  Reversing the two steps moves the answer silently, and the differential in
  `provider/differential_test.go` is what holds it down: against real published
  metadata it compares `found`, `best` and `rank` with an exact-count reference that
  calls the *same* usability function, so the two cannot drift.

- ⚠️ An index failure on a **lower-ranked** version is no longer always seen. `usable`
  returns an error rather than `false` when the index cannot answer, and that error
  aborts the resolve deliberately — an outage must not be reported as "no such
  version". That is unchanged for every version the walk reaches, but the walk stops
  at the first usable version, so a broken *older* release is not examined unless
  backtracking narrows the range to it.

  So a resolve can now succeed against an index that is broken for one old version
  where it previously aborted, and whether such a failure surfaces became
  path-dependent. This is not a weakening of the rule the error path exists for —
  nothing is reported as unavailable on the strength of an outage; it is simply not
  looked at — and it is arguably better, since an unreadable release nobody would
  have chosen is a poor reason to fail. But it is a real change and it is not
  something the differential can police, because the short-circuit walk examines
  strictly fewer versions than an exhaustive one; that test compares errors in one
  direction only, and says so.

- `candidate.Policy.Less` must now be documented-and-actually **transitive**. It was
  already required to be a "strict weak ordering" but only irreflexivity and
  asymmetry were spelled out. Transitivity is what makes ranking the in-range
  versions and stopping at the first usable one pick the same version as ranking the
  usable ones alone; without it, which version is chosen starts to depend on which
  others happened to be in range. No existing `Policy` in this module is affected —
  `Newest` compares versions — but `Policy` is an interface embedders implement.

### Added

- `pep440set`, a canonical PEP 440 version-set algebra (intersection, union,
  complement) satisfying go-pubgrub's `versionset.Set`. `FromSpecifiers` maps a
  specifier set to spans, operator-level pre- and post-release guards included,
  and is held to `version.Specifiers.Check` by **differential testing rather
  than by construction**: a generated operator/operand/version grid, a walk over
  a production PyPI snapshot, and a fuzzer over arbitrary specifier text. All
  three currently agree everywhere they look.

  Agreement on input none of them has looked at is **not** claimed. The grid
  contained only canonical operand spellings and only release segments that fit
  in an `int64`, and both gaps hid real mapping bugs — the alias spellings of
  `~=` (`~=1.0c1`, `~=1.0.pre1`, `~=1.0.r1`, `~=0.0.posT`, …), where the prefix
  is derived from the raw operand text and the seven spellings are **not**
  interchangeable, and release segments at or above 2<sup>63</sup>, which the
  ordering key silently truncated. Both are fixed and both are now in the grids.

  Arbitrary-equality (`===`) specifiers report `ErrUnrepresentable` rather than
  being approximated, as does the `||` OR-of-ANDs form, whose groups no exported
  accessor exposes. `Equal` and `IsEmpty` are exact on **positions**, not on
  versions: two sets admitting exactly the same versions can compare unequal
  when they differ only across a gap that no version occupies.
  ([#18657](https://github.com/rstudio/package-manager/issues/18657))

- `candidate`, the policy layer the resolver consults when choosing which
  version of a package to try next: a `Policy` ranking interface with a
  newest-first default, and pre-release admission derived once from the
  requirements a resolution starts with. Ranking cannot remove a version, so a
  caller's preference can never make a version look nonexistent to the solver.
  ([#18657](https://github.com/rstudio/package-manager/issues/18657))

- `provider`, the translation layer between Python packaging semantics and
  go-pubgrub's generic solver. It answers the solver's two questions -- which
  versions of a package exist within a range, and what one version requires --
  by reading a `MetadataIndex`, evaluating PEP 508 markers against one concrete
  target environment, and converting version specifiers through `pep440set`.

  Extras are modeled as virtual packages: `flask[async]` is a distinct solver
  node that depends on `flask` at exactly the same version plus whatever the
  extra adds, so a solver with no notion of extras resolves them correctly and
  the two cannot drift apart. An extra no version declares has no candidates at
  all rather than resolving happily and installing nothing.

  The interpreter is modeled as a package too, rather than as a filter applied
  behind the solver's back. A `Requires-Python` mismatch therefore appears in
  the derivation graph as an ordinary version conflict that names `python`,
  instead of a version silently vanishing and the report saying a package has
  no versions.

  A version that cannot be used -- an sdist-only release with no published
  metadata, a specifier with no version-set equivalent, a direct-reference URL
  requirement -- is excluded from the candidate count and the reason is recorded
  for the eventual failure report. An index that cannot ANSWER is a different
  thing and propagates as an error: a transport failure read as "no such
  version" would let a resolution quietly settle on an older release, or blame
  the user's constraints for an outage.
  ([#18657](https://github.com/rstudio/package-manager/issues/18657))

- `resolver`, the public entry point, which completes the adapter. A consumer
  can now hand `Resolve` a list of PEP 508 requirements, an `index.MetadataIndex`
  and a target environment, and get back one pinned version per package the
  requirements transitively reach — extras included, markers evaluated, and
  transitive multi-version conflicts backtracked rather than hard-failed.

  Virtual extra packages collapse into their base project, so a `Resolution`
  reads as `flask 3.0` with `[async]` noted alongside rather than as two
  entries; the interpreter and the synthetic root do not appear at all. `Order`
  is the solver's decision order and every extras list is sorted, so two runs
  over the same inputs produce byte-identical output.

  A conflict comes back as a `*ResolutionError` whose `Report` explains the
  chain of reasoning step by step, rendered in Python terms — `flask[async]`,
  `>=1.0,<2.0`, and `Python 3.11.4` for the interpreter. An sdist-only release
  relevant to the failure gets a note of its own, because a report that says
  "no version of flask matches >=3.0" about a release visible on PyPI is true
  in a way nobody can act on. An index that could not answer is passed through
  as itself rather than dressed up as a conflict between requirements.

  **Not included, deliberately:** no distribution-file or wheel selection —
  a `Resolution` is versions only, and the PyPI RSF carries no file records to
  select between; one concrete marker environment per resolution, not universal
  resolution; and no sdist building to discover metadata.
  ([#18657](https://github.com/rstudio/package-manager/issues/18657))

- `pep440set.Set.String`, which renders a version set as PEP 440 specifier text
  (`>=1.0,<2.0`, `==1.*`, `!=1.0`). Without it nothing outside the package could
  describe a set at all — spans and bounds are unexported — and go-pubgrub's
  failure report fell back to `%v` on the raw struct.

  A rendering that **is** a specifier is **version-exact**: parse it back and it
  holds the same versions. That is not free, because a bound is a position and a
  position is finer than a specifier — the complement of `<=1.0` starts above
  1.0's local variants and therefore holds `1.0.post1`, which `>1.0` does not
  match — so those bounds are rendered by naming the least version above them
  (`>=1.0.post0.dev0`). The few positions PEP 440 has no operator for at all are
  rendered with a bracketed marker (`<=1.0[+post]`, `<1.0[+pre]`) naming the
  region the bare specifier would misstate, so **no two sets holding different
  versions ever render the same text**. Measured over 1,500 production packages:
  54,120 renderings, 0 disagreements.
  ([#18657](https://github.com/rstudio/package-manager/issues/18657))

- `provider.ReasonMetadataUnavailable`, the `Unusable.Reason` recorded for an
  sdist-only or dynamic-metadata release, so a consumer can pick those records
  out by name rather than by matching a sentence.
  ([#18657](https://github.com/rstudio/package-manager/issues/18657))

- A cold/warm resolution benchmark over a committed corpus
  (`resolver/bench_test.go`, `resolver/bench_corpus_test.go`), the RFD 0001
  Phase 3 exit gate. Test-only: no library code changed, and a benchmark never
  runs under `go test ./...`, so the 981 MB production snapshot is never
  required. It defaults to the committed excerpt and reads the same
  `PYPIRSF_TEST_FILE` as the other real-corpus tests.

  It counts `MetadataIndex` calls through a decorator as well as timing the
  resolution, because the call count is the part that does not depend on the
  machine. **The gate is not met.** Against the production snapshot: cold under
  100 ms for five of seven corpus entries and 5-7x over for the other two; warm
  under 1 ms for none of them, and warm is within 3% of cold everywhere, because
  the only cache in the path holds raw strings and every `Metadata` call
  re-parses them. Full table, profile and verdict are in the package
  documentation of `resolver/bench_test.go`.
  ([#18651](https://github.com/rstudio/package-manager/issues/18651))

### Fixed

- `index.RSFIndex` re-parsed everything it had already parsed. Its only cache
  held the RAW record -- `pypirsf.VersionDeps` is unparsed strings throughout --
  so a warm index skipped the file seek, the zstd decode and the varint
  unmarshal, and none of those was where the time went. `Metadata` re-parsed its
  PEP 508 requirements on every call and `Versions` re-parsed and re-sorted the
  package's whole version list on every call, which is why a warm resolution
  measured within 3% of a cold one on all seven corpus entries.

  Two memos now sit above the blob cache: a parsed `PackageMetadata` per
  (package, **stored version key**), and the sorted, deduped version ORDER per
  package. Measured on the Phase 3 corpus against a 932,861-package production
  snapshot, **warm resolution is 2.3x to 4.3x faster, and makes 2.3x to 3.8x
  fewer allocations** (`app-set` 678 to 261 ms, `wide-versions` 532 to 183 ms,
  `backtracking` 44.4 to 10.2 ms). Cold improves nearly as much -- 1.1x to
  2.3x -- because a backtracking resolution asks about the same version many
  times within one resolve. `index.Metadata` falls from 47.3% of resolution CPU
  to 4.0%, and `requirement.Parse` leaves the profile entirely. Retained heap
  attributable to one resolve rises from 0.40 MB to 2.54 MB (`app-set`) against
  a ~64 MB post-open baseline: allocation churn and retained heap move in
  opposite directions here, so both are reported.

  Index call counts are unchanged, deliberately: `Provider.Candidates` returns an
  exact count, so it establishes usability by doing each in-range version's full
  dependency work. This makes each call cheaper and removes no call. (Zero-exactness
  is not what forces that walk — only the *zero* answer needs it. It is the exact
  magnitude, used solely by go-pubgrub's package-choice heuristic, that requires it
  in the common case.)

  **Both memos are bounded by the corpus, not by what callers ask for.** The
  metadata memo is keyed by the stored version key the request resolves to, so
  its key set is a subset of the keys the blob cache already holds. Keying it by
  the request's own rendering -- which is what "keyed by (package, version)"
  would naturally mean -- is unbounded: a version can be spelled PEP 440-equal
  to a stored one in unlimited ways, and a version that does not exist can be
  requested endlessly, so each distinct spelling would mint a permanent entry.
  A "not captured" outcome is therefore not memoized at all; `Metadata` resolves
  it with a binary search over the version order instead, in `O(log n)` parses
  rather than the `O(n)` scan that made caching it look worthwhile. This is
  immaterial for a CLI, which builds an index per resolve, and material for a
  long-lived server accepting arbitrary requests.

  Nothing REQUEST-SCOPED is memoized under that shared key, which the error path
  has to respect too: `ErrMetadataUnusable` is memoized as the facts (which
  requirement string, and why it would not parse) and re-rendered per call
  against the version the caller actually asked for. Memoizing the finished
  message would tell a caller asking about `1.0` that its request for `1.0.0`
  had failed, since both spellings share one entry.

  ⚠️ The version memo holds KEYS and re-parses them rather than holding parsed
  values, because **a `version.Version` cannot be shared between goroutines**.
  `Version.Compare` pads the shorter operand's release segment with `append`,
  and `cmpkey` builds that segment by reslicing away trailing zeros, so "3.0.0"
  carries a `Parts` of len 1 and cap 3 and padding it back writes into spare
  capacity in a backing array that a by-value copy shares. Eight concurrent
  `Resolve` calls against one shared index now assert this under `-race`
  (`resolver/concurrency_test.go`), over fixture versions chosen to end in ".0"
  because versions without a trailing zero cannot expose it. The defect is
  upstream, in `rstudio/go-version` v0.0.2 as reached through
  `go-python-packaging` v0.5.0, and cannot be worked around here because
  `key.release` is unexported.
  ([#18651](https://github.com/rstudio/package-manager/issues/18651))

- `index.MockIndex` handed every caller a copy of one stored `version.Version`
  from `Versions`, and one stored `PackageMetadata.Version` from `Metadata`. It
  documents itself as safe for concurrent use, and it was not: under the hazard
  above, eight concurrent resolutions against one shared `MockIndex` fail
  `go test -race` inside `candidate.Rank`. It now stores normalized version
  strings and re-parses per call, as `RSFIndex` does, and takes `Metadata`'s
  `Version` from the caller's own value.
  ([#18651](https://github.com/rstudio/package-manager/issues/18651))

- `index.Metadata` copied `RequiresDist` and `ProvidesExtra` but not
  `Requirement.Extras`, which is an exported `[]string` reachable THROUGH the
  copied `RequiresDist`. A caller assigning to it corrupted the metadata memo
  permanently, for every later caller: a second lookup of `apache-airflow` 3.3.0
  came back with `apache-airflow-core[CLOBBERED]` where the record says
  `[all]`, with nothing at the mutation site to suggest it. `Extras` is the only
  such slice below the copy -- a requirement's specifiers and marker are
  unexported all the way down -- so the copy policy is now complete rather than
  narrower. `index.MockIndex` had the identical gap and has the identical fix;
  the point of the mock copying at all is that a mock-backed test catches a
  mutating caller first, and it caught only what it copied.
  ([#18651](https://github.com/rstudio/package-manager/issues/18651))

- `PackageMetadata.SupportsPython` now documents that a parsed `target` must not
  be shared between goroutines. Parsing one interpreter version and fanning work
  out across goroutines is the natural way to use it and is a data race for
  targets carrying a trailing zero against `<` or `>` constraints ("3.11.0" with
  `>3.9.1` or `<3.12.1`); `>=`, `<=`, `==` and `!=` re-parse through `Public()`
  first and are immune, as is a target like "3.11". Same upstream cause, being
  filed upstream. Documentation only -- no behaviour change.
  ([#18651](https://github.com/rstudio/package-manager/issues/18651))

- `pep440set` derived a bound's sort key on every comparison rather than once
  per bound. `cmpBound` sits in the innermost loop of the set algebra, which is
  itself in the solver's hot loop, and each call rendered both versions' release
  segments to a string and split it, then rendered and **re-parsed** both public
  versions. One resolution against an index shaped like a curated or air-gapped
  repository -- packages present, transitive dependencies absent -- took 17.5 s
  and allocated 25 GB *cumulatively*: bytes handed out and collected over the
  run, not memory held. **The symptom is latency, not memory exhaustion.** Peak
  heap through that same resolution was measured at 115 MB of `HeapSys` before
  this change and 15 MB after, three orders of magnitude below the cumulative
  figure; what the churn buys is garbage collection, and the resolution is
  GC-bound for tens of seconds. The failing, unsatisfiable path is the expensive
  one, so a request that could not be satisfied was the slowest one to be told
  so.

  The key is now derived once, when the bound is built. The span slices the
  algebra allocates are sized up front instead of grown -- and allocated only
  once there is a span to put in them, so an operation whose result is empty
  allocates nothing at all. Same resolution, same index calls, same outcome:
  **17.5 s to 2.1 s, 25.1 GB to 3.7 GB allocated, and 568 million allocations
  to 14.8 million**. Building a set of specifiers costs slightly more than it
  did (`>=1.0`: 1.55 to 1.91 µs) because a bound now pays for its key up front;
  comparing sets costs far less (`Equal`: 10.1 µs and 320 allocations to
  0.16 µs and none).

  No ordering changed. `TestBoundKeyAgreesWithLiteral` holds the derived-once
  and derived-on-demand paths to the same answer on every pair of a widened
  ordering grid, whose PEP 440 orderings were cross-checked against
  pypa/packaging 26.2. Against the full production snapshot,
  `TestDifferentialAgainstRealCorpus` still agrees with
  `version.Specifiers.Check` on every pair it compares: **755,934 (specifier,
  version) pairs over 7,188 specifier sets** in the 400-package sample it takes
  by default, and 33,918,235 pairs over 305,548 specifier sets when widened to
  20,000 packages with `PEP440SET_CORPUS_PACKAGES`. Zero disagreements either
  way.
  ([#19713](https://github.com/rstudio/package-manager/issues/19713))

## [0.4.0] - 2026-08-10

### Breaking

- `FilteredIndex` carrying a **file-level policy** (`ExcludeYanked` or `SnapshotDate`) over an
  index that serves **no files** now **refuses** instead of answering: `Versions`, `Metadata`
  and `Files` all report `ErrFilesUnavailable`, wrapped with the advice to compose over a
  file-serving source.

  ⚠️ **Anyone on `v0.3.0` should upgrade.** There, such a composition returned an **empty
  version list with a nil error** for every package — so every package looked like it had no
  acceptable version, which downstream reads as a constraint conflict that does not exist.
  That is indistinguishable from a real resolution failure, which makes it strictly worse
  than an error. `RSFIndex` serves no files *by design* and no index ever will, so this is the
  default outcome of the most obvious wiring, not an exotic edge case.

  How `v0.3.0` came to be wrong is worth recording, because it argues against changing it
  back. The refusal existed, and was removed because `ErrFilesUnavailable` could not be
  trusted: `MultiIndex` emitted it for a mere data condition, so a legitimate partial mirror
  hard-errored with advice to compose the very thing it had composed. The fix for *that*
  — reserving the sentinel for a genuine capability statement, emitted only when every source
  is fileless — landed in the **same release**. The two corrections passed each other: one
  made the signal trustworthy while the other stopped trusting it.

  A partial mirror still stays silent and drops the affected versions, because that case now
  arrives as `ErrMetadataUnavailable`. The distinction is capability versus data, and it holds
  at every layer: a `FilteredIndex` over a fileless inner can never serve a file, so its own
  `ErrFilesUnavailable` is true about itself, and a `MultiIndex` above it demotes that in turn.

  The refusal is **total where it matters**. It cannot fire when there are no versions to
  check, or when the version-level axis already excluded them all — but in those cases the
  file axes could not have changed the answer, so an empty result is correct over any index.
  A test pins that by asserting a fileless and a file-serving index with identical version
  content give identical answers in exactly those cases.

## [0.3.0] - 2026-08-10

### Changed

- Requires `go-python-packaging` **v0.5.0** (from v0.4.0). ⚠️ **Anyone on `v0.2.0` of this
  module should upgrade**, because `v0.4.0` shipped a wildcard/`~=` padding defect that this
  module inherited: the zero-padding loops appended `⌈gap/2⌉` zeros instead of `gap`, so
  `!=7.0.0.*` matched `7` where `pypa/packaging` excludes it, and `==0.0.0.*` matched
  `1!1.0`. Measured over an 82-row oracle sweeping version-segment gaps of 1-4 in both
  directions, `v0.4.0` diverged from the reference on **30 of 82** cases and `v0.5.0` on
  **0**. It survived undetected because `⌈1/2⌉ == 1`, making a one-segment gap correct by
  accident — and every test written for it used a one-segment gap.

  `go build` and `go test ./...` are clean against `v0.5.0`; the fix changes no behavior this
  module's own suite depended on.

### Added

- `index.FilteredIndex` and `index.FilterPolicy` — a `MetadataIndex` wrapper applying
  release-admission policy: pre-release exclusion, PEP 592 yanked-file exclusion, and an
  inclusive snapshot-date cutoff. The zero `FilterPolicy` filters nothing, which is why the
  fields are spelled `Exclude*`: a wrapper that silently dropped every pre-release would
  punish whoever wrapped an index only to compose it.

  The policy is enforced on **all three** methods, not only `Versions`. Filtering the listing
  alone would be bypassed by any caller holding a version from elsewhere — a pin, a lockfile,
  another index — so it would be a default rather than a policy.

  ⚠️ Only pre-release exclusion is decidable from a version alone. Yanking is per-file per
  PEP 592 and an upload time belongs to a file, so those two axes are evaluated through
  `Files`. A file-level policy over an index that serves no files therefore admits
  **nothing**, and it says so with empty version lists rather than by failing — every package
  looks like it has no acceptable version. This is **not** guarded, because it cannot be:
  "no file evidence" is the same observation whether the operator wired up a fileless index
  or the package is simply absent from the file source, and the second is a supported
  configuration (a partial mirror). Since `RSFIndex` serves no files by the shape of the
  data, the arrangement that works is a `FilteredIndex` over a `MultiIndex` pairing the RSF
  with a file-serving source, and **verifying that is the caller's job**.

  ⚠️ The two file axes fail in opposite directions, and not in the safe one. `SnapshotDate`
  fails **closed**: a file whose `UploadTime` is the zero value is dropped, because an
  unrecorded time cannot be shown to precede the cutoff, and admitting it would let a file
  published yesterday into a snapshot dated last year. `ExcludeYanked` fails **open**:
  `DistFile.Yanked` cannot express "not captured", so a source that does not record PEP 592
  data reports every file as un-yanked and the policy admits everything while appearing to
  work. Only set `ExcludeYanked` over a source whose files genuinely carry PEP 592 data. The
  gap is fixable additively later (a `YankedKnown` field on `DistFile`), so it is not locked
  in by this release.

  Under an active file-level policy, `Versions` issues one `Files` lookup per surviving
  version, serially. Cheap in-process; a 500-version package over a network-backed source is
  500 sequential round trips. Accepted for now because it is fixable additively — an optional
  batch interface adds nothing to existing implementations — and because caching belongs in
  the index being wrapped.

  Under an active file-level policy, a version with **no admissible file** is refused with
  `ErrMetadataUnavailable` by all three methods — including a version that had zero files to
  begin with, since there is then nothing to admit it on. The interface's "known version with
  no files is empty plus nil" answer still applies whenever no file-level policy is active.

- `index.MultiIndex` — a `MetadataIndex` over ordered sources. `Versions` returns the
  **union**; `Metadata` and `Files` return the first source that can answer, with
  `PackageMetadata.Origin` naming which one did. The asymmetry is the design: which versions
  exist is naturally a union, but two sources can disagree about one release, and merging
  their answers would produce a record no publisher ever made.

  Error taxonomy across sources, since a source's `ErrPackageNotFound` is that source's
  answer about itself and not a fact about the composed index:

  - `Versions` reports `ErrPackageNotFound` only when **no** source knows the name. One
    source knowing it and carrying no versions is an empty slice and a nil error.
  - `Metadata` prefers, in order of how much was learned, a malformed record somewhere
    (`ErrMetadataUnusable`) over no record anywhere (`ErrMetadataUnavailable`) over nobody
    having heard of the package (`ErrPackageNotFound`).
  - `Files` treats an **empty list with a nil error as an answer**, not a miss — a release
    can have every file deleted, and "keep looking" would let a stale mirror resurrect files
    the authoritative source removed. `ErrFilesUnavailable` does mean "ask another source",
    and it is returned only when **every** source is fileless: a fileless source emits it for
    every lookup without inspecting the package, so it is the weakest evidence available, not
    the strongest. Letting it win would make `ErrPackageNotFound` unreachable whenever an RSF
    is in the composition, leaving a caller unable to tell a typo'd name from "nobody serves
    files".
  - Each method tolerates sentinels the interface does not list for it, since a source may
    itself be a `FilteredIndex`. `Versions` skips `ErrFilesUnavailable`; `Metadata` treats it
    as "this source supplied no metadata"; `Files` tolerates all four. One source's choice of
    error does not abort the whole lookup.

  ⚠️ Source A knowing the package but not the version, while source B knows neither, is
  `ErrMetadataUnavailable` — **not** `ErrPackageNotFound`. The package was found; a caller
  branching on not-found there reports a missing package for a present one.

  Versions are deduped by PEP 440 equality across sources, collapsing a class to the
  **earliest** source's spelling so the representative resolves to the record `MultiIndex`
  treated as authoritative. A cross-source spelling difference is not bridged when the
  earliest source can list a version but not supply its metadata; that limitation is
  documented on the type and pinned by a test.

  ⚠️ Under a file-level `FilteredIndex` that limitation is larger than a metadata miss: the
  file lookup takes the same string-keyed path, so a version whose file evidence lives in
  another source under another spelling is **dropped from the version list**, and the package
  can appear to have no usable versions at all.

- The tests that read **producer output** now run on every pull request, against a committed
  ~988 KB excerpt of a real production snapshot at `index/testdata/pypi-trimmed.rsf`.
  `TestRSFIndexAgainstRealFile` was gated behind `PYPIRSF_TEST_FILE`, which CI does not set,
  so the only tests reading bytes this module did not write never ran — the suite was green
  and the property was unchecked. A missing fixture now **fails** rather than skipping, and
  CI fails if any of these tests skips: a silent skip is the defect, not the fallback.
  `index/testdata/README.md` records how the excerpt was derived and how to regenerate it.
- Assertions on the shapes the walk does not reach. Wiring the real-data test into CI closed
  half the gap: it passes on a real snapshot because its root is `flask`, whose closure is
  well-behaved, and the review measured 507 roots for which it would have failed. The
  excerpt therefore also carries seven packages chosen for the state they carry — every
  version key unparseable (`holygrail`), a `Requires-Dist` entry PEP 508 rejects
  (`aad-token-verify`), PEP 440-equal keys with contradictory dependencies
  (`database-connector`, `guessproj`), a direct-URL requirement (`memery`), an unreadable
  `Requires-Python` (`admobilize-malos`), and an ambiguous spelling resolved by tiebreak
  (`anpy`) — each with its own assertion.
- Tests pinning behaviours that no test constrained, found by re-running a mutation pass over
  this module (rstudio/package-manager#19466). Thirteen mutations that the suite did not
  notice now fail a test, each verified by applying the mutation, watching the test go red,
  and reverting: the choice of representative within a PEP 440 equality class, the ordering of
  `UnparseableVersionKeys`, deduplication of a repeated direct-URL requirement, `walk`'s exit
  code for an uncaptured root, `deps` emitting `requires_python_raw` only when the constraint
  is unreadable, JSON output leaving `<` and `>` unescaped, a bool flag not consuming the
  token after it, `exitCodeFor`'s fallback, the guidance on an RSF that predates dependency
  capture, and four decoder refusals on malformed input — two of which panicked rather than
  returning an error once the bounds check was loosened.

  Mutations that no test can distinguish are recorded as such in
  `index/pinned_mutations_test.go` and `cmd/pyresolve/pinned_mutations_test.go`, with the
  reason, rather than being covered by a test that only appears to pin them. One pair is
  mutually masking: reversing `Versions`' internal ordering and dropping `versions`' own sort
  are each invisible alone and caught together.

## [0.2.0] - 2026-08-10

### Breaking

- `MockIndex.Metadata` and `MockIndex.Files` return `ErrMetadataUnavailable`, not
  `ErrPackageNotFound`, for an unknown **version** of a **known** package. `ErrPackageNotFound`
  is untrue on its face there — the package *was* found — so a consumer branching on it
  reported a missing package for a present one.

  This aligns the mock with `RSFIndex`, which already answered `ErrMetadataUnavailable`.
  Listed as Breaking because a test written against the mock's old answer will now fail; the
  fix is to expect `ErrMetadataUnavailable`, which is what a real index returns.
- `pyresolve`'s `--` now works. It never did, in any position: `reorderArgs` consumed the
  terminator without re-emitting it, so `flag.Parse` — which does its own scan over the
  reordered arguments, and for which `--` is the only stop token — never saw it. A package
  named `--json` was parsed as the `--json` flag, and the command then exited **1**
  ("usage or file error") reporting `expected exactly one package name argument, got []`,
  blaming the caller for a name it had silently eaten. Listed as Breaking because arguments
  after `--` are now taken literally, which is the documented behavior but not the previous
  one.

### Added

- `pyresolve walk` reports the version it selected for each package. The text output shows
  it beside the name, and `--json` gains `selected_versions`. The walk takes one version of
  each package and its shape depends on which, so previously a reader could not tell which
  version produced any edge, and two walks over different snapshots printed identically
  while describing different graphs.

  `selected_versions` is partial by design: a package appears only if a version was
  selected for it, so names under `absent` and `no_dependency_data` are missing. A missing
  entry means no version was chosen, not that the version is unknown, and the category
  lists say why. Names under `unusable_metadata` **do** appear, since a version was
  selected and its metadata then failed to parse. Added as a map rather than by changing
  `packages` into objects, which would break existing JSON consumers.

  ⚠️ Version selection now happens before the depth cutoff rather than after, so packages
  *at* the cutoff report a version too. With `--depth 1` almost none of them did otherwise.
  One behavior change follows: a package at the cutoff with no selectable version is now
  reported under `no_dependency_data`, where before the cutoff returned first and it was
  reported as plainly reachable. Having no selectable version is a fact about the package,
  not about how deep the walk went.
- `PackageMetadata.RequiresPythonRaw` and `PackageMetadata.RequiresPythonUnreadable`
  preserve an interpreter constraint that could not be parsed. `RequiresPython` alone
  cannot distinguish "the record declared nothing" from "the record declared something we
  could not read" — both leave it empty — so a caller had no way to report the difference.
  The decoder still treats an unreadable constraint as unconstrained, which is deliberate
  and matches pip; what is new is that the decision is now visible rather than silent.

### Fixed

- The `MetadataIndex` contract documented an answer no implementation gave. It promised
  `ErrPackageNotFound` "if pkg **or ver** is unknown", while `RSFIndex` returned
  `ErrMetadataUnavailable` for an unknown version and `MockIndex` returned
  `ErrPackageNotFound` — three sources, three answers for one state.

  The contract now says `ErrPackageNotFound` only when the *package* is absent, and
  `ErrMetadataUnavailable` when the package is present but the index cannot supply metadata
  for that version, whether the version is unknown to it or known with nothing captured.

  ⚠️ Those two cases share one error **deliberately**: `RSFIndex` derives its version list
  from the RSF's dependency map, so a version absent from that map is indistinguishable from
  one present with nothing captured. An interface must not promise a distinction its
  implementations cannot make. The RSF does carry a separate per-snapshot version list, so
  the distinction is recoverable in principle, but `pypirsf` does not expose it today and it
  is therefore left unpromised rather than half-supported.
- `pyresolve deps` distinguishes an unreadable `Requires-Python` from an absent one. It
  printed `(unconstrained)` for both, asserting the publisher declared no interpreter
  constraint when the publisher declared one this tool could not read, and hiding that the
  version was being admitted for every interpreter by fallback rather than by declaration.
  536 packages in a production PyPI snapshot are in that state. The unreadable case now
  quotes the raw string, and `--json` gains `requires_python_raw` and
  `requires_python_unreadable` — previously the two states were byte-identical in JSON,
  since `requires_python` carries `omitempty`.
- `pyresolve walk` no longer counts names that are absent from the RSF as reachable
  packages. A missing name appeared in **both** `packages` and `absent`, which are
  contradictory claims, and `count` included it, inflating "N package(s) reachable" with
  names that cannot be installed from the file. `packages` and `absent` are now disjoint.

  Names under `no_dependency_data` and `unusable_metadata` **remain** reachable: those
  packages do have records, and the walk simply could not expand them. The finding this
  addresses grouped absent and uncaptured names together; they are different facts, and
  only the first was wrong.
- `RSFIndex.Metadata` and `RSFIndex.Files` reject an uninitialized (zero-value)
  `version.Version` with an explicit error naming it as a caller bug, rather than reporting
  `ErrMetadataUnavailable` — which blames the RSF for having no such version when the real
  problem is the argument. Deliberately not a new sentinel: there is nothing to branch on,
  only code to fix.

  This also closed a crash. Before `go-python-packaging` v0.3.1, `Version.String()` panicked
  on a zero value, so `Metadata` died on `decoded[ver.String()]`. `Files` did *not*, because
  `fmt` recovers a panic raised inside a `String` method and substitutes
  `%!s(PANIC=...)` — so it returned a garbled but plausible-looking error. The bump alone
  would have converted the crash into a silent wrong answer; the guard is what keeps it
  diagnosable.

### Changed

- Requires `go-python-packaging` **v0.4.0** (from v0.3.0), for the zero-value `Version`
  fixes above and for its comma requirement. That requirement is inherited, so a
  requirement string whose version constraints are not comma-separated (`>=1.0<2.0`) is now
  rejected here too. Over the production PyPI snapshot upstream measured 21 such strings
  rejected out of 2,804,136, and 0 newly accepted; each one previously parsed into a
  constraint boundary the input never contained.
- Requires `klauspost/compress` **v1.19.2** (from v1.19.1). Worth noting because a consumer
  adding this module raises its own pin through minimal version selection.
- Corrected a stale warning on `PackageMetadata.RequiresPython`. It said `Specifiers.Check`
  returns false for every version when the set holds no groups, inverting the zero value's
  meaning. That was true when written and stopped being true in `go-python-packaging`
  v0.3.0, which made an empty specifier set admit every version. Verified against v0.3.1
  rather than assumed. `SupportsPython` is still the preferred spelling, for clarity and for
  older builds, but not for the reason the comment gave.
- Documented why `walk`'s `ErrMetadataUnavailable` branch is unreachable and kept. It is
  unreachable only for `RSFIndex`, whose `Versions`/`Metadata` agreement is a tested
  invariant; the `MetadataIndex` contract permits that error for a known sdist-only release
  and `MockIndex` already returns it, so deleting the branch would trade dead code for a
  latent gap that reappears the moment the command is generalized to the interface.

## [0.1.0] - 2026-08-07

First release. **Read what is not in it before depending on it.**

### Added

- `pypirsf/` — the PyPI record layout and dependency-blob decoder for Repository
  Snapshot Format files. Reads a local file; makes no network request.
- `index/` — `MetadataIndex`, the seam between a resolver and its storage, with two
  implementations: `RSFIndex` over a local RSF file, and `MockIndex` for tests.
- `cmd/pyresolve` — a CLI that inspects an RSF file: `stats`, `versions`, `deps`,
  and `walk`. It reads the file named by `--rsf` and nothing else.

`index/` distinguishes four states a caller must not conflate, each with its own
sentinel: `ErrPackageNotFound` (no such package), `ErrMetadataUnavailable`
(present, nothing captured), `ErrMetadataUnusable` (captured, does not conform to
the spec), and `ErrFilesUnavailable` (ask a different source). The CLI keeps them
apart at the process boundary too: exit `0` success, `1` usage or file error,
`2` package not found, `3` metadata present but not conforming.

### Not in this release

`candidate/`, `provider/`, and `resolver/` are **documentation-only stubs with no
declarations**. Importing them compiles and gives you nothing. Version solving is
not implemented here yet; that work is RFD 0001's later phases. What `0.1.0`
delivers is metadata *retrieval* — enough for a consumer that needs an index over
an RSF file, which is what
[rstudio/package-manager#19437](https://github.com/rstudio/package-manager/issues/19437)
needs.

### Notes

- Requires `go-python-packaging` **v0.3.0 or later**. Earlier tags accept two
  version specifiers written adjacently and re-render them with a comma the input
  never contained, so `cryptography (>=3.3.2<4)` silently became
  `cryptography>=3.3.2,<4`. Under v0.3.0 that requirement is rejected and the
  version is reported `ErrMetadataUnusable` instead of resolving against a
  fabricated constraint. Measured over a production PyPI snapshot (932,861
  packages, 7,666,849 versions, 76,741,586 requirement strings), that moves 3,820
  versions from usable to unusable and takes 80 more packages to no usable version
  at all — 413 in total. **Failing loudly there is the intended behavior**: an
  invented constraint boundary changes which versions resolve, and the publisher's
  intent is not recoverable from the malformed string.
- Module floor is Go 1.25.
- Dual-licensed Apache-2.0 OR MIT. See `LICENSE-APACHE`, `LICENSE-MIT`, and
  `NOTICE` for attribution of adapted material.

[Unreleased]: https://github.com/posit-dev/go-pyresolver/compare/v0.7.0...HEAD
[0.7.0]: https://github.com/posit-dev/go-pyresolver/compare/v0.6.0...v0.7.0
[0.6.0]: https://github.com/posit-dev/go-pyresolver/compare/v0.5.0...v0.6.0
[0.5.0]: https://github.com/posit-dev/go-pyresolver/compare/v0.4.0...v0.5.0
[0.4.0]: https://github.com/posit-dev/go-pyresolver/compare/v0.3.0...v0.4.0
[0.3.0]: https://github.com/posit-dev/go-pyresolver/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/posit-dev/go-pyresolver/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/posit-dev/go-pyresolver/releases/tag/v0.1.0
