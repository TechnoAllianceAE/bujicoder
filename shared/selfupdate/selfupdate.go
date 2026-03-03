// Package selfupdate provides self-update functionality for the BujiCoder CLI.
// It checks GitHub Releases for newer versions and can download/replace the
// running binary using atomic file operations.
package selfupdate

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	goselfupdate "github.com/creativeprojects/go-selfupdate"

	"github.com/TechnoAllianceAE/bujicoder/shared/buildinfo"
)

const (
	githubOwner  = "TechnoAllianceAE"
	githubRepo   = "bujicoder"
	checkTimeout = 5 * time.Second
)

// UpdateInfo holds information about an available update.
type UpdateInfo struct {
	LatestVersion string
	ReleaseNotes  string
	ReleaseURL    string
}

// CheckForUpdate checks GitHub for a newer version of BujiCoder.
// Returns nil (no error) if already up-to-date, version is "dev", or checks are disabled.
func CheckForUpdate(ctx context.Context) (*UpdateInfo, error) {
	if buildinfo.Version == "dev" || buildinfo.Version == "" {
		return nil, nil
	}
	if os.Getenv("BUJICODER_DISABLE_UPDATE_CHECK") == "1" {
		return nil, nil
	}

	ctx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()

	updater, err := newUpdater()
	if err != nil {
		return nil, fmt.Errorf("create updater: %w", err)
	}

	latest, found, err := updater.DetectLatest(ctx, goselfupdate.ParseSlug(githubOwner+"/"+githubRepo))
	if err != nil {
		return nil, err
	}
	if !found || latest.LessOrEqual(semverVersion()) {
		return nil, nil
	}

	return &UpdateInfo{
		LatestVersion: latest.Version(),
		ReleaseNotes:  latest.ReleaseNotes,
		ReleaseURL:    latest.URL,
	}, nil
}

// ApplyUpdate downloads and installs the latest version, replacing the
// current executable. Prints progress to stdout.
func ApplyUpdate(ctx context.Context) error {
	if buildinfo.Version == "dev" || buildinfo.Version == "" {
		return fmt.Errorf("cannot update dev builds — install a release version first")
	}

	fmt.Printf("Checking for updates (current: v%s)...\n", buildinfo.Version)

	updater, err := newUpdater()
	if err != nil {
		return fmt.Errorf("create updater: %w", err)
	}

	latest, found, err := updater.DetectLatest(ctx, goselfupdate.ParseSlug(githubOwner+"/"+githubRepo))
	if err != nil {
		return fmt.Errorf("check for updates: %w", err)
	}
	if !found {
		return fmt.Errorf("no releases found for %s/%s", runtime.GOOS, runtime.GOARCH)
	}

	if latest.LessOrEqual(semverVersion()) {
		fmt.Printf("✓ Already up to date (v%s)\n", buildinfo.Version)
		return nil
	}

	fmt.Printf("Downloading v%s...\n", latest.Version())

	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("find executable path: %w", err)
	}

	if err := updater.UpdateTo(ctx, latest, exe); err != nil {
		return fmt.Errorf("apply update: %w", err)
	}

	fmt.Printf("\n✓ Updated buji v%s → v%s\n", buildinfo.Version, latest.Version())
	if latest.ReleaseNotes != "" {
		notes := latest.ReleaseNotes
		if len(notes) > 500 {
			notes = notes[:500] + "...\n"
		}
		fmt.Printf("\nRelease notes:\n%s\n", notes)
	}
	return nil
}

// semverVersion returns the build version truncated to 3-part semver (major.minor.patch).
// go-selfupdate's LessOrEqual panics on 4-part versions like "0.28.2.282".
func semverVersion() string {
	parts := strings.SplitN(buildinfo.Version, ".", 4)
	if len(parts) > 3 {
		return strings.Join(parts[:3], ".")
	}
	return buildinfo.Version
}

func newUpdater() (*goselfupdate.Updater, error) {
	token := resolveGitHubToken()

	source, err := goselfupdate.NewGitHubSource(goselfupdate.GitHubConfig{
		APIToken: token,
	})
	if err != nil {
		return nil, fmt.Errorf("create github source: %w", err)
	}

	return goselfupdate.NewUpdater(goselfupdate.Config{
		Source:  source,
		Filters: []string{`buji_`},
	})
}

// resolveGitHubToken returns a GitHub API token from environment variables
// or the gh CLI. Priority: BUJICODER_GITHUB_TOKEN > GITHUB_TOKEN > gh auth token.
func resolveGitHubToken() string {
	if t := os.Getenv("BUJICODER_GITHUB_TOKEN"); t != "" {
		return t
	}
	if t := os.Getenv("GITHUB_TOKEN"); t != "" {
		return t
	}
	out, err := exec.Command("gh", "auth", "token").Output()
	if err == nil {
		if t := strings.TrimSpace(string(out)); t != "" {
			return t
		}
	}
	return ""
}
