// SPDX-License-Identifier: Apache-2.0 OR MIT

package index

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/posit-dev/go-python-packaging/version"
)

// IndexJSONFolder is the path segment carrying the current per-snapshot index
// documents. The name is versioned by the producer; a v1 layout ("index_json")
// predates this one and is not read here.
const IndexJSONFolder = "index_json_v2"

// Cache budget defaults.
//
// These are conservative on purpose. RFD 0001 Section 5.1 targets ~300-500MB
// for this cache, but that figure assumes headroom a small on-premises server
// does not have: PPM's Server.MemoryCacheSize has a 100MB floor on-prem
// against 4GB on P3M, and THIS cache is additional to it. Defaulting to the
// RFD's target would therefore roughly quadruple the memory floor of an
// on-prem install as a side effect of enabling resolution.
//
// So the default is small enough to be safe unconfigured, and the budget is
// explicit in CachedJSONConfig so an operator with headroom can raise it. Degradation at
// the default is graceful and quantifiable: entries are evicted
// least-recently-used, so exceeding the budget costs a repeat fetch of the
// coldest package, not a failure.
const (
	DefaultMaxCacheEntries = 512
	DefaultMaxCacheBytes   = 64 << 20 // 64 MiB
)

// CachedJSONConfig configures a CachedJSONIndex.
type CachedJSONConfig struct {
	// BaseURL is the root the index documents hang from; the request path is
	// BaseURL + "/" + IndexJSONFolder + "/" + pkg + "/" + Snapshot + ".json".
	//
	// Required. A file:// URL is NOT handled here -- see Client for how to
	// serve an air-gapped deployment.
	BaseURL string

	// Snapshot identifies which snapshot to read, e.g. "20260803T120000".
	//
	// It is fixed per index rather than per call because MetadataIndex's
	// methods take no snapshot: a resolution runs against one snapshot, and
	// letting it drift mid-resolution would let the resolver observe two
	// different worlds.
	Snapshot string

	// Client performs the requests. Defaults to a client with a 30s timeout.
	//
	// This is the injection point for anything deployment-specific: a
	// transport that reads from local disk for an air-gapped install, one that
	// adds credentials, or one that applies retries. Keeping it an
	// http.Client rather than growing options for each concern is what keeps
	// this type usable outside PPM.
	Client *http.Client

	// UserAgent is sent with each request. Empty means Go's default.
	UserAgent string

	// Origin labels which index answered, surfaced as PackageMetadata.Origin.
	// Defaults to BaseURL.
	Origin string

	// MaxCacheEntries and MaxCacheBytes bound the cache. Zero means the
	// corresponding default; negative means unbounded on that axis.
	MaxCacheEntries int
	MaxCacheBytes   int64
}

// CachedJSONIndex serves DistFile lists from the per-snapshot index JSON, with
// a bounded in-memory cache keyed by (package, snapshot).
//
// # What it does and does not serve
//
// Files is the point of this type. Versions is also served, because the same
// document lists them and answering costs nothing extra.
//
// Metadata deliberately ALWAYS returns ErrMetadataUnavailable. Dependency
// metadata must come from the resident RSF, not from here: RFD 0001 Rev 15
// reversed the carrier for the dependency fieldset precisely so resolution
// works air-gapped, and serving requires_dist from a CDN document would
// quietly reintroduce a per-package network fetch on the resolution hot path.
// Refusing is what keeps that regression from being one convenient edit away.
// Compose this with an RSF-backed index rather than using it alone.
//
// # Cache soundness
//
// An entry is only sound to cache if its key names immutable content. A
// snapshot identifier does name immutable content -- that is what a snapshot
// is -- with ONE exception: per RFD Section 5.1, a file's yanked status is
// mutable within a published snapshot. That is not a flaw in this key, it is
// an accepted property of the data, and invalidating on a yank event is
// tracked separately as rstudio/package-manager#18650. Until then a yank can
// take up to one eviction to become visible.
type CachedJSONIndex struct {
	baseURL   string
	snapshot  string
	client    *http.Client
	userAgent string
	origin    string

	cache *boundedCache[map[string][]DistFile]
}

// NewCachedJSONIndex validates cfg and returns a CachedJSONIndex.
func NewCachedJSONIndex(cfg CachedJSONConfig) (*CachedJSONIndex, error) {
	if cfg.BaseURL == "" {
		return nil, errors.New("index: CachedJSONConfig.BaseURL is required")
	}
	if _, err := url.Parse(cfg.BaseURL); err != nil {
		return nil, fmt.Errorf("index: CachedJSONConfig.BaseURL %q: %w", cfg.BaseURL, err)
	}
	if cfg.Snapshot == "" {
		return nil, errors.New("index: CachedJSONConfig.Snapshot is required")
	}

	client := cfg.Client
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}

	origin := cfg.Origin
	if origin == "" {
		origin = cfg.BaseURL
	}

	maxEntries := cfg.MaxCacheEntries
	if maxEntries == 0 {
		maxEntries = DefaultMaxCacheEntries
	} else if maxEntries < 0 {
		maxEntries = 0 // unbounded on this axis
	}

	maxBytes := cfg.MaxCacheBytes
	if maxBytes == 0 {
		maxBytes = DefaultMaxCacheBytes
	} else if maxBytes < 0 {
		maxBytes = 0
	}

	return &CachedJSONIndex{
		baseURL:   strings.TrimSuffix(cfg.BaseURL, "/"),
		snapshot:  cfg.Snapshot,
		client:    client,
		userAgent: cfg.UserAgent,
		origin:    origin,
		cache:     newBoundedCache(maxEntries, maxBytes, sizeOfReleases),
	}, nil
}

// documentURL builds the request URL for one package.
func (c *CachedJSONIndex) documentURL(pkg PackageName) string {
	return c.baseURL + "/" + IndexJSONFolder + "/" + url.PathEscape(pkg.String()) + "/" + url.PathEscape(c.snapshot) + ".json"
}

// cacheKey names the (package, snapshot) pair.
func (c *CachedJSONIndex) cacheKey(pkg PackageName) string {
	return pkg.String() + "@" + c.snapshot
}

// indexDocument is the subset of the per-snapshot document this type reads.
//
// Deliberately narrow. The producer's record carries store-side concerns --
// blocking rules, vulnerability lists, download counts -- that a resolver has
// no use for, and unmarshalling them would cost allocation per file on the
// resolution path for fields nothing reads.
type indexDocument struct {
	Releases map[string][]distributionRecord `json:"releases"`
}

type distributionRecord struct {
	Filename          string            `json:"filename"`
	URL               string            `json:"url"`
	PackageType       string            `json:"packagetype"`
	Size              int64             `json:"size"`
	Digests           map[string]string `json:"digests"`
	UploadTimeISO8601 string            `json:"upload_time_iso_8601"`
	RequiresPython    string            `json:"requires_python"`
	Yanked            bool              `json:"yanked"`
	YankedReason      string            `json:"yanked_reason"`
}

// Versions implements MetadataIndex, reading the version list from the same
// document Files uses.
func (c *CachedJSONIndex) Versions(ctx context.Context, pkg PackageName) ([]version.Version, error) {
	byVersion, err := c.releases(ctx, pkg)
	if err != nil {
		return nil, err
	}

	out := make([]version.Version, 0, len(byVersion))
	for raw := range byVersion {
		v, err := version.Parse(raw)
		if err != nil {
			// A version key the PEP 440 parser rejects is skipped rather than
			// failing the package. Third-party indexes do publish
			// non-conforming keys, and one bad key must not make every other
			// version of the package unreachable.
			continue
		}
		out = append(out, v)
	}

	return out, nil
}

// Metadata implements MetadataIndex by always reporting
// ErrMetadataUnavailable. See the type documentation: dependency metadata
// belongs to the resident RSF, and serving it from a CDN document would
// reintroduce a per-package network fetch on the resolution path and break
// air-gapped resolution.
func (c *CachedJSONIndex) Metadata(_ context.Context, pkg PackageName, ver version.Version) (PackageMetadata, error) {
	return PackageMetadata{}, fmt.Errorf(
		"index %q: %q %s: dependency metadata is served from the resident RSF, not the index JSON: %w",
		c.origin, pkg, ver, ErrMetadataUnavailable)
}

// Files implements MetadataIndex.
func (c *CachedJSONIndex) Files(ctx context.Context, pkg PackageName, ver version.Version) ([]DistFile, error) {
	byVersion, err := c.releases(ctx, pkg)
	if err != nil {
		return nil, err
	}

	files, ok := byVersion[ver.String()]
	if !ok {
		// Fall back to a normalized comparison: the document's keys are
		// whatever the publisher wrote, so "1.0" and "1.0.0" can both appear
		// and neither is wrong.
		for raw, candidate := range byVersion {
			parsed, parseErr := version.Parse(raw)
			if parseErr == nil && parsed.Equal(ver) {
				files = candidate
				ok = true
				break
			}
		}
	}
	if !ok {
		return nil, fmt.Errorf("index %q: %q %s: %w", c.origin, pkg, ver, ErrPackageNotFound)
	}

	// Copy before returning. The cache shares one slice with every reader, and
	// a consumer is entitled to sort what it is handed -- candidate selection
	// does exactly that.
	return append([]DistFile(nil), files...), nil
}

// releases returns the cached per-version file lists for pkg.
//
// The returned map is shared with the cache and MUST NOT be mutated or handed
// to a caller; the exported methods copy what they return out of it.
func (c *CachedJSONIndex) releases(ctx context.Context, pkg PackageName) (map[string][]DistFile, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	return c.cache.get(c.cacheKey(pkg), func() (map[string][]DistFile, error) {
		return c.fetch(ctx, pkg)
	})
}

// fetch retrieves and decodes the per-snapshot document for pkg.
func (c *CachedJSONIndex) fetch(ctx context.Context, pkg PackageName) (map[string][]DistFile, error) {
	target := c.documentURL(pkg)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, fmt.Errorf("index %q: building request for %q: %w", c.origin, pkg, err)
	}
	if c.userAgent != "" {
		req.Header.Set("User-Agent", c.userAgent)
	}
	// Accept-Encoding is deliberately not set, so net/http negotiates gzip and
	// transparently decompresses. Setting it by hand would make this code
	// responsible for decompression.

	res, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("index %q: fetching %q: %w", c.origin, target, err)
	}
	defer func() { _ = res.Body.Close() }()

	switch {
	case res.StatusCode == http.StatusNotFound || res.StatusCode == http.StatusGone:
		return nil, fmt.Errorf("index %q: %q: %w", c.origin, pkg, ErrPackageNotFound)
	case res.StatusCode < 200 || res.StatusCode > 299:
		return nil, fmt.Errorf("index %q: fetching %q: unexpected status %s", c.origin, target, res.Status)
	}

	body, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, fmt.Errorf("index %q: reading %q: %w", c.origin, target, err)
	}

	var doc indexDocument
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("index %q: decoding %q: %w", c.origin, target, err)
	}

	out := make(map[string][]DistFile, len(doc.Releases))
	for raw, records := range doc.Releases {
		files := make([]DistFile, 0, len(records))
		for _, rec := range records {
			files = append(files, rec.toDistFile())
		}
		out[raw] = files
	}
	return out, nil
}

// toDistFile converts one producer record.
func (r distributionRecord) toDistFile() DistFile {
	f := DistFile{
		Filename:     r.Filename,
		Location:     r.URL,
		Kind:         distKindFromPackageType(r.PackageType, r.Filename),
		Size:         r.Size,
		Yanked:       r.Yanked,
		YankedReason: r.YankedReason,
	}

	if len(r.Digests) > 0 {
		f.Hashes = make(map[string]string, len(r.Digests))
		for algo, digest := range r.Digests {
			f.Hashes[strings.ToLower(algo)] = strings.ToLower(digest)
		}
	}

	if r.UploadTimeISO8601 != "" {
		// The producer writes RFC 3339 with fractional seconds. Parsing with
		// RFC3339 accepts that; a failure leaves the zero time rather than
		// rejecting the file, since an unusable timestamp is not a reason to
		// hide a distribution that exists.
		if t, err := time.Parse(time.RFC3339, r.UploadTimeISO8601); err == nil {
			f.UploadTime = t
		}
	}

	if r.RequiresPython != "" {
		// A malformed constraint leaves RequiresPython unset, i.e.
		// unconstrained, rather than dropping the file.
		//
		// This is the lenient direction, chosen because this type is a
		// data-fidelity layer: dropping the only wheel of a package because
		// its metadata is malformed makes the package unresolvable, which is a
		// worse and much harder-to-diagnose outcome than surfacing a file
		// whose interpreter constraint could not be read. Enforcing a stricter
		// policy is candidate selection's job, which can see all the files at
		// once and choose among them.
		if specs, err := version.NewSpecifiers(r.RequiresPython); err == nil {
			f.RequiresPython = specs
		}
	}

	return f
}

// distKindFromPackageType maps the producer's packagetype to a DistKind.
//
// packagetype is authoritative when present -- it comes straight from the
// upstream index -- and the filename is only a fallback for a record that
// omits it.
func distKindFromPackageType(packageType, filename string) DistKind {
	switch strings.ToLower(packageType) {
	case "bdist_wheel":
		return DistKindWheel
	case "sdist":
		return DistKindSDist
	}

	switch {
	case strings.HasSuffix(strings.ToLower(filename), ".whl"):
		return DistKindWheel
	case strings.HasSuffix(strings.ToLower(filename), ".tar.gz"),
		strings.HasSuffix(strings.ToLower(filename), ".zip"),
		strings.HasSuffix(strings.ToLower(filename), ".tar.bz2"):
		return DistKindSDist
	}

	return DistKindUnknown
}

// sizeOfReleases approximates the retained size of one cache entry.
//
// Approximate is sufficient and honest: the budget exists to bound growth, and
// spending real time measuring an exact figure would cost more than the
// precision is worth. The constant covers the fixed part of the struct.
func sizeOfReleases(byVersion map[string][]DistFile) int64 {
	const (
		perVersionOverhead = 64
		perFileOverhead    = 256
	)

	var total int64
	for rawVersion, files := range byVersion {
		total += perVersionOverhead + int64(len(rawVersion))
		for _, f := range files {
			total += perFileOverhead
			total += int64(len(f.Filename) + len(f.Location) + len(f.YankedReason))
			for algo, digest := range f.Hashes {
				total += int64(len(algo) + len(digest))
			}
		}
	}
	return total
}

// Compile-time assertion that CachedJSONIndex satisfies the interface.
var _ MetadataIndex = (*CachedJSONIndex)(nil)
