package release

import (
	"context"
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
	tags     map[string]string         // tag -> sha
	commits  map[string][]forge.Commit // path -> commits (newest first)
	files    map[string]*forge.File    // "ref\x00path" -> file
	branches map[string]bool           // branch -> exists
	pulls    []forge.Pull              // all PRs

	puts     map[string]string // "branch\x00path" -> content written
	created  []forge.Pull
	releases []string // tags released
	edits    int
	labelSet map[int][]int64
}

func newFake() *fakeForge {
	return &fakeForge{
		tags: map[string]string{}, commits: map[string][]forge.Commit{},
		files: map[string]*forge.File{}, branches: map[string]bool{},
		puts: map[string]string{}, labelSet: map[int][]int64{},
	}
}

func notFound() error { return &forge.APIError{Status: http.StatusNotFound, Body: "not found"} }

func (f *fakeForge) TagCommit(_ context.Context, tag string) (string, error) { return f.tags[tag], nil }

func (f *fakeForge) Commits(_ context.Context, _, path string, _ int) ([]forge.Commit, error) {
	return f.commits[path], nil
}

func (f *fakeForge) BranchExists(_ context.Context, name string) (bool, error) {
	return f.branches[name], nil
}
func (f *fakeForge) CreateBranch(_ context.Context, name, _ string) error {
	f.branches[name] = true
	return nil
}
func (f *fakeForge) GetFile(_ context.Context, path, ref string) (*forge.File, error) {
	if file, ok := f.files[ref+"\x00"+path]; ok {
		return file, nil
	}
	return nil, notFound()
}
func (f *fakeForge) PutFile(_ context.Context, path, branch, content, _, _ string) error {
	f.puts[branch+"\x00"+path] = content
	f.files[branch+"\x00"+path] = &forge.File{Content: content, SHA: "sha-" + path}
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
	return &p, nil
}
func (f *fakeForge) EditPull(_ context.Context, _ int, _, _ string) error      { f.edits++; return nil }
func (f *fakeForge) EnsureLabel(_ context.Context, _, _ string) (int64, error) { return 7, nil }
func (f *fakeForge) SetIssueLabels(_ context.Context, number int, ids []int64) error {
	f.labelSet[number] = ids
	return nil
}
func (f *fakeForge) CreateRelease(_ context.Context, tag, _, _, _ string, _ bool) error {
	f.releases = append(f.releases, tag)
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
