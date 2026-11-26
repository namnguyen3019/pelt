package main

import (
	"fmt"
	"runtime"
	"runtime/debug"
)

// version is set at build time by the release workflow:
//
//	go build -ldflags "-X main.version=v0.4.3" ./cmd/pelt
//
// When it is empty (go install or a local build), versionString falls back
// to what the Go toolchain recorded in the binary.
var version = ""

// versionString is what -version prints, e.g.
// "pelt v0.4.3 (go1.24.7, linux/amd64)".
func versionString() string {
	return fmt.Sprintf("pelt %s (%s, %s/%s)", buildVersion(version, readBuildInfo), runtime.Version(), runtime.GOOS, runtime.GOARCH)
}

// buildVersion picks the version to show: the stamped one if set, else the
// module version from go install (v0.4.3), else "dev" with the commit a
// local build came from.
func buildVersion(stamped string, read func() (*debug.BuildInfo, bool)) string {
	if stamped != "" {
		return stamped
	}
	bi, ok := read()
	if !ok {
		return "dev"
	}
	if v := bi.Main.Version; v != "" && v != "(devel)" {
		return v
	}
	var rev, dirty string
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			if s.Value == "true" {
				dirty = "-dirty"
			}
		}
	}
	if len(rev) > 12 {
		rev = rev[:12]
	}
	if rev == "" {
		return "dev"
	}
	return "dev-" + rev + dirty
}

var readBuildInfo = debug.ReadBuildInfo
