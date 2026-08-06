package updater

import "testing"

func TestApplyMarkers(t *testing.T) {
	tests := []struct {
		name       string
		content    string
		newVersion string
		want       string
	}{
		{
			name:       "go const version marker",
			content:    `const Version = "0.1.0" // x-release-please-version`,
			newVersion: "1.4.0",
			want:       `const Version = "1.4.0" // x-release-please-version`,
		},
		{
			name:       "version marker with prerelease target",
			content:    `version = "1.0.0" # x-release-please-version`,
			newVersion: "2.0.0-rc.1",
			want:       `version = "2.0.0-rc.1" # x-release-please-version`,
		},
		{
			name:       "major marker on badge replaces first number only",
			content:    `[![version](https://img/badge/v0-blue)] <!-- x-release-please-major -->`,
			newVersion: "1.4.0",
			want:       `[![version](https://img/badge/v1-blue)] <!-- x-release-please-major -->`,
		},
		{
			name:       "major marker replaces semver token when present",
			content:    `image: app:0.1.0 # x-release-please-major`,
			newVersion: "1.4.0",
			want:       `image: app:1 # x-release-please-major`,
		},
		{
			name:       "minor marker",
			content:    `series = "0.1.0" ; x-release-please-minor`,
			newVersion: "1.4.2",
			want:       `series = "1.4" ; x-release-please-minor`,
		},
		{
			name:       "patch marker uses full core version",
			content:    `tag = "0.1.0-rc.1" // x-release-please-patch`,
			newVersion: "1.4.2-beta.3",
			want:       `tag = "1.4.2" // x-release-please-patch`,
		},
		{
			name:       "line without marker unchanged",
			content:    `const Other = "0.1.0"`,
			newVersion: "1.4.0",
			want:       `const Other = "0.1.0"`,
		},
		{
			name:       "multiline processes each independently",
			content:    "a = \"0.1.0\" // x-release-please-version\nb = \"0.1.0\"\nc = 0 // x-release-please-major\n",
			newVersion: "1.4.0",
			want:       "a = \"1.4.0\" // x-release-please-version\nb = \"0.1.0\"\nc = 1 // x-release-please-major\n",
		},
		{
			name:       "crlf newline style preserved",
			content:    "const V = \"0.1.0\" // x-release-please-version\r\nnext\r\n",
			newVersion: "1.4.0",
			want:       "const V = \"1.4.0\" // x-release-please-version\r\nnext\r\n",
		},
		{
			name:       "non-semver version: only version marker applies, components skipped",
			content:    "a = \"0.1.0\" // x-release-please-version\nb = 0 // x-release-please-major\n",
			newVersion: "not-a-semver",
			// -version does full-string replacement of the first semver token;
			// there is no semver token on that line's value "0.1.0"? there is,
			// so it is replaced. The major line has no semver token and is
			// skipped because the version does not parse.
			want: "a = \"not-a-semver\" // x-release-please-version\nb = 0 // x-release-please-major\n",
		},
		{
			name:       "no markers at all unchanged",
			content:    "package main\n\nfunc main() {}\n",
			newVersion: "1.4.0",
			want:       "package main\n\nfunc main() {}\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ApplyMarkers(tt.content, tt.newVersion)
			if got != tt.want {
				t.Errorf("ApplyMarkers()\n got: %q\nwant: %q", got, tt.want)
			}
		})
	}
}

func TestApply(t *testing.T) {
	tests := []struct {
		name        string
		releaseType string
		path        string
		content     string
		newVersion  string
		want        string
	}{
		{
			name:       "version.txt replaces entire content",
			path:       "version.txt",
			content:    "0.1.0\n",
			newVersion: "1.4.0",
			want:       "1.4.0\n",
		},
		{
			name:       "version.txt nested path by basename",
			path:       "sub/dir/version.txt",
			content:    "anything at all\nmore\n",
			newVersion: "2.0.0",
			want:       "2.0.0\n",
		},
		{
			name:       "package.json version updated, formatting intact",
			path:       "package.json",
			content:    "{\n  \"name\": \"x\",\n  \"version\": \"0.1.0\"\n}\n",
			newVersion: "1.4.0",
			want:       "{\n  \"name\": \"x\",\n  \"version\": \"1.4.0\"\n}\n",
		},
		{
			name:        "package.json via node releaseType",
			releaseType: "node",
			path:        "package.json",
			content:     "{ \"version\":\"0.0.1\" }",
			newVersion:  "3.2.1",
			want:        "{ \"version\":\"3.2.1\" }",
		},
		{
			name:       "package.json only first version replaced",
			path:       "package.json",
			content:    "{\n  \"version\": \"0.1.0\",\n  \"deps\": { \"version\": \"9.9.9\" }\n}\n",
			newVersion: "1.4.0",
			want:       "{\n  \"version\": \"1.4.0\",\n  \"deps\": { \"version\": \"9.9.9\" }\n}\n",
		},
		{
			name:       "package.json also runs markers",
			path:       "package.json",
			content:    "{\n  \"version\": \"0.1.0\",\n  \"x\": \"0.1.0\"\n}\n// tag 0.1.0 x-release-please-version\n",
			newVersion: "1.4.0",
			want:       "{\n  \"version\": \"1.4.0\",\n  \"x\": \"0.1.0\"\n}\n// tag 1.4.0 x-release-please-version\n",
		},
		{
			name:       "unknown file with marker gets marker substitution",
			path:       "main.go",
			content:    `const Version = "0.1.0" // x-release-please-version`,
			newVersion: "1.4.0",
			want:       `const Version = "1.4.0" // x-release-please-version`,
		},
		{
			name:        "go release type markerless main.go unchanged",
			releaseType: "go",
			path:        "main.go",
			content:     "package main\n\nfunc main() {}\n",
			newVersion:  "1.4.0",
			want:        "package main\n\nfunc main() {}\n",
		},
		{
			name:       "unknown file no marker unchanged",
			path:       "notes.txt",
			content:    "just some text with 0.1.0 in it\n",
			newVersion: "1.4.0",
			want:       "just some text with 0.1.0 in it\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Apply(tt.releaseType, tt.path, tt.content, tt.newVersion)
			if got != tt.want {
				t.Errorf("Apply()\n got: %q\nwant: %q", got, tt.want)
			}
		})
	}
}
