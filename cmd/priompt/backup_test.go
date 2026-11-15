package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	store "github.com/priompt/db-adapters"
)

// runRestore runs restore in a child process (it exits via log.Fatal).
func runRestore(t *testing.T, db, in string) (string, error) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestRestoreHelperProcess$")
	cmd.Env = append(os.Environ(), "PRIOMPT_RESTORE_ARGS=-db="+db+",-in="+in)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestRestoreHelperProcess(t *testing.T) {
	args := os.Getenv("PRIOMPT_RESTORE_ARGS")
	if args == "" {
		t.Skip("helper for runRestore")
	}
	restore(strings.Split(args, ","))
	os.Exit(0)
}

func writeBackup(t *testing.T, recs ...record) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "backup.jsonl")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	for _, r := range recs {
		if err := enc.Encode(r); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

// A backup whose ref names a commit it does not contain must be refused, not
// restored into a branch that points at nothing.
func TestRestoreRefusesRefToMissingCommit(t *testing.T) {
	in := writeBackup(t,
		record{Kind: "header", Version: backupVersion},
		record{Kind: "ref", Ref: &store.Ref{URI: "priompt://acme/a", Branch: "main", Commit: "deadbeef"}},
	)
	out, err := runRestore(t, filepath.Join(t.TempDir(), "r.db"), in)
	if err == nil || !strings.Contains(out, "backup is inconsistent") {
		t.Fatalf("restore of a dangling ref: err=%v out=%q, want refusal", err, out)
	}
}

// backup then restore round-trips history and the served prompt.
func TestBackupRestoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.db")
	st, err := store.Open(src)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, v := range []string{"Hi {name}", "Hello {name}"} {
		if _, err := st.Commit(ctx, "priompt://acme/a", store.DefaultBranch, v, []string{"name"}, "t", "edit"); err != nil {
			t.Fatal(err)
		}
	}
	st.Close()

	file := filepath.Join(dir, "b.jsonl")
	backup([]string{"-db=" + src, "-out=" + file})
	dst := filepath.Join(dir, "dst.db")
	if out, err := runRestore(t, dst, file); err != nil {
		t.Fatalf("restore: %v\n%s", err, out)
	}

	got, err := store.Open(dst)
	if err != nil {
		t.Fatal(err)
	}
	defer got.Close()
	p, err := got.Get(ctx, "priompt://acme/a")
	if err != nil || p.Template != "Hello {name}" {
		t.Fatalf("restored prompt = %+v, %v; want the latest version", p, err)
	}
	if n, _ := got.CountCommits(ctx); n != 2 {
		t.Errorf("restored %d commits, want 2", n)
	}
}
