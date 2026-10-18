package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	store "github.com/priompt/db-adapters"
)

// TestMain lets a test run the real command: re-exec the test binary with
// PRIOMPT_TEST_MAIN set and it behaves as `priompt <args>`.
func TestMain(m *testing.M) {
	if args := os.Getenv("PRIOMPT_TEST_MAIN"); args != "" {
		os.Args = append([]string{"priompt"}, strings.Split(args, "\x1f")...)
		main()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func runCLI(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), "PRIOMPT_TEST_MAIN="+strings.Join(args, "\x1f"))
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// import and put write through the same validate + commit path as
// PublishPrompt: invalid prompts are refused, every write is a commit on main
// (so History and rollback work), and repeating a write adds no commit.
func TestImportAndPutCommit(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "p.db")
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	const good = `{"URI":"priompt://acme/r/good","Template":"Hi {name}","Slots":["name"]}` + "\n"
	bad := write("bad.jsonl", good+`{"URI":"priompt://acme/r/bad","Template":"Hi {org}","Slots":["name"]}`+"\n")
	ok := write("good.jsonl", good)
	tmpl := write("t.txt", "Hello {name}")

	if out, err := runCLI(t, "import", "-db", db, "-in", bad); err == nil {
		t.Errorf("import of an invalid prompt succeeded:\n%s", out)
	}
	for _, args := range [][]string{
		{"import", "-db", db, "-in", ok},
		{"import", "-db", db, "-in", ok},
		{"put", "-db", db, "-uri", "priompt://acme/r/put", "-file", tmpl, "-slot", "name"},
		{"put", "-db", db, "-uri", "priompt://acme/r/put", "-file", tmpl, "-slot", "name"},
	} {
		if out, err := runCLI(t, args...); err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
	}

	st, err := store.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	if _, err := st.Get(ctx, "priompt://acme/r/bad"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("invalid prompt was stored (Get err = %v)", err)
	}
	for uri, msg := range map[string]string{"priompt://acme/r/good": "import", "priompt://acme/r/put": "put"} {
		log, err := st.Log(ctx, uri, store.DefaultBranch)
		if err != nil {
			t.Errorf("%s: history: %v", uri, err)
			continue
		}
		if len(log) != 1 || log[0].Author != "priompt" || log[0].Message != msg {
			t.Errorf("%s: history = %+v; want one commit by priompt, message %q", uri, log, msg)
		}
	}
}
