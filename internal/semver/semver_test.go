package semver

import "testing"

func TestParseAndString(t *testing.T) {
	cases := []struct{ in, want string }{
		{"1.2.3", "1.2.3"},
		{"v1.2.3", "1.2.3"},
		{" v0.1.0 ", "0.1.0"},
		{"1.0.0-rc.1", "1.0.0-rc.1"},
		{"2.3.4+build.5", "2.3.4"},
	}
	for _, c := range cases {
		v, err := Parse(c.in)
		if err != nil {
			t.Fatalf("Parse(%q) error: %v", c.in, err)
		}
		if v.String() != c.want {
			t.Errorf("Parse(%q).String() = %q, want %q", c.in, v.String(), c.want)
		}
	}
	for _, bad := range []string{"1.2", "x.y.z", "1.2.3.4", ""} {
		if _, err := Parse(bad); err == nil {
			t.Errorf("Parse(%q) expected error", bad)
		}
	}
}

func TestCompare(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"1.0.0", "1.0.0", 0},
		{"1.0.1", "1.0.0", 1},
		{"1.1.0", "1.2.0", -1},
		{"2.0.0", "1.9.9", 1},
		{"1.0.0-rc.1", "1.0.0", -1}, // pre-release < release
		{"1.0.0-rc.2", "1.0.0-rc.1", 1},
		{"1.0.0-alpha", "1.0.0-beta", -1},
	}
	for _, c := range cases {
		got := MustParse(c.a).Compare(MustParse(c.b))
		if got != c.want {
			t.Errorf("Compare(%q,%q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestBump(t *testing.T) {
	cases := []struct {
		from     string
		level    Level
		preMinor bool
		want     string
	}{
		{"1.2.3", Patch, false, "1.2.4"},
		{"1.2.3", Minor, false, "1.3.0"},
		{"1.2.3", Major, false, "2.0.0"},
		{"1.2.3-rc.1", Patch, false, "1.2.4"}, // pre-release dropped
		// pre-1.0 semantics: breaking bumps minor, feat/fix bump patch, major stays 0
		{"0.3.1", Major, true, "0.4.0"},
		{"0.3.1", Minor, true, "0.3.2"},
		{"0.3.1", Patch, true, "0.3.2"},
		// pre-1.0 WITHOUT preMinor behaves normally
		{"0.3.1", Minor, false, "0.4.0"},
	}
	for _, c := range cases {
		got := MustParse(c.from).Bump(c.level, c.preMinor).String()
		if got != c.want {
			t.Errorf("Bump(%q,%v,preMinor=%v) = %q, want %q", c.from, c.level, c.preMinor, got, c.want)
		}
	}
}
