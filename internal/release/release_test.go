package release

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/lassediercksAI/releasejo/internal/config"
	"github.com/lassediercksAI/releasejo/internal/forge"
)

// fakeForge is an in-memory API implementation for testing the orchestrator
// without a live Forgejo instance.
type fakeForge struct {
	tags    map[string]string         // tag -> sha
	commits map[string][]forge.Commit // path -> commits (newest first)
	// commitsAt overrides commits for a specific ref: "ref\x00path". Used to
	// model history as seen from a release tag.
	commitsAt map[string][]forge.Commit
	files     map[string]*forge.File // "ref\x00path" -> file
	branches  map[string]bool        // branch -> exists
	pulls     []forge.Pull           // all PRs

	puts        map[string]string // "branch\x00path" -> content written
	commitCount int               // number of ChangeFiles commits made
	created     []forge.Pull
	releases    []string // tags released
	targets     []string // target ref of each release, in order
	edits       int
	edited      []int // PR numbers passed to EditPull, in order
	deletes     int   // number of DeleteBranch (reset) calls
	closedBy    int   // PRs auto-closed because their head branch was deleted
	labelSet    map[int][]int64
}

// pull returns the PR with that number, or nil.
func (f *fakeForge) pull(number int) *forge.Pull {
	for i := range f.pulls {
		if f.pulls[i].Number == number {
			return &f.pulls[i]
		}
	}
	return nil
}

func newFake() *fakeForge {
	return &fakeForge{
		tags: map[string]string{}, commits: map[string][]forge.Commit{}, commitsAt: map[string][]forge.Commit{},
		files: map[string]*forge.File{}, branches: map[string]bool{},
		puts: map[string]string{}, labelSet: map[int][]int64{},
	}
}

func notFound() error { return &forge.APIError{Status: http.StatusNotFound, Body: "not found"} }

func (f *fakeForge) TagCommit(_ context.Context, tag string) (string, error) { return f.tags[tag], nil }

func (f *fakeForge) Commits(_ context.Context, ref, path string, _ int) ([]forge.Commit, error) {
	if cs, ok := f.commitsAt[ref+"\x00"+path]; ok {
		return cs, nil
	}
	if ref != "main" {
		return nil, nil // history at a tag is only modelled via commitsAt
	}
	return f.commits[path], nil
}

func (f *fakeForge) BranchExists(_ context.Context, name string) (bool, error) {
	return f.branches[name], nil
}
func (f *fakeForge) CreateBranch(_ context.Context, name, _ string) error {
	f.branches[name] = true
	return nil
}

// DeleteBranch removes a branch. Forgejo CLOSES any open PR whose head branch is
// deleted — the behaviour that left release PRs closed — so the fake models it;
// the release flow has to cope.
func (f *fakeForge) DeleteBranch(_ context.Context, name string) error {
	f.branches[name] = false
	f.deletes++
	for i := range f.pulls {
		if f.pulls[i].Head.Ref == name && f.pulls[i].State == "open" && !f.pulls[i].Merged {
			f.pulls[i].State = "closed"
			f.closedBy++
		}
	}
	return nil
}
func (f *fakeForge) GetFile(_ context.Context, path, ref string) (*forge.File, error) {
	if file, ok := f.files[ref+"\x00"+path]; ok {
		return file, nil
	}
	return nil, notFound()
}

// ChangeFiles applies a multi-file commit, recording each file into puts (and
// files, so a later GetFile on the branch reflects the write). One call =
// one commit, mirroring the real client.
func (f *fakeForge) ChangeFiles(_ context.Context, branch, _ string, files []forge.FileChange) error {
	f.commitCount++
	for _, fc := range files {
		key := branch + "\x00" + fc.Path
		if fc.Op == "delete" {
			delete(f.puts, key)
			delete(f.files, key)
			continue
		}
		f.puts[key] = fc.Content
		f.files[key] = &forge.File{Content: fc.Content, SHA: "sha-" + fc.Path}
	}
	return nil
}
func (f *fakeForge) Pulls(_ context.Context, state string) ([]forge.Pull, error) {
	var out []forge.Pull
	for _, p := range f.pulls {
		if state == "all" || p.State == state {
			out = append(out, p)
		}
	}
	return out, nil
}
func (f *fakeForge) CreatePull(_ context.Context, head, base, title, body string) (*forge.Pull, error) {
	p := forge.Pull{Number: 100 + len(f.created), Title: title, Body: body, State: "open"}
	p.Head.Ref = head
	f.created = append(f.created, p)
	f.pulls = append(f.pulls, p) // a later run must see it, like the real API
	return &p, nil
}

// EditPull refreshes title/body and REOPENS the PR, mirroring the real client,
// which sends state:"open" on every edit.
func (f *fakeForge) EditPull(_ context.Context, number int, title, body string) error {
	f.edits++
	f.edited = append(f.edited, number)
	if p := f.pull(number); p != nil {
		p.Title, p.Body, p.State = title, body, "open"
	}
	return nil
}
func (f *fakeForge) EnsureLabel(_ context.Context, _, _ string) (int64, error) { return 7, nil }
func (f *fakeForge) SetIssueLabels(_ context.Context, number int, ids []int64) error {
	f.labelSet[number] = ids
	return nil
}
func (f *fakeForge) CreateRelease(_ context.Context, tag, target, _, _ string, _ bool) error {
	f.releases = append(f.releases, tag)
	f.targets = append(f.targets, target)
	return nil
}

func fixedClock() func() time.Time {
	return func() time.Time { return time.Date(2026, 8, 6, 0, 0, 0, 0, time.UTC) }
}

func goConfig() *config.Config {
	cfg, err := config.ParseConfig([]byte(`{
      "release-type": "go",
      "packages": { ".": { "component": "root" } }
    }`))
	if err != nil {
		panic(err)
	}
	return cfg
}

// logSink collects Logf output so tests can assert on the warnings that reach
// stderr in production (main wires Logf to log.Printf).
type logSink struct{ lines []string }

func (l *logSink) logf(f string, a ...any) { l.lines = append(l.lines, fmt.Sprintf(f, a...)) }
func (l *logSink) text() string            { return strings.Join(l.lines, "\n") }

func TestRunFirstRelease(t *testing.T) {
	f := newFake()
	f.commits["."] = []forge.Commit{
		{SHA: "c2", Message: "fix: correct a boundary check"},
		{SHA: "c1", Message: "feat: add the thing"},
	}
	man := config.Manifest{".": "0.0.0"}

	err := Run(context.Background(), f, goConfig(), man, Options{TargetBranch: "main", Now: fixedClock()})
	if err != nil {
		t.Fatal(err)
	}

	if len(f.created) != 1 {
		t.Fatalf("expected 1 PR created, got %d", len(f.created))
	}
	pr := f.created[0]
	if !strings.Contains(pr.Title, "0.1.0") { // feat on 0.0.0 -> minor -> 0.1.0
		t.Errorf("PR title = %q, want it to mention 0.1.0", pr.Title)
	}
	// manifest on the release branch bumped
	gotMan := f.puts["releasejo--branches--main\x00.release-please-manifest.json"]
	if !strings.Contains(gotMan, `"0.1.0"`) {
		t.Errorf("manifest not bumped to 0.1.0:\n%s", gotMan)
	}
	// changelog written with both sections
	cl := f.puts["releasejo--branches--main\x00CHANGELOG.md"]
	if !strings.Contains(cl, "### Features") || !strings.Contains(cl, "### Bug Fixes") {
		t.Errorf("changelog missing sections:\n%s", cl)
	}
	// pending label applied
	if ids := f.labelSet[pr.Number]; len(ids) != 1 {
		t.Errorf("expected pending label on PR, got %v", ids)
	}
	// all release files land in a SINGLE commit (not one per file)
	if f.commitCount != 1 {
		t.Errorf("expected 1 release commit, got %d", f.commitCount)
	}
}

// A release branch left over from a prior run must be REBUILT from base, not
// written onto in place — otherwise its non-release files (e.g. a CI workflow
// fixed on main) stay frozen and release commits stack. Guards the fix for the
// "branch never reset to base" bug.
func TestRunResetsExistingReleaseBranch(t *testing.T) {
	f := newFake()
	f.commits["."] = []forge.Commit{
		{SHA: "c1", Message: "feat: add the thing"},
	}
	man := config.Manifest{".": "0.0.0"}
	const branch = "releasejo--branches--main"
	f.branches[branch] = true // a stale branch from a previous run

	if err := Run(context.Background(), f, goConfig(), man, Options{TargetBranch: "main", Now: fixedClock()}); err != nil {
		t.Fatal(err)
	}
	if f.deletes != 1 {
		t.Errorf("existing release branch must be reset (deleted) once; got %d deletes", f.deletes)
	}
	if !f.branches[branch] {
		t.Errorf("branch must be recreated from base after the reset")
	}
	if f.commitCount != 1 {
		t.Errorf("expected 1 release commit after reset, got %d", f.commitCount)
	}
}

func TestRunNothingToRelease(t *testing.T) {
	f := newFake()
	f.commits["."] = []forge.Commit{{SHA: "c1", Message: "docs: tweak readme"}}
	man := config.Manifest{".": "1.0.0"}
	if err := Run(context.Background(), f, goConfig(), man, Options{TargetBranch: "main", Now: fixedClock()}); err != nil {
		t.Fatal(err)
	}
	if len(f.created) != 0 {
		t.Errorf("expected no PR for chore/docs-only commits, got %d", len(f.created))
	}
}

func TestVersioningAlwaysBumpPatch(t *testing.T) {
	f := newFake()
	// a breaking change that would normally force a major bump
	f.commits["."] = []forge.Commit{{SHA: "b1", Message: "feat!: remove a thing\n\nBREAKING CHANGE: gone"}}
	cfg, err := config.ParseConfig([]byte(`{
      "release-type": "simple",
      "versioning": "always-bump-patch",
      "packages": { ".": { "package-name": "brain" } }
    }`))
	if err != nil {
		t.Fatal(err)
	}
	man := config.Manifest{".": "0.0.24"}
	if err := Run(context.Background(), f, cfg, man, Options{TargetBranch: "main", Now: fixedClock()}); err != nil {
		t.Fatal(err)
	}
	if len(f.created) != 1 {
		t.Fatalf("expected 1 PR, got %d", len(f.created))
	}
	// always-bump-patch forces 0.0.25, NOT 1.0.0 despite the breaking change
	if !strings.Contains(f.created[0].Title, "0.0.25") {
		t.Errorf("PR title = %q, want 0.0.25 (always-bump-patch)", f.created[0].Title)
	}
}

func TestSeparatePullRequests(t *testing.T) {
	f := newFake()
	f.commits["services/api"] = []forge.Commit{{SHA: "a1", Message: "feat: api thing"}}
	f.commits["services/web"] = []forge.Commit{{SHA: "w1", Message: "fix: web thing"}}
	cfg, err := config.ParseConfig([]byte(`{
      "release-type": "node",
      "separate-pull-requests": true,
      "packages": {
        "services/api": { "component": "api" },
        "services/web": { "component": "web" }
      }
    }`))
	if err != nil {
		t.Fatal(err)
	}
	man := config.Manifest{"services/api": "1.0.0", "services/web": "2.0.0"}

	if err := Run(context.Background(), f, cfg, man, Options{TargetBranch: "main", Now: fixedClock()}); err != nil {
		t.Fatal(err)
	}
	if len(f.created) != 2 {
		t.Fatalf("expected 2 separate PRs, got %d", len(f.created))
	}
	// distinct component branches
	branches := map[string]bool{}
	for _, pr := range f.created {
		branches[pr.Head.Ref] = true
	}
	if !branches["releasejo--branches--main--components--api"] || !branches["releasejo--branches--main--components--web"] {
		t.Errorf("component branches wrong: %v", branches)
	}
	// api PR bumped only api's manifest entry (feat on 1.0.0 -> 1.1.0), from base
	apiMan := f.puts["releasejo--branches--main--components--api\x00.release-please-manifest.json"]
	if !strings.Contains(apiMan, `"1.1.0"`) || !strings.Contains(apiMan, `"2.0.0"`) {
		t.Errorf("api PR manifest should have api=1.1.0 and untouched web=2.0.0:\n%s", apiMan)
	}
}

func TestTagOnMerge(t *testing.T) {
	f := newFake()
	// a merged release PR carrying the pending label, no new commits to release
	merged := forge.Pull{Number: 42, State: "closed", Merged: true, MergeCommitSHA: "mmm",
		Labels: []forge.Label{{ID: 7, Name: pendingLabel}}}
	f.pulls = []forge.Pull{merged}
	// main's manifest already bumped by the merge; tag not yet created
	man := config.Manifest{".": "0.1.0"}
	f.files["main\x00CHANGELOG.md"] = &forge.File{
		Content: "# Changelog\n\n## 0.1.0 (2026-08-06)\n\n### Features\n\n* add the thing (c1)\n",
	}

	if err := Run(context.Background(), f, goConfig(), man, Options{TargetBranch: "main", Now: fixedClock()}); err != nil {
		t.Fatal(err)
	}
	if len(f.releases) != 1 || f.releases[0] != "v0.1.0" {
		t.Fatalf("expected release v0.1.0, got %v", f.releases)
	}
	// PR relabelled to tagged (label id 7 set)
	if _, ok := f.labelSet[42]; !ok {
		t.Error("expected merged PR to be relabelled tagged")
	}
}

// A run that checked out the branch BEFORE the release PR was merged still
// holds the previous manifest. It must tag the version the PR merged (read at
// the merge commit), on the merge commit — not find the previous tag already
// there, cut nothing and mark the PR tagged anyway.
func TestTagOnMergeReadsTheManifestAtTheMergeCommit(t *testing.T) {
	f := newFake()
	merged := forge.Pull{Number: 74, State: "closed", Merged: true, MergeCommitSHA: "m055",
		Labels: []forge.Label{{ID: 7, Name: pendingLabel}}}
	f.pulls = []forge.Pull{merged}
	f.tags["v0.54.0"] = "old"
	stale := config.Manifest{".": "0.54.0"} // this run's checkout
	f.files["m055\x00.release-please-manifest.json"] = &forge.File{Content: "{\n  \".\": \"0.55.0\"\n}\n"}
	f.files["m055\x00CHANGELOG.md"] = &forge.File{
		Content: "# Changelog\n\n## 0.55.0 (2026-10-22)\n\n### Features\n\n* bill anywhere (c2)\n",
	}

	if err := Run(context.Background(), f, goConfig(), stale, Options{TargetBranch: "main", Now: fixedClock()}); err != nil {
		t.Fatal(err)
	}
	if len(f.releases) != 1 || f.releases[0] != "v0.55.0" {
		t.Fatalf("expected release v0.55.0, got %v", f.releases)
	}
	if f.targets[0] != "m055" {
		t.Errorf("release target = %q, want the merge commit m055", f.targets[0])
	}
	if _, ok := f.labelSet[74]; !ok {
		t.Error("expected merged PR to be relabelled tagged")
	}
}

// ---- Release-As -----------------------------------------------------------

// runWith executes one cycle over the given commits (newest first) against a
// manifest holding `current`, returning the fake forge and everything logged.
func runWith(t *testing.T, cfg *config.Config, current string, commits ...forge.Commit) (*fakeForge, string) {
	t.Helper()
	f := newFake()
	f.commits["."] = commits
	l := &logSink{}
	opts := Options{TargetBranch: "main", Now: fixedClock(), Logf: l.logf}
	if err := Run(context.Background(), f, cfg, config.Manifest{".": current}, opts); err != nil {
		t.Fatal(err)
	}
	return f, l.text()
}

// releaseAsConfig is goConfig with a package-level "release-as".
func releaseAsConfig(t *testing.T, releaseAs string) *config.Config {
	t.Helper()
	cfg, err := config.ParseConfig([]byte(`{
      "release-type": "go",
      "packages": { ".": { "component": "root", "release-as": "` + releaseAs + `" } }
    }`))
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// wantPRAt asserts exactly one release PR was opened for version v and that v
// reached every place the computed version is used: PR title, PR body,
// changelog heading and manifest (the tag is cut from the manifest on merge —
// see TestTagOnMerge).
func wantPRAt(t *testing.T, f *fakeForge, v string) {
	t.Helper()
	if len(f.created) != 1 {
		t.Fatalf("expected 1 release PR, got %d", len(f.created))
	}
	if got, want := f.created[0].Title, "chore(release): "+v; got != want {
		t.Errorf("PR title = %q, want %q", got, want)
	}
	if body := f.created[0].Body; !strings.Contains(body, v) {
		t.Errorf("PR body does not mention %s:\n%s", v, body)
	}
	if cl := f.puts["releasejo--branches--main\x00CHANGELOG.md"]; !strings.Contains(cl, "## "+v+" (") {
		t.Errorf("changelog heading is not %q:\n%s", "## "+v, cl)
	}
	if man := f.puts["releasejo--branches--main\x00.release-please-manifest.json"]; !strings.Contains(man, `"`+v+`"`) {
		t.Errorf("manifest not set to %s:\n%s", v, man)
	}
}

// A "Release-As:" footer pins the release: a fix on 0.0.5 would bump to 0.0.6,
// the footer makes it 0.1.0 — in the title, body, changelog and manifest alike.
func TestReleaseAsFooterHonoured(t *testing.T) {
	f, _ := runWith(t, goConfig(), "0.0.5",
		forge.Commit{SHA: "c1", Message: "fix: correct a boundary check\n\nRelease-As: 0.1.0"})
	wantPRAt(t, f, "0.1.0")
}

// With a footer on several commits the NEWEST wins (commits arrive newest-first).
func TestReleaseAsNewestFooterWins(t *testing.T) {
	f, _ := runWith(t, goConfig(), "0.0.5",
		forge.Commit{SHA: "c2", Message: "fix: later fix\n\nRelease-As: 0.3.0"},
		forge.Commit{SHA: "c1", Message: "fix: earlier fix\n\nRelease-As: 0.2.0"})
	wantPRAt(t, f, "0.3.0")
}

// The config key is honoured, and beats a commit footer (release-please precedence).
func TestReleaseAsConfigWinsOverFooter(t *testing.T) {
	f, _ := runWith(t, releaseAsConfig(t, "1.0.0"), "0.0.5",
		forge.Commit{SHA: "c1", Message: "fix: a fix\n\nRelease-As: 0.9.0"})
	wantPRAt(t, f, "1.0.0")
}

// A value that is not a semver is ignored with a warning; the normal bump applies.
func TestReleaseAsInvalidIgnored(t *testing.T) {
	t.Run("footer", func(t *testing.T) {
		f, logged := runWith(t, goConfig(), "1.2.3",
			forge.Commit{SHA: "c1", Message: "fix: a fix\n\nRelease-As: banana"})
		wantPRAt(t, f, "1.2.4")
		if !strings.Contains(logged, `ignoring release-as "banana"`) {
			t.Errorf("expected a warning about the invalid value, got:\n%s", logged)
		}
	})
	t.Run("config", func(t *testing.T) {
		f, logged := runWith(t, releaseAsConfig(t, "1.2"), "1.2.3",
			forge.Commit{SHA: "c1", Message: "fix: a fix"})
		wantPRAt(t, f, "1.2.4")
		if !strings.Contains(logged, `ignoring release-as "1.2"`) || !strings.Contains(logged, "config release-as") {
			t.Errorf("expected a warning about the invalid config value, got:\n%s", logged)
		}
	})
}

// Releases never go backwards: a request at or below the current version is
// refused with a warning and the normal bump applies.
func TestReleaseAsNotGreaterRefused(t *testing.T) {
	for _, want := range []string{"0.0.9", "0.1.0"} {
		t.Run(want, func(t *testing.T) {
			f, logged := runWith(t, goConfig(), "0.1.0",
				forge.Commit{SHA: "c1", Message: "fix: a fix\n\nRelease-As: " + want})
			wantPRAt(t, f, "0.1.1")
			if !strings.Contains(logged, "not greater than the current version 0.1.0") {
				t.Errorf("expected a not-greater warning, got:\n%s", logged)
			}
		})
	}
}

// Once the requested version is released the footer is inert, two ways over:
// the commit carrying it is behind the tag boundary, so it is not even parsed,
// and a fresh commit asking for the version already released is refused.
func TestReleaseAsNoEffectOnceTagged(t *testing.T) {
	t.Run("commit behind the tag boundary", func(t *testing.T) {
		f := newFake()
		f.tags["v0.1.0"] = "c1" // the release-as commit is what v0.1.0 points at
		f.commits["."] = []forge.Commit{
			{SHA: "c2", Message: "docs: tweak readme"},
			{SHA: "c1", Message: "feat: add the thing\n\nRelease-As: 0.1.0"},
		}
		l := &logSink{}
		man := config.Manifest{".": "0.1.0"}
		opts := Options{TargetBranch: "main", Now: fixedClock(), Logf: l.logf}
		if err := Run(context.Background(), f, goConfig(), man, opts); err != nil {
			t.Fatal(err)
		}
		if len(f.created) != 0 {
			t.Errorf("expected no PR (only docs since the tag), got %d", len(f.created))
		}
	})
	t.Run("asking again is refused", func(t *testing.T) {
		f, logged := runWith(t, goConfig(), "0.1.0",
			forge.Commit{SHA: "c3", Message: "fix: later fix\n\nRelease-As: 0.1.0"})
		wantPRAt(t, f, "0.1.1")
		if !strings.Contains(logged, "not greater than the current version 0.1.0") {
			t.Errorf("expected a not-greater warning, got:\n%s", logged)
		}
	})
}

// The footer key is case-insensitive and a leading "v" is tolerated.
func TestReleaseAsCaseAndVPrefix(t *testing.T) {
	for _, footer := range []string{
		"Release-As: 0.2.0",
		"release-as: 0.2.0",
		"RELEASE-AS: 0.2.0",
		"Release-As: v0.2.0",
		"release-as: V0.2.0",
	} {
		t.Run(footer, func(t *testing.T) {
			f, _ := runWith(t, goConfig(), "0.1.0",
				forge.Commit{SHA: "c1", Message: "fix: a fix\n\n" + footer})
			wantPRAt(t, f, "0.2.0") // not the 0.1.1 a plain fix would give
		})
	}
}

// release-please opens a release PR for a Release-As request even when no commit
// implies a bump, so a chore-only range still releases when one is pinned.
func TestReleaseAsReleasesWithoutReleasableCommits(t *testing.T) {
	f, _ := runWith(t, goConfig(), "0.0.5",
		forge.Commit{SHA: "c1", Message: "chore: tidy up\n\nRelease-As: 0.1.0"})
	wantPRAt(t, f, "0.1.0")
}

// ...but with nothing at all since the tag there is still no release: a config
// release-as alone must not reopen a PR for an already-released tree.
func TestReleaseAsNoCommitsSinceTag(t *testing.T) {
	f, _ := runWith(t, releaseAsConfig(t, "1.0.0"), "0.0.5")
	if len(f.created) != 0 {
		t.Errorf("expected no PR with no commits since the tag, got %d", len(f.created))
	}
}

// ---- release PR reuse (branch reset closes the PR) -------------------------

const releaseBranch = "releasejo--branches--main"

// pendingPull is a release PR on the release branch in the given state.
func pendingPull(number int, state string, labels ...forge.Label) forge.Pull {
	p := forge.Pull{Number: number, State: state, Labels: labels}
	p.Head.Ref = releaseBranch
	return p
}

// runRefresh executes one cycle with a stale release branch and the given PRs,
// returning the fake and the log.
func runRefresh(t *testing.T, current string, pulls ...forge.Pull) (*fakeForge, string) {
	t.Helper()
	f := newFake()
	f.commits["."] = []forge.Commit{{SHA: "c1", Message: "fix: correct a boundary check"}}
	f.branches[releaseBranch] = true // a branch from a previous run
	f.pulls = pulls
	l := &logSink{}
	opts := Options{TargetBranch: "main", Now: fixedClock(), Logf: l.logf}
	if err := Run(context.Background(), f, goConfig(), config.Manifest{".": current}, opts); err != nil {
		t.Fatal(err)
	}
	return f, l.text()
}

// The branch reset deletes the head branch, which makes Forgejo close the PR.
// The refresh must reopen THAT PR (same number), not leave it closed and not
// open another one. Regression test for #93: "refreshing release PR #93"
// followed by the PR being closed with no new PR opened.
func TestRefreshReopensPRClosedByBranchReset(t *testing.T) {
	f, logged := runRefresh(t, "0.0.6", pendingPull(93, "open", forge.Label{ID: 7, Name: pendingLabel}))

	if f.deletes != 1 || f.closedBy != 1 {
		t.Fatalf("expected the reset to delete the branch and close the PR; deletes=%d closedByReset=%d", f.deletes, f.closedBy)
	}
	if len(f.created) != 0 {
		t.Errorf("expected no new PR to be opened, got %d", len(f.created))
	}
	if len(f.edited) != 1 || f.edited[0] != 93 {
		t.Fatalf("expected PR #93 to be refreshed, edited=%v", f.edited)
	}
	pr := f.pull(93)
	if pr == nil || pr.State != "open" {
		t.Fatalf("PR #93 must be open at the end of the run, got %+v", pr)
	}
	if pr.Title != "chore(release): 0.0.7" {
		t.Errorf("refreshed title = %q, want %q", pr.Title, "chore(release): 0.0.7")
	}
	if !strings.Contains(logged, "refreshing release PR #93") {
		t.Errorf("expected a refresh log line, got:\n%s", logged)
	}
}

// A PR a previous (broken) run left closed and unmerged is picked up and
// reopened rather than replaced — this is what stops the number churning.
func TestRefreshReusesClosedUnmergedPR(t *testing.T) {
	f, _ := runRefresh(t, "0.0.6", pendingPull(93, "closed", forge.Label{ID: 7, Name: pendingLabel}))

	if len(f.created) != 0 {
		t.Errorf("expected the closed release PR to be reused, but %d new PR(s) were opened", len(f.created))
	}
	if len(f.edited) != 1 || f.edited[0] != 93 {
		t.Fatalf("expected PR #93 to be refreshed, edited=%v", f.edited)
	}
	if pr := f.pull(93); pr == nil || pr.State != "open" {
		t.Fatalf("PR #93 must be reopened, got %+v", pr)
	}
}

// A MERGED release PR is not a refresh candidate: it is already released and
// belongs to tagMerged. The run must open a fresh PR instead of reopening it.
func TestRefreshIgnoresMergedPR(t *testing.T) {
	f := newFake()
	f.commits["."] = []forge.Commit{{SHA: "c1", Message: "fix: correct a boundary check"}}
	f.branches[releaseBranch] = true
	f.tags["v0.0.6"] = "t0" // already tagged, so tagMerged cuts nothing new
	merged := pendingPull(93, "closed", forge.Label{ID: 7, Name: pendingLabel})
	merged.Merged = true
	merged.MergeCommitSHA = "mmm"
	f.pulls = []forge.Pull{merged}

	opts := Options{TargetBranch: "main", Now: fixedClock()}
	if err := Run(context.Background(), f, goConfig(), config.Manifest{".": "0.0.6"}, opts); err != nil {
		t.Fatal(err)
	}
	if len(f.edited) != 0 {
		t.Errorf("a merged PR must not be refreshed, edited=%v", f.edited)
	}
	if len(f.created) != 1 {
		t.Fatalf("expected a fresh PR to be opened, got %d", len(f.created))
	}
	if ids := f.labelSet[f.created[0].Number]; len(ids) != 1 {
		t.Errorf("expected the pending label on the new PR, got %v", ids)
	}
}

// A closed PR on the release branch WITHOUT the pending label is not ours to
// reopen (someone closed it deliberately); open a new one.
func TestRefreshIgnoresClosedPRWithoutPendingLabel(t *testing.T) {
	f, _ := runRefresh(t, "0.0.6", pendingPull(93, "closed"))

	if len(f.edited) != 0 {
		t.Errorf("an unlabelled closed PR must not be refreshed, edited=%v", f.edited)
	}
	if len(f.created) != 1 {
		t.Errorf("expected a new PR, got %d", len(f.created))
	}
}

// Two consecutive runs must keep ONE PR number: the first opens it, the second
// refreshes (and reopens) the same one, even though the reset closed it.
func TestRefreshKeepsPRNumberAcrossRuns(t *testing.T) {
	f := newFake()
	f.commits["."] = []forge.Commit{{SHA: "c1", Message: "fix: correct a boundary check"}}
	man := config.Manifest{".": "0.0.6"}
	opts := Options{TargetBranch: "main", Now: fixedClock()}

	if err := Run(context.Background(), f, goConfig(), man, opts); err != nil {
		t.Fatal(err)
	}
	if len(f.created) != 1 {
		t.Fatalf("first run should open 1 PR, got %d", len(f.created))
	}
	number := f.created[0].Number

	if err := Run(context.Background(), f, goConfig(), man, opts); err != nil {
		t.Fatal(err)
	}
	if len(f.created) != 1 {
		t.Errorf("second run must reuse PR #%d, but opened %d PRs in total", number, len(f.created))
	}
	if len(f.edited) != 1 || f.edited[0] != number {
		t.Errorf("second run should refresh PR #%d, edited=%v", number, f.edited)
	}
	if pr := f.pull(number); pr == nil || pr.State != "open" {
		t.Fatalf("PR #%d must be open after the second run, got %+v", number, pr)
	}
}

// In a monorepo the release tag points at the release-PR merge, which only
// touches the manifest and changelog, so it never shows up in the package's
// path-filtered history. Commits reachable from the tag must still count as
// released; before this, every merge re-released the same commits forever.
func TestMonorepoTagOffPathDoesNotReRelease(t *testing.T) {
	f := newFake()
	feat := forge.Commit{SHA: "r1", Message: "feat(ratelimit): limiter"}
	f.commits["ratelimit"] = []forge.Commit{feat}
	f.tags["ratelimit/v0.0.1"] = "merge1" // release merge, not on the ratelimit path
	f.commitsAt["merge1\x00ratelimit"] = []forge.Commit{feat}
	cfg, err := config.ParseConfig([]byte(`{
      "release-type": "go",
      "include-component-in-tag": true,
      "tag-separator": "/",
      "separate-pull-requests": true,
      "packages": {
        "ratelimit": { "component": "ratelimit" },
        "csrf": { "component": "csrf" }
      }
    }`))
	if err != nil {
		t.Fatal(err)
	}
	man := config.Manifest{"ratelimit": "0.0.1", "csrf": "0.0.0"}
	f.commits["csrf"] = []forge.Commit{{SHA: "c1", Message: "feat(csrf): protector"}}

	if err := Run(context.Background(), f, cfg, man, Options{TargetBranch: "main", Now: fixedClock()}); err != nil {
		t.Fatal(err)
	}
	for _, pr := range f.created {
		if strings.HasSuffix(pr.Head.Ref, "--ratelimit") {
			t.Fatalf("ratelimit re-released with no new commits: %s", pr.Title)
		}
	}
	if len(f.created) != 1 {
		t.Fatalf("expected only the csrf release PR, got %d", len(f.created))
	}

	// A new ratelimit commit after the tag is released.
	f.commits["ratelimit"] = []forge.Commit{{SHA: "r2", Message: "fix(ratelimit): retry-after"}, feat}
	f.created = nil
	if err := Run(context.Background(), f, cfg, man, Options{TargetBranch: "main", Now: fixedClock()}); err != nil {
		t.Fatal(err)
	}
	var got bool
	for _, pr := range f.created {
		if strings.HasSuffix(pr.Head.Ref, "--ratelimit") {
			got = true
			if pr.Title != "chore(release): 0.0.2" {
				t.Errorf("ratelimit title = %q, want chore(release): 0.0.2", pr.Title)
			}
		}
	}
	if !got {
		t.Fatal("new ratelimit commit after the tag was not released")
	}
}

// changelog-path is relative to the package, as in release-please.
func TestMonorepoChangelogPerPackage(t *testing.T) {
	f := newFake()
	f.commits["services/api"] = []forge.Commit{{SHA: "a1", Message: "feat: api thing"}}
	cfg, err := config.ParseConfig([]byte(`{
      "release-type": "go",
      "packages": {
        ".": { "component": "root" },
        "services/api": { "component": "api" }
      }
    }`))
	if err != nil {
		t.Fatal(err)
	}
	man := config.Manifest{".": "1.0.0", "services/api": "1.0.0"}
	if err := Run(context.Background(), f, cfg, man, Options{TargetBranch: "main", Now: fixedClock()}); err != nil {
		t.Fatal(err)
	}
	var wrote []string
	for k := range f.puts {
		wrote = append(wrote, k[strings.IndexByte(k, 0)+1:])
	}
	joined := strings.Join(wrote, ",")
	if !strings.Contains(joined, "services/api/CHANGELOG.md") {
		t.Errorf("api changelog not written under its package: %v", wrote)
	}
	for _, w := range wrote {
		if w == "CHANGELOG.md" {
			t.Errorf("api release wrote the root changelog: %v", wrote)
		}
	}
}

// Merging one component's release PR must not tag the packages that have never
// been released: their manifest still says 0.0.0, and a 0.0.0 tag would count
// all their commits as released, so their first release PR would never come.
func TestTagOnMergeSkipsUnreleasedPackages(t *testing.T) {
	f := newFake()
	merged := forge.Pull{Number: 9, State: "closed", Merged: true, MergeCommitSHA: "mmm",
		Labels: []forge.Label{{ID: 7, Name: pendingLabel}}}
	f.pulls = []forge.Pull{merged}
	cfg, err := config.ParseConfig([]byte(`{
      "release-type": "go",
      "include-component-in-tag": true,
      "tag-separator": "/",
      "separate-pull-requests": true,
      "packages": {
        "csrf": { "component": "csrf" },
        "authn": { "component": "authn" }
      }
    }`))
	if err != nil {
		t.Fatal(err)
	}
	man := config.Manifest{"csrf": "0.0.1", "authn": "0.0.0"}

	if err := Run(context.Background(), f, cfg, man, Options{TargetBranch: "main", Now: fixedClock()}); err != nil {
		t.Fatal(err)
	}
	if len(f.releases) != 1 || f.releases[0] != "csrf/v0.0.1" {
		t.Fatalf("expected only csrf/v0.0.1, got %v", f.releases)
	}
}

// A commit authored before the last release but merged after it lists below
// the tag commit in date order. It is still unreleased and must count.
func TestCommitMergedAfterTagButAuthoredBeforeIsReleased(t *testing.T) {
	f := newFake()
	old := forge.Commit{SHA: "o1", Message: "feat: shipped in 1.0.0"}
	tag := forge.Commit{SHA: "t1", Message: "chore(release): 1.0.0"}
	late := forge.Commit{SHA: "f1", Message: "fix: lost purchase mail"}
	merge := forge.Commit{SHA: "m2", Message: "Merge pull request 'fix: lost purchase mail' (#94)"}
	f.commits["."] = []forge.Commit{merge, tag, late, old} // newest first, by date
	f.tags["1.0.0"] = "t1"
	f.commitsAt["t1\x00."] = []forge.Commit{tag, old}
	cfg, err := config.ParseConfig([]byte(`{
      "release-type": "go",
      "include-v-in-tag": false,
      "packages": { ".": {} }
    }`))
	if err != nil {
		t.Fatal(err)
	}
	man := config.Manifest{".": "1.0.0"}
	if err := Run(context.Background(), f, cfg, man, Options{TargetBranch: "main", Now: fixedClock()}); err != nil {
		t.Fatal(err)
	}
	if len(f.created) != 1 {
		t.Fatalf("want one release PR for the late fix, got %d", len(f.created))
	}
	pr := f.created[0]
	if pr.Title != "chore(release): 1.0.1" {
		t.Errorf("title = %q, want chore(release): 1.0.1", pr.Title)
	}
	if !strings.Contains(pr.Body, "lost purchase mail") || strings.Contains(pr.Body, "shipped in 1.0.0") {
		t.Errorf("changelog should hold only the late fix:\n%s", pr.Body)
	}
}

// The run that tags a release can be one that checked out main just before
// the release PR merged: its manifest still holds the previous version while
// the commits it lists come from the live branch. It must not propose the
// version it just tagged again (website #111, #114, #120).
func TestRunThatTagsAReleaseDoesNotProposeItAgain(t *testing.T) {
	f := newFake()
	merged := forge.Pull{Number: 119, State: "closed", Merged: true, MergeCommitSHA: "m067",
		Labels: []forge.Label{{ID: 7, Name: pendingLabel}}}
	f.pulls = []forge.Pull{merged}
	f.tags["v0.66.1"] = "t661"
	f.files["m067\x00.release-please-manifest.json"] = &forge.File{Content: "{\n  \".\": \"0.67.0\"\n}\n"}
	f.files["m067\x00CHANGELOG.md"] = &forge.File{
		Content: "# Changelog\n\n## 0.67.0 (2026-10-23)\n\n### Features\n\n* products filter (f2)\n",
	}
	rel := forge.Commit{SHA: "m067", Message: "chore(release): 0.67.0 (#119)"}
	feat := forge.Commit{SHA: "f2", Message: "feat: products filter"}
	fix := forge.Commit{SHA: "f1", Message: "fix: no shore fade"}
	tag := forge.Commit{SHA: "t661", Message: "chore(release): 0.66.1"}
	f.commits["."] = []forge.Commit{rel, feat, fix, tag}
	f.commitsAt["t661\x00."] = []forge.Commit{tag}
	f.commitsAt["m067\x00."] = []forge.Commit{rel, feat, fix, tag}
	stale := config.Manifest{".": "0.66.1"} // checked out before #119 merged
	l := &logSink{}

	if err := Run(context.Background(), f, goConfig(), stale, Options{TargetBranch: "main", Now: fixedClock(), Logf: l.logf}); err != nil {
		t.Fatal(err)
	}
	if len(f.releases) != 1 || f.releases[0] != "v0.67.0" {
		t.Fatalf("expected release v0.67.0, got %v", f.releases)
	}
	if len(f.created) != 0 {
		t.Fatalf("proposed %q again right after tagging it", f.created[0].Title)
	}
	if !strings.Contains(l.text(), "nothing to release") {
		t.Errorf("expected nothing to release, log:\n%s", l.text())
	}
}

// A later run whose checkout trails main plans from main's manifest: only the
// commits after the newest release count.
func TestStaleCheckoutPlansFromMainsManifest(t *testing.T) {
	f := newFake()
	f.tags["v0.66.1"] = "t661"
	f.tags["v0.67.0"] = "m067"
	f.files["main\x00.release-please-manifest.json"] = &forge.File{Content: "{\n  \".\": \"0.67.0\"\n}\n"}
	next := forge.Commit{SHA: "f3", Message: "feat: map of shores"}
	rel := forge.Commit{SHA: "m067", Message: "chore(release): 0.67.0 (#119)"}
	feat := forge.Commit{SHA: "f2", Message: "feat: products filter"}
	tag := forge.Commit{SHA: "t661", Message: "chore(release): 0.66.1"}
	f.commits["."] = []forge.Commit{next, rel, feat, tag}
	f.commitsAt["t661\x00."] = []forge.Commit{tag}
	f.commitsAt["m067\x00."] = []forge.Commit{rel, feat, tag}

	if err := Run(context.Background(), f, goConfig(), config.Manifest{".": "0.66.1"}, Options{TargetBranch: "main", Now: fixedClock()}); err != nil {
		t.Fatal(err)
	}
	if len(f.created) != 1 {
		t.Fatalf("want one release PR, got %d", len(f.created))
	}
	pr := f.created[0]
	if pr.Title != "chore(release): 0.68.0" {
		t.Errorf("title = %q, want chore(release): 0.68.0", pr.Title)
	}
	if !strings.Contains(pr.Body, "map of shores") || strings.Contains(pr.Body, "products filter") {
		t.Errorf("changelog should hold only the commit after 0.67.0:\n%s", pr.Body)
	}
}
