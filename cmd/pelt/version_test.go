package main

import (
	"bytes"
	"runtime/debug"
	"strings"
	"testing"
)

func TestBuildVersion(t *testing.T) {
	info := func(version string, settings ...string) func() (*debug.BuildInfo, bool) {
		return func() (*debug.BuildInfo, bool) {
			bi := &debug.BuildInfo{Main: debug.Module{Version: version}}
			for i := 0; i+1 < len(settings); i += 2 {
				bi.Settings = append(bi.Settings, debug.BuildSetting{Key: settings[i], Value: settings[i+1]})
			}
			return bi, true
		}
	}
	none := func() (*debug.BuildInfo, bool) { return nil, false }

	tests := []struct {
		name    string
		stamped string
		read    func() (*debug.BuildInfo, bool)
		want    string
	}{
		{"stamped by the release build", "v0.4.3", info("v0.1.0"), "v0.4.3"},
		{"go install @version", "", info("v0.4.3"), "v0.4.3"},
		{"local build with git", "", info("(devel)", "vcs.revision", "0123456789abcdef0123", "vcs.modified", "false"), "dev-0123456789ab"},
		{"local build with edits", "", info("(devel)", "vcs.revision", "abc", "vcs.modified", "true"), "dev-abc-dirty"},
		{"no vcs info", "", info("(devel)"), "dev"},
		{"no build info", "", none, "dev"},
	}
	for _, tt := range tests {
		if got := buildVersion(tt.stamped, tt.read); got != tt.want {
			t.Errorf("%s: buildVersion = %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestVersionFlag(t *testing.T) {
	old := version
	version = "v1.2.3"
	defer func() { version = old }()

	for _, args := range [][]string{{"-version"}, {"--version"}, {"run", "-version"}} {
		var stdout, stderr bytes.Buffer
		if code := run(args, &stdout, &stderr); code != 0 {
			t.Fatalf("%v: exit %d, stderr %q", args, code, stderr.String())
		}
		if got := stdout.String(); !strings.HasPrefix(got, "pelt v1.2.3 (go") {
			t.Errorf("%v: printed %q", args, got)
		}
	}
}
