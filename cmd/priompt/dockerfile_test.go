package main

import (
	"os"
	"strings"
	"testing"
)

// Docker only treats '#' as a comment at the start of a line. A trailing
// comment on any instruction but RUN (where the shell strips it) becomes part
// of the arguments: `WORKDIR /data  # ...` set the working directory to that
// whole string, so the default relative -db landed outside the /data volume.
func TestDockerfileNoTrailingComments(t *testing.T) {
	b, err := os.ReadFile("../../Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	inRun, user := false, ""
	for i, line := range strings.Split(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n") {
		trimmed := strings.TrimSpace(line)
		cont := strings.HasSuffix(trimmed, `\`)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if !inRun {
			instr := strings.ToUpper(strings.Fields(trimmed)[0])
			switch {
			case instr == "RUN":
				inRun = cont
			case strings.Contains(trimmed, "#"):
				t.Errorf("Dockerfile:%d: trailing comment becomes part of %s: %q", i+1, instr, trimmed)
			}
			if instr == "USER" {
				user = strings.Fields(trimmed)[1]
			}
			continue
		}
		inRun = cont
	}
	if user == "" || user == "root" || user == "0" {
		t.Errorf("final image must run as a non-root USER, got %q", user)
	}
}
