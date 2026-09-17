# pypirsf testdata

## golden_blob.bin, golden_depsdict.bin

A tags-free deps blob and its dictionary, from the producer's own test vectors.
Predates wheel tags, so the blob has no trailing tag section — which is why it
stays tags-free: it is the pre-cutover artifact this decoder must keep reading.

## crossrepo_tags_\*.bin, crossrepo_tags_expected.json

The cross-repo wire handshake for wheel tags
([rstudio/package-manager#20578](https://github.com/rstudio/package-manager/issues/20578)).
**Copied byte-for-byte** from the producer, `rstudio/pypi-manifest`, where the
same three files live at `internal/depsblob/testdata/` and its encoder is tested
against them. `rstudio/package-manager` reads the same three at
`src/rsf/depsblob/testdata/`. All three repos therefore assert the same bytes,
which is the only thing keeping the sides of this format in step — a fixture
generated locally would prove only that this decoder agrees with itself.

| file | sha256 |
|---|---|
| `crossrepo_tags_blob.bin` | `07b118f1b275f670d2d742e0d1a814861775f3a8241db7c2620a9ddc16a03121` |
| `crossrepo_tags_tagsdict.bin` | `2ed24a0ce58bd505f1d9beff17b79d070bff04df02ef685f7fc0574babeaccd7` |
| `crossrepo_tags_expected.json` | `7012fdbcc11413fb581f4ca6ccfe685ad8f6bb45e922bbe46220ebe53c34573d` |

Source: `rstudio/pypi-manifest` `main`, verified 2026-09-17 (landed in PR #87,
`50ed48d`). The hashes match `rstudio/package-manager`'s copies at `9048219dd2`.

⚠️ **A copy is not drift detection.** Tests cannot fetch the producer's copy (no
network at test time), so if the producer's fixture changes, nothing here fails on
its own. Re-verify on any later change to the tag wire format:

```sh
gh api "repos/rstudio/pypi-manifest/contents/internal/depsblob/testdata/crossrepo_tags_blob.bin?ref=main" -q .content | base64 -d | shasum -a 256
```

`crossrepo_tags_blob.bin` and `crossrepo_tags_tagsdict.bin` are the raw
pre-compression blob body and the raw `tagsdict` RSF field. The blob needs a
format byte prepended to run through `decompress` (`0x02`, stored); see
`crossrepoTagsFixture`.
