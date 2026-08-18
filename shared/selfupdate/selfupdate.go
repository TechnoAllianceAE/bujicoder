// Package selfupdate provides self-update functionality for the BujiCoder CLI.
// It checks GitHub Releases for newer versions and can download/replace the
// running binary using atomic file operations.
package selfupdate

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
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
	// tokenTimeout bounds the `gh auth token` helper so a hung/interactive gh
	// process cannot block an update check (and therefore CLI startup) forever.
	tokenTimeout = 3 * time.Second
	// checksumAsset is the release asset holding `sha256sum` output for every
	// published binary. Every downloaded artifact is verified against it before
	// the running executable is replaced.
	checksumAsset = "checksums.txt"
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
	return CheckForUpdateFrom(ctx, githubOwner, githubRepo, "buji_")
}

// CheckForUpdateFrom checks a specific GitHub owner/repo for a newer version.
// The filter parameter controls which release assets to match (e.g. "buji_" or "bujicoder_").
func CheckForUpdateFrom(ctx context.Context, owner, repo, filter string) (*UpdateInfo, error) {
	if buildinfo.Version == "dev" || buildinfo.Version == "" {
		return nil, nil
	}
	if os.Getenv("BUJICODER_DISABLE_UPDATE_CHECK") == "1" {
		return nil, nil
	}

	// go-selfupdate compares via semver.MustParse, which panics on a version
	// string that is not valid semver. Refuse to compare instead of crashing
	// the whole CLI from a background update check.
	current, err := comparableVersion()
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()

	updater, err := newUpdaterWithFilter(filter)
	if err != nil {
		return nil, fmt.Errorf("create updater: %w", err)
	}

	latest, found, err := updater.DetectLatest(ctx, goselfupdate.ParseSlug(owner+"/"+repo))
	if err != nil {
		return nil, err
	}
	if !found || latest.LessOrEqual(current) {
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
	return ApplyUpdateFrom(ctx, githubOwner, githubRepo, "buji_")
}

// ApplyUpdateFrom downloads and installs the latest version from a specific
// GitHub owner/repo, filtering release assets by the given prefix.
func ApplyUpdateFrom(ctx context.Context, owner, repo, filter string) error {
	if buildinfo.Version == "dev" || buildinfo.Version == "" {
		return fmt.Errorf("cannot update dev builds — install a release version first")
	}
	// Validate the running version before any comparison: go-selfupdate's
	// LessOrEqual panics on non-semver input.
	current, err := comparableVersion()
	if err != nil {
		return err
	}

	fmt.Printf("Checking for updates (current: v%s)...\n", buildinfo.Version)

	updater, err := newUpdaterWithFilter(filter)
	if err != nil {
		return fmt.Errorf("create updater: %w", err)
	}

	slug := owner + "/" + repo
	latest, found, err := updater.DetectLatest(ctx, goselfupdate.ParseSlug(slug))
	if err != nil {
		return fmt.Errorf("check for updates: %w", err)
	}
	if !found {
		return fmt.Errorf("no releases found for %s/%s", runtime.GOOS, runtime.GOARCH)
	}

	if latest.LessOrEqual(current) {
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

	fmt.Printf("\n✓ Updated bujicoder v%s → v%s\n", buildinfo.Version, latest.Version())
	if latest.ReleaseNotes != "" {
		fmt.Printf("\nRelease notes:\n%s\n", truncateRunes(latest.ReleaseNotes, 500))
	}
	return nil
}

// comparableVersion returns the build version reduced to a 3-part semver
// (major.minor.patch) suitable for go-selfupdate's comparison helpers, which
// call semver.MustParse and panic on anything else. An error is returned when
// the build version cannot be reduced to a valid semver string.
func comparableVersion() (string, error) {
	v := strings.TrimPrefix(buildinfo.Version, "v")
	// Build metadata like "0.28.2.282" carries a 4th numeric component.
	if parts := strings.SplitN(v, ".", 4); len(parts) > 3 {
		v = strings.Join(parts[:3], ".")
	}
	if !semverRe.MatchString(v) {
		return "", fmt.Errorf("build version %q is not a comparable semver version", buildinfo.Version)
	}
	return v, nil
}

// semverRe matches major.minor.patch with optional pre-release / build metadata.
var semverRe = regexp.MustCompile(`^\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?$`)

// truncateRunes shortens s to at most max runes without splitting a rune,
// which byte slicing would do for multi-byte release notes.
func truncateRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "..."
}

func newUpdaterWithFilter(filter string) (*goselfupdate.Updater, error) {
	token := resolveGitHubToken()

	source, err := goselfupdate.NewGitHubSource(goselfupdate.GitHubConfig{
		APIToken: token,
	})
	if err != nil {
		return nil, fmt.Errorf("create github source: %w", err)
	}

	// Validator is mandatory: without it go-selfupdate replaces the running
	// binary with whatever bytes the network returned. With it, a missing
	// checksums.txt asset fails DetectLatest (ErrValidationAssetNotFound) and a
	// hash mismatch fails UpdateTo before the executable is touched.
	return goselfupdate.NewUpdater(goselfupdate.Config{
		Source:    source,
		Filters:   []string{filter},
		Validator: &goselfupdate.ChecksumValidator{UniqueFilename: checksumAsset},
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
	ctx, cancel := context.WithTimeout(context.Background(), tokenTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "gh", "auth", "token").Output()
	if err == nil {
		if t := strings.TrimSpace(string(out)); t != "" {
			return t
		}
	}
	return ""
}
