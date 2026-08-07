// Package version holds the build-time version of the polaris binary.
// Release builds inject the tag version via
// -ldflags "-X github.com/liyuwei007036/polaris/internal/version.Version=...";
// local builds keep the "dev" default so update checks stay silent.
package version

var Version = "dev"
