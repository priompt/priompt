package main

import (
	"runtime/debug"
	"testing"
)

func TestFromBuildInfo(t *testing.T) {
	rev := []debug.BuildSetting{{Key: "vcs.revision", Value: "abc123"}}
	cases := []struct {
		name                string
		bi                  debug.BuildInfo
		inVer, inCommit     string
		wantVer, wantCommit string
	}{
		{"go install @version", debug.BuildInfo{Main: debug.Module{Version: "v0.7.1"}}, "dev", "unknown", "v0.7.1", "unknown"},
		{"go build in checkout", debug.BuildInfo{Main: debug.Module{Version: "(devel)"}, Settings: rev}, "dev", "unknown", "dev", "abc123"},
		{"ldflags win", debug.BuildInfo{Main: debug.Module{Version: "v0.7.1"}, Settings: rev}, "v9.9.9", "deadbeef", "v9.9.9", "deadbeef"},
		{"nothing recorded", debug.BuildInfo{}, "dev", "unknown", "dev", "unknown"},
	}
	for _, c := range cases {
		v, cm := fromBuildInfo(&c.bi, c.inVer, c.inCommit)
		if v != c.wantVer || cm != c.wantCommit {
			t.Errorf("%s: got (%q,%q), want (%q,%q)", c.name, v, cm, c.wantVer, c.wantCommit)
		}
	}
}
