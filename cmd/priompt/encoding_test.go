package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	store "github.com/priompt/db-adapters"
)

// Byte sequences as Windows PowerShell 5.1 writes them.
func TestNormalizeTemplate(t *testing.T) {
	ok := []struct{ name, in, want string }{
		{"Out-File -Encoding utf8 (BOM + CRLF)", "\xEF\xBB\xBFHi {name}\r\n", "Hi {name}\n"},
		{"CRLF", "a\r\nb\r\n", "a\nb\n"},
		{"plain LF unchanged", "a\nb\n", "a\nb\n"},
		{"lone CR unchanged", "a\rb", "a\rb"},
		{"BOM only at the start", "a\uFEFFb", "a\uFEFFb"},
	}
	for _, c := range ok {
		got, err := normalizeTemplate(c.in)
		if err != nil || got != c.want {
			t.Errorf("%s: normalizeTemplate(%q) = %q, %v; want %q", c.name, c.in, got, err, c.want)
		}
	}
	bad := map[string]string{
		"> (UTF-16LE + BOM)":   "\xFF\xFEH\x00i\x00\r\x00\n\x00",
		"UTF-16BE + BOM":       "\xFE\xFF\x00H\x00i",
		"UTF-16LE without BOM": "H\x00i\x00",
	}
	for name, in := range bad {
		if _, err := normalizeTemplate(in); err == nil || err.Error() != "file is UTF-16; save as UTF-8" {
			t.Errorf("%s: err = %v, want \"file is UTF-16; save as UTF-8\"", name, err)
		}
	}
}

// import accepts a JSON-lines file with a UTF-8 BOM and stores LF templates.
func TestImportStripsBOMAndCRLF(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "in.jsonl")
	body := "\xEF\xBB\xBF" + `{"URI":"priompt://acme/x/y","Template":"\ufeffHi {name}\r\nbye\r\n","Slots":["name"]}` + "\n"
	if err := os.WriteFile(in, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	db := filepath.Join(dir, "p.db")
	importCmd([]string{"-db", db, "-in", in})

	st, err := store.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	p, err := st.Get(context.Background(), "priompt://acme/x/y")
	if err != nil {
		t.Fatal(err)
	}
	if p.Template != "Hi {name}\nbye\n" || strings.Contains(p.Template, "\r") {
		t.Fatalf("stored template = %q, want %q", p.Template, "Hi {name}\nbye\n")
	}
}
