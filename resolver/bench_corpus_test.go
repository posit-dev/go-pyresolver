// SPDX-License-Identifier: Apache-2.0 OR MIT

package resolver_test

import (
	"testing"

	"github.com/posit-dev/go-pyresolver/index"
)

// benchEntry is one input to the resolution benchmark.
//
// Committed data, not sampling. A benchmark that picked its inputs at random
// per run would report a different number every time and could not be used as
// an exit gate: the whole point of a gate is that two people on two machines
// measure the SAME thing. See rstudio/package-manager#18651.
type benchEntry struct {
	// Name is the sub-benchmark name. It appears in every `go test -bench`
	// line, so it is stable and shell-safe.
	Name string

	// Requirements are the caller's requirements, exactly as a user would
	// write them in a requirements.txt.
	Requirements []string

	// Why records what this entry is measuring that no other entry measures.
	// An entry that cannot answer this does not belong in the corpus.
	Why string

	// WantFailure is true when the requirements are unsatisfiable and a
	// *resolver.ResolutionError is the correct outcome.
	//
	// Enforced only against a full snapshot. Against the committed excerpt
	// nearly every entry fails for a boring reason -- the excerpt carries 139
	// packages -- and asserting there would say nothing about the resolver.
	WantFailure bool

	// OnExcerpt is true when the entry resolves against
	// index/testdata/pypi-trimmed.rsf, which is flask's depth-4 closure plus
	// seven shape packages. It is what lets the benchmark be exercised in CI
	// without the 981 MB snapshot.
	OnExcerpt bool

	// MustNotBeNewest names a package this entry requires the resolution to pin
	// BELOW its newest published version. Empty means no such requirement.
	//
	// # This exists because an entry went stale silently and nobody noticed
	//
	// The `backtracking` entry was `pandas, numpy<2`, chosen because recent pandas
	// required numpy>=2 and so had to be backed out of. Then pandas relaxed its
	// floor, the newest pandas satisfied numpy<2 on its own, and the entry quietly
	// became an ordinary resolve -- still passing, still producing numbers, still
	// named `backtracking`, and measuring nothing of the kind. It stayed that way
	// across at least one release, and the cost analysis written on top of it
	// implied a coverage the corpus did not have.
	//
	// A corpus entry defined by "the newest version cannot be used" is inherently
	// perishable, because the packages it names keep releasing. So the property is
	// asserted rather than assumed: if the driver package can be taken at its
	// newest version, no backing-out happened and the benchmark FAILS instead of
	// reporting a comfortable number.
	//
	// ⚠️ Enforced only against a full snapshot, like WantFailure. The excerpt does
	// not carry the version history this reasoning needs.
	MustNotBeNewest index.PackageName
}

// benchCorpus is the fixed corpus this benchmark measures.
//
// # Why these
//
// Each entry isolates one cost the resolver can be expected to have, and the
// entries are ordered so that the difference between two adjacent ones is
// attributable. Sizes below are from the production snapshot dated 2026-08-04
// (932,861 packages).
//
// The interpreter is fixed too: CPython 3.11.4 on linux/x86_64, from testEnv.
// Markers are evaluated against it, so a different target is a different
// measurement -- python_version alone changes which requirements are live.
var benchCorpus = []benchEntry{
	{
		Name:         "single-no-deps",
		Requirements: []string{"certifi"},
		Why: "The floor. certifi has no runtime requirements at all, so what " +
			"this measures is the fixed cost of a resolution: build the provider, " +
			"decide the root and the interpreter, read one package's version list " +
			"and one version's metadata. Every other entry is this plus its own " +
			"work, so a regression that shows up here is in the machinery rather " +
			"than in the graph.",
	},
	{
		Name:         "small-tree",
		Requirements: []string{"flask"},
		OnExcerpt:    true,
		Why: "A small, well-behaved application dependency tree: flask pulls " +
			"werkzeug, jinja2, markupsafe, itsdangerous, click and blinker, with " +
			"no conflict and no backtracking. This is the shape a `pip install " +
			"flask` actually has, and it is the one entry the committed excerpt " +
			"can resolve -- the excerpt IS flask's closure -- so it is what keeps " +
			"the benchmark exercised in CI.",
	},
	{
		Name:         "extras",
		Requirements: []string{"flask[async]"},
		OnExcerpt:    true,
		Why: "The same root as small-tree with one extra requested, so the " +
			"difference between the two is the marginal cost of the virtual-package " +
			"model: an extra becomes a distinct solver package depending on its base " +
			"at the same version, plus the extra's own requirements (asgiref). " +
			"Nothing else in the corpus exercises that path.",
	},
	{
		Name:         "app-set",
		Requirements: []string{"flask", "requests", "sqlalchemy", "gunicorn", "python-dotenv"},
		Why: "Five roots rather than one: a realistic service requirements.txt. " +
			"Several independent subtrees that share packages (both flask and " +
			"requests reach into the same closure), which is where per-resolution " +
			"caching either pays off or does not.",
	},
	{
		Name:         "wide-versions",
		Requirements: []string{"boto3"},
		Why: "Depth plus an extreme version list. botocore carries over ten " +
			"thousand releases, so this is the entry that shows how index calls " +
			"scale with the number of candidate versions rather than with the " +
			"number of packages -- the cost provider.usable imposes by " +
			"establishing usability one version at a time.",
	},
	{
		Name:            "backtracking",
		Requirements:    []string{"pandas", "numpy<1.26"},
		MustNotBeNewest: "pandas",
		Why: "A resolution that cannot be reached by taking the newest of " +
			"everything: pandas from 3.0 on requires numpy>=1.26 on this " +
			"interpreter, so the solver must back out of its first choice and walk " +
			"down to pandas 2.x. Backtracking is what makes Candidates get asked " +
			"about the same package repeatedly, which is the case the warm target " +
			"is most exposed to. ⚠️ The bound was numpy<2 until 2026-08-13, when " +
			"pandas relaxed its floor and the entry silently stopped backtracking " +
			"-- it pinned the NEWEST pandas and measured an ordinary resolve while " +
			"still being named backtracking. MustNotBeNewest exists so that cannot " +
			"happen quietly again; if this entry starts taking the newest pandas, " +
			"the benchmark fails and the bound needs tightening. ⚠️ Against the " +
			"committed excerpt, where pandas' own dependencies are absent, this " +
			"entry is still by far the most expensive: ~3.1 GB and a couple of " +
			"seconds per iteration, against milliseconds for the others. (It was " +
			"~25 GB and ~17 s before the found/rank change; the second cost profile " +
			"in bench_test.go still quotes the old figures and is marked historical.) " +
			"Exclude it with -bench 'Cold/(single|small|extras|unsat)' if you only " +
			"want a quick check that the benchmark still runs.",
	},
	{
		Name:         "unsatisfiable",
		Requirements: []string{"flask==3.0.0", "werkzeug<2"},
		WantFailure:  true,
		OnExcerpt:    true,
		Why: "A genuine conflict -- flask 3.0.0 requires Werkzeug>=3.0.0 -- so " +
			"the resolution ends in a *ResolutionError built from the derivation " +
			"graph. A failing resolve is a real user-facing path and a plausible " +
			"place for cost to hide, since building the report walks the graph. " +
			"It must be measured, not assumed to be cheap because it produced no " +
			"answer.",
	},
}

// TestBenchCorpusIsWellFormed keeps the committed corpus honest without needing
// a snapshot: names unique and non-empty, requirements parseable, and every
// entry carrying its rationale.
//
// The rationale is checked because an entry that nobody can justify is how a
// fixed corpus turns into an arbitrary one, which is the failure mode the
// reproducibility requirement in rstudio/package-manager#18651 exists to
// prevent.
func TestBenchCorpusIsWellFormed(t *testing.T) {
	seen := map[string]bool{}
	for _, entry := range benchCorpus {
		if entry.Name == "" {
			t.Fatalf("a corpus entry has no name: %+v", entry)
		}
		if seen[entry.Name] {
			t.Errorf("duplicate corpus entry name %q; sub-benchmark names must be unique", entry.Name)
		}
		seen[entry.Name] = true

		if len(entry.Requirements) == 0 {
			t.Errorf("%s: no requirements", entry.Name)
		}
		// Parses through the same path the benchmark uses, so a typo in a
		// committed requirement fails here rather than as an unexplained
		// benchmark result.
		mustRequirements(t, entry.Requirements...)

		if len(entry.Why) < 80 {
			t.Errorf("%s: Why is %d characters; state what this entry measures that no "+
				"other entry does", entry.Name, len(entry.Why))
		}

		checkMustNotBeNewest(t, entry)
	}

	// The corpus is only useful in CI if something in it resolves against the
	// committed excerpt.
	var onExcerpt int
	for _, entry := range benchCorpus {
		if entry.OnExcerpt {
			onExcerpt++
		}
	}
	if onExcerpt == 0 {
		t.Error("no corpus entry is marked OnExcerpt, so the benchmark cannot be exercised " +
			"without the 981 MB production snapshot")
	}
}

// checkMustNotBeNewest is the snapshot-free half of the backtracking guard.
//
// checkBackedOut can only see that the driver was pinned below its newest selectable
// version, and that is NECESSARY but not SUFFICIENT: bounding the driver directly
// produces the same observation with no back-out at all. `pandas<3, numpy` pins an
// older pandas on the first try and costs 28 Metadata calls where the real entry
// costs 42, and checkBackedOut passes it.
//
// So the shape is enforced here instead: the constraint must be INDIRECT. An entry
// that names MustNotBeNewest may not itself put a version bound on that package —
// something the driver depends on has to be what excludes the newer versions.
//
// ⚠️ This runs without a snapshot, so it fails in CI rather than only for whoever
// runs the full benchmark. The guard it completes does not.
func checkMustNotBeNewest(t *testing.T, entry benchEntry) {
	t.Helper()

	if entry.MustNotBeNewest == "" {
		return
	}

	if got := index.NewPackageName(string(entry.MustNotBeNewest)); got != entry.MustNotBeNewest {
		t.Errorf("%s: MustNotBeNewest is %q, which canonicalizes to %q — it is looked up in "+
			"Resolution.Pinned by exact key, so a non-canonical name silently never matches",
			entry.Name, entry.MustNotBeNewest, got)
	}

	if entry.WantFailure {
		t.Errorf("%s: MustNotBeNewest and WantFailure cannot both be set; a failed resolution "+
			"pins nothing, so there is no version to compare", entry.Name)
	}

	var mentioned bool
	for _, r := range mustRequirements(t, entry.Requirements...) {
		if index.NewPackageName(r.Name) != entry.MustNotBeNewest {
			continue
		}
		mentioned = true
		if r.Specifiers.Len() > 0 {
			t.Errorf("%s: MustNotBeNewest names %s, but this entry bounds %s directly (%q). "+
				"That makes the back-out check vacuous — the solver picks an older version on "+
				"its FIRST try and nothing is abandoned, while the check still passes. "+
				"Constrain something %s depends on instead.",
				entry.Name, entry.MustNotBeNewest, entry.MustNotBeNewest,
				r.String(), entry.MustNotBeNewest)
		}
	}
	if !mentioned {
		t.Errorf("%s: MustNotBeNewest names %s, which this entry does not require, so it may "+
			"not appear in the resolution at all", entry.Name, entry.MustNotBeNewest)
	}
}
