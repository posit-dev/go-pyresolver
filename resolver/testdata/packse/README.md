# packse scenarios

Vendored from [astral-sh/packse](https://github.com/astral-sh/packse), commit
`18be7766b14aa3e03db37bc575959b3f4c6fbacb` (2026-08-18, after tag `0.3.59`).

## What this is

All 147 scenario TOML files under packse's `scenarios/` directory, copied verbatim
with their category subdirectories, plus `LICENSE-APACHE` and `LICENSE-MIT`.
`scenarios/examples/` (one `.toml`, one `.json`, one `.yaml`, not real test
scenarios) is left out.

## How it was copied

```sh
git clone https://github.com/astral-sh/packse /tmp/packse
git -C /tmp/packse checkout 18be7766b14aa3e03db37bc575959b3f4c6fbacb
cp -R /tmp/packse/scenarios/{backtracking,does_not_exist,excluded,extras,fork,\
incompatible_versions,local,post,prereleases,requires_python,tag_and_markers,\
wheels,yanked} resolver/testdata/packse/
cp /tmp/packse/LICENSE-APACHE /tmp/packse/LICENSE-MIT resolver/testdata/packse/
```

## Out of scope

`resolver_options.universal = true` scenarios (41 of them: all 32 of `fork/`, 5
`tag_and_markers/`, 2 `backtracking/`, 2 `wheels/`) are not run. go-pyresolver
resolves one concrete marker environment at a time (see
`resolver.Options.Environment`); universal (environment-independent)
resolution is deferred by RFD 0001. `resolver/packse_test.go`'s `outOfScope`
list names them.

## Intentional divergence

Six scenarios run against the real resolver but are asserted against pip's outcome instead of
packse's `expected` block, because go-pyresolver deliberately matches pip over packse (uv).
`resolver/packse_test.go`'s `intentionalDivergence` list names them and the pip outcome each one
must produce.

- `requires_python/python-less-than-current`: pip enforces the whole `Requires-Python` specifier,
  upper bound included; uv ignores the upper bound.
- The five `prereleases/*` scenarios where packse expects "unsatisfiable" because no final release
  is in range: pip (`packaging.specifiers.SpecifierSet.filter`, PEP 440's own recommendation) and
  current uv (astral-sh/uv#19993, "Support transitive pre-release dependencies") both fall back to
  a pre-release instead of failing. packse's `prereleases/` scenarios were last touched before that
  uv change, so they still encode the older, per-package rule.

## How to refresh

Bump the pinned commit above, re-run the copy command (all 147 files, so a
diff shows exactly what packse changed), and re-run
`go test ./resolver/ -run TestPackse -v`. A new or removed scenario fails the
"every scenario classified exactly once" check in `packse_test.go`, and a
scenario with a new `resolver_options` or per-version key fails the loader's
strict-decode check -- both by design, so update the classification lists
rather than loosening the check.
