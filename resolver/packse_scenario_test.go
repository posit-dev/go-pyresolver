// SPDX-License-Identifier: Apache-2.0 OR MIT

package resolver_test

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/posit-dev/go-pyresolver/index"
	"github.com/posit-dev/go-pyresolver/provider"
	"github.com/posit-dev/go-python-packaging/marker"
	"github.com/posit-dev/go-python-packaging/tags"
)

// packseTestdataDir holds the vendored scenario TOML files. See
// testdata/packse/README.md for the pinned upstream commit and refresh steps.
const packseTestdataDir = "testdata/packse"

// packseDefaultRequiresPython is packse's own default for both the root
// package and every version that omits requires_python. See
// astral-sh/packse's scenario.py: `requires_python: str | None = ">=3.12"`.
const packseDefaultRequiresPython = ">=3.12"

// packseDefaultPython is packse's default active interpreter when a scenario
// has no [environment] section at all.
const packseDefaultPython = "3.12"

// tomlScenario mirrors astral-sh/packse's scenario.py Scenario struct field
// for field. It is decoded with toml.DecodeFile and MetaData.Undecoded() is
// checked, matching packse's own forbid_unknown_fields=True: a re-pull that
// adds a field must fail the loader rather than silently drop it.
type tomlScenario struct {
	Name            string                 `toml:"name"`
	Description     string                 `toml:"description"`
	Packages        map[string]tomlPackage `toml:"packages"`
	Root            tomlRoot               `toml:"root"`
	Expected        tomlExpected           `toml:"expected"`
	Environment     tomlEnvironment        `toml:"environment"`
	ResolverOptions tomlResolverOptions    `toml:"resolver_options"`
	Template        string                 `toml:"template"`
}

type tomlPackage struct {
	Versions map[string]tomlVersion `toml:"versions"`
}

type tomlVersion struct {
	RequiresPython *string             `toml:"requires_python"`
	Requires       []string            `toml:"requires"`
	Extras         map[string][]string `toml:"extras"`
	Sdist          *bool               `toml:"sdist"`
	Wheel          *bool               `toml:"wheel"`
	Yanked         bool                `toml:"yanked"`
	WheelTags      []string            `toml:"wheel_tags"`
	Description    string              `toml:"description"`
}

type tomlRoot struct {
	RequiresPython *string  `toml:"requires_python"`
	Requires       []string `toml:"requires"`
}

type tomlExpected struct {
	Satisfiable bool              `toml:"satisfiable"`
	Packages    map[string]string `toml:"packages"`
	Explanation *string           `toml:"explanation"`
}

type tomlEnvironment struct {
	Python           string   `toml:"python"`
	AdditionalPython []string `toml:"additional_python"`
}

type tomlResolverOptions struct {
	Python               *string  `toml:"python"`
	Prereleases          bool     `toml:"prereleases"`
	NoBuild              []string `toml:"no_build"`
	NoBinary             []string `toml:"no_binary"`
	Universal            bool     `toml:"universal"`
	PythonPlatform       *string  `toml:"python_platform"`
	RequiredEnvironments []string `toml:"required_environments"`
}

// packseScenario is one loaded scenario, named by its path relative to
// testdata/packse (e.g. "requires_python/python-less-than-current"), which is
// what the classification lists (outOfScope, knownFail, unsupportedOption) key
// on.
type packseScenario struct {
	relName  string // category/scenario-name, no extension
	path     string
	scenario tomlScenario
}

// loadPackseScenarios walks testdata/packse and decodes every scenario TOML
// file, failing the test if any file carries a key this loader does not know
// about -- a silent drop on re-pull is exactly the failure mode packse's own
// forbid_unknown_fields guards against, and the loader must match it.
func loadPackseScenarios(t *testing.T) []packseScenario {
	t.Helper()

	var out []packseScenario
	err := filepath.WalkDir(packseTestdataDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || filepath.Ext(path) != ".toml" {
			return nil
		}

		var s tomlScenario
		md, decErr := toml.DecodeFile(path, &s)
		if decErr != nil {
			t.Fatalf("decode %s: %v", path, decErr)
		}
		if undecoded := md.Undecoded(); len(undecoded) > 0 {
			keys := make([]string, len(undecoded))
			for i, k := range undecoded {
				keys[i] = k.String()
			}
			t.Fatalf("%s: undecoded keys %v -- packse's schema grew a field this loader "+
				"does not know about; add it rather than silently dropping it", path, keys)
		}

		rel, relErr := filepath.Rel(packseTestdataDir, path)
		if relErr != nil {
			return relErr
		}
		rel = strings.TrimSuffix(rel, ".toml")
		out = append(out, packseScenario{relName: filepath.ToSlash(rel), path: path, scenario: s})
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", packseTestdataDir, err)
	}

	sort.Slice(out, func(i, j int) bool { return out[i].relName < out[j].relName })
	return out
}

// pythonSpec is the single Python version this scenario resolves for, and the
// tags.Target derived from it.
//
// ⚠️ resolver.Options carries exactly ONE python version, used both to filter
// Requires-Python and to evaluate markers (Resolve.validate enforces they
// agree). packse/uv can express a resolver_options.python DIFFERENT from
// environment.python -- a "resolve for 3.11 while running 3.9" override. This
// harness cannot represent that split, so it picks resolver_options.python
// when set, else environment.python, matching the brief. A scenario where
// that choice changes the answer packse expects is a knownFail candidate, not
// a bug in the loader.
type pythonSpec struct {
	full  string // e.g. "3.9.0"
	major int
	minor int
}

// scenarioPython derives the pythonSpec for s, per the rule above.
func scenarioPython(s tomlScenario) (pythonSpec, error) {
	raw := packseDefaultPython
	if s.Environment.Python != "" {
		raw = s.Environment.Python
	}
	if s.ResolverOptions.Python != nil && *s.ResolverOptions.Python != "" {
		raw = *s.ResolverOptions.Python
	}

	full := raw
	parts := strings.Split(raw, ".")
	if len(parts) == 2 {
		// "3.9" -> "3.9.0": packse's own fixtures document this padding
		// (requires_python/python-patch-override-no-patch.toml's explanation:
		// "the minimum compatible Python requirement is treated as 3.9.0").
		full = raw + ".0"
	}
	if len(parts) < 2 {
		return pythonSpec{}, fmt.Errorf("python version %q has no minor component", raw)
	}
	major, err := strconv.Atoi(parts[0])
	if err != nil {
		return pythonSpec{}, fmt.Errorf("python version %q: bad major: %w", raw, err)
	}
	minor, err := strconv.Atoi(parts[1])
	if err != nil {
		return pythonSpec{}, fmt.Errorf("python version %q: bad minor: %w", raw, err)
	}
	return pythonSpec{full: full, major: major, minor: minor}, nil
}

// packsePlatform maps a resolver_options.python_platform string to a
// tags.Target's OS/Arch. Only the one value the vendored corpus actually uses
// is recognized (measured: all three non-universal python_platform scenarios
// say "x86_64-manylinux2014", which is also this harness's default target);
// anything else is unsupportedOption rather than guessed at.
func packsePlatform(raw string) (os, arch string, ok bool) {
	if raw == "x86_64-manylinux2014" {
		return "linux", "x86_64", true
	}
	return "", "", false
}

// buildTarget builds the tags.Target this scenario resolves for.
func buildTarget(py pythonSpec, platform *string) (tags.Target, error) {
	target := tags.Target{
		Implementation: "cp", PyMajor: py.major, PyMinor: py.minor,
		OS: "linux", Arch: "x86_64", Libc: "glibc", LibcMajor: 2, LibcMinor: 28,
	}
	if platform != nil {
		os, arch, ok := packsePlatform(*platform)
		if !ok {
			return tags.Target{}, fmt.Errorf("unsupported python_platform %q", *platform)
		}
		target.OS, target.Arch = os, arch
	}
	return target, nil
}

// buildEnvironment builds the marker.Environment for py, matching testEnv's
// own construction through EnvironmentFromTarget rather than a struct literal
// (see resolver.Options.Environment's doc comment for why a literal is
// wrong).
func buildEnvironment(t tags.Target, py pythonSpec) (marker.Environment, error) {
	return marker.EnvironmentFromTarget(t, marker.InterpreterIdentity{
		ImplementationName:           "cpython",
		PlatformPythonImplementation: "CPython",
		PythonFullVersion:            py.full,
		ImplementationVersion:        py.full,
	})
}

// distFacts is what the harness (and the SAT oracle) derive about one
// version's publications from packse's wheel/sdist/wheel_tags fields.
type distFacts struct {
	wheelTags []string
	hasSdist  bool
}

func versionDistFacts(v tomlVersion) distFacts {
	wheel := true
	if v.Wheel != nil {
		wheel = *v.Wheel
	}
	sdist := true
	if v.Sdist != nil {
		sdist = *v.Sdist
	}

	tags := v.WheelTags
	if wheel && len(tags) == 0 {
		// packse's build.py always produces a "py3-none-any" wheel when
		// wheel_tags is empty and wheel=true (its default template package).
		tags = []string{"py3-none-any"}
	}
	if !wheel {
		tags = nil
	}
	return distFacts{wheelTags: tags, hasSdist: sdist}
}

// requiresPythonOf returns v's Requires-Python, or packse's default.
func requiresPythonOf(v tomlVersion) string {
	if v.RequiresPython != nil {
		return *v.RequiresPython
	}
	return packseDefaultRequiresPython
}

// extraRequirement rewrites req to be conditional on extra being requested,
// combining with any marker req already carries. packse's schema stores extra
// requirements separately from a version's own "requires" (unlike a real
// published package, which folds both into one Requires-Dist list with
// "; extra == ..." markers) -- this is what puts them back together for
// go-pyresolver, which only understands the folded form.
func extraRequirement(req, extra string) string {
	return fmt.Sprintf(`%s; extra == %q`, req, extra)
}

// buildMockIndex builds a MockIndex from the scenario's [packages], with wheel
// tag data declared complete so WheelTagFilter can act on it.
func buildMockIndex(t *testing.T, name string, pkgs map[string]tomlPackage) *index.MockIndex {
	t.Helper()

	idx := index.NewMockIndex(name).SetWheelTagsComplete(true)
	for pkgName, pkg := range pkgs {
		if len(pkg.Versions) == 0 {
			idx.AddPackage(pkgName)
			continue
		}
		for verStr, v := range pkg.Versions {
			reqs := append([]string(nil), v.Requires...)
			var extraNames []string
			for extra, extraReqs := range v.Extras {
				extraNames = append(extraNames, extra)
				for _, r := range extraReqs {
					reqs = append(reqs, extraRequirement(r, extra))
				}
			}
			sort.Strings(extraNames)

			facts := versionDistFacts(v)
			meta, err := index.ParseRecord(index.RawRecord{
				RequiresDist:   reqs,
				RequiresPython: requiresPythonOf(v),
				ProvidesExtra:  extraNames,
				WheelTags:      facts.wheelTags,
				HasSdist:       facts.hasSdist,
				TagsCaptured:   true,
			})
			if err != nil {
				t.Fatalf("%s %s %s: %v", name, pkgName, verStr, err)
			}
			idx.SetMetadata(pkgName, verStr, meta)

			// Files carry Yanked, which FilteredIndex.ExcludeYanked (wrapped on in
			// runPackseScenario) reads. Populated for every version, not just a
			// yanked one -- ExcludeYanked only drops a version whose files are ALL
			// yanked, so an unyanked version needs at least one file recorded or it
			// would look like it has none.
			for _, tag := range facts.wheelTags {
				idx.AddFiles(pkgName, verStr, index.DistFile{
					Filename: fmt.Sprintf("%s-%s-%s.whl", pkgName, verStr, tag),
					Kind:     index.DistKindWheel,
					Yanked:   v.Yanked,
				})
			}
			if facts.hasSdist {
				idx.AddFiles(pkgName, verStr, index.DistFile{
					Filename: fmt.Sprintf("%s-%s.tar.gz", pkgName, verStr),
					Kind:     index.DistKindSDist,
					Yanked:   v.Yanked,
				})
			}
		}
	}
	return idx
}

// allPackageNames returns every package name the scenario declares, in sorted
// order -- used to emulate resolver_options.prereleases (a blanket "allow
// pre-releases everywhere") through resolver.Options.AllowPrerelease, which is
// a per-package allow list. The two are equivalent exactly because no package
// outside this scenario can ever be offered.
func allPackageNames(pkgs map[string]tomlPackage) []index.PackageName {
	names := make([]string, 0, len(pkgs))
	for name := range pkgs {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]index.PackageName, len(names))
	for i, n := range names {
		out[i] = index.NewPackageName(n)
	}
	return out
}

// wheelTagFilter compiles a WheelTagFilter for target, or returns an error
// packsePlatform / tags.Target.Compile produced.
func wheelTagFilter(target tags.Target, label string) (*provider.WheelTagFilter, error) {
	matcher, err := target.Compile()
	if err != nil {
		return nil, fmt.Errorf("compile target %+v: %w", target, err)
	}
	return &provider.WheelTagFilter{Matcher: matcher, Target: label}, nil
}
