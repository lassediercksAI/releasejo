# releasejo

**release-please-compatible release automation for Forgejo & Gitea.**

`releasejo` reads [Conventional Commits](https://www.conventionalcommits.org),
maintains a **release pull request** that accumulates the pending changelog +
version bumps, and — when you merge it — tags the release and publishes it. It
reads release-please's own config (`release-please-config.json` +
`.release-please-manifest.json`), so projects already on release-please migrate
without rewriting configuration.

It exists because upstream [release-please](https://github.com/googleapis/release-please)
is excellent but **GitHub-only** (Octokit + GitHub's GraphQL API). `releasejo`
targets the Forgejo/Gitea REST API instead.

## Why this design

- **Single static Go binary, zero third-party dependencies.** Trivial to audit,
  vendor, and run air-gapped inside CI. No `node_modules`, no npm supply chain.
- **Forge-native.** Talks the Forgejo/Gitea REST API — no GraphQL, no GitHub.
- **Config-compatible.** Drop-in for the release-please config subset most
  projects use; your existing files carry over.

## Quick start (Forgejo Actions)

```yaml
# .forgejo/workflows/release.yml
name: release
on:
  push:
    branches: [main]
jobs:
  release:
    runs-on: self-hosted
    steps:
      - uses: actions/checkout@v4
      - uses: lassediercksAI/releasejo@v0
        with:
          token: ${{ secrets.RELEASE_TOKEN }}   # bot PAT: contents + PR write
```

`token` must be a **bot PAT**, not the automatic job token: the auto token can't
open PRs cleanly and (like GitHub) won't trigger downstream workflows from the
release tag. The repo, instance URL and branch are read from the standard
`GITHUB_*` env the runner sets.

### CLI

```
releasejo --repo owner/name --api-url https://code.example.com \
          --token "$RELEASE_TOKEN" --target-branch main [--dry-run]
```

All flags default to the Forgejo Actions environment (`GITHUB_REPOSITORY`,
`GITHUB_API_URL`, `GITHUB_REF_NAME`, `RELEASE_TOKEN`/`GITHUB_TOKEN`).

## Config

Same files as release-please:

```json
// release-please-config.json
{
  "release-type": "go",
  "bump-minor-pre-major": true,
  "packages": {
    ".": { "component": "root" },
    "services/api": { "release-type": "node", "component": "api" }
  }
}
```
```json
// .release-please-manifest.json — the last released version per package
{ ".": "1.4.0", "services/api": "0.3.2" }
```

Supported keys and release-types: see [docs/config.md](docs/config.md).
Migrating from release-please: [docs/migrating-from-release-please.md](docs/migrating-from-release-please.md).

### Release-As

To release a specific version instead of the one the commits imply — cutting
`1.0.0`, jumping a minor, correcting a mis-numbered release — pin it, as in
release-please, either per commit or in the config.

**Commit footer** (any commit since the last release tag):

```
fix: correct the retry budget

Release-As: 1.0.0
```

The key is case-insensitive (`Release-As`, `release-as`, `RELEASE-AS`) and a
leading `v` is accepted (`Release-As: v1.0.0`). If several commits carry one, the
**newest** wins.

**Config key** (global default, or per package):

```json
{
  "release-type": "go",
  "release-as": "1.0.0",
  "packages": { ".": { "component": "root" } }
}
```

Precedence: **config `release-as` > commit footer > the conventional-commit
bump.** The pinned version is used everywhere the computed one would be — PR
title, changelog heading, version files, the manifest, and the tag cut when the
release PR merges.

Validation — a request that fails either check is **ignored with a warning on
stderr** and the normal bump applies:

- it must be a valid `major.minor.patch` (so `banana` or `1.2` is rejected);
- it must be **greater than the current version** in the manifest. Releases never
  go backwards — which is also why a footer is a one-shot: once the version it
  asks for is released, the request is behind the tag boundary and asking for it
  again is refused.

A valid request releases on its own, as in release-please: it opens a release PR
even when no commit implies a bump (a `chore:`-only range still cuts the pinned
release). With no commits at all since the tag there is still nothing to release.

**Remove `release-as` from the config once the release lands.** Unlike a commit
footer — which falls behind the tag boundary and is gone — the config key stays,
and keeps asking for the same version on every later run. `releasejo` refuses it
once it is no longer greater than the current version, so later releases bump
normally rather than staying pinned, but every run warns about it until the key
is deleted. A commit footer needs no cleanup.

## How it works

On every push to the target branch, `releasejo`:

1. **Tags merged releases.** If a release PR was just merged, it cuts the tag +
   Release for each package whose manifest version isn't tagged yet, then
   deletes the merged PR's head branch.
2. **(Re)builds the release PR.** It collects commits since each package's last
   release tag, computes the next semver (conventional-commit driven, with
   `bump-minor-pre-major` support and any [Release-As](#release-as) override),
   regenerates the changelog, bumps version
   files + the manifest, and opens or refreshes the `autorelease: pending` PR.

The release branch is always rebuilt from the base branch's content, so
re-running never stacks duplicate changelog entries. Rebuilding deletes and
recreates the head branch — which makes Forgejo close the PR — so a refresh
reopens and reuses the same release PR instead of opening a new one: the release
PR keeps one number for the whole cycle.

Forgejo closes those PRs from a queue and matches them by branch name when the
close lands, so the close can also take a PR opened on the recreated branch
after the delete. That is why the merged PR's branch is deleted when its
release is cut, and why a PR opened on a branch deleted in the same run is
watched for a short while and reopened if the close takes it.

## Status & scope (v0.x)

Implemented and tested: conventional-commit parsing, semver bump (incl.
pre-1.0 semantics), `Release-As` overrides (footer + config),
release-please config + manifest parsing, changelog
generation, version updaters (`go`, `simple`, `node`, generic
`x-release-please-version` markers), monorepo **`separate-pull-requests`**, and
the release-PR + tag-on-merge orchestration against the Forgejo/Gitea REST API.

Not yet (contributions welcome):

- The full release-please release-type catalogue (only the common ones above).
- Linked-versions, plugins, and `bootstrap-sha`.

The pure-logic core has unit tests; the forge REST client is tested against a
stubbed Gitea API (httptest) and the orchestrator via an in-memory fake. Before
relying on it, run the ~10-minute end-to-end check against a throwaway Forgejo:
[`docs/validating-live.md`](docs/validating-live.md).

## License

[Apache-2.0](LICENSE).
