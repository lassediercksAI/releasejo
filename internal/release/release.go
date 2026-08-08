// Package release is the orchestrator: it turns conventional commits into a
// release pull request, and — when that PR is merged — into tags + releases.
// It is the releasejo equivalent of release-please's "manifest" run, targeting
// Forgejo/Gitea instead of GitHub.
package release

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/lassediercksAI/releasejo/internal/changelog"
	"github.com/lassediercksAI/releasejo/internal/config"
	"github.com/lassediercksAI/releasejo/internal/conventional"
	"github.com/lassediercksAI/releasejo/internal/forge"
	"github.com/lassediercksAI/releasejo/internal/semver"
	"github.com/lassediercksAI/releasejo/internal/updater"
)

const (
	pendingLabel = "autorelease: pending"
	taggedLabel  = "autorelease: tagged"
	labelColor   = "ededed"
	prBodyMarker = "<!-- releasejo -->"
)

// API is the slice of the forge client the orchestrator uses. Defining it here
// (consumer-side) keeps *forge.Client as the production impl while letting tests
// drive Run with an in-memory fake.
type API interface {
	TagCommit(ctx context.Context, tag string) (string, error)
	Commits(ctx context.Context, sha, path string, max int) ([]forge.Commit, error)
	BranchExists(ctx context.Context, name string) (bool, error)
	CreateBranch(ctx context.Context, name, from string) error
	GetFile(ctx context.Context, path, ref string) (*forge.File, error)
	ChangeFiles(ctx context.Context, branch, message string, files []forge.FileChange) error
	Pulls(ctx context.Context, state string) ([]forge.Pull, error)
	CreatePull(ctx context.Context, head, base, title, body string) (*forge.Pull, error)
	EditPull(ctx context.Context, number int, title, body string) error
	EnsureLabel(ctx context.Context, name, color string) (int64, error)
	SetIssueLabels(ctx context.Context, number int, ids []int64) error
	CreateRelease(ctx context.Context, tag, target, name, body string, prerelease bool) error
}

// Options tune a run. Zero values are sensible defaults except TargetBranch.
type Options struct {
	TargetBranch string
	MaxCommits   int
	DryRun       bool
	Now          func() time.Time
	Logf         func(string, ...any)
}

func (o *Options) defaults() {
	if o.MaxCommits == 0 {
		o.MaxCommits = 500
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Logf == nil {
		o.Logf = func(string, ...any) {}
	}
}

// pkgPlan is a pending release for one package.
type pkgPlan struct {
	pkg     config.Package
	last    semver.Version
	next    semver.Version
	tag     string
	entry   string // rendered changelog entry
	commits int
}

// Run executes one full cycle: first tag any just-merged release PR, then
// (re)build the pending release PR from new commits.
func Run(ctx context.Context, cl API, cfg *config.Config, man config.Manifest, opts Options) error {
	opts.defaults()

	if err := tagMerged(ctx, cl, cfg, man, opts); err != nil {
		return fmt.Errorf("tagging merged release: %w", err)
	}

	plans, err := computePlans(ctx, cl, cfg, man, opts)
	if err != nil {
		return err
	}
	if len(plans) == 0 {
		opts.Logf("no releasable changes since the last release")
		return nil
	}
	return upsertReleasePRs(ctx, cl, cfg, man, plans, opts)
}

// TagFor builds the git tag for a package version, honouring include-v-in-tag
// and include-component-in-tag (e.g. "v1.2.3" or "build-harbor-image-v0.1.0").
func TagFor(cfg *config.Config, p config.Package, v semver.Version) string {
	ver := v.String()
	if p.IncludeV() {
		ver = "v" + ver
	}
	if cfg.IncludeComponent(p) && p.Component != "" {
		return p.Component + p.TagSeparator + ver
	}
	return ver
}

// computePlans figures out, per package, the next version + changelog from the
// commits since that package's last release tag.
func computePlans(ctx context.Context, cl API, cfg *config.Config, man config.Manifest, opts Options) ([]pkgPlan, error) {
	var plans []pkgPlan
	date := opts.Now().UTC().Format("2006-01-02")

	for _, p := range cfg.Packages {
		last, err := semver.Parse(orZero(man[p.Path]))
		if err != nil {
			return nil, fmt.Errorf("package %q: manifest version: %w", p.Path, err)
		}
		boundary, err := cl.TagCommit(ctx, TagFor(cfg, p, last))
		if err != nil {
			return nil, fmt.Errorf("package %q: resolving last tag: %w", p.Path, err)
		}

		raw, err := cl.Commits(ctx, opts.TargetBranch, p.Path, opts.MaxCommits)
		if err != nil {
			return nil, fmt.Errorf("package %q: listing commits: %w", p.Path, err)
		}

		var ccs []conventional.Commit
		level := semver.None
		for _, rc := range raw {
			if boundary != "" && rc.SHA == boundary {
				break // reached the last release
			}
			cc := conventional.Parse(rc.Message, rc.SHA)
			if isReleaseCommit(cc) {
				continue
			}
			ccs = append(ccs, cc)
			level = semver.Max(level, cc.Level())
		}
		if level == semver.None {
			opts.Logf("%s: nothing to release", label(p))
			continue
		}

		next := bumpFor(p, last, level)
		compareURL := "" // reserved: a Forgejo compare link once we track the base sha
		entry := changelog.Render(next.String(), date, ccs, sections(p), compareURL)
		plans = append(plans, pkgPlan{
			pkg:     p,
			last:    last,
			next:    next,
			tag:     TagFor(cfg, p, next),
			entry:   entry,
			commits: len(ccs),
		})
		opts.Logf("%s: %s -> %s (%s, %d commits)", label(p), last, next, level, len(ccs))
	}
	return plans, nil
}

// upsertReleasePRs opens/refreshes the release PR(s): one aggregate PR by
// default, or one PR per package when separate-pull-requests is set. Every branch
// is rebuilt from base, so after one component PR merges the others are re-derived
// from the new base on the next run — sequential merges reconcile the shared
// manifest cleanly.
func upsertReleasePRs(ctx context.Context, cl API, cfg *config.Config, man config.Manifest, plans []pkgPlan, opts Options) error {
	if cfg.SeparatePullRequests {
		for _, pl := range plans {
			branch := "releasejo--branches--" + opts.TargetBranch + "--components--" + componentSlug(pl.pkg)
			if err := buildAndUpsertPR(ctx, cl, man, []pkgPlan{pl}, branch, opts); err != nil {
				return err
			}
		}
		return nil
	}
	return buildAndUpsertPR(ctx, cl, man, plans, "releasejo--branches--"+opts.TargetBranch, opts)
}

// buildAndUpsertPR rebuilds `branch` from base with the given plans' changes and
// opens or refreshes its PR.
func buildAndUpsertPR(ctx context.Context, cl API, man config.Manifest, plans []pkgPlan, branch string, opts Options) error {
	title := releaseTitle(plans)
	body := releaseBody(plans)

	if opts.DryRun {
		opts.Logf("[dry-run] would open/update PR %q on branch %s:\n%s", title, branch, body)
		return nil
	}

	exists, err := cl.BranchExists(ctx, branch)
	if err != nil {
		return err
	}
	if !exists {
		if err := cl.CreateBranch(ctx, branch, opts.TargetBranch); err != nil {
			return fmt.Errorf("creating release branch: %w", err)
		}
	}

	// Every file's desired content derives from BASE, so re-running is
	// idempotent. All changed files land in ONE commit (Gitea's multi-file
	// contents API) titled with the release — not one commit per file, and
	// not the doubled "chore(release): chore(release): …" the per-file path
	// produced. Unchanged files are skipped, so a no-op re-run adds no commit.
	changes, err := collectChanges(ctx, cl, man, plans, branch, opts)
	if err != nil {
		return err
	}
	if len(changes) > 0 {
		if err := cl.ChangeFiles(ctx, branch, title, changes); err != nil {
			return fmt.Errorf("writing release commit: %w", err)
		}
	}

	// open or refresh the PR
	open, err := findOpenReleasePR(ctx, cl, branch)
	if err != nil {
		return err
	}
	if open != nil {
		opts.Logf("refreshing release PR #%d", open.Number)
		return cl.EditPull(ctx, open.Number, title, body)
	}
	pr, err := cl.CreatePull(ctx, branch, opts.TargetBranch, title, body)
	if err != nil {
		return fmt.Errorf("creating release PR: %w", err)
	}
	opts.Logf("opened release PR #%d", pr.Number)
	id, err := cl.EnsureLabel(ctx, pendingLabel, labelColor)
	if err != nil {
		return err
	}
	return cl.SetIssueLabels(ctx, pr.Number, []int64{id})
}

// tagMerged looks for a just-merged release PR and cuts the tags/releases its
// merge introduced (versions now present in main's manifest but not yet tagged).
func tagMerged(ctx context.Context, cl API, cfg *config.Config, man config.Manifest, opts Options) error {
	closed, err := cl.Pulls(ctx, "closed")
	if err != nil {
		return err
	}
	for _, pr := range closed {
		if !pr.Merged || !hasLabel(pr, pendingLabel) || hasLabel(pr, taggedLabel) {
			continue
		}
		opts.Logf("release PR #%d merged (%s); cutting releases", pr.Number, short(pr.MergeCommitSHA))
		for _, p := range cfg.Packages {
			v, err := semver.Parse(orZero(man[p.Path]))
			if err != nil {
				return err
			}
			tag := TagFor(cfg, p, v)
			existing, err := cl.TagCommit(ctx, tag)
			if err != nil {
				return err
			}
			if existing != "" {
				continue // already released
			}
			notes := extractEntry(changelogOnMain(ctx, cl, p, opts), v.String())
			if opts.DryRun {
				opts.Logf("[dry-run] would create release %s", tag)
				continue
			}
			if err := cl.CreateRelease(ctx, tag, opts.TargetBranch, tag, notes, v.IsPrerelease()); err != nil {
				return fmt.Errorf("creating release %s: %w", tag, err)
			}
			opts.Logf("created release %s", tag)
		}
		if !opts.DryRun {
			id, err := cl.EnsureLabel(ctx, taggedLabel, labelColor)
			if err != nil {
				return err
			}
			// keep pending removed by replacing labels with just "tagged"
			if err := cl.SetIssueLabels(ctx, pr.Number, []int64{id}); err != nil {
				return err
			}
		}
	}
	return nil
}

// ---- helpers --------------------------------------------------------------

// collectChanges computes the desired content of every release-managed file
// (changelog + version files + manifest) from BASE, then diffs each against the
// branch's current content, returning the create/update operations needed. An
// unchanged file yields no operation, so a re-run with nothing new commits
// nothing — and everything that IS changed goes into a single commit.
func collectChanges(ctx context.Context, cl API, man config.Manifest, plans []pkgPlan, branch string, opts Options) ([]forge.FileChange, error) {
	newMan := cloneManifest(man)
	var order []string
	desired := map[string]string{}
	want := func(path, content string) {
		if _, seen := desired[path]; !seen {
			order = append(order, path)
		}
		desired[path] = content
	}

	for _, pl := range plans {
		base, err := readFileOr(ctx, cl, pl.pkg.ChangelogPath, opts.TargetBranch)
		if err != nil {
			return nil, fmt.Errorf("%s: changelog: %w", label(pl.pkg), err)
		}
		want(pl.pkg.ChangelogPath, changelog.Prepend(base, pl.entry))

		for _, f := range versionFiles(pl.pkg) {
			base, err := readFileOr(ctx, cl, f, opts.TargetBranch)
			if err != nil {
				return nil, fmt.Errorf("%s: %s: %w", label(pl.pkg), f, err)
			}
			want(f, updater.Apply(pl.pkg.ReleaseType, f, base, pl.next.String()))
		}
		newMan[pl.pkg.Path] = pl.next.String()
	}
	manBytes, err := newMan.Marshal()
	if err != nil {
		return nil, err
	}
	want(".release-please-manifest.json", string(manBytes))

	var changes []forge.FileChange
	for _, path := range order {
		cur, err := cl.GetFile(ctx, path, branch)
		switch {
		case err == nil && cur.Content == desired[path]:
			continue // already up to date on the branch
		case err == nil:
			changes = append(changes, forge.FileChange{Op: "update", Path: path, Content: desired[path], SHA: cur.SHA})
		case forge.NotFound(err):
			changes = append(changes, forge.FileChange{Op: "create", Path: path, Content: desired[path]})
		default:
			return nil, err
		}
	}
	return changes, nil
}

// readFileOr returns the file's content at ref, or "" if it does not exist.
func readFileOr(ctx context.Context, cl API, path, ref string) (string, error) {
	f, err := cl.GetFile(ctx, path, ref)
	if err != nil {
		if forge.NotFound(err) {
			return "", nil
		}
		return "", err
	}
	return f.Content, nil
}

func findOpenReleasePR(ctx context.Context, cl API, branch string) (*forge.Pull, error) {
	open, err := cl.Pulls(ctx, "open")
	if err != nil {
		return nil, err
	}
	for i := range open {
		if open[i].Head.Ref == branch {
			return &open[i], nil
		}
	}
	return nil, nil
}

func changelogOnMain(ctx context.Context, cl API, p config.Package, opts Options) string {
	f, err := cl.GetFile(ctx, p.ChangelogPath, opts.TargetBranch)
	if err != nil {
		return ""
	}
	return f.Content
}

// extractEntry pulls the section for a specific version out of a CHANGELOG,
// used as release notes. Matches "## [ver]" or "## ver".
func extractEntry(changelogText, version string) string {
	lines := strings.Split(changelogText, "\n")
	start := -1
	for i, ln := range lines {
		if strings.HasPrefix(ln, "## ") && strings.Contains(ln, version) {
			start = i
			break
		}
	}
	if start == -1 {
		return ""
	}
	end := len(lines)
	for i := start + 1; i < len(lines); i++ {
		if strings.HasPrefix(lines[i], "## ") {
			end = i
			break
		}
	}
	return strings.TrimSpace(strings.Join(lines[start+1:end], "\n"))
}

func isReleaseCommit(c conventional.Commit) bool {
	return c.Type == "chore" && strings.Contains(strings.ToLower(c.Description), "release")
}

func sections(p config.Package) []changelog.Section {
	if len(p.ChangelogSections) == 0 {
		return changelog.DefaultSections()
	}
	out := make([]changelog.Section, len(p.ChangelogSections))
	for i, s := range p.ChangelogSections {
		out[i] = changelog.Section{Type: s.Type, Section: s.Section, Hidden: s.Hidden}
	}
	return out
}

// versionFiles is the set of files whose version should be bumped: the
// release-type's conventional file (if any) plus configured extra-files.
func versionFiles(p config.Package) []string {
	var files []string
	switch p.ReleaseType {
	case "simple":
		files = append(files, join(p.Path, "version.txt"))
	case "node":
		files = append(files, join(p.Path, "package.json"))
	}
	for _, f := range p.ExtraFiles {
		files = append(files, join(p.Path, f))
	}
	return files
}

func releaseTitle(plans []pkgPlan) string {
	if len(plans) == 1 {
		return "chore(release): " + plans[0].next.String()
	}
	var parts []string
	for _, p := range plans {
		parts = append(parts, fmt.Sprintf("%s %s", compName(p.pkg), p.next))
	}
	return "chore(release): " + strings.Join(parts, ", ")
}

func releaseBody(plans []pkgPlan) string {
	var b strings.Builder
	b.WriteString(prBodyMarker + "\n\n")
	b.WriteString("Automated release prepared by releasejo. Merge to tag and publish.\n\n")
	for _, p := range plans {
		fmt.Fprintf(&b, "## %s: %s\n\n%s\n\n", compName(p.pkg), p.next, p.entry)
	}
	return strings.TrimSpace(b.String())
}

func label(p config.Package) string {
	if c := compName(p); c != "" {
		return c
	}
	return p.Path
}
func compName(p config.Package) string {
	if p.Component != "" {
		return p.Component
	}
	if p.PackageName != "" {
		return p.PackageName
	}
	if p.Path == "." || p.Path == "" {
		return "root"
	}
	return p.Path
}

// bumpFor applies the package's versioning strategy. `always-bump-patch` /
// `always-bump-minor` force that bump size for any releasable change (release-please
// semantics); anything else is the default semantic bump (level-driven, honouring
// bump-minor-pre-major).
func bumpFor(p config.Package, v semver.Version, level semver.Level) semver.Version {
	switch p.Versioning {
	case "always-bump-patch":
		return v.Bump(semver.Patch, false)
	case "always-bump-minor":
		return v.Bump(semver.Minor, false)
	default:
		return v.Bump(level, p.BumpMinorPreMajor)
	}
}

// componentSlug makes a branch-safe slug from a package's component/path.
func componentSlug(p config.Package) string {
	var b strings.Builder
	for _, r := range compName(p) {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	return b.String()
}

func hasLabel(p forge.Pull, name string) bool {
	for _, l := range p.Labels {
		if l.Name == name {
			return true
		}
	}
	return false
}

func cloneManifest(m config.Manifest) config.Manifest {
	out := make(config.Manifest, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func orZero(v string) string {
	if v == "" {
		return "0.0.0"
	}
	return v
}
func short(s string) string {
	if len(s) > 7 {
		return s[:7]
	}
	return s
}
func join(base, f string) string {
	if base == "" || base == "." {
		return f
	}
	return strings.TrimRight(base, "/") + "/" + f
}
