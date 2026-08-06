// Package updater applies a new version to project files. It implements the
// release-please-compatible "generic" (annotation-driven) version updater plus
// a couple of filename/type-specific structured updaters (version.txt,
// package.json). Stdlib-only by design.
package updater

import (
	"path"
	"regexp"
	"strconv"
	"strings"

	"github.com/lassediercksAI/releasejo/internal/semver"
)

// semverToken matches a semver-looking token: three dot-separated numbers with
// an optional pre-release suffix.
var semverToken = regexp.MustCompile(`\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?`)

// numToken matches the first run of digits.
var numToken = regexp.MustCompile(`\d+`)

// pkgVersion matches a JSON `"version": "..."` field, capturing the prefix
// (including the opening quote of the value) and the closing quote so the
// value can be swapped while preserving indentation and quoting.
var pkgVersion = regexp.MustCompile(`("version"\s*:\s*")[^"]*(")`)

// Apply returns the new content for a file given the release type, the file
// path, its current content, and the new bare version string (e.g. "1.4.0").
// It never errors — an unparseable version or unknown file just yields
// marker-only substitution (or the content unchanged).
func Apply(releaseType, path_, content, newVersion string) string {
	content = ApplyMarkers(content, newVersion)

	base := path.Base(path_)
	switch {
	case base == "version.txt":
		return newVersion + "\n"
	case base == "package.json", releaseType == "node" && base == "package.json":
		return replaceFirstPkgVersion(content, newVersion)
	default:
		return content
	}
}

// replaceFirstPkgVersion replaces the value in the first top-level
// `"version": "..."` line, preserving indentation and quoting. It does not
// full-parse JSON so formatting and comments elsewhere are untouched.
func replaceFirstPkgVersion(content, newVersion string) string {
	loc := pkgVersion.FindStringSubmatchIndex(content)
	if loc == nil {
		return content
	}
	// loc: [full0, full1, g1s, g1e, g2s, g2e]
	prefix := content[loc[2]:loc[3]]
	suffix := content[loc[4]:loc[5]]
	return content[:loc[0]] + prefix + newVersion + suffix + content[loc[1]:]
}

// ApplyMarkers performs only the annotation-based substitution (exported so the
// orchestrator can run it on arbitrary extra-files).
func ApplyMarkers(content, newVersion string) string {
	if !strings.Contains(content, "x-release-please-") {
		return content
	}

	// Parse the version once; component substitutions are skipped if it does
	// not parse as semver.
	v, err := semver.Parse(newVersion)
	parsed := err == nil

	major := ""
	minorRepl := ""
	patchRepl := ""
	if parsed {
		major = strconv.Itoa(v.Major)
		minorRepl = strconv.Itoa(v.Major) + "." + strconv.Itoa(v.Minor)
		patchRepl = strconv.Itoa(v.Major) + "." + strconv.Itoa(v.Minor) + "." + strconv.Itoa(v.Patch)
	}

	// Split preserving newline style: operate line by line but keep the
	// original separators intact.
	lines := splitKeepEnds(content)
	for i, line := range lines {
		body, end := splitEnd(line)
		switch {
		case strings.Contains(body, "x-release-please-version"):
			body = replaceFirstToken(body, semverToken, newVersion)
		case strings.Contains(body, "x-release-please-major"):
			if parsed {
				body = replaceComponent(body, major)
			}
		case strings.Contains(body, "x-release-please-minor"):
			if parsed {
				body = replaceComponent(body, minorRepl)
			}
		case strings.Contains(body, "x-release-please-patch"):
			if parsed {
				body = replaceComponent(body, patchRepl)
			}
		}
		lines[i] = body + end
	}
	return strings.Join(lines, "")
}

// replaceComponent replaces the first semver token if present, otherwise the
// first plain number.
func replaceComponent(body, repl string) string {
	if loc := semverToken.FindStringIndex(body); loc != nil {
		return body[:loc[0]] + repl + body[loc[1]:]
	}
	return replaceFirstToken(body, numToken, repl)
}

// replaceFirstToken replaces the first match of re in body with repl.
func replaceFirstToken(body string, re *regexp.Regexp, repl string) string {
	loc := re.FindStringIndex(body)
	if loc == nil {
		return body
	}
	return body[:loc[0]] + repl + body[loc[1]:]
}

// splitKeepEnds splits content into lines, each retaining its trailing newline
// sequence ("\n" or "\r\n") so joining reproduces the original exactly.
func splitKeepEnds(content string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(content); i++ {
		if content[i] == '\n' {
			lines = append(lines, content[start:i+1])
			start = i + 1
		}
	}
	if start < len(content) {
		lines = append(lines, content[start:])
	}
	if len(lines) == 0 {
		lines = append(lines, "")
	}
	return lines
}

// splitEnd separates a line's body from its trailing newline sequence.
func splitEnd(line string) (body, end string) {
	if strings.HasSuffix(line, "\r\n") {
		return line[:len(line)-2], "\r\n"
	}
	if strings.HasSuffix(line, "\n") {
		return line[:len(line)-1], "\n"
	}
	return line, ""
}
