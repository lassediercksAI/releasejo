// Package semver implements the small slice of semantic versioning that
// release automation needs: parse, compare, and bump. Stdlib-only.
package semver

import (
	"fmt"
	"strconv"
	"strings"
)

// Level is the size of a version bump implied by a set of changes.
type Level int

const (
	None  Level = iota // no releasable change
	Patch              // fix:
	Minor              // feat:
	Major              // breaking change
)

func (l Level) String() string {
	switch l {
	case Patch:
		return "patch"
	case Minor:
		return "minor"
	case Major:
		return "major"
	default:
		return "none"
	}
}

// Max returns the larger of two levels (used to fold a commit set down to one bump).
func Max(a, b Level) Level {
	if a > b {
		return a
	}
	return b
}

// Version is a parsed semantic version. Build metadata is not retained (release
// tooling never needs it). A leading "v" is accepted on parse and dropped.
type Version struct {
	Major, Minor, Patch int
	Pre                 string // pre-release, without the leading '-'
}

// Parse accepts "1.2.3", "v1.2.3", or "1.2.3-rc.1" (optional leading v).
func Parse(s string) (Version, error) {
	orig := s
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "v")

	var pre string
	if i := strings.IndexByte(s, '-'); i >= 0 {
		pre = s[i+1:]
		s = s[:i]
	}
	// discard build metadata if present
	if i := strings.IndexByte(pre, '+'); i >= 0 {
		pre = pre[:i]
	} else if i := strings.IndexByte(s, '+'); i >= 0 {
		s = s[:i]
	}

	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return Version{}, fmt.Errorf("semver: %q is not major.minor.patch", orig)
	}
	nums := make([]int, 3)
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return Version{}, fmt.Errorf("semver: %q has a non-numeric component %q", orig, p)
		}
		nums[i] = n
	}
	return Version{Major: nums[0], Minor: nums[1], Patch: nums[2], Pre: pre}, nil
}

// MustParse is Parse that panics on error — for constants/tests only.
func MustParse(s string) Version {
	v, err := Parse(s)
	if err != nil {
		panic(err)
	}
	return v
}

// String renders without a leading "v" (release-please stores bare versions in
// the manifest; the "v" is added at tag time when configured).
func (v Version) String() string {
	s := fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch)
	if v.Pre != "" {
		s += "-" + v.Pre
	}
	return s
}

// IsPrerelease reports whether this is a pre-release version.
func (v Version) IsPrerelease() bool { return v.Pre != "" }

// Compare returns -1, 0, or +1. Pre-release ordering follows semver §11
// (a version with a pre-release is lower than the same without).
func (v Version) Compare(o Version) int {
	if c := cmpInt(v.Major, o.Major); c != 0 {
		return c
	}
	if c := cmpInt(v.Minor, o.Minor); c != 0 {
		return c
	}
	if c := cmpInt(v.Patch, o.Patch); c != 0 {
		return c
	}
	return comparePre(v.Pre, o.Pre)
}

func cmpInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

// comparePre orders pre-release identifiers per semver §11.4.
func comparePre(a, b string) int {
	if a == b {
		return 0
	}
	if a == "" { // no pre-release outranks any pre-release
		return 1
	}
	if b == "" {
		return -1
	}
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(as) && i < len(bs); i++ {
		an, aNum := strconv.Atoi(as[i])
		bn, bNum := strconv.Atoi(bs[i])
		switch {
		case aNum == nil && bNum == nil: // both numeric
			if c := cmpInt(an, bn); c != 0 {
				return c
			}
		case aNum == nil: // numeric < alphanumeric
			return -1
		case bNum == nil:
			return 1
		default:
			if c := strings.Compare(as[i], bs[i]); c != 0 {
				return c
			}
		}
	}
	return cmpInt(len(as), len(bs))
}

// Bump applies a release Level, following release-please's pre-1.0 semantics
// when preMinor is set (a "breaking" change bumps the minor, and a feat bumps
// the patch, while major stays 0 — so 0.x stays 0.x until an explicit 1.0.0).
func (v Version) Bump(l Level, preMinor bool) Version {
	next := Version{Major: v.Major, Minor: v.Minor, Patch: v.Patch} // drops any pre-release
	if v.Major == 0 && preMinor {
		switch l {
		case Major:
			next.Minor++
			next.Patch = 0
		case Minor, Patch:
			next.Patch++
		}
		return next
	}
	switch l {
	case Major:
		next.Major++
		next.Minor = 0
		next.Patch = 0
	case Minor:
		next.Minor++
		next.Patch = 0
	case Patch:
		next.Patch++
	}
	return next
}
