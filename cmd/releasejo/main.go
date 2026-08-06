// Command releasejo prepares release PRs and cuts releases on a Forgejo/Gitea
// instance from conventional commits, using release-please's config format.
//
// It is designed to run as a step in a Forgejo Actions workflow, where the
// GITHUB_* environment variables describe the repo + instance, but every value
// can be overridden by a flag for local/manual use.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/lassediercksAI/releasejo/internal/config"
	"github.com/lassediercksAI/releasejo/internal/forge"
	"github.com/lassediercksAI/releasejo/internal/release"
	"github.com/lassediercksAI/releasejo/internal/version"
)

func main() {
	log.SetFlags(0)
	log.SetPrefix("releasejo: ")

	var (
		configPath   = flag.String("config", "release-please-config.json", "path to the release config")
		manifestPath = flag.String("manifest", ".release-please-manifest.json", "path to the version manifest")
		repo         = flag.String("repo", env("GITHUB_REPOSITORY", ""), "target repository as owner/name")
		apiURL       = flag.String("api-url", firstEnv("GITHUB_API_URL", "GITHUB_SERVER_URL"), "Forgejo/Gitea base or API URL")
		token        = flag.String("token", firstEnv("RELEASE_TOKEN", "GITHUB_TOKEN", "FORGEJO_TOKEN"), "API token (bot PAT: contents+PR write)")
		branch       = flag.String("target-branch", env("GITHUB_REF_NAME", "main"), "release target branch")
		dryRun       = flag.Bool("dry-run", false, "compute and print, but make no changes")
		showVer      = flag.Bool("version", false, "print version and exit")
	)
	flag.Parse()

	if *showVer {
		fmt.Println("releasejo", version.Version)
		return
	}
	if err := run(*configPath, *manifestPath, *repo, *apiURL, *token, *branch, *dryRun); err != nil {
		log.Fatal(err)
	}
}

func run(configPath, manifestPath, repo, apiURL, token, branch string, dryRun bool) error {
	owner, name, ok := strings.Cut(repo, "/")
	if !ok {
		return fmt.Errorf("repo must be owner/name, got %q (set --repo or GITHUB_REPOSITORY)", repo)
	}
	if apiURL == "" {
		return fmt.Errorf("no instance URL (set --api-url or run under Forgejo Actions)")
	}
	if token == "" && !dryRun {
		return fmt.Errorf("no token (set --token / RELEASE_TOKEN); required unless --dry-run")
	}

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		return err
	}
	man, err := config.LoadManifest(manifestPath)
	if err != nil {
		return err
	}

	cl := forge.New(apiURL, token, owner, name)
	ctx := context.Background()

	opts := release.Options{
		TargetBranch: branch,
		DryRun:       dryRun,
		Logf:         func(f string, a ...any) { log.Printf(f, a...) },
	}
	log.Printf("targeting %s/%s @ %s (branch %s)%s", owner, name, apiURL, branch, dryTag(dryRun))
	return release.Run(ctx, cl, cfg, man, opts)
}

func dryTag(d bool) string {
	if d {
		return " [dry-run]"
	}
	return ""
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func firstEnv(keys ...string) string {
	for _, k := range keys {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	return ""
}
