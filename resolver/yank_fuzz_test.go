// SPDX-License-Identifier: Apache-2.0 OR MIT

package resolver_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/posit-dev/go-pyresolver/index"
	"github.com/posit-dev/go-pyresolver/resolver"
)

// Differential test for YankExemptTransitivePins against a brute-force oracle.
// The oracle below re-implements the rule on its own and shares no code with
// the resolver:
//
// A resolution is valid iff (1) it satisfies the root requirements and every
// requirement of every selected version, and (3) every selected version is in
// J, where (2) J is the least fixpoint from {root}: X joins when a member of J
// has a requirement on X's package that X satisfies and, if X is yanked, a
// member of J pins X's package with `==` to exactly X's version.

// yfRelease and yfCase are the export schema the pip harness reads.
type yfRelease struct {
	Yanked   bool     `json:"yanked"`
	Requires []string `json:"requires"`
	// Extras maps an extra this release declares to the requirements it adds.
	Extras map[string][]string `json:"extras,omitempty"`
}

type yfCase struct {
	Packages map[string]map[string]yfRelease `json:"packages"`
	Root     []string                        `json:"root"`
}

func (c yfCase) String() string {
	b, _ := json.Marshal(c)
	return string(b)
}

func (c yfCase) clone() yfCase {
	out := yfCase{Packages: map[string]map[string]yfRelease{}, Root: slices.Clone(c.Root)}
	for n, vs := range c.Packages {
		out.Packages[n] = map[string]yfRelease{}
		for v, rel := range vs {
			d := yfRelease{Yanked: rel.Yanked, Requires: slices.Clone(rel.Requires)}
			for x, reqs := range rel.Extras {
				if d.Extras == nil {
					d.Extras = map[string][]string{}
				}
				d.Extras[x] = slices.Clone(reqs)
			}
			out.Packages[n][v] = d
		}
	}
	return out
}

// genYankCase draws an index of minPkgs-maxPkgs packages of 1-4 versions,
// random edges with any, >=v, ==v (weighted up) or <v, about 30% of versions
// yanked, cycles allowed. v can name a version that does not exist.
func genYankCase(r *rand.Rand, minPkgs, maxPkgs int) yfCase {
	names := []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j"}[:minPkgs+r.IntN(maxPkgs-minPkgs+1)]
	nv := map[string]int{}
	for _, n := range names {
		nv[n] = 1 + r.IntN(4)
	}
	spec := func(target string) string {
		v := strconv.Itoa(1+r.IntN(nv[target]+1)) + ".0"
		switch r.IntN(6) {
		case 1:
			return target + ">=" + v
		case 2, 4, 5:
			return target + "==" + v
		case 3:
			return target + "<" + v
		}
		return target
	}
	c := yfCase{Packages: map[string]map[string]yfRelease{}}
	for _, n := range names {
		c.Packages[n] = map[string]yfRelease{}
		for i := 1; i <= nv[n]; i++ {
			rel := yfRelease{Yanked: r.IntN(10) < 3, Requires: []string{}}
			for _, j := range r.Perm(len(names))[:r.IntN(4)] {
				if names[j] != n {
					rel.Requires = append(rel.Requires, spec(names[j]))
				}
			}
			c.Packages[n][strconv.Itoa(i)+".0"] = rel
		}
	}
	for _, j := range r.Perm(len(names))[:1+r.IntN(3)] {
		c.Root = append(c.Root, spec(names[j]))
	}
	if r.IntN(3) == 0 {
		addDeadEnd(r, c)
	}
	return c
}

// yfMarkers are the markers genRichCase attaches, with their value for
// testEnv (CPython 3.11 on Linux). The oracle reads this table, not the parser.
var yfMarkers = []struct {
	text   string
	active bool
}{
	{`sys_platform == "linux"`, true},
	{`sys_platform == "win32"`, false},
	{`python_version >= "3.8"`, true},
	{`python_version < "3.0"`, false},
}

// genRichCase is genYankCase plus environment markers (true and false, pins
// included), a second requirement on the same dependency, and an extra "x" on
// one or two packages that adds dependencies and that some requirers request.
func genRichCase(r *rand.Rand, minPkgs, maxPkgs int) yfCase {
	c := genYankCase(r, minPkgs, maxPkgs)
	var names []string
	for _, n := range slices.Sorted(mapKeys(c.Packages)) {
		if n != "w" && n != "z" {
			names = append(names, n)
		}
	}
	spec := func(target string) string {
		v := strconv.Itoa(1+r.IntN(len(c.Packages[target])+1)) + ".0"
		switch r.IntN(6) {
		case 1:
			return target + ">=" + v
		case 2, 4, 5:
			return target + "==" + v
		case 3:
			return target + "<" + v
		}
		return target
	}
	withExtra := map[string]bool{}
	for _, j := range r.Perm(len(names))[:1+r.IntN(2)] {
		withExtra[names[j]] = true
	}
	decorate := func(reqs []string) []string {
		out := slices.Clone(reqs)
		if len(out) > 0 && r.IntN(4) == 0 {
			out = append(out, spec(oParse(out[r.IntN(len(out))]).name))
		}
		for i := range out {
			if t := oParse(out[i]).name; withExtra[t] && r.IntN(3) == 0 {
				out[i] = t + "[x]" + out[i][len(t):]
			}
			if r.IntN(4) == 0 {
				out[i] += "; " + yfMarkers[r.IntN(len(yfMarkers))].text
			}
		}
		return out
	}
	c.Root = decorate(c.Root)
	for _, n := range slices.Sorted(mapKeys(c.Packages)) {
		for _, v := range slices.Sorted(mapKeys(c.Packages[n])) {
			rel := c.Packages[n][v]
			rel.Requires = decorate(rel.Requires)
			if withExtra[n] {
				var deps []string
				for _, j := range r.Perm(len(names))[:r.IntN(3)] {
					if names[j] != n {
						deps = append(deps, spec(names[j]))
					}
				}
				rel.Extras = map[string][]string{"x": decorate(deps)}
			}
			c.Packages[n][v] = rel
		}
	}
	return c
}

// addDeadEnd adds w, whose two releases both need a z that does not exist, and
// has some versions require it. The solver decides such a version and only
// later backs off, which is how a pinner gets abandoned.
func addDeadEnd(r *rand.Rand, c yfCase) {
	dead := []string{"z>=9.0"}
	c.Packages["w"] = map[string]yfRelease{"1.0": {Requires: dead}, "2.0": {Requires: dead}}
	c.Packages["z"] = map[string]yfRelease{"1.0": {Requires: []string{}}}
	for _, n := range slices.Sorted(mapKeys(c.Packages)) {
		vs := c.Packages[n]
		if n == "w" || n == "z" {
			continue
		}
		for _, v := range slices.Sorted(mapKeys(vs)) {
			rel := vs[v]
			if r.IntN(4) == 0 {
				rel.Requires = append(rel.Requires, "w")
				vs[v] = rel
			}
		}
	}
}

// --- oracle ---

type oReq struct {
	name, op, extra string
	ver             []int
	// active is false when the marker is false for testEnv. implicit marks an
	// extra's edge to its own base version, which is not a pin.
	active, implicit bool
}

func oVersion(s string) []int {
	var out []int
	for _, p := range strings.Split(s, ".") {
		n, _ := strconv.Atoi(p)
		out = append(out, n)
	}
	return out
}

func oCompare(a, b []int) int {
	for i := range max(len(a), len(b)) {
		var x, y int
		if i < len(a) {
			x = a[i]
		}
		if i < len(b) {
			y = b[i]
		}
		if x != y {
			return x - y
		}
	}
	return 0
}

func oParse(s string) oReq {
	q := oReq{active: true}
	if k := strings.Index(s, ";"); k >= 0 {
		m := strings.TrimSpace(s[k+1:])
		s = strings.TrimSpace(s[:k])
		found := false
		for _, ym := range yfMarkers {
			if ym.text == m {
				q.active, found = ym.active, true
			}
		}
		if !found {
			panic("oracle: unknown marker " + m)
		}
	}
	i := strings.IndexAny(s, "<>=")
	if i < 0 {
		i = len(s)
	}
	q.name = s[:i]
	if b := strings.Index(q.name, "["); b >= 0 {
		q.extra = strings.TrimSuffix(q.name[b+1:], "]")
		q.name = q.name[:b]
	}
	j := i
	for j < len(s) && strings.ContainsRune("<>=", rune(s[j])) {
		j++
	}
	q.op = s[i:j]
	if q.op != "" {
		q.ver = oVersion(s[j:])
	}
	return q
}

func (q oReq) check(v []int) bool {
	c := oCompare(v, q.ver)
	switch q.op {
	case "":
		return true
	case ">=":
		return c >= 0
	case "==":
		return c == 0
	case "<":
		return c < 0
	}
	panic("oracle: unknown operator " + q.op)
}

// oracleValid applies the rule to sel, a map of package to version. With
// rootOnly, only root pins justify a yanked version (the option off). A
// requirement whose marker is false is ignored. Each requested extra is its own
// node: its requirements are the extra's plus an edge (not a pin) to its base.
func oracleValid(c yfCase, sel map[string]string, rootOnly bool) bool {
	type node struct{ n, x string }
	active := func(ss []string) []oReq {
		var out []oReq
		for _, s := range ss {
			if q := oParse(s); q.active {
				out = append(out, q)
			}
		}
		return out
	}
	reqsOf := func(nd node) []oReq {
		rel := c.Packages[nd.n][sel[nd.n]]
		if nd.x == "" {
			return active(rel.Requires)
		}
		return append(active(rel.Extras[nd.x]),
			oReq{name: nd.n, op: "==", ver: oVersion(sel[nd.n]), active: true, implicit: true})
	}
	root := active(c.Root)
	for n, v := range sel {
		if _, ok := c.Packages[n][v]; !ok {
			return false
		}
	}

	// The selected nodes: every base in sel, and every extra some selected
	// node requests.
	selected := map[node]bool{}
	for n := range sel {
		selected[node{n, ""}] = true
	}
	queue := [][]oReq{root}
	for n := range sel {
		queue = append(queue, reqsOf(node{n, ""}))
	}
	for len(queue) > 0 {
		reqs := queue[0]
		queue = queue[1:]
		for _, q := range reqs {
			if _, ok := sel[q.name]; !ok {
				return false
			}
			if nd := (node{q.name, q.extra}); q.extra != "" && !selected[nd] {
				selected[nd] = true
				queue = append(queue, reqsOf(nd))
			}
		}
	}
	satisfied := func(reqs []oReq) bool {
		for _, q := range reqs {
			v, ok := sel[q.name]
			if !ok || !q.check(oVersion(v)) {
				return false
			}
		}
		return true
	}
	if !satisfied(root) {
		return false
	}
	for nd := range selected {
		if !satisfied(reqsOf(nd)) {
			return false
		}
	}

	justified := map[node]bool{}
	members := [][]oReq{root}
	for grew := true; grew; {
		grew = false
		for nd := range selected {
			if justified[nd] {
				continue
			}
			v := sel[nd.n]
			edge, pin := false, false
			for i, reqs := range members {
				for _, q := range reqs {
					if q.name != nd.n || !q.check(oVersion(v)) {
						continue
					}
					if nd.x == "" || q.extra == nd.x {
						edge = true
					}
					if q.op == "==" && !q.implicit && (i == 0 || !rootOnly) {
						pin = true
					}
				}
			}
			if edge && (pin || !c.Packages[nd.n][v].Yanked) {
				justified[nd] = true
				members = append(members, reqsOf(nd))
				grew = true
			}
		}
	}
	return len(justified) == len(selected)
}

// oracleAll enumerates every valid resolution, pruning an assignment as soon
// as two chosen packages (or the root) disagree.
func oracleAll(c yfCase) []map[string]string {
	names := slices.Sorted(func(yield func(string) bool) {
		for n := range c.Packages {
			if !yield(n) {
				return
			}
		}
	})
	var out []map[string]string
	sel := map[string]string{}
	assigned := map[string]bool{}
	consistent := func(reqs []string) bool {
		for _, s := range reqs {
			q := oParse(s)
			if !q.active || !assigned[q.name] {
				continue
			}
			v, ok := sel[q.name]
			if !ok || !q.check(oVersion(v)) {
				return false
			}
		}
		return true
	}
	var walk func(i int)
	walk = func(i int) {
		if !consistent(c.Root) {
			return
		}
		for n, v := range sel {
			if !consistent(c.Packages[n][v].Requires) {
				return
			}
		}
		if i == len(names) {
			if oracleValid(c, sel, false) {
				out = append(out, mapsClone(sel))
			}
			return
		}
		n := names[i]
		assigned[n] = true
		walk(i + 1)
		for v := range c.Packages[n] {
			sel[n] = v
			walk(i + 1)
			delete(sel, n)
		}
		delete(assigned, n)
	}
	walk(0)
	return out
}

func mapsClone(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// newestFirst approximates pip's pick among valid answers: packages ordered
// by first mention walking breadth-first from the root requirements, then by
// name; the answer that is newest at the first package where two differ wins,
// with absent below every version. pip's real order depends on its backtracking.
func newestFirst(c yfCase, valid []map[string]string) map[string]string {
	var order []string
	seen := map[string]bool{}
	queue := slices.Clone(c.Root)
	for len(queue) > 0 {
		n := oParse(queue[0]).name
		queue = queue[1:]
		if seen[n] {
			continue
		}
		seen[n] = true
		order = append(order, n)
		for _, v := range slices.Sorted(mapKeys(c.Packages[n])) {
			queue = append(queue, c.Packages[n][v].Requires...)
		}
	}
	for _, n := range slices.Sorted(mapKeys(c.Packages)) {
		if !seen[n] {
			order = append(order, n)
		}
	}
	better := func(a, b map[string]string) bool {
		for _, n := range order {
			av, aok := a[n]
			bv, bok := b[n]
			if aok != bok {
				return aok
			}
			if cmp := oCompare(oVersion(av), oVersion(bv)); aok && cmp != 0 {
				return cmp > 0
			}
		}
		return false
	}
	var best map[string]string
	for _, v := range valid {
		if best == nil || better(v, best) {
			best = v
		}
	}
	return best
}

func mapKeys[V any](m map[string]V) func(func(string) bool) {
	return func(yield func(string) bool) {
		for k := range m {
			if !yield(k) {
				return
			}
		}
	}
}

// --- running the resolver on a case ---

func indexFromCase(t testing.TB, c yfCase) *index.MockIndex {
	t.Helper()
	idx := newYankIndex()
	for n, vs := range c.Packages {
		for v, rel := range vs {
			reqs := slices.Clone(rel.Requires)
			var provides []string
			for _, x := range slices.Sorted(mapKeys(rel.Extras)) {
				provides = append(provides, x)
				for _, s := range rel.Extras[x] {
					if k := strings.Index(s, ";"); k >= 0 {
						s = s[:k] + "; (" + strings.TrimSpace(s[k+1:]) + ") and extra == \"" + x + "\""
					} else {
						s += "; extra == \"" + x + "\""
					}
					reqs = append(reqs, s)
				}
			}
			idx.SetMetadata(n, v, index.PackageMetadata{
				RequiresDist:  mustRequirements(t, reqs...),
				ProvidesExtra: provides,
				Yanked:        rel.Yanked,
			})
		}
	}
	return idx
}

// resolveCase returns the resolver's selection, or nil when it reports a
// conflict. Any other error fails the test.
func resolveCase(t testing.TB, c yfCase, transitive bool) map[string]string {
	t.Helper()
	opts := testOptions(t)
	opts.YankExemptTransitivePins = transitive
	res, err := resolver.Resolve(context.Background(), mustRequirements(t, c.Root...), indexFromCase(t, c), opts)
	if err != nil {
		var re *resolver.ResolutionError
		if !errors.As(err, &re) {
			t.Fatalf("case %s: transitive=%v: unexpected error: %v", c, transitive, err)
		}
		return nil
	}
	out := map[string]string{}
	for n, v := range res.Pinned {
		out[n.String()] = v.String()
	}
	return out
}

// checkYankCase asserts soundness and option-off parity for one case.
func checkYankCase(t testing.TB, c yfCase) (on, off map[string]string) {
	t.Helper()
	on = resolveCase(t, c, true)
	if on != nil && !oracleValid(c, on, false) {
		t.Fatalf("SOUNDNESS: case %s: resolver returned %v, which the oracle rejects", c, on)
	}
	off = resolveCase(t, c, false)
	if off != nil && !oracleValid(c, off, true) {
		t.Fatalf("OPTION OFF: case %s: resolver returned %v, which selects a yanked version "+
			"without a root pin or breaks a requirement", c, off)
	}
	if off != nil && on == nil {
		t.Fatalf("case %s: resolves with the option off (%v) but not with it on", c, off)
	}
	return on, off
}

// shrinkMiss removes versions, edges, root requirements and yanks while the
// resolver still misses a valid resolution.
func shrinkMiss(t testing.TB, c yfCase) yfCase {
	isMiss := func(c yfCase) bool {
		return len(oracleAll(c)) > 0 && resolveCase(t, c, true) == nil
	}
	for changed := true; changed; {
		changed = false
		var tries []yfCase
		for i := range c.Root {
			if len(c.Root) > 1 {
				d := c.clone()
				d.Root = slices.Delete(d.Root, i, i+1)
				tries = append(tries, d)
			}
		}
		for _, n := range slices.Sorted(mapKeys(c.Packages)) {
			for _, v := range slices.Sorted(mapKeys(c.Packages[n])) {
				rel := c.Packages[n][v]
				if len(c.Packages[n]) > 1 {
					d := c.clone()
					delete(d.Packages[n], v)
					tries = append(tries, d)
				}
				if rel.Yanked {
					d := c.clone()
					r := d.Packages[n][v]
					r.Yanked = false
					d.Packages[n][v] = r
					tries = append(tries, d)
				}
				for i := range rel.Requires {
					d := c.clone()
					r := d.Packages[n][v]
					r.Requires = slices.Delete(r.Requires, i, i+1)
					d.Packages[n][v] = r
					tries = append(tries, d)
				}
			}
		}
		for _, d := range tries {
			if isMiss(d) {
				c, changed = d, true
				break
			}
		}
	}
	// Drop packages nothing mentions.
	mentioned := map[string]bool{}
	for _, s := range c.Root {
		mentioned[oParse(s).name] = true
	}
	for _, vs := range c.Packages {
		for _, rel := range vs {
			for _, s := range rel.Requires {
				mentioned[oParse(s).name] = true
			}
			for _, reqs := range rel.Extras {
				for _, s := range reqs {
					mentioned[oParse(s).name] = true
				}
			}
		}
	}
	for n := range c.Packages {
		if !mentioned[n] {
			delete(c.Packages, n)
		}
	}
	return c
}

// maxYankMissRate is set just above the measured completeness miss rate, so a
// search regression fails the test. See TestYankTransitiveDifferential.
const maxYankMissRate = 0.018

func TestYankTransitiveDifferential(t *testing.T) {
	n := 20000
	if testing.Short() || raceEnabled {
		n = 2000
	}
	exportDir := os.Getenv("YANK_FUZZ_EXPORT")
	r := rand.New(rand.NewPCG(21025, 70))

	var solvable, misses, preferenceDiffs, compared, exported int
	var results []map[string]any
	export := func(c yfCase, kind string, ours, newest map[string]string) {
		exported++
		name := fmt.Sprintf("case-%05d-%s.json", exported, kind)
		b, err := json.MarshalIndent(c, "", " ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(exportDir, name), b, 0o600); err != nil {
			t.Fatal(err)
		}
		results = append(results, map[string]any{"case": name, "kind": kind, "ours": ours, "newest_first": newest})
	}

	for i := range n {
		c := genYankCase(r, 3, 6)
		on, _ := checkYankCase(t, c)
		valid := oracleAll(c)
		if len(valid) == 0 {
			continue
		}
		solvable++
		newest := newestFirst(c, valid)
		if on == nil {
			misses++
			if misses <= 3 {
				t.Logf("completeness miss %d (case %d), shrunk: %s", misses, i, shrinkMiss(t, c))
			}
			if exportDir != "" {
				export(c, "miss", nil, newest)
			}
			continue
		}
		compared++
		if fmt.Sprint(on) != fmt.Sprint(newest) {
			preferenceDiffs++
		}
		if exportDir != "" && i%100 == 0 {
			export(c, "hit", on, newest)
		}
	}

	// Larger indexes, too big to enumerate: soundness and option-off only.
	for range n / 4 {
		checkYankCase(t, genYankCase(r, 7, 10))
	}

	rate := float64(misses) / float64(max(solvable, 1))
	t.Logf("%d cases, %d with a valid resolution; completeness misses %d (%.3f%%); "+
		"answer differs from the newest-first valid answer in %d of %d (%.1f%%)",
		n, solvable, misses, 100*rate, preferenceDiffs, compared,
		100*float64(preferenceDiffs)/float64(max(compared, 1)))
	if rate > maxYankMissRate {
		t.Errorf("completeness miss rate %.3f%% is above %.3f%%", 100*rate, 100*maxYankMissRate)
	}
	if exportDir != "" {
		b, err := json.MarshalIndent(results, "", " ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(exportDir, "results.json"), b, 0o600); err != nil {
			t.Fatal(err)
		}
		t.Logf("exported %d cases to %s", exported, exportDir)
	}
}

// FuzzYankTransitive checks soundness and option-off parity on generated
// cases, for long offline runs: go test -run xxx -fuzz FuzzYankTransitive ./resolver
func FuzzYankTransitive(f *testing.F) {
	for _, seed := range []uint64{1, 2, 3, 21025} {
		f.Add(seed, seed*7)
	}
	f.Fuzz(func(t *testing.T, a, b uint64) {
		checkYankCase(t, genYankCase(rand.New(rand.NewPCG(a, b)), 3, 10))
	})
}

// maxRichMissRate is maxYankMissRate for genRichCase.
const maxRichMissRate = 0.014

// TestYankTransitiveDifferentialRich is the differential over genRichCase:
// markers, a second requirement on one dependency, and extras.
func TestYankTransitiveDifferentialRich(t *testing.T) {
	n := 10000
	if testing.Short() || raceEnabled {
		n = 1000
	}
	r := rand.New(rand.NewPCG(21025, 71))
	var solvable, misses int
	for i := range n {
		c := genRichCase(r, 3, 6)
		on, _ := checkYankCase(t, c)
		if len(oracleAll(c)) == 0 {
			continue
		}
		solvable++
		if on == nil {
			misses++
			if misses <= 3 {
				t.Logf("completeness miss %d (case %d), shrunk: %s", misses, i, shrinkMiss(t, c))
			}
		}
	}
	for range n / 4 {
		checkYankCase(t, genRichCase(r, 7, 10))
	}
	rate := float64(misses) / float64(max(solvable, 1))
	t.Logf("%d cases, %d with a valid resolution; completeness misses %d (%.3f%%)",
		n, solvable, misses, 100*rate)
	if rate > maxRichMissRate {
		t.Errorf("completeness miss rate %.3f%% is above %.3f%%", 100*rate, 100*maxRichMissRate)
	}
}
