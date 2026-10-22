# Changelog

## [0.0.10](https://github.com/lassediercksAI/releasejo/compare/v0.0.9...v0.0.10) (2026-10-22)


### Bug Fixes

* **release:** tag the version a release PR merged, at its merge commit ([#13](https://github.com/lassediercksAI/releasejo/issues/13)) ([1a3885a](https://github.com/lassediercksAI/releasejo/commit/1a3885a9cf5184d194ad973b0258652aa95f33d8))

## [0.0.9](https://github.com/lassediercksAI/releasejo/compare/v0.0.8...v0.0.9) (2026-10-19)


### Bug Fixes

* don't tag never-released packages when another release PR merges ([52ba296](https://github.com/lassediercksAI/releasejo/commit/52ba296f0f8f7887987f3cfa317b55eaaeeac5fa))
* don't tag never-released packages when another release PR merges ([b225a9d](https://github.com/lassediercksAI/releasejo/commit/b225a9d7ecd91953a261543d2c7398e0e4163a4b))

## [0.0.8](https://github.com/lassediercksAI/releasejo/compare/v0.0.7...v0.0.8) (2026-10-19)


### Bug Fixes

* stop re-releasing monorepo packages, write changelogs per package ([101eaa9](https://github.com/lassediercksAI/releasejo/commit/101eaa9fafbec2f34311b895e30d420ae4116c6e))
* stop re-releasing monorepo packages, write changelogs per package ([93bf1eb](https://github.com/lassediercksAI/releasejo/commit/93bf1eb0b42e9da296f36af355c729b465a7159b))

## [0.0.7](https://github.com/lassediercksAI/releasejo/compare/v0.0.6...v0.0.7) (2026-10-12)


### Bug Fixes

* a refresh reopens the release PR the branch reset closed, instead of leaving it closed ([992f2c1](https://github.com/lassediercksAI/releasejo/commit/992f2c10eb7ab247345ec6b5037f15e32088e874))
* a refresh reopens the release PR the branch reset closed, instead of leaving it closed ([96c067b](https://github.com/lassediercksAI/releasejo/commit/96c067b36532e61f6b42b129ea85a6a86c05c1b5))

## [0.0.6](https://github.com/lassediercksAI/releasejo/compare/v0.0.5...v0.0.6) (2026-10-12)


### Features

* honour Release-As from a commit footer or the config ([9818ecd](https://github.com/lassediercksAI/releasejo/commit/9818ecd926b2b393bd8848f7081242741cb140d1))
* honour Release-As from a commit footer or the config ([8ebb76a](https://github.com/lassediercksAI/releasejo/commit/8ebb76a522bdbef6b3681b054e5519c0d5d3840c))

## [0.0.5](https://github.com/lassediercksAI/releasejo/compare/v0.0.4...v0.0.5) (2026-08-10)


### Bug Fixes

* release on perf and revert (patch), matching release-please ([66af464](https://github.com/lassediercksAI/releasejo/commit/66af464ce55f3c9b6204f8d77106142292abb348))

## [0.0.4](https://github.com/lassediercksAI/releasejo/compare/v0.0.3...v0.0.4) (2026-08-09)


### Bug Fixes

* rebuild release branch from base each run (never reset → frozen tree + stacked commits) ([7d592a1](https://github.com/lassediercksAI/releasejo/commit/7d592a19c78048eb9268b35b9d0ad37519e25c39))

## 0.0.3 (2026-08-08)


### Bug Fixes

* **release:** write the whole release PR as ONE commit. Previously each managed file (CHANGELOG.md, version file, manifest) was a separate `PUT /contents` call, so a release PR carried 3 commits — all titled `chore(release): chore(release): X` (doubled prefix). Now all changed files go through Gitea's multi-file `POST /contents` in a single commit titled `chore(release): X`, and unchanged files are skipped so a no-op re-run adds no commit.

## 0.0.2 (2026-08-08)


### Bug Fixes

* **forge:** create files with POST — Forgejo/Gitea's `PUT /contents` requires a `sha`, so first-time writes (CHANGELOG.md, version.txt, manifest on a fresh release branch) 422'd with "[SHA]: Required". Verified live against Forgejo 16.0.2 (gitea-1.22): release PR now opens end-to-end.

## 0.0.1 (2026-08-06)


### Features

* implement separate-pull-requests (monorepo per-component PRs) ([d26ed96](https://github.com/lassediercksAI/releasejo/commit/d26ed96d615ad76cd90693e3853aa5ecfba60398))
* releasejo v0 — release-please-compatible release automation for Forgejo & Gitea ([2c5e6a8](https://github.com/lassediercksAI/releasejo/commit/2c5e6a83dd30a4207288bad22d32469cafd43509))


### Miscellaneous Chores

* adopt release-please for versioning ([005de72](https://github.com/lassediercksAI/releasejo/commit/005de721df3e76ce0c3afc673c182884a43cbd87))

## Changelog
