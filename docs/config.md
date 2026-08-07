# Configuration

`releasejo` reads release-please's config files. This documents the supported
subset; unknown keys are ignored (not an error), so a fuller release-please
config still loads.

## `release-please-config.json`

Top-level keys act as **defaults** for every package; per-package keys override.

| Key | Scope | Default | Notes |
|-----|-------|---------|-------|
| `release-type` | global + package | — (required) | `go`, `simple`, `node`, or any type (falls back to marker-only updates). |
| `versioning` | global + package | `default` | `always-bump-patch` / `always-bump-minor` force that bump for any releasable change; `default` is the semantic (feat=minor, fix=patch, breaking=major) bump. |
| `bump-minor-pre-major` | global + package | `false` | Pre-1.0: a breaking change bumps the minor and feat/fix bump the patch. |
| `separate-pull-requests` | global | `false` | When true, one release PR **per package** on `releasejo--branches--<base>--components--<component>` branches (rebuilt from base, so sequential merges reconcile the shared manifest). |
| `include-component-in-tag` | global + package | `true` if >1 package | `<component><sep>v<version>` vs `v<version>`. |
| `include-v-in-tag` | global + package | `true` | Leading `v` in the tag. |
| `tag-separator` | global + package | `-` | Between component and version. |
| `changelog-sections` | global + package | release-please defaults | `[{type, section, hidden}]`. |
| `packages` | global | — (required) | Map of path → package config. |

### Per-package keys

| Key | Default | Notes |
|-----|---------|-------|
| `component` | path leaf / `root` | Used in tags + PR titles. |
| `package-name` | — | Cosmetic label. |
| `changelog-path` | `CHANGELOG.md` | Relative to the package path. |
| `extra-files` | — | Files to run version-marker substitution on. |

## `.release-please-manifest.json`

Maps each package path to its **last released** version:

```json
{ ".": "1.4.0", "services/api": "0.3.2" }
```

`releasejo` reads this to know the starting point and rewrites it (on the release
branch) as part of the release PR.

## Version updaters

- **`go`** — no default version file; use markers (below) for `version.go`.
- **`simple`** — updates `version.txt` (whole-file).
- **`node`** — updates the `"version"` field in `package.json`.
- **Generic markers** (any release-type, applied to `extra-files` and the type's
  default file): on a line containing a marker comment, the version token is
  replaced:
  - `x-release-please-version` → full version
  - `x-release-please-major` → major
  - `x-release-please-minor` → major.minor
  - `x-release-please-patch` → major.minor.patch

```go
const Version = "0.0.0" // x-release-please-version
```
