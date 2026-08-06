# Validating releasejo against a live Forgejo

releasejo's pure logic and REST client are unit-tested (the REST client via an
httptest stub), and the orchestrator via an in-memory fake. This is the
**end-to-end gate**: prove a full release cycle against a real Forgejo before
depending on it. ~10 minutes with a throwaway instance.

## 1. Bring up a throwaway Forgejo

```sh
docker compose -f test/compose.yml up -d
# wait until http://localhost:3000 responds
```

Create an admin user + a token (scoped `write:repository`, `write:issue`):

```sh
docker compose -f test/compose.yml exec forgejo \
  forgejo admin user create --admin --username bot --password botpass \
  --email bot@example.com --must-change-password=false

TOKEN=$(curl -s -u bot:botpass -X POST http://localhost:3000/api/v1/users/bot/tokens \
  -H 'Content-Type: application/json' \
  -d '{"name":"releasejo","scopes":["write:repository","write:issue","write:user"]}' \
  | jq -r .sha1)
```

## 2. Seed a repo with conventional commits + config

```sh
API=http://localhost:3000/api/v1
H="Authorization: token $TOKEN"

curl -s -X POST "$API/user/repos" -H "$H" -H 'Content-Type: application/json' \
  -d '{"name":"demo","auto_init":true,"default_branch":"main"}' >/dev/null

put() { # path content message
  curl -s -X POST "$API/repos/bot/demo/contents/$1" -H "$H" -H 'Content-Type: application/json' \
    -d "$(jq -n --arg c "$(printf %s "$2" | base64 -w0)" --arg m "$3" \
      '{content:$c,message:$m,branch:"main"}')" >/dev/null
}
put release-please-config.json '{"packages":{".":{"release-type":"simple"}}}' 'chore: add release config'
put .release-please-manifest.json '{".":"0.0.0"}' 'chore: add manifest'
put version.txt '0.0.0' 'feat: initial feature'
put a.txt 'x' 'fix: a bug fix'
```

## 3. Run releasejo — expect a release PR

```sh
go build -o /tmp/releasejo ./cmd/releasejo
/tmp/releasejo --repo bot/demo --api-url http://localhost:3000 --token "$TOKEN" --target-branch main

# assert: exactly one open PR titled "chore(release): 0.1.0"
curl -s "$API/repos/bot/demo/pulls?state=open" -H "$H" | jq -r '.[].title'
```

Check the PR's branch has an updated `CHANGELOG.md` (Features + Bug Fixes),
`version.txt` = `0.1.0`, and `.release-please-manifest.json` = `{".":"0.1.0"}`.

## 4. Merge it, run again — expect a tag + release

```sh
PR=$(curl -s "$API/repos/bot/demo/pulls?state=open" -H "$H" | jq -r '.[0].number')
curl -s -X POST "$API/repos/bot/demo/pulls/$PR/merge" -H "$H" \
  -H 'Content-Type: application/json' -d '{"Do":"merge"}' >/dev/null

/tmp/releasejo --repo bot/demo --api-url http://localhost:3000 --token "$TOKEN" --target-branch main

# assert: release v0.1.0 exists
curl -s "$API/repos/bot/demo/releases" -H "$H" | jq -r '.[].tag_name'   # -> v0.1.0
```

## 5. Idempotency + monorepo

- Re-run step 3 with no new commits → "no releasable changes", no PR.
- Add `feat:`/`fix:` commits → the PR reopens/refreshes with the next version.
- For `separate-pull-requests`, use a config with 2+ packages and per-package
  paths; expect one PR per component on
  `releasejo--branches--main--components--<component>` branches.

## Teardown

```sh
docker compose -f test/compose.yml down
```

## What this proves (the gate)

Real Forgejo REST behaviour for: commit listing + tag-boundary, contents
create/update on a branch, PR create/refresh + labels, and release creation —
i.e. everything the in-memory fake stands in for. Once green here, releasejo is
ready to drive a real repo's releases (plan `forgejo-actions-runners.md` §10).
