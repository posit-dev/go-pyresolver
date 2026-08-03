// SPDX-License-Identifier: Apache-2.0 OR MIT

package index

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/posit-dev/go-python-packaging/version"
)

// clickDocument is a trimmed excerpt of a real per-snapshot document, taken
// from PPM's own fixture at
// test/bats/localpython_e2e/pypi/index_json_v2/click/20210710T082015.json.
//
// Using the real producer's bytes rather than a hand-written approximation is
// deliberate: the field spellings (upload_time_iso_8601, packagetype, digests
// as a map) and the JSON null in yanked_reason are exactly the details a
// hand-rolled fixture gets subtly wrong.
const clickDocument = `{
  "info": {"name": "click", "version": "7.1.2"},
  "last_serial": 7175136,
  "releases": {
    "7.1.2": [
      {
        "comment_text": "",
        "digests": {
          "md5": "B4233221CACC473ACD422A1D54FF4C41",
          "sha256": "DACCA89F4BFADD5DE3D7489B7C8A566EEE0D3676333FBB50030263894C38C0DC"
        },
        "downloads": -1,
        "filename": "click-7.1.2-py2.py3-none-any.whl",
        "has_sig": true,
        "md5_digest": "b4233221cacc473acd422a1d54ff4c41",
        "packagetype": "bdist_wheel",
        "python_version": "py2.py3",
        "requires_python": ">=2.7, !=3.0.*, !=3.1.*, !=3.2.*, !=3.3.*, !=3.4.*",
        "size": 82780,
        "upload_time": "2020-04-27T20:22:42",
        "upload_time_iso_8601": "2020-04-27T20:22:42.629571Z",
        "url": "https://files.pythonhosted.org/packages/d2/3d/click-7.1.2-py2.py3-none-any.whl",
        "yanked": false,
        "yanked_reason": null
      },
      {
        "comment_text": "",
        "digests": {"md5": "53692f62cb99a1a10c59248f1776d9c0", "sha256": "d2b5255c7c6349bc1bd1e59e08cd12acbbd63ce649f2588755783aa94dfb6b1a"},
        "downloads": -1,
        "filename": "click-7.1.2.tar.gz",
        "has_sig": true,
        "packagetype": "sdist",
        "python_version": "source",
        "requires_python": ">=2.7, !=3.0.*, !=3.1.*, !=3.2.*, !=3.3.*, !=3.4.*",
        "size": 297279,
        "upload_time_iso_8601": "2020-04-27T20:22:45.014623Z",
        "url": "https://files.pythonhosted.org/packages/27/6f/click-7.1.2.tar.gz",
        "yanked": false,
        "yanked_reason": null
      }
    ],
    "0.1": [
      {
        "digests": {"sha256": "aaa"},
        "filename": "click-0.1-py2.py3-none-any.whl",
        "packagetype": "bdist_wheel",
        "size": 100,
        "upload_time_iso_8601": "2014-01-01T00:00:00Z",
        "url": "https://files.pythonhosted.org/packages/aa/click-0.1-py2.py3-none-any.whl",
        "yanked": true,
        "yanked_reason": "friends with Mitch McConnell - yanked file"
      }
    ]
  }
}`

// newTestIndex serves the given body for every request and returns the index
// plus a counter of requests actually made.
func newTestIndex(t *testing.T, body string, cfg CachedJSONConfig) (*CachedJSONIndex, *atomic.Int64, *httptest.Server) {
	t.Helper()

	var requests atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	cfg.BaseURL = srv.URL
	if cfg.Snapshot == "" {
		cfg.Snapshot = "20210710T082015"
	}
	cfg.Client = srv.Client()

	idx, err := NewCachedJSONIndex(cfg)
	if err != nil {
		t.Fatalf("NewCachedJSONIndex: %v", err)
	}
	return idx, &requests, srv
}

func TestCachedJSONIndexParsesRealProducerDocument(t *testing.T) {
	idx, _, _ := newTestIndex(t, clickDocument, CachedJSONConfig{})

	files, err := idx.Files(context.Background(), NewPackageName("click"), mustVersion(t, "7.1.2"))
	if err != nil {
		t.Fatalf("Files: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("got %d files, want 2 (one wheel, one sdist)", len(files))
	}

	sort.Slice(files, func(i, j int) bool { return files[i].Filename < files[j].Filename })
	wheel, sdist := files[0], files[1]

	if wheel.Filename != "click-7.1.2-py2.py3-none-any.whl" || !wheel.IsWheel() {
		t.Errorf("wheel = %q kind=%v, want the .whl classified as a wheel", wheel.Filename, wheel.Kind)
	}
	if sdist.Kind != DistKindSDist {
		t.Errorf("sdist kind = %v, want DistKindSDist", sdist.Kind)
	}
	if wheel.Location != "https://files.pythonhosted.org/packages/d2/3d/click-7.1.2-py2.py3-none-any.whl" {
		t.Errorf("Location = %q", wheel.Location)
	}
	if wheel.Size != 82780 {
		t.Errorf("Size = %d, want 82780", wheel.Size)
	}

	// Digests arrive as a map and are lowercased on both sides, so a
	// case-varying producer cannot produce two spellings of one digest.
	if got := wheel.Hashes["sha256"]; got != "dacca89f4bfadd5de3d7489b7c8a566eee0d3676333fbb50030263894c38c0dc" {
		t.Errorf("sha256 = %q, want it lowercased", got)
	}
	if _, ok := wheel.Hashes["md5"]; !ok {
		t.Error("md5 digest missing; all published digests should be carried")
	}

	// upload_time_iso_8601 carries fractional seconds.
	want := time.Date(2020, 4, 27, 20, 22, 42, 629571000, time.UTC)
	if !wheel.UploadTime.Equal(want) {
		t.Errorf("UploadTime = %v, want %v", wheel.UploadTime, want)
	}

	// A comma-separated requires_python must parse as a specifier set.
	if wheel.RequiresPython.String() == "" {
		t.Error("RequiresPython did not parse; the producer publishes a comma-separated set here")
	}
	if wheel.RequiresPython.Check(mustVersion(t, "3.0.1")) {
		t.Error("requires_python excludes 3.0.*, so 3.0.1 must not satisfy it")
	}
	if !wheel.RequiresPython.Check(mustVersion(t, "3.9")) {
		t.Error("requires_python should admit 3.9")
	}
}

// TestCachedJSONIndexYankedFields covers the one mutable field in an otherwise
// immutable snapshot, and the JSON null the producer writes for absent reasons.
func TestCachedJSONIndexYankedFields(t *testing.T) {
	idx, _, _ := newTestIndex(t, clickDocument, CachedJSONConfig{})
	ctx := context.Background()

	yanked, err := idx.Files(ctx, NewPackageName("click"), mustVersion(t, "0.1"))
	if err != nil {
		t.Fatalf("Files: %v", err)
	}
	if len(yanked) != 1 {
		t.Fatalf("got %d files, want 1", len(yanked))
	}
	if !yanked[0].Yanked {
		t.Error("Yanked = false, want true")
	}
	if yanked[0].YankedReason != "friends with Mitch McConnell - yanked file" {
		t.Errorf("YankedReason = %q", yanked[0].YankedReason)
	}

	// "yanked_reason": null must decode to "" rather than failing the document.
	live, err := idx.Files(ctx, NewPackageName("click"), mustVersion(t, "7.1.2"))
	if err != nil {
		t.Fatalf("Files: %v", err)
	}
	for _, f := range live {
		if f.Yanked || f.YankedReason != "" {
			t.Errorf("%q: Yanked=%v reason=%q, want false/empty", f.Filename, f.Yanked, f.YankedReason)
		}
	}
}

func TestCachedJSONIndexVersions(t *testing.T) {
	idx, _, _ := newTestIndex(t, clickDocument, CachedJSONConfig{})

	versions, err := idx.Versions(context.Background(), NewPackageName("click"))
	if err != nil {
		t.Fatalf("Versions: %v", err)
	}

	got := make([]string, 0, len(versions))
	for _, v := range versions {
		got = append(got, v.String())
	}
	sort.Strings(got)

	if len(got) != 2 || got[0] != "0.1" || got[1] != "7.1.2" {
		t.Errorf("versions = %v, want [0.1 7.1.2]", got)
	}
}

// TestCachedJSONIndexMetadataAlwaysRefuses pins the deliberate refusal. If this
// ever starts answering, dependency resolution has quietly acquired a
// per-package network fetch and air-gapped resolution is broken.
func TestCachedJSONIndexMetadataAlwaysRefuses(t *testing.T) {
	idx, requests, _ := newTestIndex(t, clickDocument, CachedJSONConfig{})

	_, err := idx.Metadata(context.Background(), NewPackageName("click"), mustVersion(t, "7.1.2"))
	if !errors.Is(err, ErrMetadataUnavailable) {
		t.Fatalf("got %v, want ErrMetadataUnavailable", err)
	}
	if n := requests.Load(); n != 0 {
		t.Errorf("Metadata made %d HTTP requests, want 0 -- it must not even try", n)
	}
}

func TestCachedJSONIndexCachesByPackageAndSnapshot(t *testing.T) {
	idx, requests, _ := newTestIndex(t, clickDocument, CachedJSONConfig{})
	ctx := context.Background()
	pkg := NewPackageName("click")

	for range 5 {
		if _, err := idx.Files(ctx, pkg, mustVersion(t, "7.1.2")); err != nil {
			t.Fatalf("Files: %v", err)
		}
	}
	if _, err := idx.Versions(ctx, pkg); err != nil {
		t.Fatalf("Versions: %v", err)
	}

	if n := requests.Load(); n != 1 {
		t.Errorf("made %d requests for one (package, snapshot), want 1", n)
	}
}

// TestCachedJSONIndexSingleflightCoalesces is the -race test the issue calls
// for. Singleflight hands the SAME value to every coalesced waiter, so a
// consumer mutating what it received would corrupt the shared entry. Every
// goroutine here sorts its own result, which is what a real candidate-selection
// layer does.
func TestCachedJSONIndexSingleflightCoalescesAndIsolates(t *testing.T) {
	var requests atomic.Int64
	release := make(chan struct{})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		<-release // hold every request open so the flights overlap
		_, _ = w.Write([]byte(clickDocument))
	}))
	defer srv.Close()

	idx, err := NewCachedJSONIndex(CachedJSONConfig{
		BaseURL:  srv.URL,
		Snapshot: "20210710T082015",
		Client:   srv.Client(),
	})
	if err != nil {
		t.Fatalf("NewCachedJSONIndex: %v", err)
	}

	ctx := context.Background()
	pkg := NewPackageName("click")
	v := mustVersion(t, "7.1.2")

	const goroutines = 24
	var wg sync.WaitGroup
	results := make([][]DistFile, goroutines)

	for i := range goroutines {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			files, err := idx.Files(ctx, pkg, v)
			if err != nil {
				t.Errorf("Files: %v", err)
				return
			}
			// Mutate: reverse-sort in place. If the cache leaked one shared
			// slice, this races with every sibling goroutine and corrupts the
			// entry -- exactly the bug -race exists to catch.
			sort.Slice(files, func(a, b int) bool { return files[a].Filename > files[b].Filename })
			results[i] = files
		}(i)
	}

	// Let the goroutines pile into the flight before answering.
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()

	if n := requests.Load(); n != 1 {
		t.Errorf("made %d requests, want 1 (singleflight should coalesce)", n)
	}

	for i, files := range results {
		if len(files) != 2 {
			t.Fatalf("goroutine %d got %d files, want 2", i, len(files))
		}
	}

	// The cached entry must still be intact after all that mutation.
	after, err := idx.Files(ctx, pkg, v)
	if err != nil {
		t.Fatalf("Files after concurrent mutation: %v", err)
	}
	if len(after) != 2 {
		t.Errorf("cached entry now has %d files, want 2", len(after))
	}
}

// TestCachedJSONIndexReturnsCopies is the non-concurrent statement of the same
// property, so a failure points at the copy rather than at a data race.
func TestCachedJSONIndexReturnsCopies(t *testing.T) {
	idx, _, _ := newTestIndex(t, clickDocument, CachedJSONConfig{})
	ctx := context.Background()
	pkg := NewPackageName("click")
	v := mustVersion(t, "7.1.2")

	first, err := idx.Files(ctx, pkg, v)
	if err != nil {
		t.Fatalf("Files: %v", err)
	}
	first[0].Filename = "mutated.whl"

	second, err := idx.Files(ctx, pkg, v)
	if err != nil {
		t.Fatalf("Files: %v", err)
	}
	for _, f := range second {
		if f.Filename == "mutated.whl" {
			t.Error("mutating a returned DistFile changed the cached entry")
		}
	}
}

func TestCachedJSONIndexNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()

	idx, err := NewCachedJSONIndex(CachedJSONConfig{BaseURL: srv.URL, Snapshot: "s", Client: srv.Client()})
	if err != nil {
		t.Fatalf("NewCachedJSONIndex: %v", err)
	}

	if _, err := idx.Versions(context.Background(), NewPackageName("nope")); !errors.Is(err, ErrPackageNotFound) {
		t.Errorf("got %v, want ErrPackageNotFound", err)
	}
}

func TestCachedJSONIndexUnknownVersionIsNotFound(t *testing.T) {
	idx, _, _ := newTestIndex(t, clickDocument, CachedJSONConfig{})

	_, err := idx.Files(context.Background(), NewPackageName("click"), mustVersion(t, "99.0"))
	if !errors.Is(err, ErrPackageNotFound) {
		t.Errorf("got %v, want ErrPackageNotFound", err)
	}
}

// TestCachedJSONIndexMatchesEquivalentVersionSpelling covers a document key
// that is PEP 440-equal to the requested version but spelled differently. The
// producer writes whatever the publisher used, and "1.0" vs "1.0.0" are the
// same version.
func TestCachedJSONIndexMatchesEquivalentVersionSpelling(t *testing.T) {
	doc := `{"releases": {"1.0": [{"filename": "p-1.0.tar.gz", "packagetype": "sdist", "url": "https://e/p-1.0.tar.gz"}]}}`
	idx, _, _ := newTestIndex(t, doc, CachedJSONConfig{})

	files, err := idx.Files(context.Background(), NewPackageName("p"), mustVersion(t, "1.0.0"))
	if err != nil {
		t.Fatalf("Files for the equivalent spelling 1.0.0: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("got %d files, want 1", len(files))
	}
}

func TestCachedJSONIndexServerErrorIsNotSwallowed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	idx, err := NewCachedJSONIndex(CachedJSONConfig{BaseURL: srv.URL, Snapshot: "s", Client: srv.Client()})
	if err != nil {
		t.Fatalf("NewCachedJSONIndex: %v", err)
	}

	_, err = idx.Files(context.Background(), NewPackageName("p"), mustVersion(t, "1.0"))
	if err == nil {
		t.Fatal("expected an error for a 500")
	}
	// A 5xx must NOT look like "no such package": one is retryable, the other
	// tells a resolver to give up on the package.
	if errors.Is(err, ErrPackageNotFound) {
		t.Error("a 500 must not be reported as ErrPackageNotFound")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("error should name the status: %v", err)
	}
}

func TestCachedJSONIndexMalformedJSON(t *testing.T) {
	idx, _, _ := newTestIndex(t, `{"releases": [`, CachedJSONConfig{})

	if _, err := idx.Versions(context.Background(), NewPackageName("p")); err == nil {
		t.Error("expected an error for malformed JSON")
	}
}

// TestCachedJSONIndexToleratesBadPerFileMetadata pins the lenient direction,
// which is a judgment call worth making explicit: a file whose requires_python
// cannot be parsed is surfaced unconstrained rather than dropped, because
// dropping a package's only wheel makes it unresolvable for a reason nobody can
// see. Stricter policy belongs to candidate selection.
func TestCachedJSONIndexToleratesBadPerFileMetadata(t *testing.T) {
	doc := `{"releases": {"1.0": [
	  {"filename": "p-1.0-py3-none-any.whl", "packagetype": "bdist_wheel", "url": "https://e/p.whl",
	   "requires_python": "not a specifier", "upload_time_iso_8601": "never"}
	]}}`
	idx, _, _ := newTestIndex(t, doc, CachedJSONConfig{})

	files, err := idx.Files(context.Background(), NewPackageName("p"), mustVersion(t, "1.0"))
	if err != nil {
		t.Fatalf("Files: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("got %d files, want 1 -- a bad field must not drop the file", len(files))
	}
	if files[0].RequiresPython.String() != "" {
		t.Errorf("RequiresPython = %q, want unset", files[0].RequiresPython)
	}
	if !files[0].UploadTime.IsZero() {
		t.Errorf("UploadTime = %v, want the zero time", files[0].UploadTime)
	}
}

// TestCachedJSONIndexSkipsUnparseableVersionKeys checks one bad version key
// does not hide the rest of the package.
func TestCachedJSONIndexSkipsUnparseableVersionKeys(t *testing.T) {
	doc := `{"releases": {"1.0": [], "not-a-version": [], "2.0": []}}`
	idx, _, _ := newTestIndex(t, doc, CachedJSONConfig{})

	versions, err := idx.Versions(context.Background(), NewPackageName("p"))
	if err != nil {
		t.Fatalf("Versions: %v", err)
	}
	if len(versions) != 2 {
		t.Errorf("got %d versions, want 2 (the unparseable key skipped)", len(versions))
	}
}

func TestCachedJSONIndexHonorsContextCancellation(t *testing.T) {
	idx, requests, _ := newTestIndex(t, clickDocument, CachedJSONConfig{})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := idx.Files(ctx, NewPackageName("click"), mustVersion(t, "1.0")); !errors.Is(err, context.Canceled) {
		t.Errorf("Files: got %v, want context.Canceled", err)
	}
	if n := requests.Load(); n != 0 {
		t.Errorf("made %d requests on a cancelled context, want 0", n)
	}
}

func TestCachedJSONIndexRequestShape(t *testing.T) {
	var gotPath, gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotUA = r.Header.Get("User-Agent")
		_, _ = w.Write([]byte(clickDocument))
	}))
	defer srv.Close()

	idx, err := NewCachedJSONIndex(CachedJSONConfig{
		BaseURL:   srv.URL + "/", // trailing slash must not double up
		Snapshot:  "20210710T082015",
		Client:    srv.Client(),
		UserAgent: "test-agent/1.0",
	})
	if err != nil {
		t.Fatalf("NewCachedJSONIndex: %v", err)
	}

	if _, err := idx.Versions(context.Background(), NewPackageName("Click")); err != nil {
		t.Fatalf("Versions: %v", err)
	}

	// The name is normalized before it reaches the URL, and the folder is the
	// v2 layout.
	want := "/" + IndexJSONFolder + "/click/20210710T082015.json"
	if gotPath != want {
		t.Errorf("request path = %q, want %q", gotPath, want)
	}
	if gotUA != "test-agent/1.0" {
		t.Errorf("User-Agent = %q, want %q", gotUA, "test-agent/1.0")
	}
}

func TestNewCachedJSONIndexValidation(t *testing.T) {
	if _, err := NewCachedJSONIndex(CachedJSONConfig{Snapshot: "s"}); err == nil {
		t.Error("expected an error when BaseURL is empty")
	}
	if _, err := NewCachedJSONIndex(CachedJSONConfig{BaseURL: "https://e"}); err == nil {
		t.Error("expected an error when Snapshot is empty")
	}
}

func TestCachedJSONIndexOriginDefaultsToBaseURL(t *testing.T) {
	idx, err := NewCachedJSONIndex(CachedJSONConfig{BaseURL: "https://example.com/pypi", Snapshot: "s"})
	if err != nil {
		t.Fatalf("NewCachedJSONIndex: %v", err)
	}

	// Origin surfaces in the refusal message, which is the one place it is
	// observable on this type.
	_, err = idx.Metadata(context.Background(), NewPackageName("p"), mustVersion(t, "1.0"))
	if !strings.Contains(err.Error(), "https://example.com/pypi") {
		t.Errorf("error should carry the default Origin: %v", err)
	}
}

func TestDistKindFromPackageType(t *testing.T) {
	for _, tc := range []struct {
		packageType, filename string
		want                  DistKind
	}{
		{"bdist_wheel", "p-1.0-py3-none-any.whl", DistKindWheel},
		{"sdist", "p-1.0.tar.gz", DistKindSDist},
		{"BDIST_WHEEL", "p.whl", DistKindWheel},
		// packagetype is authoritative when present, even against the filename.
		{"sdist", "p-1.0-py3-none-any.whl", DistKindSDist},
		// Fall back to the filename only when packagetype is absent.
		{"", "p-1.0-py3-none-any.whl", DistKindWheel},
		{"", "p-1.0.tar.gz", DistKindSDist},
		{"", "p-1.0.zip", DistKindSDist},
		{"", "p-1.0.tar.bz2", DistKindSDist},
		{"", "p-1.0.exe", DistKindUnknown},
		{"bdist_egg", "p-1.0.egg", DistKindUnknown},
	} {
		if got := distKindFromPackageType(tc.packageType, tc.filename); got != tc.want {
			t.Errorf("distKindFromPackageType(%q, %q) = %v, want %v",
				tc.packageType, tc.filename, got, tc.want)
		}
	}
}

// --- boundedCache ---

func TestBoundedCacheEvictsByEntryCount(t *testing.T) {
	c := newBoundedCache(2, 0, func(v string) int64 { return int64(len(v)) })

	for _, k := range []string{"a", "b", "c"} {
		if _, err := c.get(k, func() (string, error) { return k, nil }); err != nil {
			t.Fatalf("get(%q): %v", k, err)
		}
	}

	if entries, _ := c.stats(); entries != 2 {
		t.Errorf("cache holds %d entries, want 2", entries)
	}
	if _, ok := c.lookup("a"); ok {
		t.Error("oldest entry should have been evicted")
	}
	if _, ok := c.lookup("c"); !ok {
		t.Error("newest entry should be present")
	}
}

func TestBoundedCacheEvictsByBytes(t *testing.T) {
	c := newBoundedCache(0, 10, func(v string) int64 { return int64(len(v)) })

	for _, k := range []string{"aaaa", "bbbb", "cccc"} {
		if _, err := c.get(k, func() (string, error) { return k, nil }); err != nil {
			t.Fatalf("get: %v", err)
		}
	}

	_, bytes := c.stats()
	if bytes > 10 {
		t.Errorf("cache holds %d bytes, want <= 10", bytes)
	}
}

// TestBoundedCacheRejectsOversizedEntry pins that an entry larger than the
// whole budget is not cached, rather than evicting everything and still not
// fitting.
func TestBoundedCacheRejectsOversizedEntry(t *testing.T) {
	c := newBoundedCache(0, 4, func(v string) int64 { return int64(len(v)) })

	if _, err := c.get("big", func() (string, error) { return "waytoolong", nil }); err != nil {
		t.Fatalf("get: %v", err)
	}

	if entries, bytes := c.stats(); entries != 0 || bytes != 0 {
		t.Errorf("cache holds %d entries / %d bytes, want 0/0", entries, bytes)
	}
}

func TestBoundedCacheLRUPromotesOnRead(t *testing.T) {
	c := newBoundedCache(2, 0, func(v string) int64 { return 1 })

	for _, k := range []string{"a", "b"} {
		if _, err := c.get(k, func() (string, error) { return k, nil }); err != nil {
			t.Fatalf("get: %v", err)
		}
	}
	// Touch "a" so "b" becomes least-recently-used.
	if _, ok := c.lookup("a"); !ok {
		t.Fatal("a should be present")
	}
	if _, err := c.get("c", func() (string, error) { return "c", nil }); err != nil {
		t.Fatalf("get: %v", err)
	}

	if _, ok := c.lookup("a"); !ok {
		t.Error("a was touched and should have survived")
	}
	if _, ok := c.lookup("b"); ok {
		t.Error("b was least-recently-used and should have been evicted")
	}
}

func TestBoundedCacheBuildErrorIsNotCached(t *testing.T) {
	c := newBoundedCache(4, 0, func(v string) int64 { return 1 })

	var calls atomic.Int64
	build := func() (string, error) {
		calls.Add(1)
		return "", fmt.Errorf("boom")
	}

	for range 3 {
		if _, err := c.get("k", build); err == nil {
			t.Fatal("expected the build error")
		}
	}

	if n := calls.Load(); n != 3 {
		t.Errorf("build called %d times, want 3 -- a failure must not be cached", n)
	}
	if entries, _ := c.stats(); entries != 0 {
		t.Errorf("cache holds %d entries after failures, want 0", entries)
	}
}

func TestBoundedCacheUnboundedWhenNegative(t *testing.T) {
	idx, err := NewCachedJSONIndex(CachedJSONConfig{
		BaseURL:         "https://example.com",
		Snapshot:        "s",
		MaxCacheEntries: -1,
		MaxCacheBytes:   -1,
	})
	if err != nil {
		t.Fatalf("NewCachedJSONIndex: %v", err)
	}
	if idx.cache.maxEntries != 0 || idx.cache.maxBytes != 0 {
		t.Errorf("negative budgets should map to unbounded (0), got %d/%d",
			idx.cache.maxEntries, idx.cache.maxBytes)
	}
}

func TestSizeOfReleasesGrowsWithContent(t *testing.T) {
	small := map[string][]DistFile{"1.0": {{Filename: "a.whl"}}}
	large := map[string][]DistFile{
		"1.0": {{Filename: "a.whl", Location: strings.Repeat("x", 500), Hashes: map[string]string{"sha256": strings.Repeat("y", 64)}}},
	}

	if sizeOfReleases(large) <= sizeOfReleases(small) {
		t.Errorf("sizer must grow with content: small=%d large=%d",
			sizeOfReleases(small), sizeOfReleases(large))
	}
	if sizeOfReleases(nil) != 0 {
		t.Errorf("sizeOfReleases(nil) = %d, want 0", sizeOfReleases(nil))
	}
}

// TestProducerRequiresPythonFormParses guards that the version library still
// parses the comma-separated form the producer publishes, since the
// requires_python assertions above depend on it.
func TestProducerRequiresPythonFormParses(t *testing.T) {
	if _, err := version.NewSpecifiers(">=2.7, !=3.0.*, !=3.1.*"); err != nil {
		t.Fatalf("the producer's comma-separated requires_python form must parse: %v", err)
	}
}
