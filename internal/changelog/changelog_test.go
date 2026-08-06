package changelog

import (
	"strings"
	"testing"

	"github.com/lassediercksAI/releasejo/internal/conventional"
)

func TestRender(t *testing.T) {
	tests := []struct {
		name       string
		version    string
		date       string
		commits    []conventional.Commit
		compareURL string
		want       string
	}{
		{
			name:    "feat fix and breaking feat",
			version: "1.1.0",
			date:    "2026-08-06",
			commits: []conventional.Commit{
				{Type: "feat", Scope: "api", Description: "add pagination", Hash: "abcdef1234567", Breaking: true},
				{Type: "feat", Description: "add search", Hash: "1234567890abc"},
				{Type: "fix", Scope: "db", Description: "handle nil rows", Hash: "deadbeefcafe0"},
			},
			compareURL: "",
			want: "## 1.1.0 (2026-08-06)\n" +
				"\n" +
				"### ⚠ BREAKING CHANGES\n" +
				"\n" +
				"* **api:** add pagination\n" +
				"\n" +
				"### Features\n" +
				"\n" +
				"* **api:** add pagination (abcdef1)\n" +
				"* add search (1234567)\n" +
				"\n" +
				"### Bug Fixes\n" +
				"\n" +
				"* **db:** handle nil rows (deadbee)\n",
		},
		{
			name:    "compare url links heading",
			version: "2.0.0",
			date:    "2026-08-06",
			commits: []conventional.Commit{
				{Type: "feat", Description: "brand new thing", Hash: "0000000abcdef"},
			},
			compareURL: "https://example.com/compare/v1.0.0...v2.0.0",
			want: "## [2.0.0](https://example.com/compare/v1.0.0...v2.0.0) (2026-08-06)\n" +
				"\n" +
				"### Features\n" +
				"\n" +
				"* brand new thing (0000000)\n",
		},
		{
			name:    "hidden types omitted but breaking chore shows",
			version: "1.2.0",
			date:    "2026-08-06",
			commits: []conventional.Commit{
				{Type: "chore", Description: "bump deps", Hash: "aaaaaaa1111", Breaking: true},
				{Type: "docs", Description: "tweak readme", Hash: "bbbbbbb2222"},
				{Type: "feat", Description: "visible feature", Hash: "ccccccc3333"},
			},
			compareURL: "",
			want: "## 1.2.0 (2026-08-06)\n" +
				"\n" +
				"### ⚠ BREAKING CHANGES\n" +
				"\n" +
				"* bump deps\n" +
				"\n" +
				"### Features\n" +
				"\n" +
				"* visible feature (ccccccc)\n",
		},
		{
			name:    "short hash uses full when shorter than seven",
			version: "1.0.1",
			date:    "2026-08-06",
			commits: []conventional.Commit{
				{Type: "fix", Description: "tiny", Hash: "abc"},
				{Type: "fix", Description: "no hash"},
			},
			compareURL: "",
			want: "## 1.0.1 (2026-08-06)\n" +
				"\n" +
				"### Bug Fixes\n" +
				"\n" +
				"* tiny (abc)\n" +
				"* no hash\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Render(tt.version, tt.date, tt.commits, DefaultSections(), tt.compareURL)
			if got != tt.want {
				t.Errorf("Render mismatch\n--- got ---\n%q\n--- want ---\n%q", got, tt.want)
			}
		})
	}
}

func TestDefaultSections(t *testing.T) {
	got := DefaultSections()
	if len(got) != 12 {
		t.Fatalf("expected 12 sections, got %d", len(got))
	}
	if got[0].Type != "feat" || got[0].Section != "Features" || got[0].Hidden {
		t.Errorf("first section = %+v, want visible feat/Features", got[0])
	}
	if !got[7].Hidden || got[7].Type != "chore" {
		t.Errorf("chore section = %+v, want hidden chore", got[7])
	}
}

func TestPrepend(t *testing.T) {
	entry := "## 1.1.0 (2026-08-06)\n\n### Features\n\n* new thing (abc1234)\n"

	tests := []struct {
		name     string
		existing string
		entry    string
		want     string
	}{
		{
			name:     "into empty",
			existing: "",
			entry:    entry,
			want:     "# Changelog\n\n" + entry,
		},
		{
			name:     "into whitespace only",
			existing: "   \n\n",
			entry:    entry,
			want:     "# Changelog\n\n" + entry,
		},
		{
			name:     "above existing release keeps header on top",
			existing: "# Changelog\n\n## 1.0.0 (2026-01-01)\n\n### Features\n\n* first (0000000)\n",
			entry:    entry,
			want: "# Changelog\n\n" +
				"## 1.1.0 (2026-08-06)\n\n### Features\n\n* new thing (abc1234)\n\n" +
				"## 1.0.0 (2026-01-01)\n\n### Features\n\n* first (0000000)\n",
		},
		{
			name:     "header with preamble but no release yet",
			existing: "# Changelog\n\nAll notable changes are documented here.\n",
			entry:    entry,
			want:     "# Changelog\n\nAll notable changes are documented here.\n\n" + entry,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Prepend(tt.existing, tt.entry)
			if got != tt.want {
				t.Errorf("Prepend mismatch\n--- got ---\n%q\n--- want ---\n%q", got, tt.want)
			}
		})
	}
}

func TestPrependHeaderNotDuplicated(t *testing.T) {
	existing := "# Changelog\n\n## 1.0.0 (2026-01-01)\n\n* first\n"
	got := Prepend(existing, "## 1.1.0 (2026-08-06)\n\n* new\n")
	if n := strings.Count(got, "# Changelog"); n != 1 {
		t.Errorf("expected exactly one '# Changelog' header, got %d\n%s", n, got)
	}
	if !strings.HasPrefix(got, "# Changelog\n") {
		t.Errorf("header not on top:\n%s", got)
	}
	// New entry must land above the old one.
	iNew := strings.Index(got, "1.1.0")
	iOld := strings.Index(got, "1.0.0")
	if iNew == -1 || iOld == -1 || iNew > iOld {
		t.Errorf("new entry not above old:\n%s", got)
	}
}
