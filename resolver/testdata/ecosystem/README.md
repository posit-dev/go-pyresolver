# Ecosystem goldens

Seven real projects from uv's ecosystem suite
(`crates/uv/tests/it/ecosystem.rs` at astral-sh/uv `e7cb965`), resolved by
`TestEcosystem` and compared pin for pin with what uv locks. Tracks
rstudio/package-manager#18659.

uv's own goldens are universal lock files. go-pyresolver solves one
environment, so these goldens are single-target `uv pip compile` output.

## Per project

| Project | Upstream commit | License |
|---|---|---|
| black | psf/black `9ff047a9575f105f659043f28573e1941e9cdfb3` | MIT |
| cookiecutter | cookiecutter/cookiecutter `083dd3c6104124221e2cbc3e13e0929795861ed5` | BSD-3-Clause |
| flask | pallets/flask `06ea505ce2b2042af26e96d35ebf159af7c0869d` | BSD-3-Clause |
| httpx | encode/httpx `767cf6baa608a56d03f8fe438a39c2013904f0ae` | BSD-3-Clause |
| llm | simonw/llm `512659547241a61e30116e9ada4db34a624062ae` | Apache-2.0 |
| packse | astral-sh/packse `737bc7008fa7825669ee50e90d9d0c26df32a016` | Apache-2.0 OR MIT |
| pytest-cov | pytest-dev/pytest-cov `66c8a526b1246b5eb8fb1bc218878131bc628622` | MIT |

Each directory holds that project's license file, copied from uv's
`test/ecosystem/<project>/` and checked byte for byte against the upstream file
at the commit above. Sentry is not here: its license (FSL) is not permissive.

## Files

- `requirements.in`: `[project].dependencies` plus every
  `[project.optional-dependencies]` extra, flattened. `[dependency-groups]` are
  left out. Both uv and go-pyresolver read this one file. packse's
  `packse[index]` (in the `serve` extra) is expanded in place to
  `pypiserver>=2.0.1`.
- `golden.txt`: uv's output.
- `fixture.json`: the metadata closure go-pyresolver resolves over.

## Goldens

uv 0.12.23 (`46b84fd0b 2026-10-03`, `uv-aarch64-apple-darwin.tar.gz`, sha256
`50487ae565ccd96e499056b4674d438f4c53170202617b4c759defe0c6a1b544`, matching the
release's `.sha256` asset). In each project directory:

```sh
uv pip compile requirements.in --python-version 3.12.11 \
  --python-platform x86_64-manylinux_2_28 \
  --exclude-newer 2026-06-30T00:00:00Z \
  --no-header --no-annotate --no-cache --output-file golden.txt
```

The date is uv's own `EXCLUDE_NEWER`. The test resolves for Python 3.12.11 on
`x86_64` with glibc 2.28 wheel tags.

## Fixtures

go-pyresolver has no network index, so `fixture.json` is recorded. Regenerate:

```sh
GPR_ECOSYSTEM_GEN=1 go test ./resolver/ -run TestGenerateEcosystemFixtures -v -timeout 30m
```

The generator resolves each project over a PyPI-backed index defined in
`resolver/ecosystem_gen_test.go` only, and records exactly what the resolver
asked: every version of each package it looked at, and the metadata of each
version it inspected. Rules:

- `--exclude-newer` is applied per file. A version exists only if some file was
  uploaded before the cutoff, and only those files count toward its wheel tags
  and sdist.
- Requirements come from the wheel's PEP 658 `.metadata` file, as uv reads it,
  falling back to the JSON API. Each entry records which (`source`).
- A version is yanked when every visible file is yanked, as PyPI says today.

`TestEcosystem` replays the fixture offline. It fails with "fixture lacks ..."
if the resolver asks for something the recording does not hold.

## Expected differences

`expectedDifferences` in `resolver/ecosystem_test.go` lists each place we answer
differently from uv. An entry may cite only an existing ruling: R1
(2026-09-24, pip enforces the Requires-Python upper bound and uv does not) or R2
(2026-09-25, pre-release fallback when no final release is in range). There are
none today: all seven projects agree with uv.

## Size

367 KB of fixtures in total, 480 KB for the directory.
