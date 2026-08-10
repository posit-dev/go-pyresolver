// SPDX-License-Identifier: Apache-2.0 OR MIT

package index

import (
	"context"
	"fmt"
	"sync"

	"github.com/posit-dev/go-python-packaging/requirement"
	"github.com/posit-dev/go-python-packaging/version"
)

// MockIndex is an in-memory MetadataIndex for tests.
//
// It lives in a normal (non-test) file because it is consumed by the tests of
// other packages in this module -- candidate, provider, and resolver all need
// an index to drive -- and Go cannot share _test.go helpers across packages.
// It has no test-only dependencies, so this costs nothing at build time.
//
// A MockIndex is safe for concurrent use, matching the MetadataIndex contract,
// so a resolver that looks ahead in parallel can be tested against it.
//
// The mutating methods return the receiver so setup reads as a chain:
//
//	idx := NewMockIndex("test").
//	    AddVersion("flask", "3.0.0", "werkzeug>=3.0", "jinja2>=3.1").
//	    AddVersion("werkzeug", "3.0.1").
//	    AddVersion("jinja2", "3.1.2")
type MockIndex struct {
	// origin is reported as PackageMetadata.Origin.
	origin string

	mu sync.RWMutex

	// packages holds every registered package. A package present here with an
	// empty version list is what distinguishes "exists but has no acceptable
	// version" from ErrPackageNotFound.
	packages map[PackageName]*mockPackage
}

type mockPackage struct {
	// order preserves insertion order so Versions can return a deterministic
	// but deliberately unsorted result. See MockIndex.Versions.
	order []version.Version

	// versions holds per-version state, keyed by normalized version string.
	versions map[string]*mockVersion
}

type mockVersion struct {
	// metadata is nil when the version exists but its dependency metadata is
	// not retrievable -- the sdist-only-needs-a-build case, which surfaces as
	// ErrMetadataUnavailable. An explicit nil rather than a sentinel field
	// value, so the two states cannot be confused.
	metadata *PackageMetadata

	files []DistFile
}

// NewMockIndex returns an empty MockIndex. The origin string is reported as
// PackageMetadata.Origin, which matters when composing several into a
// MultiIndex and asserting which one answered.
func NewMockIndex(origin string) *MockIndex {
	return &MockIndex{
		origin:   origin,
		packages: make(map[PackageName]*mockPackage),
	}
}

// pkgLocked returns the entry for name, creating it if absent. Callers must
// hold the write lock.
func (m *MockIndex) pkgLocked(pkg PackageName) *mockPackage {
	p, ok := m.packages[pkg]
	if !ok {
		p = &mockPackage{versions: make(map[string]*mockVersion)}
		m.packages[pkg] = p
	}
	return p
}

// versionLocked returns the entry for (pkg, v), registering the version if it
// is new. Callers must hold the write lock.
func (m *MockIndex) versionLocked(pkg PackageName, v version.Version) *mockVersion {
	p := m.pkgLocked(pkg)

	key := v.String()
	mv, ok := p.versions[key]
	if !ok {
		mv = &mockVersion{}
		p.versions[key] = mv
		p.order = append(p.order, v)
	}
	return mv
}

// mustParseVersion parses ver or panics.
//
// Panicking is right here: an unparseable literal in test setup is a bug in the
// test, and an error return would be assigned to _ in practice, turning a typo
// into a silently empty index.
func mustParseVersion(method, name, ver string) version.Version {
	v, err := version.Parse(ver)
	if err != nil {
		panic(fmt.Sprintf("MockIndex.%s: bad version %q for %q: %v", method, ver, name, err))
	}
	return v
}

// AddPackage registers a package as existing with no versions.
//
// Needed to build the "package exists but has no acceptable version" case,
// which the interface distinguishes from ErrPackageNotFound and which a
// resolver must report differently -- an unknown name is probably a typo,
// whereas a known name with no usable version is a constraint conflict.
func (m *MockIndex) AddPackage(name string) *MockIndex {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.pkgLocked(NewPackageName(name))
	return m
}

// AddVersion registers one version of a package with the given dependency
// requirement strings, each parsed as PEP 508.
//
// Requirements are given as strings rather than pre-parsed values because that
// is what makes a test table readable; a test that has to construct
// requirement values loses the thing it was trying to assert.
//
// Calling it twice for the same version replaces that version's metadata and
// leaves its position in the insertion order unchanged.
func (m *MockIndex) AddVersion(name, ver string, requires ...string) *MockIndex {
	pkg := NewPackageName(name)
	v := mustParseVersion("AddVersion", name, ver)

	reqs := make([]requirement.Requirement, 0, len(requires))
	for _, raw := range requires {
		r, err := requirement.Parse(raw)
		if err != nil {
			panic(fmt.Sprintf("MockIndex.AddVersion: bad requirement %q for %s %s: %v", raw, name, ver, err))
		}
		reqs = append(reqs, r)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	mv := m.versionLocked(pkg, v)
	mv.metadata = &PackageMetadata{
		Name:         pkg,
		Version:      v,
		RequiresDist: reqs,
		Origin:       m.origin,
	}

	return m
}

// SetMetadata replaces the stored metadata for one (package, version),
// registering the version if it is new.
//
// Use this for the fields AddVersion does not reach: RequiresPython,
// ProvidesExtra, or a deliberately odd Origin.
func (m *MockIndex) SetMetadata(name, ver string, meta PackageMetadata) *MockIndex {
	pkg := NewPackageName(name)
	v := mustParseVersion("SetMetadata", name, ver)

	m.mu.Lock()
	defer m.mu.Unlock()

	// Force the identity fields to match where the entry is stored, so a
	// caller cannot file metadata under one key that claims to be another.
	meta.Name = pkg
	meta.Version = v
	if meta.Origin == "" {
		meta.Origin = m.origin
	}

	mv := m.versionLocked(pkg, v)
	mv.metadata = &meta

	return m
}

// AddFiles appends distribution files for one (package, version), registering
// the version if it is new.
//
// Registering the version matters: otherwise a test that only called AddFiles
// would get ErrPackageNotFound from Files, which looks like a bug in the code
// under test rather than in its setup. A version registered this way has no
// metadata, so Metadata reports ErrMetadataUnavailable for it.
func (m *MockIndex) AddFiles(name, ver string, files ...DistFile) *MockIndex {
	pkg := NewPackageName(name)
	v := mustParseVersion("AddFiles", name, ver)

	m.mu.Lock()
	defer m.mu.Unlock()

	mv := m.versionLocked(pkg, v)
	mv.files = append(mv.files, files...)

	return m
}

// SetUnavailable marks a (package, version) as existing but having no
// retrievable dependency metadata, so Metadata returns
// ErrMetadataUnavailable. This is the sdist-only-needs-a-build case.
//
// Any metadata previously set for the version is discarded.
func (m *MockIndex) SetUnavailable(name, ver string) *MockIndex {
	pkg := NewPackageName(name)
	v := mustParseVersion("SetUnavailable", name, ver)

	m.mu.Lock()
	defer m.mu.Unlock()

	m.versionLocked(pkg, v).metadata = nil
	return m
}

// lookup resolves pkg and ver under the read lock.
func (m *MockIndex) lookup(pkg PackageName, ver version.Version) (*mockVersion, error) {
	p, ok := m.packages[pkg]
	if !ok {
		return nil, fmt.Errorf("mock index %q: %q: %w", m.origin, pkg, ErrPackageNotFound)
	}

	mv, ok := p.versions[ver.String()]
	if !ok {
		// ⚠️ ErrMetadataUnavailable, NOT ErrPackageNotFound: the package WAS
		// found, so "package not found" is untrue on its face, and a caller
		// branching on it would report a missing package for a present one.
		//
		// This used to return ErrPackageNotFound and was the mock half of
		// rstudio/package-manager#19466's F12: the mock said not-found, RSFIndex
		// said unavailable, and the interface doc said not-found. Only one answer
		// can be right, and it is this one -- see the MetadataIndex contract for
		// why an unknown version and an uncaptured one share an error.
		return nil, fmt.Errorf("mock index %q: %q %s: %w", m.origin, pkg, ver, ErrMetadataUnavailable)
	}
	return mv, nil
}

// Versions implements MetadataIndex.
//
// It returns versions in REVERSE insertion order, which is deterministic but
// deliberately not sorted. The interface promises no ordering, and a mock that
// returned sorted versions would let an ordering assumption in a consumer pass
// here and then fail against a real index. Reverse insertion order breaks that
// assumption without the flakiness a shuffle would introduce.
func (m *MockIndex) Versions(ctx context.Context, pkg PackageName) ([]version.Version, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	p, ok := m.packages[pkg]
	if !ok {
		return nil, fmt.Errorf("mock index %q: %q: %w", m.origin, pkg, ErrPackageNotFound)
	}

	out := make([]version.Version, 0, len(p.order))
	for i := len(p.order) - 1; i >= 0; i-- {
		out = append(out, p.order[i])
	}

	return out, nil
}

// Metadata implements MetadataIndex.
func (m *MockIndex) Metadata(ctx context.Context, pkg PackageName, ver version.Version) (PackageMetadata, error) {
	if err := ctx.Err(); err != nil {
		return PackageMetadata{}, err
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	mv, err := m.lookup(pkg, ver)
	if err != nil {
		return PackageMetadata{}, err
	}
	if mv.metadata == nil {
		return PackageMetadata{}, fmt.Errorf("mock index %q: %q %s: %w", m.origin, pkg, ver, ErrMetadataUnavailable)
	}

	// Copy the slices so a caller cannot mutate the mock's state through the
	// value it was handed. A resolver that sorted RequiresDist in place would
	// otherwise silently change what later assertions see.
	out := *mv.metadata
	out.RequiresDist = append([]requirement.Requirement(nil), mv.metadata.RequiresDist...)
	out.ProvidesExtra = append([]string(nil), mv.metadata.ProvidesExtra...)

	return out, nil
}

// Files implements MetadataIndex.
func (m *MockIndex) Files(ctx context.Context, pkg PackageName, ver version.Version) ([]DistFile, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	mv, err := m.lookup(pkg, ver)
	if err != nil {
		return nil, err
	}

	return append([]DistFile(nil), mv.files...), nil
}

// Compile-time assertion that MockIndex satisfies the interface. Without this,
// a change to MetadataIndex would only surface wherever a MockIndex happened to
// be passed as one.
var _ MetadataIndex = (*MockIndex)(nil)
