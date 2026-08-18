package selfupdate

import (
	"context"
	"strings"
	"testing"

	goselfupdate "github.com/creativeprojects/go-selfupdate"

	"github.com/TechnoAllianceAE/bujicoder/shared/buildinfo"
)

// go-selfupdate compares versions with semver.MustParse, which panics on
// anything that is not valid semver. A background update check must never be
// able to crash the CLI because of an odd -ldflags version string.
func TestComparableVersion(t *testing.T) {
	tests := []struct {
		name    string
		version string
		want    string
		wantErr bool
	}{
		{name: "plain semver", version: "0.10.0", want: "0.10.0"},
		{name: "v prefix", version: "v0.10.0", want: "0.10.0"},
		{name: "four part build", version: "0.28.2.282", want: "0.28.2"},
		{name: "prerelease", version: "1.2.3-rc1", want: "1.2.3-rc1"},
		{name: "build metadata", version: "1.2.3+abc", want: "1.2.3+abc"},
		{name: "two part", version: "0.10", wantErr: true},
		{name: "not a version", version: "unknown", wantErr: true},
		{name: "date", version: "2026-05-01", wantErr: true},
		{name: "dirty suffix", version: "0.10.0_dirty", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			orig := buildinfo.Version
			buildinfo.Version = tc.version
			t.Cleanup(func() { buildinfo.Version = orig })

			got, err := comparableVersion()
			if tc.wantErr {
				if err == nil {
					t.Fatalf("comparableVersion(%q) = %q, want an error", tc.version, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("comparableVersion(%q): %v", tc.version, err)
			}
			if got != tc.want {
				t.Fatalf("comparableVersion(%q) = %q, want %q", tc.version, got, tc.want)
			}
			// Whatever comes back must be a 3-part semver, which is what
			// go-selfupdate's semver.MustParse accepts.
			if strings.Count(strings.SplitN(got, "-", 2)[0], ".") != 2 {
				t.Fatalf("comparableVersion returned %q, which semver.MustParse would reject", got)
			}
		})
	}
}

// A malformed build version must abort the check with an error instead of
// panicking inside the comparison.
func TestCheckForUpdateRejectsUncomparableVersion(t *testing.T) {
	orig := buildinfo.Version
	buildinfo.Version = "not-a-version"
	t.Cleanup(func() { buildinfo.Version = orig })

	info, err := CheckForUpdateFrom(context.Background(), "owner", "repo", "buji_")
	if err == nil {
		t.Fatalf("expected an error, got info=%+v", info)
	}
	if !strings.Contains(err.Error(), "comparable semver") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestApplyUpdateRejectsUncomparableVersion(t *testing.T) {
	orig := buildinfo.Version
	buildinfo.Version = "0.10"
	t.Cleanup(func() { buildinfo.Version = orig })

	if err := ApplyUpdateFrom(context.Background(), "owner", "repo", "buji_"); err == nil {
		t.Fatal("expected ApplyUpdateFrom to refuse an uncomparable version")
	}
}

func TestCheckForUpdateSkipsDevBuilds(t *testing.T) {
	for _, v := range []string{"dev", ""} {
		orig := buildinfo.Version
		buildinfo.Version = v
		info, err := CheckForUpdateFrom(context.Background(), "owner", "repo", "buji_")
		buildinfo.Version = orig
		if err != nil || info != nil {
			t.Fatalf("version %q: got (%+v, %v), want (nil, nil)", v, info, err)
		}
	}
}

// The updater must always carry a checksum validator: without one, go-selfupdate
// replaces the running binary with unverified bytes.
func TestUpdaterAlwaysHasChecksumValidator(t *testing.T) {
	if checksumAsset != "checksums.txt" {
		t.Fatalf("checksumAsset = %q, want checksums.txt (must match the release asset)", checksumAsset)
	}
	updater, err := newUpdaterWithFilter("buji_")
	if err != nil {
		t.Fatalf("newUpdaterWithFilter: %v", err)
	}
	if updater == nil {
		t.Fatal("nil updater")
	}

	// The validator itself must resolve the shared checksums asset and reject a
	// mismatching hash.
	v := &goselfupdate.ChecksumValidator{UniqueFilename: checksumAsset}
	if got := v.GetValidationAssetName("buji_darwin_arm64"); got != checksumAsset {
		t.Fatalf("GetValidationAssetName = %q, want %q", got, checksumAsset)
	}
	// sha256sum output format: "<hash>  <name>".
	sums := []byte("0000000000000000000000000000000000000000000000000000000000000000  buji_darwin_arm64\n")
	if err := v.Validate("buji_darwin_arm64", []byte("payload"), sums); err == nil {
		t.Fatal("validator accepted a payload whose checksum does not match")
	}
	if err := v.Validate("buji_linux_amd64", []byte("payload"), sums); err == nil {
		t.Fatal("validator accepted an artifact missing from checksums.txt")
	}
}

func TestTruncateRunes(t *testing.T) {
	tests := []struct {
		name  string
		input string
		max   int
		want  string
	}{
		{name: "short", input: "abc", max: 10, want: "abc"},
		{name: "exact", input: "abc", max: 3, want: "abc"},
		{name: "truncated", input: "abcdef", max: 3, want: "abc..."},
		{name: "multi-byte is not split", input: strings.Repeat("é", 10), max: 3, want: "ééé..."},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := truncateRunes(tc.input, tc.max); got != tc.want {
				t.Fatalf("truncateRunes(%q, %d) = %q, want %q", tc.input, tc.max, got, tc.want)
			}
		})
	}
}
