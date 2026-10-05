package main

import "runtime/debug"

// version is the release version reported by `tyci --version`. Release builds
// inject it at link time with
// `-ldflags "-X main.version=$TAG"` (see the Makefile and
// .github/workflows/release.yaml). It stays empty in a plain `go build` or
// `go install`, where resolveVersion falls back to the version the Go
// toolchain embedded in the binary.
var version = ""

// readBuildInfo is runtime/debug.ReadBuildInfo behind a variable so tests can
// exercise resolveVersion's module-version branch without building a binary
// with a stamped module version.
var readBuildInfo = debug.ReadBuildInfo

// resolveVersion returns the version `tyci --version` should print, in
// preference order:
//
//  1. the version stamped into the binary with -ldflags "-X main.version=...",
//     which is what release builds (the release tag) and Makefile builds
//     (`git describe --tags --always --dirty`, or "dev" without Git) carry,
//  2. the main-module version the Go toolchain recorded: a tag for a build at
//     an exact tag, or a pseudo-version for a commit after a tag, as produced
//     by `go install github.com/crazy-goat/tyci-agent@vX.Y.Z` or a plain
//     `go build` from a Git checkout,
//  3. "dev" when the toolchain recorded no version at all (a source tarball,
//     `go build -buildvcs=false`, tests).
//
// A build that was not installed from a module can report "(devel)" as its
// main version; that is the toolchain's placeholder, not a version, so it is
// treated the same as no version at all.
func resolveVersion() string {
	if version != "" {
		return version
	}
	if info, ok := readBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return "dev"
}
