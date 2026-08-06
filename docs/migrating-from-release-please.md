# Migrating from release-please

`releasejo` reads the same config, so migration is mostly swapping the workflow.

## 1. Keep your config

`release-please-config.json` and `.release-please-manifest.json` stay as-is. If
you used keys releasejo doesn't support yet (e.g. `separate-pull-requests`,
exotic release-types), see [config.md](config.md) — unsupported keys are ignored,
so nothing errors, but behaviour may differ.

## 2. Swap the workflow

Replace the release-please action step:

```yaml
# before (GitHub Actions)
- uses: googleapis/release-please-action@v4
  with:
    token: ${{ secrets.RELEASE_TOKEN }}
    config-file: release-please-config.json
    manifest-file: .release-please-manifest.json
```

```yaml
# after (Forgejo Actions)
- uses: lassediercksAI/releasejo@v0
  with:
    token: ${{ secrets.RELEASE_TOKEN }}
    config-file: release-please-config.json
    manifest-file: .release-please-manifest.json
```

## 3. Provide a bot PAT

Create a Forgejo user token with **`contents:write` + `pull-requests:write`**,
store it as the repo Actions secret `RELEASE_TOKEN`. Do **not** use the automatic
job token — it can't open PRs cleanly and won't trigger downstream workflows
(e.g. an image build on the release tag).

## 4. Labels

`releasejo` uses the same label convention: `autorelease: pending` on the open
release PR, `autorelease: tagged` once released. They're created automatically.

## Differences to know

- One release PR for all packages in v0.x (no `separate-pull-requests` yet).
- Tag/release are created via the Forgejo REST API; release notes come from the
  changelog entry for the version.
