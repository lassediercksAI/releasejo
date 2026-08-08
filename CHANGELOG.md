# Changelog

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
