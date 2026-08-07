// Package config loads release-please's own config format —
// release-please-config.json + .release-please-manifest.json — so existing
// projects migrate to releasejo without rewriting configuration. Only the
// widely-used subset is honoured; unknown keys are ignored, not rejected.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
)

// Package is the effective, defaults-merged settings for one releasable path.
type Package struct {
	Path          string `json:"-"` // key in the packages map, e.g. "." or "services/api"
	ReleaseType   string `json:"release-type,omitempty"`
	Component     string `json:"component,omitempty"`
	PackageName   string `json:"package-name,omitempty"`
	ChangelogPath string `json:"changelog-path,omitempty"`

	BumpMinorPreMajor bool `json:"bump-minor-pre-major,omitempty"`

	// versioning strategy: "" / "default" (semantic), "always-bump-patch",
	// or "always-bump-minor".
	Versioning string `json:"versioning,omitempty"`

	// tag shape
	IncludeComponentInTag *bool  `json:"include-component-in-tag,omitempty"`
	IncludeVInTag         *bool  `json:"include-v-in-tag,omitempty"`
	TagSeparator          string `json:"tag-separator,omitempty"`

	ExtraFiles        []string           `json:"extra-files,omitempty"`
	ChangelogSections []ChangelogSection `json:"changelog-sections,omitempty"`
}

// ChangelogSection mirrors release-please's changelog-sections entries.
type ChangelogSection struct {
	Type    string `json:"type"`
	Section string `json:"section"`
	Hidden  bool   `json:"hidden,omitempty"`
}

// Config is the parsed release-please-config.json with global defaults resolved
// into each package.
type Config struct {
	SeparatePullRequests bool
	Packages             []Package // sorted by Path for deterministic output
}

// rawConfig captures both the global defaults (top level) and per-package
// overrides. Pointers distinguish "unset" from "false".
type rawConfig struct {
	ReleaseType           string                `json:"release-type"`
	Versioning            string                `json:"versioning"`
	BumpMinorPreMajor     *bool                 `json:"bump-minor-pre-major"`
	SeparatePullRequests  *bool                 `json:"separate-pull-requests"`
	IncludeComponentInTag *bool                 `json:"include-component-in-tag"`
	IncludeVInTag         *bool                 `json:"include-v-in-tag"`
	TagSeparator          string                `json:"tag-separator"`
	ChangelogSections     []ChangelogSection    `json:"changelog-sections"`
	Packages              map[string]rawPackage `json:"packages"`
}

type rawPackage struct {
	ReleaseType           *string            `json:"release-type"`
	Versioning            *string            `json:"versioning"`
	Component             string             `json:"component"`
	PackageName           string             `json:"package-name"`
	ChangelogPath         string             `json:"changelog-path"`
	BumpMinorPreMajor     *bool              `json:"bump-minor-pre-major"`
	IncludeComponentInTag *bool              `json:"include-component-in-tag"`
	IncludeVInTag         *bool              `json:"include-v-in-tag"`
	TagSeparator          string             `json:"tag-separator"`
	ExtraFiles            []string           `json:"extra-files"`
	ChangelogSections     []ChangelogSection `json:"changelog-sections"`
}

// LoadConfig reads and resolves a release-please-config.json.
func LoadConfig(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	return ParseConfig(b)
}

// ParseConfig resolves defaults so callers get fully-populated packages.
func ParseConfig(b []byte) (*Config, error) {
	var raw rawConfig
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, fmt.Errorf("config: invalid JSON: %w", err)
	}
	if len(raw.Packages) == 0 {
		return nil, fmt.Errorf("config: no packages defined")
	}

	cfg := &Config{SeparatePullRequests: boolOr(raw.SeparatePullRequests, false)}

	for path, rp := range raw.Packages {
		p := Package{
			Path:                  path,
			ReleaseType:           strPtrOr(rp.ReleaseType, raw.ReleaseType),
			Versioning:            strPtrOr(rp.Versioning, raw.Versioning),
			Component:             rp.Component,
			PackageName:           rp.PackageName,
			ChangelogPath:         strOr(rp.ChangelogPath, "CHANGELOG.md"),
			BumpMinorPreMajor:     boolOr(orPtr(rp.BumpMinorPreMajor, raw.BumpMinorPreMajor), false),
			IncludeComponentInTag: orPtr(rp.IncludeComponentInTag, raw.IncludeComponentInTag),
			IncludeVInTag:         orPtr(rp.IncludeVInTag, raw.IncludeVInTag),
			TagSeparator:          strOr(rp.TagSeparator, strOr(raw.TagSeparator, "-")),
			ExtraFiles:            rp.ExtraFiles,
			ChangelogSections:     firstNonEmptySections(rp.ChangelogSections, raw.ChangelogSections),
		}
		if p.ReleaseType == "" {
			return nil, fmt.Errorf("config: package %q has no release-type (and no global default)", path)
		}
		// default component to the path's leaf when a monorepo tags per component
		cfg.Packages = append(cfg.Packages, p)
	}
	sort.Slice(cfg.Packages, func(i, j int) bool { return cfg.Packages[i].Path < cfg.Packages[j].Path })
	return cfg, nil
}

// IncludeComponent reports the effective include-component-in-tag, defaulting to
// true for genuine monorepos (more than one package) and false for a single root.
func (c *Config) IncludeComponent(p Package) bool {
	if p.IncludeComponentInTag != nil {
		return *p.IncludeComponentInTag
	}
	return len(c.Packages) > 1
}

// IncludeV reports the effective include-v-in-tag (release-please defaults true).
func (p Package) IncludeV() bool { return boolOr(p.IncludeVInTag, true) }

// Manifest maps a package path to its last released version.
type Manifest map[string]string

// LoadManifest reads .release-please-manifest.json.
func LoadManifest(path string) (Manifest, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("manifest: %w", err)
	}
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("manifest: invalid JSON: %w", err)
	}
	return m, nil
}

// Marshal renders the manifest as pretty JSON (2-space, sorted keys — matches
// release-please's on-disk format so diffs stay minimal).
func (m Manifest) Marshal() ([]byte, error) {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

func boolOr(p *bool, d bool) bool {
	if p != nil {
		return *p
	}
	return d
}
func orPtr(a, b *bool) *bool {
	if a != nil {
		return a
	}
	return b
}
func strOr(a, d string) string {
	if a != "" {
		return a
	}
	return d
}
func strPtrOr(a *string, d string) string {
	if a != nil && *a != "" {
		return *a
	}
	return d
}
func firstNonEmptySections(a, b []ChangelogSection) []ChangelogSection {
	if len(a) > 0 {
		return a
	}
	return b
}
