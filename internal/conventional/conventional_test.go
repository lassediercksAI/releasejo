package conventional

import (
	"testing"

	"github.com/lassediercksAI/releasejo/internal/semver"
)

func TestParse(t *testing.T) {
	tests := []struct {
		name         string
		message      string
		wantType     string
		wantScope    string
		wantBreaking bool
		wantDesc     string
		wantBody     string
		wantConv     bool
		wantLevel    semver.Level
	}{
		{
			name:      "feat is minor",
			message:   "feat: add login flow",
			wantType:  "feat",
			wantDesc:  "add login flow",
			wantConv:  true,
			wantLevel: semver.Minor,
		},
		{
			name:      "fix is patch",
			message:   "fix: correct off-by-one",
			wantType:  "fix",
			wantDesc:  "correct off-by-one",
			wantConv:  true,
			wantLevel: semver.Patch,
		},
		{
			name:         "bang marks breaking major",
			message:      "feat!: drop node 14",
			wantType:     "feat",
			wantDesc:     "drop node 14",
			wantBreaking: true,
			wantConv:     true,
			wantLevel:    semver.Major,
		},
		{
			name:         "scope with bang is breaking major",
			message:      "feat(api)!: rename endpoint",
			wantType:     "feat",
			wantScope:    "api",
			wantDesc:     "rename endpoint",
			wantBreaking: true,
			wantConv:     true,
			wantLevel:    semver.Major,
		},
		{
			name:         "breaking change footer promotes to major",
			message:      "feat: reshape config\n\nBREAKING CHANGE: config keys renamed",
			wantType:     "feat",
			wantDesc:     "reshape config",
			wantBody:     "BREAKING CHANGE: config keys renamed",
			wantBreaking: true,
			wantConv:     true,
			wantLevel:    semver.Major,
		},
		{
			name:         "breaking change hyphen footer promotes to major",
			message:      "fix: tweak default\n\nBREAKING-CHANGE: default flipped",
			wantType:     "fix",
			wantDesc:     "tweak default",
			wantBody:     "BREAKING-CHANGE: default flipped",
			wantBreaking: true,
			wantConv:     true,
			wantLevel:    semver.Major,
		},
		{
			name:      "scope extraction",
			message:   "fix(parser): handle empty input",
			wantType:  "fix",
			wantScope: "parser",
			wantDesc:  "handle empty input",
			wantConv:  true,
			wantLevel: semver.Patch,
		},
		{
			name:      "non-conventional line",
			message:   "just a plain commit message",
			wantType:  "",
			wantDesc:  "just a plain commit message",
			wantConv:  false,
			wantLevel: semver.None,
		},
		{
			name:      "multi-line body captured",
			message:   "feat: big feature\n\nThis explains the change\nover several lines.",
			wantType:  "feat",
			wantDesc:  "big feature",
			wantBody:  "This explains the change\nover several lines.",
			wantConv:  true,
			wantLevel: semver.Minor,
		},
		{
			name:      "chore is none",
			message:   "chore: bump deps",
			wantType:  "chore",
			wantDesc:  "bump deps",
			wantConv:  true,
			wantLevel: semver.None,
		},
		{
			name:      "docs is none",
			message:   "docs: fix typo in readme",
			wantType:  "docs",
			wantDesc:  "fix typo in readme",
			wantConv:  true,
			wantLevel: semver.None,
		},
		{
			name:      "refactor is none",
			message:   "refactor: extract helper",
			wantType:  "refactor",
			wantDesc:  "extract helper",
			wantConv:  true,
			wantLevel: semver.None,
		},
		{
			name:      "type is lowercased",
			message:   "FEAT(API): shout",
			wantType:  "feat",
			wantScope: "API",
			wantDesc:  "shout",
			wantConv:  true,
			wantLevel: semver.Minor,
		},
		{
			name:      "crlf line endings handled",
			message:   "feat: crlf header\r\n\r\nbody line one\r\nbody line two",
			wantType:  "feat",
			wantDesc:  "crlf header",
			wantBody:  "body line one\nbody line two",
			wantConv:  true,
			wantLevel: semver.Minor,
		},
		{
			name:      "trailing whitespace on header tolerated",
			message:   "fix: trailing spaces   ",
			wantType:  "fix",
			wantDesc:  "trailing spaces",
			wantConv:  true,
			wantLevel: semver.Patch,
		},
		{
			name:      "lowercase breaking change is prose not a declaration",
			message:   "chore: note\n\nbreaking change: not really",
			wantType:  "chore",
			wantDesc:  "note",
			wantBody:  "breaking change: not really",
			wantConv:  true,
			wantLevel: semver.None,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := Parse(tt.message, "deadbeef")

			if c.Type != tt.wantType {
				t.Errorf("Type = %q, want %q", c.Type, tt.wantType)
			}
			if c.Scope != tt.wantScope {
				t.Errorf("Scope = %q, want %q", c.Scope, tt.wantScope)
			}
			if c.Breaking != tt.wantBreaking {
				t.Errorf("Breaking = %v, want %v", c.Breaking, tt.wantBreaking)
			}
			if c.Description != tt.wantDesc {
				t.Errorf("Description = %q, want %q", c.Description, tt.wantDesc)
			}
			if c.Body != tt.wantBody {
				t.Errorf("Body = %q, want %q", c.Body, tt.wantBody)
			}
			if c.IsConventional() != tt.wantConv {
				t.Errorf("IsConventional() = %v, want %v", c.IsConventional(), tt.wantConv)
			}
			if got := c.Level(); got != tt.wantLevel {
				t.Errorf("Level() = %v, want %v", got, tt.wantLevel)
			}
			if c.Hash != "deadbeef" {
				t.Errorf("Hash = %q, want %q", c.Hash, "deadbeef")
			}
			if c.Raw != tt.message {
				t.Errorf("Raw = %q, want %q", c.Raw, tt.message)
			}
		})
	}
}
