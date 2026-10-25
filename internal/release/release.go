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
	DeleteBranch(ctx context.Context, name string) error
	GetFile(ctx context.Context, path, ref string) (*forge.File, error)
	ChangeFiles(ctx context.Context, branch, message string, files []forge.FileChange) error
	Pulls(ctx context.Context, state string) ([]forge.Pull, error)
	GetPull(ctx context.Context, number int) (*forge.Pull, error)
	CreatePull(ctx context.Context, head, base, title, body string) (*forge.Pull, error)
	EditPull(ctx context.Context, number int, title, body string) error // refreshes AND reopens
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
	// CloseWait bounds the wait for Forgejo to close the release PR after
	// its head branch is reset (default 30s), polling every PollEvery
	// (default 1s).
	CloseWait time.Duration
	PollEvery time.Duration
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
	if o.CloseWait == 0 {
		o.CloseWait = 30 * time.Second
	}
	if o.PollEvery == 0 {
		o.PollEvery = time.Second
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

	cut, err := tagMerged(ctx, cl, cfg, man, opts)
	if err != nil {
		return fmt.Errorf("tagging merged release: %w", err)
	}

	// Plan against main as it is now, not this run's checkout. A run that
	// checked out main just before a release PR merged still holds the
	// previous manifest, while the commits it lists come from the live
	// branch: it would tag the merged release above and then propose the
	// very same version again from the old tag (website #111, #114, #120).
	man, err = liveManifest(ctx, cl, man, cut, opts)
	if err != nil {
		return err
	}

	plans, err := computePlans(ctx, cl, cfg, man, cut, opts)
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
func computePlans(ctx context.Context, cl API, cfg *config.Config, man config.Manifest, cut map[string]cutRelease, opts Options) ([]pkgPlan, error) {
	var plans []pkgPlan
	date := opts.Now().UTC().Format("2006-01-02")

	for _, p := range cfg.Packages {
		last, err := semver.Parse(orZero(man[p.Path]))
		if err != nil {
			return nil, fmt.Errorf("package %q: manifest version: %w", p.Path, err)
		}
		tag := TagFor(cfg, p, last)
		boundary, err := cl.TagCommit(ctx, tag)
		if err != nil {
			return nil, fmt.Errorf("package %q: resolving last tag: %w", p.Path, err)
		}
		// A tag this run just cut may not be listed yet; it sits on the
		// release PR's merge commit.
		if c, ok := cut[p.Path]; boundary == "" && ok && c.tag == tag {
			boundary = c.at
		}

		raw, err := cl.Commits(ctx, opts.TargetBranch, p.Path, opts.MaxCommits)
		if err != nil {
			return nil, fmt.Errorf("package %q: listing commits: %w", p.Path, err)
		}
		// The tag commit only bounds the walk when it touches the package path.
		// In a monorepo the tag sits on a release merge that touches only the
		// manifest and changelog, so it never appears in a path-filtered list
		// and every commit would count as unreleased again — re-releasing the
		// package on every merge. Exclude everything reachable from the tag.
		released, err := releasedCommits(ctx, cl, boundary, p.Path, opts.MaxCommits)
		if err != nil {
			return nil, fmt.Errorf("package %q: listing released commits: %w", p.Path, err)
		}

		var ccs []conventional.Commit
		level := semver.None
		for _, rc := range raw {
			// Membership, not position, decides what was released. The list is
			// ordered by date, so a commit authored before the tag but merged
			// after it (a rebased or long-lived branch) sits below the tag
			// commit; stopping at the tag would drop it from every future
			// release. Stop at the tag only when the tag's history could not
			// be listed (a tag's own history always contains the tag commit).
			if boundary != "" && rc.SHA == boundary {
				if len(released) == 0 {
					break
				}
				continue
			}
			if released[rc.SHA] {
				continue // already part of the last release
			}
			cc := conventional.Parse(rc.Message, rc.SHA)
			if isReleaseCommit(cc) {
				continue
			}
			ccs = append(ccs, cc)
			level = semver.Max(level, cc.Level())
		}
		next, overridden := nextVersion(p, last, level, ccs, opts.Logf)
		// Release unless the commits imply no bump — except when a Release-As
		// request raised the version, which releases on its own (release-please
		// opens a release PR for release-as even without releasable commits), as
		// long as there is something to release since the tag.
		if level == semver.None && !(overridden && len(ccs) > 0) {
			opts.Logf("%s: nothing to release", label(p))
			continue
		}

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

	// Find the release PR BEFORE touching the branch. Deleting the head branch
	// (below) makes Forgejo close the PR, and a PR closed mid-run may already be
	// out of the "open" listing by the time we look — which is how 0.0.5 opened a
	// fresh PR on nearly every run (#23–#26, #29, #30 …). Looking first pins the
	// number we then refresh and reopen.
	pr, err := findReleasePR(ctx, cl, branch)
	if err != nil {
		return err
	}

	// Rebuild the branch from base EVERY run so its tree matches base except for
	// the release files. Without this the branch stays frozen at its fork point:
	// non-release files (e.g. a CI workflow fixed on main) stay stale — so the
	// release PR runs outdated config while main is green — and release commits
	// stack (repeated identical versions). Delete-then-recreate is the API-only
	// equivalent of release-please's force-push, and the reset is what advances
	// the branch onto the new base: writing files onto the existing branch would
	// converge the release files (collectChanges always derives them from base)
	// but leave the rest of the tree behind main, so the reset stays.
	//
	// Forgejo CLOSES a PR whose head branch is deleted (it does not survive the
	// delete+recreate, contrary to what this comment claimed before #93 was found
	// closed right after a "refreshing release PR" run). That is why EditPull
	// sends state:"open" and why the PR is located above: the refresh reopens the
	// same PR instead of leaving it closed or opening another one.
	//
	// Only reached when there IS a release to make (callers gate on
	// len(plans) > 0), so this can never reset a no-op branch.
	exists, err := cl.BranchExists(ctx, branch)
	if err != nil {
		return err
	}
	if exists {
		if err := cl.DeleteBranch(ctx, branch); err != nil {
			return fmt.Errorf("resetting release branch to base: %w", err)
		}
		// Forgejo closes the PR from a queue, so the close can land after
		// the refresh below has "reopened" a PR that was still open, which
		// leaves it closed with no release PR at all (website #161, ogof
		// #362). Wait for the close before reopening.
		if pr != nil && pr.State == "open" {
			if err := awaitClosed(ctx, cl, pr.Number, opts); err != nil {
				return err
			}
		}
	}
	if err := cl.CreateBranch(ctx, branch, opts.TargetBranch); err != nil {
		return fmt.Errorf("creating release branch: %w", err)
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

	// Refresh the PR we found (reopening it if the branch reset closed it), or
	// open one if there is none.
	if pr != nil {
		opts.Logf("refreshing release PR #%d (was %s)", pr.Number, prState(pr))
		if err := cl.EditPull(ctx, pr.Number, title, body); err != nil {
			return err
		}
		got, err := cl.GetPull(ctx, pr.Number)
		if err != nil {
			return err
		}
		if got.State != "open" {
			return fmt.Errorf("release PR #%d is %s after the refresh, not open", pr.Number, prState(got))
		}
		return nil
	}
	created, err := cl.CreatePull(ctx, branch, opts.TargetBranch, title, body)
	if err != nil {
		return fmt.Errorf("creating release PR: %w", err)
	}
	opts.Logf("opened release PR #%d", created.Number)
	id, err := cl.EnsureLabel(ctx, pendingLabel, labelColor)
	if err != nil {
		return err
	}
	return cl.SetIssueLabels(ctx, created.Number, []int64{id})
}

// cutRelease is a version a merged release PR released, and the commit its
// tag sits on.
type cutRelease struct {
	version semver.Version
	tag     string
	at      string
}

// tagMerged looks for a just-merged release PR and cuts the tags/releases its
// merge introduced (versions now present in main's manifest but not yet tagged).
// It returns, per package path, the newest version those merges released.
func tagMerged(ctx context.Context, cl API, cfg *config.Config, man config.Manifest, opts Options) (map[string]cutRelease, error) {
	cut := map[string]cutRelease{}
	closed, err := cl.Pulls(ctx, "closed")
	if err != nil {
		return nil, err
	}
	for _, pr := range closed {
		if !pr.Merged || !hasLabel(pr, pendingLabel) || hasLabel(pr, taggedLabel) {
			continue
		}
		opts.Logf("release PR #%d merged (%s); cutting releases", pr.Number, short(pr.MergeCommitSHA))
		// The versions to tag are the ones the release PR merged, read from its
		// merge commit — not from this run's checkout. A run that started
		// before the PR was merged checked out the previous manifest; tagging
		// from that finds the previous tag already there, cuts nothing, and
		// would still mark the PR tagged, losing the release. Tags and notes
		// also point at the merge commit, not wherever the branch is now.
		relMan, at, err := manifestAt(ctx, cl, man, pr, opts)
		if err != nil {
			return nil, err
		}
		atOpts := opts
		atOpts.TargetBranch = at
		for _, p := range cfg.Packages {
			v, err := semver.Parse(orZero(relMan[p.Path]))
			if err != nil {
				return nil, err
			}
			// 0.0.0 is the manifest's "never released" placeholder, not a
			// version anyone merged. Tagging it would mark every commit as
			// released, so the package's real first release would never come.
			if v.String() == "0.0.0" {
				continue
			}
			tag := TagFor(cfg, p, v)
			existing, err := cl.TagCommit(ctx, tag)
			if err != nil {
				return nil, err
			}
			if c, ok := cut[p.Path]; !ok || v.Compare(c.version) > 0 {
				c = cutRelease{version: v, tag: tag, at: at}
				if existing != "" {
					c.at = existing
				}
				cut[p.Path] = c
			}
			if existing != "" {
				continue // already released
			}
			notes := extractEntry(changelogOnMain(ctx, cl, p, atOpts), v.String())
			if opts.DryRun {
				opts.Logf("[dry-run] would create release %s", tag)
				continue
			}
			if err := cl.CreateRelease(ctx, tag, at, tag, notes, v.IsPrerelease()); err != nil {
				return nil, fmt.Errorf("creating release %s: %w", tag, err)
			}
			opts.Logf("created release %s", tag)
		}
		if !opts.DryRun {
			id, err := cl.EnsureLabel(ctx, taggedLabel, labelColor)
			if err != nil {
				return nil, err
			}
			// keep pending removed by replacing labels with just "tagged"
			if err := cl.SetIssueLabels(ctx, pr.Number, []int64{id}); err != nil {
				return nil, err
			}
		}
	}
	return cut, nil
}

// liveManifest returns the manifest on the target branch now, falling back to
// the run's checkout when the branch has none, raised to any version this run
// just released (the branch read can trail the merge it was cut from).
func liveManifest(ctx context.Context, cl API, man config.Manifest, cut map[string]cutRelease, opts Options) (config.Manifest, error) {
	live := cloneManifest(man)
	f, err := cl.GetFile(ctx, manifestFile, opts.TargetBranch)
	switch {
	case forge.NotFound(err):
	case err != nil:
		return nil, fmt.Errorf("manifest on %s: %w", opts.TargetBranch, err)
	default:
		m, err := config.ParseManifest([]byte(f.Content))
		if err != nil {
			return nil, fmt.Errorf("manifest on %s: %w", opts.TargetBranch, err)
		}
		live = m
	}
	for path, c := range cut {
		cur, err := semver.Parse(orZero(live[path]))
		if err != nil || c.version.Compare(cur) > 0 {
			live[path] = c.version.String()
		}
	}
	return live, nil
}

// ---- helpers --------------------------------------------------------------

// manifestFile is where the version manifest lives in the repository.
const manifestFile = ".release-please-manifest.json"

// manifestAt returns the manifest a merged release PR left behind and the ref
// to release from: its merge commit. Without a merge commit SHA (or without a
// manifest there) it falls back to the run's own manifest and target branch.
func manifestAt(ctx context.Context, cl API, man config.Manifest, pr forge.Pull, opts Options) (config.Manifest, string, error) {
	if pr.MergeCommitSHA == "" {
		return man, opts.TargetBranch, nil
	}
	f, err := cl.GetFile(ctx, manifestFile, pr.MergeCommitSHA)
	if forge.NotFound(err) {
		return man, pr.MergeCommitSHA, nil
	}
	if err != nil {
		return nil, "", fmt.Errorf("manifest at %s: %w", short(pr.MergeCommitSHA), err)
	}
	m, err := config.ParseManifest([]byte(f.Content))
	if err != nil {
		return nil, "", fmt.Errorf("manifest at %s: %w", short(pr.MergeCommitSHA), err)
	}
	return m, pr.MergeCommitSHA, nil
}

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
		base, err := readFileOr(ctx, cl, changelogPath(pl.pkg), opts.TargetBranch)
		if err != nil {
			return nil, fmt.Errorf("%s: changelog: %w", label(pl.pkg), err)
		}
		want(changelogPath(pl.pkg), changelog.Prepend(base, pl.entry))

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
	want(manifestFile, string(manBytes))

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

// findReleasePR locates the release PR for a release branch: the open PR whose
// head is that branch, or else the most recent CLOSED-but-UNMERGED one carrying
// the pending label — which is exactly what a branch reset leaves behind, since
// Forgejo closes a PR when its head branch is deleted. Reusing that PR (the
// caller refreshes it, and EditPull reopens it) is what stops the number from
// churning on every run.
//
// A MERGED PR is never a candidate: that one is already released, and cutting
// its tags is tagMerged's business. The pending label is required so an
// unrelated closed PR on the same branch name is left alone.
func findReleasePR(ctx context.Context, cl API, branch string) (*forge.Pull, error) {
	all, err := cl.Pulls(ctx, "all")
	if err != nil {
		return nil, err
	}
	var closed *forge.Pull
	for i := range all {
		p := &all[i]
		if p.Head.Ref != branch || p.Merged {
			continue
		}
		if p.State == "open" {
			return p, nil // an open PR always wins
		}
		// Pick the newest candidate by number rather than trusting the API's
		// listing order.
		if hasLabel(*p, pendingLabel) && (closed == nil || p.Number > closed.Number) {
			closed = p
		}
	}
	return closed, nil
}

// awaitClosed waits until Forgejo has closed PR number after its head branch
// was deleted. A close that doesn't come within CloseWait is logged and not
// waited for further: the refresh then reopens nothing, which is fine.
func awaitClosed(ctx context.Context, cl API, number int, opts Options) error {
	deadline := time.Now().Add(opts.CloseWait)
	for {
		p, err := cl.GetPull(ctx, number)
		if err != nil {
			return err
		}
		if p.State != "open" {
			return nil
		}
		if !time.Now().Before(deadline) {
			opts.Logf("release PR #%d still open %s after its branch was reset", number, opts.CloseWait)
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(opts.PollEvery):
		}
	}
}

// prState renders a PR's state for logs ("open"/"closed"), tolerating an
// instance that omits it.
func prState(p *forge.Pull) string {
	if p.State == "" {
		return "unknown"
	}
	return p.State
}

func changelogOnMain(ctx context.Context, cl API, p config.Package, opts Options) string {
	f, err := cl.GetFile(ctx, changelogPath(p), opts.TargetBranch)
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

// nextVersion is the single place a release's version is decided: the package's
// normal bump, then any Release-As override. Every downstream use (PR title,
// changelog heading, version files, manifest, and so the tag cut on merge) reads
// the version this returns, so an override never has to be applied twice.
//
// Precedence follows release-please: the config's "release-as" wins over a
// "Release-As:" commit footer, which wins over the conventional-commit bump. The
// winning request is the only one considered — if it is unusable the normal bump
// applies, rather than silently falling back to a lower-priority request.
//
// A request is refused, with a warning, when it is not a valid semver or is not
// greater than the current version: releases never go backwards, and that is
// also what makes a Release-As footer a one-shot — once the version it asks for
// is released, asking again is a no-op.
//
// The second result reports whether an override was applied, so the caller can
// release on a Release-As request alone (no releasable commits needed).
func nextVersion(p config.Package, last semver.Version, level semver.Level, ccs []conventional.Commit, logf func(string, ...any)) (semver.Version, bool) {
	bumped := bumpFor(p, last, level)

	want, source := releaseAsRequest(p, ccs)
	if want == "" {
		return bumped, false
	}
	v, err := semver.Parse(want)
	if err != nil {
		logf("%s: ignoring release-as %q (%s): not a valid version", label(p), want, source)
		return bumped, false
	}
	if v.Compare(last) <= 0 {
		logf("%s: ignoring release-as %s (%s): not greater than the current version %s", label(p), v, source, last)
		return bumped, false
	}
	logf("%s: release-as %s (%s) overrides the computed %s", label(p), v, source, bumped)
	return v, true
}

// releaseAsRequest returns the requested release version and a human-readable
// source for logs. The config key beats a commit footer; among footers the
// newest commit wins (cl.Commits returns newest-first, and ccs preserves that
// order). The value is returned unvalidated.
func releaseAsRequest(p config.Package, ccs []conventional.Commit) (want, source string) {
	if p.ReleaseAs != "" {
		return p.ReleaseAs, "config release-as"
	}
	for _, c := range ccs {
		if c.ReleaseAs != "" {
			return c.ReleaseAs, "footer on " + short(c.Hash)
		}
	}
	return "", ""
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

// releasedCommits returns the SHAs of the package's commits reachable from the
// last release tag, or nil when there is no tag yet.
func releasedCommits(ctx context.Context, cl API, boundary, path string, max int) (map[string]bool, error) {
	if boundary == "" {
		return nil, nil
	}
	cs, err := cl.Commits(ctx, boundary, path, max)
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(cs))
	for _, c := range cs {
		out[c.SHA] = true
	}
	return out, nil
}

// changelogPath resolves a package's changelog-path relative to the package
// directory, as release-please does: packages/foo with the default
// "CHANGELOG.md" writes packages/foo/CHANGELOG.md, not the root changelog.
func changelogPath(p config.Package) string { return join(p.Path, p.ChangelogPath) }

func join(base, f string) string {
	if base == "" || base == "." {
		return f
	}
	return strings.TrimRight(base, "/") + "/" + f
}
