// SPDX-License-Identifier: Apache-2.0 OR MIT

package resolver_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/posit-dev/go-pyresolver/index"
	"github.com/posit-dev/go-pyresolver/resolver"
	"github.com/posit-dev/go-python-packaging/version"
)

// ecosystemGenEnv turns the generator on. It needs network access to PyPI, so an
// ordinary run skips it; TestEcosystem never does.
//
//	GPR_ECOSYSTEM_GEN=1 go test ./resolver/ -run TestGenerateEcosystemFixtures -v -timeout 30m
const ecosystemGenEnv = "GPR_ECOSYSTEM_GEN"

// pypiFile is one file of a release in PyPI's JSON API.
type pypiFile struct {
	Filename     string `json:"filename"`
	PackageType  string `json:"packagetype"`
	UploadTime   string `json:"upload_time_iso_8601"`
	Yanked       bool   `json:"yanked"`
	URL          string `json:"url"`
	RequiresPy   string `json:"requires_python"`
	uploadedTime time.Time
}

type pypiProject struct {
	Releases map[string][]pypiFile `json:"releases"`
}

// ecosystemRecorder is a network-backed index that remembers what the resolver
// asks, so the fixture holds exactly that and nothing more.
type ecosystemRecorder struct {
	cutoff time.Time
	client *http.Client

	mu       sync.Mutex
	projects map[string]*pypiProject // by normalized name, shared across projects
	fx       ecosystemFixture
}

func (r *ecosystemRecorder) get(url string) ([]byte, int, error) {
	var lastErr error
	for attempt := 0; attempt < 6; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt) * 2 * time.Second)
		}
		req, err := http.NewRequest(http.MethodGet, url, nil)
		if err != nil {
			return nil, 0, err
		}
		req.Header.Set("User-Agent", "go-pyresolver-ecosystem-gen (+https://github.com/posit-dev/go-pyresolver)")
		resp, err := r.client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		body, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil {
			lastErr = err
			continue
		}
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			lastErr = fmt.Errorf("GET %s: %s", url, resp.Status)
			continue
		}
		return body, resp.StatusCode, nil
	}
	return nil, 0, lastErr
}

// project returns the JSON API document for pkg, or nil if PyPI has none.
func (r *ecosystemRecorder) project(pkg string) (*pypiProject, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if p, ok := r.projects[pkg]; ok {
		return p, nil
	}
	body, code, err := r.get("https://pypi.org/pypi/" + pkg + "/json")
	if err != nil {
		return nil, err
	}
	if code == http.StatusNotFound {
		r.projects[pkg] = nil
		return nil, nil
	}
	if code != http.StatusOK {
		return nil, fmt.Errorf("pypi json for %s: HTTP %d", pkg, code)
	}
	var p pypiProject
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, fmt.Errorf("pypi json for %s: %w", pkg, err)
	}
	for _, files := range p.Releases {
		for i := range files {
			ts, err := time.Parse(time.RFC3339, files[i].UploadTime)
			if err != nil {
				return nil, fmt.Errorf("pypi json for %s: %s: upload time %q: %w", pkg, files[i].Filename, files[i].UploadTime, err)
			}
			files[i].uploadedTime = ts
		}
	}
	r.projects[pkg] = &p
	return &p, nil
}

// visible returns each parseable version of p with the files uploaded before the
// cutoff. A version with no such file does not exist. A file uploaded after the
// cutoff contributes no tag and no sdist.
func (r *ecosystemRecorder) visible(p *pypiProject) map[string][]pypiFile {
	out := map[string][]pypiFile{}
	for key, files := range p.Releases {
		v, err := version.Parse(key)
		if err != nil {
			continue
		}
		for _, f := range files {
			if f.uploadedTime.Before(r.cutoff) {
				out[v.String()] = append(out[v.String()], f)
			}
		}
	}
	return out
}

// Versions implements index.MetadataIndex.
func (r *ecosystemRecorder) Versions(_ context.Context, pkg index.PackageName) ([]version.Version, error) {
	p, err := r.project(pkg.String())
	if err != nil {
		return nil, err
	}
	if p == nil {
		return nil, fmt.Errorf("%s: %w", pkg, index.ErrPackageNotFound)
	}
	vis := r.visible(p)
	vs := make([]version.Version, 0, len(vis))
	for s := range vis {
		vs = append(vs, version.MustParse(s))
	}
	sort.Slice(vs, func(i, j int) bool { return vs[i].Compare(vs[j]) < 0 })

	r.mu.Lock()
	defer r.mu.Unlock()
	rec := r.packageLocked(pkg.String())
	rec.Versions = rec.Versions[:0]
	for _, v := range vs {
		rec.Versions = append(rec.Versions, v.String())
	}
	return vs, nil
}

func (r *ecosystemRecorder) packageLocked(name string) *ecosystemPackage {
	p, ok := r.fx.Packages[name]
	if !ok {
		p = &ecosystemPackage{Metadata: map[string]ecosystemVersion{}}
		r.fx.Packages[name] = p
	}
	return p
}

// Metadata implements index.MetadataIndex.
func (r *ecosystemRecorder) Metadata(
	_ context.Context, pkg index.PackageName, ver version.Version,
) (index.PackageMetadata, error) {
	p, err := r.project(pkg.String())
	if err != nil {
		return index.PackageMetadata{}, err
	}
	if p == nil {
		return index.PackageMetadata{}, fmt.Errorf("%s: %w", pkg, index.ErrPackageNotFound)
	}
	files := r.visible(p)[ver.String()]
	if len(files) == 0 {
		return index.PackageMetadata{}, fmt.Errorf("%s %s: %w", pkg, ver, index.ErrMetadataUnavailable)
	}

	rec, err := r.record(pkg.String(), ver.String(), files)
	if err != nil {
		return index.PackageMetadata{}, err
	}

	r.mu.Lock()
	r.packageLocked(pkg.String()).Metadata[ver.String()] = rec
	r.mu.Unlock()

	if rec.Unusable != "" {
		return index.PackageMetadata{}, fmt.Errorf("%s %s: %s: %w", pkg, ver, rec.Unusable, index.ErrMetadataUnusable)
	}
	meta, err := index.ParseRecord(index.RawRecord{
		RequiresDist:   rec.RequiresDist,
		RequiresPython: rec.RequiresPython,
		ProvidesExtra:  rec.ProvidesExtra,
		WheelTags:      rec.WheelTags,
		HasSdist:       rec.HasSdist,
		TagsCaptured:   true,
		Yanked:         rec.Yanked,
	})
	if err != nil {
		return index.PackageMetadata{}, err
	}
	meta.Name, meta.Version = pkg, ver
	return meta, nil
}

// record builds the fixture entry for one version from its visible files.
func (r *ecosystemRecorder) record(pkg, ver string, files []pypiFile) (ecosystemVersion, error) {
	sort.Slice(files, func(i, j int) bool { return files[i].Filename < files[j].Filename })

	rec := ecosystemVersion{Yanked: true}
	tagSet := map[string]bool{}
	var wheels, sdists []pypiFile
	for _, f := range files {
		rec.Yanked = rec.Yanked && f.Yanked
		switch {
		case strings.HasSuffix(f.Filename, ".whl"):
			wheels = append(wheels, f)
			for _, tag := range wheelFileTags(f.Filename) {
				tagSet[tag] = true
			}
		case f.PackageType == "sdist":
			sdists = append(sdists, f)
			rec.HasSdist = true
		}
	}
	for tag := range tagSet {
		rec.WheelTags = append(rec.WheelTags, tag)
	}
	sort.Strings(rec.WheelTags)

	// PEP 658 metadata is what uv reads. Wheels first; PyPI serves it for
	// wheels and for few sdists.
	for _, f := range append(wheels, sdists...) {
		body, code, err := r.get(f.URL + ".metadata")
		if err != nil {
			return rec, err
		}
		if code != http.StatusOK {
			continue
		}
		rec.RequiresDist, rec.RequiresPython, rec.ProvidesExtra = parseCoreMetadata(string(body))
		rec.Source = "pep658"
		break
	}
	if rec.Source == "" {
		body, code, err := r.get("https://pypi.org/pypi/" + pkg + "/" + ver + "/json")
		if err != nil {
			return rec, err
		}
		if code != http.StatusOK {
			// The release key may not be the normalized spelling.
			return rec, fmt.Errorf("%s %s: no PEP 658 metadata and JSON API says HTTP %d", pkg, ver, code)
		}
		var doc struct {
			Info struct {
				RequiresDist  []string `json:"requires_dist"`
				RequiresPy    string   `json:"requires_python"`
				ProvidesExtra []string `json:"provides_extra"`
			} `json:"info"`
		}
		if err := json.Unmarshal(body, &doc); err != nil {
			return rec, fmt.Errorf("%s %s: %w", pkg, ver, err)
		}
		rec.RequiresDist, rec.RequiresPython, rec.ProvidesExtra = doc.Info.RequiresDist, doc.Info.RequiresPy, doc.Info.ProvidesExtra
		rec.Source = "json-api"
	}

	if _, err := index.ParseRecord(index.RawRecord{
		RequiresDist:   rec.RequiresDist,
		RequiresPython: rec.RequiresPython,
		ProvidesExtra:  rec.ProvidesExtra,
		WheelTags:      rec.WheelTags,
		HasSdist:       rec.HasSdist,
		TagsCaptured:   true,
	}); err != nil {
		rec.Unusable = err.Error()
	}
	return rec, nil
}

// wheelFileTags returns the PEP 425 triples of a wheel filename. A compressed
// tag set ("py2.py3-none-any") is expanded into one triple per combination.
func wheelFileTags(filename string) []string {
	parts := strings.Split(strings.TrimSuffix(filename, ".whl"), "-")
	if len(parts) < 5 {
		return nil
	}
	pys := strings.Split(parts[len(parts)-3], ".")
	abis := strings.Split(parts[len(parts)-2], ".")
	plats := strings.Split(parts[len(parts)-1], ".")
	var out []string
	for _, py := range pys {
		for _, abi := range abis {
			for _, plat := range plats {
				out = append(out, py+"-"+abi+"-"+plat)
			}
		}
	}
	return out
}

// parseCoreMetadata reads the three fields the resolver uses from a core
// metadata (METADATA) document. Only the header block counts.
func parseCoreMetadata(doc string) (requiresDist []string, requiresPython string, providesExtra []string) {
	sc := bufio.NewScanner(strings.NewReader(doc))
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		if line == "" {
			break
		}
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		val = strings.TrimSpace(val)
		switch strings.ToLower(key) {
		case "requires-dist":
			requiresDist = append(requiresDist, val)
		case "requires-python":
			requiresPython = val
		case "provides-extra":
			providesExtra = append(providesExtra, val)
		}
	}
	return requiresDist, requiresPython, providesExtra
}

func TestGenerateEcosystemFixtures(t *testing.T) {
	if os.Getenv(ecosystemGenEnv) == "" {
		t.Skipf("set %s=1 to regenerate %s/*/fixture.json from PyPI (needs network)", ecosystemGenEnv, ecosystemDir)
	}
	cutoff, err := time.Parse(time.RFC3339, ecosystemExcludeNewer)
	if err != nil {
		t.Fatal(err)
	}
	projects := map[string]*pypiProject{}
	for _, project := range ecosystemProjects {
		rec := &ecosystemRecorder{
			cutoff: cutoff, client: &http.Client{Timeout: 2 * time.Minute},
			projects: projects, fx: ecosystemFixture{Packages: map[string]*ecosystemPackage{}},
		}
		dir := filepath.Join(ecosystemDir, project)
		roots := mustRequirements(t, readLines(t, filepath.Join(dir, "requirements.in"))...)

		py := pythonSpec{full: ecosystemPython, major: 3, minor: 12}
		target, err := buildTarget(py, nil)
		if err != nil {
			t.Fatal(err)
		}
		env, err := buildEnvironment(target, py)
		if err != nil {
			t.Fatal(err)
		}
		filter, err := wheelTagFilter(target, "cp312 on linux/x86_64")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := resolver.Resolve(context.Background(), roots, rec, resolver.Options{
			Environment:   env,
			PythonVersion: version.MustParse(py.full),
			WheelTags:     filter,
		}); err != nil {
			t.Fatalf("%s: Resolve: %v", project, err)
		}

		out, err := json.MarshalIndent(rec.fx, "", " ")
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, "fixture.json")
		if err := os.WriteFile(path, append(out, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("%s: wrote %s (%d packages, %d bytes)", project, path, len(rec.fx.Packages), len(out))
	}
}

// WheelTagsComplete and YanksCaptured are checked by Resolve before it filters.
func (*ecosystemRecorder) WheelTagsComplete() bool { return true }
func (*ecosystemRecorder) YanksCaptured() bool     { return true }

// Files implements index.MetadataIndex. The resolver does not call it.
func (*ecosystemRecorder) Files(context.Context, index.PackageName, version.Version) ([]index.DistFile, error) {
	return nil, errors.New("ecosystem recorder: Files is not used by the resolver")
}

var _ index.MetadataIndex = (*ecosystemRecorder)(nil)
