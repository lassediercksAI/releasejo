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

## How it works

On every push to the target branch, `releasejo`:

1. **Tags merged releases.** If a release PR was just merged, it cuts the tag +
   Release for each package whose manifest version isn't tagged yet.
2. **(Re)builds the release PR.** It collects commits since each package's last
   release tag, computes the next semver (conventional-commit driven, with
   `bump-minor-pre-major` support), regenerates the changelog, bumps version
   files + the manifest, and opens or refreshes the `autorelease: pending` PR.

The release branch is always rebuilt from the base branch's content, so
re-running never stacks duplicate changelog entries.

## Status & scope (v0.x)

Implemented and tested: conventional-commit parsing, semver bump (incl.
pre-1.0 semantics), release-please config + manifest parsing, changelog
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
