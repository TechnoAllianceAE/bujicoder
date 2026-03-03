// Package buildinfo holds version metadata injected at build time via ldflags.
//
// The Makefile populates these with:
//
//	-X github.com/TechnoAllianceAE/bujicoder/shared/buildinfo.Version=$(VERSION)
//	-X github.com/TechnoAllianceAE/bujicoder/shared/buildinfo.Commit=$(GIT_COMMIT)
//	-X github.com/TechnoAllianceAE/bujicoder/shared/buildinfo.BuildTime=$(BUILD_TIME)
package buildinfo

var (
	Version   = "dev"
	Commit    = "unknown"
	BuildTime = "unknown"
)

// String returns a human-readable version string.
func String() string {
	return Version + " (" + Commit + ") built " + BuildTime
}
