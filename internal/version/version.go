// Package version holds the build-time version of the sb-control binary.
// Release builds inject the tag version via
// -ldflags "-X github.com/sb-control/sb-control/internal/version.Version=...";
// local builds keep the "dev" default so update checks stay silent.
package version

var Version = "dev"
