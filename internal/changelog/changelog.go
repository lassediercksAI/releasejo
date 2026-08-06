// Package changelog renders keep-a-changelog / release-please-style CHANGELOG
// markdown from parsed conventional commits. Stdlib-only by design.
package changelog

import (
	"strings"

	"github.com/lassediercksAI/releasejo/internal/conventional"
)

// Section maps a conventional commit type to a human-readable heading and
// controls whether commits of that type are rendered.
type Section struct {
	Type    string // conventional commit type, e.g. "feat"
	Section string // human heading, e.g. "Features"
	Hidden  bool   // if true, commits of this type are not rendered
}

// DefaultSections returns the release-please default section configuration, in
// the canonical order used for rendering.
func DefaultSections() []Section {
	return []Section{
		{Type: "feat", Section: "Features"},
		{Type: "fix", Section: "Bug Fixes"},
		{Type: "perf", Section: "Performance Improvements"},
		{Type: "deps", Section: "Dependencies"},
		{Type: "revert", Section: "Reverts"},
		{Type: "docs", Section: "Documentation", Hidden: true},
		{Type: "style", Section: "Styles", Hidden: true},
		{Type: "chore", Section: "Miscellaneous Chores", Hidden: true},
		{Type: "refactor", Section: "Code Refactoring", Hidden: true},
		{Type: "test", Section: "Tests", Hidden: true},
		{Type: "build", Section: "Build System", Hidden: true},
		{Type: "ci", Section: "Continuous Integration", Hidden: true},
	}
}

// shortHash returns the first 7 characters of h, or all of h if it is shorter.
func shortHash(h string) string {
	if len(h) <= 7 {
		return h
	}
	return h[:7]
}

// bullet renders a single commit as a markdown list item, without a trailing
// newline. When withHash is true the short commit hash is appended in
// parentheses (when present).
func bullet(c conventional.Commit, withHash bool) string {
	var b strings.Builder
	b.WriteString("* ")
	if c.Scope != "" {
		b.WriteString("**")
		b.WriteString(c.Scope)
		b.WriteString(":** ")
	}
	b.WriteString(c.Description)
	if withHash && c.Hash != "" {
		b.WriteString(" (")
		b.WriteString(shortHash(c.Hash))
		b.WriteString(")")
	}
	return b.String()
}

// Render produces the markdown for a single release entry. The heading uses
// compareURL as a link target when non-empty. A "BREAKING CHANGES" section is
// emitted first when any commit is breaking, followed by one section per
// non-hidden configured type that has at least one matching commit, in the
// order given by sections. Input commit order is preserved within each section.
func Render(version, date string, commits []conventional.Commit, sections []Section, compareURL string) string {
	var b strings.Builder

	// Heading.
	b.WriteString("## ")
	if compareURL != "" {
		b.WriteString("[")
		b.WriteString(version)
		b.WriteString("](")
		b.WriteString(compareURL)
		b.WriteString(")")
	} else {
		b.WriteString(version)
	}
	b.WriteString(" (")
	b.WriteString(date)
	b.WriteString(")\n")

	// blocks holds each rendered section body (heading + bullets), to be joined
	// with blank lines.
	var blocks []string

	// Breaking changes section.
	var breaking strings.Builder
	hasBreaking := false
	for _, c := range commits {
		if !c.Breaking {
			continue
		}
		if !hasBreaking {
			breaking.WriteString("### ⚠ BREAKING CHANGES\n\n")
			hasBreaking = true
		}
		breaking.WriteString(bullet(c, false))
		breaking.WriteString("\n")
	}
	if hasBreaking {
		blocks = append(blocks, breaking.String())
	}

	// Type-based sections, in configured order.
	for _, s := range sections {
		if s.Hidden {
			continue
		}
		var sec strings.Builder
		matched := false
		for _, c := range commits {
			if c.Type != s.Type {
				continue
			}
			if !matched {
				sec.WriteString("### ")
				sec.WriteString(s.Section)
				sec.WriteString("\n\n")
				matched = true
			}
			sec.WriteString(bullet(c, true))
			sec.WriteString("\n")
		}
		if matched {
			blocks = append(blocks, sec.String())
		}
	}

	b.WriteString("\n")
	b.WriteString(strings.Join(blocks, "\n"))
	return b.String()
}

// normalizeEntry ensures entry is terminated by exactly one trailing newline.
func normalizeEntry(entry string) string {
	return strings.TrimRight(entry, "\n") + "\n"
}

// Prepend inserts a new release entry into an existing CHANGELOG following
// keep-a-changelog conventions. The "# Changelog" header is preserved (and not
// duplicated), the entry is placed above the most recent "## " release heading
// (or after the header/preamble when there is none), and entries are separated
// by a blank line.
func Prepend(existing, entry string) string {
	entry = normalizeEntry(entry)

	// Empty/whitespace-only existing content: start a fresh changelog.
	if strings.TrimSpace(existing) == "" {
		return "# Changelog\n\n" + entry
	}

	lines := strings.Split(existing, "\n")

	// Locate the "# Changelog" header (first top-level header) and the first
	// "## " release heading.
	headerIdx := -1
	firstRelease := -1
	for i, ln := range lines {
		t := strings.TrimSpace(ln)
		if headerIdx == -1 && strings.HasPrefix(t, "# ") && strings.EqualFold(strings.TrimSpace(t[2:]), "changelog") {
			headerIdx = i
			continue
		}
		if strings.HasPrefix(ln, "## ") {
			firstRelease = i
			break
		}
	}

	// No recognizable "# Changelog" header: treat the whole thing as body and
	// insert before the first release (or prepend a header + entry).
	if headerIdx == -1 {
		if firstRelease == -1 {
			return "# Changelog\n\n" + entry + "\n" + strings.TrimRight(existing, "\n") + "\n"
		}
		before := strings.Join(lines[:firstRelease], "\n")
		after := strings.Join(lines[firstRelease:], "\n")
		before = strings.TrimRight(before, "\n")
		out := "# Changelog\n\n" + entry + "\n"
		if before != "" {
			out += before + "\n\n"
		}
		return out + strings.TrimRight(after, "\n") + "\n"
	}

	// Header present, no release yet: append the entry after header/preamble.
	if firstRelease == -1 {
		body := strings.TrimRight(existing, "\n")
		return body + "\n\n" + entry
	}

	// Header present with releases: keep everything above the first release
	// (header + preamble), insert the entry, then the rest.
	head := strings.Join(lines[:firstRelease], "\n")
	head = strings.TrimRight(head, "\n")
	rest := strings.Join(lines[firstRelease:], "\n")
	rest = strings.TrimRight(rest, "\n")
	return head + "\n\n" + entry + "\n" + rest + "\n"
}
