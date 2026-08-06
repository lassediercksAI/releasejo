#!/bin/sh
# Maps Forgejo Actions inputs (INPUT_*) to releasejo flags. The repo, instance
# URL and target branch are read from the standard GITHUB_* env the runner sets,
# so only the token and optional overrides come through inputs.
set -eu

set -- \
  --config="${INPUT_CONFIG_FILE:-release-please-config.json}" \
  --manifest="${INPUT_MANIFEST_FILE:-.release-please-manifest.json}"

[ -n "${INPUT_TOKEN:-}" ] && set -- "$@" --token="${INPUT_TOKEN}"
[ -n "${INPUT_TARGET_BRANCH:-}" ] && set -- "$@" --target-branch="${INPUT_TARGET_BRANCH}"
[ "${INPUT_DRY_RUN:-false}" = "true" ] && set -- "$@" --dry-run

exec /usr/local/bin/releasejo "$@"
