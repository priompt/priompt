package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// seed-dev.sh must use the binary it resolved for every call, including the
// final list: with priompt only on PATH (Linux/macOS, `go install`) there is no
// ./priompt.exe to fall back to.
func TestSeedDevUsesResolvedBinary(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh on PATH")
	}
	script, err := filepath.Abs("../../seed-dev.sh")
	if err != nil {
		t.Fatal(err)
	}
	// A stub priompt on PATH that records each subcommand and reads stdin.
	bin, work := t.TempDir(), t.TempDir()
	calls := filepath.Join(work, "calls")
	stub := "#!/bin/sh\necho \"$1\" >> '" + filepath.ToSlash(calls) + "'\ncat > /dev/null\n"
	if err := os.WriteFile(filepath.Join(bin, "priompt"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	// Run a copy from an empty dir so neither ./priompt nor ./priompt.exe exists.
	b, err := os.ReadFile(script)
	if err != nil {
		t.Fatal(err)
	}
	copyPath := filepath.Join(work, "seed-dev.sh")
	if err := os.WriteFile(copyPath, b, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(sh, "seed-dev.sh", "localhost:1")
	cmd.Dir = work
	cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("seed-dev.sh failed: %v\n%s", err, out)
	}
	got, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Fields(string(got))
	if len(lines) == 0 || lines[len(lines)-1] != "list" {
		t.Fatalf("stub calls = %v, want publishes then list", lines)
	}
}
